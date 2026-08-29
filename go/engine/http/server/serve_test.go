// Linux only, matching the file under test.
//go:build linux

package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// builderFor makes an app answering with its own name, so a test can tell
// which generation replied.
func builderFor(t *testing.T, name string) Builder {
	t.Helper()
	return func(addr string) (*fiber.App, net.Listener, error) {
		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.All("/*", func(c *fiber.Ctx) error { return c.SendString(name) })
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, nil, err
		}
		return app, ln, nil
	}
}

// get asks the address who is answering.
func get(t *testing.T, addr string) (string, error) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get("http://" + addr + "/")
	if err != nil {
		return "", err
	}
	body, rerr := io.ReadAll(res.Body)
	if cerr := res.Body.Close(); cerr != nil {
		t.Errorf("closing the body: %v", cerr)
	}
	if rerr != nil {
		return "", rerr
	}
	return string(body), nil
}

// freeAddr reserves a port and releases it, which is how a test picks one that
// is very likely free without racing every other test in the tree.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := ln.Addr().String()
	if cerr := ln.Close(); cerr != nil {
		t.Fatalf("releasing the port: %v", cerr)
	}
	return addr
}

// A generation serves after the swap returns, which is what makes the wait for
// the accept loop worth having: the caller is told it succeeded only once the
// address answers.
func TestASwapServesBeforeItReturns(t *testing.T) {
	addr := freeAddr(t)
	s := NewServe(builderFor(t, "first"), nil)
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	if err := s.Swap(addr); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	// No sleep: if the swap's own confirmation is real, the address answers
	// the instant it returns.
	got, err := get(t, addr)
	if err != nil {
		t.Fatalf("the address did not answer after the swap returned: %v", err)
	}
	if got != "first" {
		t.Errorf("the address answered %q", got)
	}
}

// The new generation answers and the old one stops, which is the swap doing
// its job.
func TestASwapReplacesWhoAnswers(t *testing.T) {
	first, second := freeAddr(t), freeAddr(t)
	s := NewServe(func(addr string) (*fiber.App, net.Listener, error) {
		name := "first"
		if addr == second {
			name = "second"
		}
		return builderFor(t, name)(addr)
	}, nil)
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	if err := s.Swap(first); err != nil {
		t.Fatalf("the first swap: %v", err)
	}
	if got, err := get(t, first); err != nil || got != "first" {
		t.Fatalf("the first address answered %q, %v", got, err)
	}

	if err := s.Swap(second); err != nil {
		t.Fatalf("the second swap: %v", err)
	}
	if got, err := get(t, second); err != nil || got != "second" {
		t.Errorf("the second address answered %q, %v", got, err)
	}
	if s.Current().Addr != second {
		t.Errorf("the current generation is %q", s.Current().Addr)
	}
}

// A failed swap changes nothing. An operator who typed the wrong address still
// has a server to correct it with, which is the entire reason the new socket
// is bound before the old one is dropped.
func TestAFailedSwapLeavesTheOldServerReachable(t *testing.T) {
	addr := freeAddr(t)
	buildErr := errors.New("the certificate could not be loaded")

	fail := false
	s := NewServe(func(a string) (*fiber.App, net.Listener, error) {
		if fail {
			return nil, nil, buildErr
		}
		return builderFor(t, "original")(a)
	}, nil)
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	if err := s.Swap(addr); err != nil {
		t.Fatalf("the first swap: %v", err)
	}

	fail = true
	err := s.Swap(freeAddr(t))
	if !errors.Is(err, buildErr) {
		t.Fatalf("a failed build returned %v", err)
	}

	// The original is still answering, and is still the current generation.
	got, gerr := get(t, addr)
	if gerr != nil {
		t.Fatalf("the original stopped answering after a failed swap: %v", gerr)
	}
	if got != "original" {
		t.Errorf("the original answered %q", got)
	}
	if s.Current() == nil || s.Current().Addr != addr {
		t.Errorf("the current generation is %+v", s.Current())
	}
}

// A bind failure is a failed swap too, not a crash: the address may simply be
// taken by something else on the machine.
func TestABindFailureIsAFailedSwap(t *testing.T) {
	addr := freeAddr(t)
	blocker, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("holding the address: %v", err)
	}
	t.Cleanup(func() {
		if cerr := blocker.Close(); cerr != nil {
			t.Errorf("releasing the address: %v", cerr)
		}
	})

	s := NewServe(builderFor(t, "x"), nil)
	if serr := s.Swap(addr); serr == nil {
		t.Fatal("binding a taken address succeeded")
	}
	if s.Current() != nil {
		t.Errorf("a failed first swap published a generation: %+v", s.Current())
	}
}

