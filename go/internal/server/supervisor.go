// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// The serve layer, held apart from everything under it so it can be replaced
// without replacing the process.
//
// A bind address is decided when a socket is opened, and a certificate is
// decided when the TLS configuration is built. Neither can be changed in a
// running listener, so a deployment used to be told to restart itself. The
// engine below (the store, the core, the sandbox, the uploads in flight) has
// no reason to go with it: what changed is which socket the requests arrive
// on.
//
// So the listener and the server are one unit and this replaces it: bind the
// new socket first, put it into service, then drain the old one. Binding first
// is what makes a typo survivable. A refused bind leaves the old listener
// serving and the save is refused naming the field, rather than the deployment
// going dark over an address nothing can listen on.

// drainDeadline is how long the old listener has to finish what it is holding.
// It is the same deadline the shutdown path uses: a request that cannot finish
// in this long is one the other end has stopped reading.
const drainDeadline = 15 * time.Second

// Serve is the running listener and the server on it.
//
// One at a time, and Swap is the only thing that changes which. Its mutex is
// held across the bind and the handover, so two saves landing together produce
// two swaps in some order rather than two listeners on one address.
type Serve struct {
	mu      sync.Mutex
	log     *slog.Logger
	addr    string
	srv     *http.Server
	ln      net.Listener
	done    chan struct{}
	stopped bool

	// build makes a server for an address. It is the wiring's, because this
	// package's assembly needs the whole dependency set and this file needs
	// none of it.
	build func(addr string) (*http.Server, error)
}

// NewServe binds the first listener and starts serving on it.
func NewServe(ctx context.Context, log *slog.Logger, addr string, build func(addr string) (*http.Server, error)) (*Serve, error) {
	s := &Serve{log: log, build: build}
	if err := s.start(ctx, addr); err != nil {
		return nil, err
	}
	return s, nil
}

// start binds addr, builds a server for it and serves. The caller holds no
// lock on the first call and holds it on a swap.
func (s *Serve) start(ctx context.Context, addr string) error {
	srv, err := s.build(addr)
	if err != nil {
		return err
	}
	ln, lerr := net.Listen("tcp", addr)
	if lerr != nil {
		return fmt.Errorf("binding %s: %w", addr, lerr)
	}
	done := make(chan struct{})
	s.addr, s.srv, s.ln, s.done = addr, srv, ln, done

	// Serving runs until the server stops accepting. A serve error other than
	// the ordinary close is logged rather than returned: by then the caller has
	// moved on and there is nowhere to return it to.
	task.Go(ctx, "listener", func() {
		defer close(done)
		if serr := srv.ServeTLS(ln, "", ""); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			s.log.Error("the listener stopped", "addr", addr, "error", serr)
		}
	})
	return nil
}

// Addr is the address currently being served.
func (s *Serve) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Swap moves the listener to addr.
//
// The new socket is bound before the old one is touched. A bind that fails
// returns the error with nothing changed, which is what lets the settings save
// refuse and leave the deployment reachable.
//
// The old server is drained in the background: it is holding requests that are
// still being answered, and a save that waited for them would hold the
// administrator's own request open for as long as the longest download.
func (s *Serve) Swap(ctx context.Context, addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return errors.New("the server is shutting down")
	}
	if addr == s.addr {
		return nil
	}

	old, oldDone, oldAddr := s.srv, s.done, s.addr
	if err := s.start(ctx, addr); err != nil {
		// Nothing moved: start only assigns after both the build and the bind
		// succeeded.
		return err
	}
	s.log.Info("the listener moved", "from", oldAddr, "to", addr)

	// Detached from the request that caused the swap: that request is on the
	// old listener, and draining it is what this is waiting for.
	dctx := context.WithoutCancel(ctx)
	task.Go(dctx, "listener drain", func() { drain(dctx, s.log, old, oldDone, oldAddr) })
	return nil
}

// Stop drains the current listener and stops serving.
func (s *Serve) Stop(ctx context.Context) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	srv, done, addr := s.srv, s.done, s.addr
	s.mu.Unlock()
	drain(ctx, s.log, srv, done, addr)
}

// drain stops a server accepting and waits for what it is holding, bounded.
func drain(ctx context.Context, log *slog.Logger, srv *http.Server, done chan struct{}, addr string) {
	dctx, cancel := context.WithTimeout(ctx, drainDeadline)
	defer cancel()
	if err := srv.Shutdown(dctx); err != nil {
		log.Warn("a listener did not drain cleanly and was closed", "addr", addr, "error", err)
		_ = srv.Close() //nolint:errcheck // the drain already failed; closing is the fallback.
	}
	<-done
}
