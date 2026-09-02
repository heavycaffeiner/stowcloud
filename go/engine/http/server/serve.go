// Linux only, because it serves a Linux-only engine.
//go:build linux

// Listener generations and the live swap between them.
//
// The order is the whole design: a new socket is bound and serving before the
// old one stops. A swap that fails at any step leaves the previous generation
// answering, so an operator who typed the wrong address still has a server to
// correct it with.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// DrainTimeout is how long a replaced generation has to finish its in-flight
// requests before it is closed regardless.
//
// Bounded because a generation that never drains is a generation that never
// releases its socket, and the next swap would find the address in use.
const DrainTimeout = 15 * time.Second

// ErrStopped is a swap requested after the supervisor has shut down.
var ErrStopped = errors.New("the server is stopped")

// Generation is one listener and the app serving it.
//
// Each generation owns its own app because shutting one down operates on the
// app: two generations sharing one would mean draining the old stops the new.
type Generation struct {
	Addr string
	App  *fiber.App
	Ln   net.Listener
	// Done closes when this generation's serve loop has returned.
	Done <-chan struct{}
}

// Builder makes the app and listener for an address.
//
// A function rather than a value so a swap builds its replacement fresh, and
// so a build failure is an error rather than a half-configured app.
type Builder func(addr string) (*fiber.App, net.Listener, error)

// Serve owns the current generation and serialises swaps.
type Serve struct {
	build Builder
	log   *slog.Logger

	mu      sync.Mutex
	current *Generation
	stopped bool
}

// NewServe builds a supervisor. Nothing is listening until Swap is called.
//
// log receives what a drain cannot return: the swap has already told its
// caller it succeeded by then, and a generation that will not release its
// socket is otherwise invisible until the next swap to that address fails for
// a reason nothing explains.
func NewServe(build Builder, log *slog.Logger) *Serve {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Serve{build: build, log: log}
}

// Current returns the generation now serving, or nil.
func (s *Serve) Current() *Generation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Swap moves to a new address, or replaces the current generation at the same
// one.
//
// The old generation keeps answering until the new one is serving. A build or
// bind failure changes nothing at all: the caller gets an error and the server
// it had is the server it still has.
func (s *Serve) Swap(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return ErrStopped
	}
	if s.current != nil && s.current.Addr == addr {
		// A no-op generation does nothing rather than rebuilding, because
		// rebuilding drops every in-flight request to arrive at the same
		// place.
		return nil
	}

	app, ln, err := s.build(addr)
	if err != nil {
		return fmt.Errorf("building the listener for %s: %w", addr, err)
	}

	// Serving starts before the old generation is touched, and the swap waits
	// for it to be answering. A socket that bound and a server that failed to
	// serve on it are different failures, and without this wait only the first
	// is visible: the swap would publish a generation nothing is listening on
	// and stop the one that was.
	done := make(chan struct{})
	failed := make(chan error, 1)
	task.Go(context.Background(), "server: listener generation", func() {
		defer close(done)
		if err := app.Listener(ln); err != nil {
			failed <- err
		}
	})

	if serr := confirmServing(ln, failed); serr != nil {
		// The listener is closed here rather than left for the caller: nothing
		// else holds it, and an unclosed socket is an address the next attempt
		// cannot bind.
		if cerr := ln.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			serr = errors.Join(serr, cerr)
		}
		<-done
		return fmt.Errorf("serving on %s: %w", addr, serr)
	}

	old := s.current
	s.current = &Generation{Addr: addr, App: app, Ln: ln, Done: done}

	if old != nil {
		// Detached from the caller's context: a settings save that returns
		// must not cancel the drain of the generation it replaced.
		task.Go(context.Background(), "server: draining a replaced listener", func() {
			s.drain(old)
		})
	}
	return nil
}

// Shutdown stops the current generation and refuses further swaps.
//
// Idempotent, because a signal handler and a deferred call both reaching it is
// the ordinary shape of a process ending.
func (s *Serve) Shutdown() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	gen := s.current
	s.current = nil
	s.mu.Unlock()

	if gen == nil {
		return nil
	}
	return shutdown(gen)
}

// confirmServing waits until the new generation answers a request, or the
// serve loop reports why it cannot.
//
// A whole request rather than a dial. The kernel accepts connections onto the
// backlog of a bound socket whether or not anything is serving it, so a
// successful dial proves the bind and nothing else; a reply proves the app
// behind it is running. That distinction is the point of this wait, since the
// swap drops a working listener immediately afterwards.
//
// Both schemes are tried, because this package is handed a listener and is not
// told whether something wrapped it in TLS. Asking only in plaintext is why
// this was never wired into a deployment: a TLS listener does not answer such
// a request at all, so every swap reported a listener that never began
// serving.
//
// Certificates are not verified. The question is whether anything is behind
// the socket, and the deployment's own certificate is routinely one this
// process would not trust: self-signed on first boot, and issued for a host
// name rather than for the address being dialled here.
func confirmServing(ln net.Listener, failed <-chan error) error {
	deadline := time.After(confirmTimeout)
	client := &http.Client{
		Timeout: confirmRequestTimeout,
		Transport: &http.Transport{
			//nolint:gosec // G402: proving something answers, not trusting it.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	urls := []string{
		"https://" + ln.Addr().String() + "/",
		"http://" + ln.Addr().String() + "/",
	}

	for {
		select {
		case err := <-failed:
			return err
		case <-deadline:
			return errServeTimeout
		default:
		}

		// The path does not exist and the status does not matter: any reply is
		// a reply, and a 404 is as much proof as a 200 that something is
		// serving.
		for _, url := range urls {
			res, err := client.Get(url) //nolint:noctx // the client's own timeout is the bound.
			if err != nil {
				continue
			}
			if _, derr := io.Copy(io.Discard, res.Body); derr != nil {
				// A reply that arrived and would not finish reading is still a
				// reply: the app is serving, which is the question.
				_ = derr
			}
			if cerr := res.Body.Close(); cerr != nil {
				return cerr
			}
			return nil
		}
		time.Sleep(confirmInterval)
	}
}

// confirmTimeout bounds the wait for a new generation to answer, and
// confirmInterval is how often it is asked.
const (
	confirmTimeout        = 5 * time.Second
	confirmInterval       = 5 * time.Millisecond
	confirmRequestTimeout = 500 * time.Millisecond
)

// errServeTimeout is a generation that bound its socket and never answered.
var errServeTimeout = errors.New("the listener did not begin serving")

// drain stops a replaced generation, bounded, and logs what it could not do.
func (s *Serve) drain(gen *Generation) {
	if err := shutdown(gen); err != nil {
		s.log.Warn("draining a replaced listener failed",
			"addr", gen.Addr, "error", err)
	}
}

// shutdown asks a generation to finish and closes it if it will not.
func shutdown(gen *Generation) error {
	ctx, cancel := context.WithTimeout(context.Background(), DrainTimeout)
	defer cancel()

	err := gen.App.ShutdownWithContext(ctx)

	select {
	case <-gen.Done:
		return err
	case <-ctx.Done():
		// Past the deadline the socket is closed regardless, because a
		// generation holding one forever means the next swap to that address
		// cannot bind.
		if cerr := gen.Ln.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			err = errors.Join(err, cerr)
		}
		return err
	}
}