// Swapping to the same address does nothing, since rebuilding would drop every
// in-flight request to arrive at the same place.
func TestSwappingToTheSameAddressIsANoOp(t *testing.T) {
	addr := freeAddr(t)
	builds := 0
	s := NewServe(func(a string) (*fiber.App, net.Listener, error) {
		builds++
		return builderFor(t, "only")(a)
	}, nil)
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	if err := s.Swap(addr); err != nil {
		t.Fatalf("the first swap: %v", err)
	}
	if err := s.Swap(addr); err != nil {
		t.Fatalf("the second swap: %v", err)
	}
	if builds != 1 {
		t.Errorf("the address was built %d times", builds)
	}
	if got, err := get(t, addr); err != nil || got != "only" {
		t.Errorf("the address answered %q, %v", got, err)
	}
}

// Shutdown releases the socket, refuses further swaps, and is idempotent
// because a signal handler and a deferred call both reaching it is ordinary.
func TestShutdownReleasesAndRefuses(t *testing.T) {
	addr := freeAddr(t)
	s := NewServe(builderFor(t, "x"), nil)
	if err := s.Swap(addr); err != nil {
		t.Fatalf("Swap: %v", err)
	}

	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := s.Shutdown(); err != nil {
		t.Errorf("the second Shutdown returned %v", err)
	}
	if !errors.Is(s.Swap(freeAddr(t)), ErrStopped) {
		t.Error("a swap after shutdown was accepted")
	}

	// The address is free again, which is what "released" means.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the address was not released: %v", err)
	}
	if cerr := ln.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
}

// Two swaps at once are serialised, so one generation is current at the end
// rather than two sockets both believing they are.
func TestConcurrentSwapsAreSerialised(t *testing.T) {
	addrs := []string{freeAddr(t), freeAddr(t), freeAddr(t)}
	s := NewServe(builderFor(t, "x"), nil)
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for _, addr := range addrs {
		wg.Add(1)
		task.Go(context.Background(), "server: concurrent swap", func() {
			defer wg.Done()
			if err := s.Swap(addr); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	// All three should succeed: they are different addresses and the lock
	// serialises them rather than refusing the losers.
	if failures != 0 {
		t.Errorf("%d of %d concurrent swaps failed", failures, len(addrs))
	}

	cur := s.Current()
	if cur == nil {
		t.Fatal("no generation is current after three swaps")
	}
	if !strings.Contains(strings.Join(addrs, " "), cur.Addr) {
		t.Fatalf("the current address %q is not one of the three", cur.Addr)
	}

	// The current generation answers. The replaced ones are draining, which is
	// deliberately asynchronous: a swap returns as soon as its replacement is
	// serving and does not wait 15 seconds for the old one to finish. So this
	// checks that exactly one is current and that it works, not that the
	// others have already stopped.
	if got, err := get(t, cur.Addr); err != nil {
		t.Errorf("the current generation at %s does not answer: %v", cur.Addr, err)
	} else if got != "x" {
		t.Errorf("the current generation answered %q", got)
	}

	// They do stop, within the drain window. Waited for rather than assumed,
	// since a generation that never released its socket would make the next
	// swap to that address fail.
	deadline := time.Now().Add(DrainTimeout)
	for {
		live := 0
		for _, addr := range addrs {
			if _, err := get(t, addr); err == nil {
				live++
			}
		}
		if live == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d addresses still answer after the drain window", live, len(addrs))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// slowListener accepts nothing until it is released, which is a serve loop
// that has not started yet.
type slowListener struct {
	net.Listener
	release <-chan struct{}
}

func (l *slowListener) Accept() (net.Conn, error) {
	<-l.release
	return l.Listener.Accept()
}

// The swap waits for the new generation to answer, not merely to bind.
//
// The kernel accepts connections onto the backlog of a bound socket whether or
// not anything is serving it, so a dial proves the bind and nothing else. This
// holds the accept loop closed and checks the swap has not returned: without
// the wait it returns immediately and drops the old generation for one that is
// not answering.
func TestASwapWaitsForTheAppNotJustTheSocket(t *testing.T) {
	addr := freeAddr(t)
	release := make(chan struct{})

	s := NewServe(func(a string) (*fiber.App, net.Listener, error) {
		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.All("/*", func(c *fiber.Ctx) error { return c.SendString("slow") })
		ln, err := net.Listen("tcp", a)
		if err != nil {
			return nil, nil, err
		}
		return app, &slowListener{Listener: ln, release: release}, nil
	}, nil)
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	returned := make(chan error, 1)
	task.Go(context.Background(), "server: swap onto a stalled listener", func() {
		returned <- s.Swap(addr)
	})

	// The socket binds immediately; the app is not answering. The swap must
	// still be waiting.
	select {
	case err := <-returned:
		t.Fatalf("the swap returned before the app answered: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("the swap failed once the app answered: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the swap never returned after the app began answering")
	}
}
