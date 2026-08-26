// Linux only, because what it tests is.
//go:build linux

package server

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// The listener swap.
//
// The claim is that a deployment can move the address it serves on without the
// process going down and without dropping what it is holding, and that an
// address nothing can bind leaves the old one serving rather than taking the
// deployment offline.

// testServe builds a Serve on a free loopback port whose handler is fn.
func testServe(t *testing.T, fn http.HandlerFunc) *Serve {
	t.Helper()
	cert := selfSignedForTest(t)
	build := func(string) (*http.Server, error) {
		return newHTTPServer(fn, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}), nil
	}
	s, err := NewServe(context.Background(), testLogger(t), freeAddr(t), build)
	if err != nil {
		t.Fatalf("NewServe: %v", err)
	}
	t.Cleanup(func() { s.Stop(context.Background()) })
	return s
}

// A saved bind address moves the socket, and the old one stops answering.
func TestSwapMovesTheListener(t *testing.T) {
	s := testServe(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok")) //nolint:errcheck // a test handler with nowhere to report to.
	})
	first := s.Addr()
	if body := get(t, first); body != "ok" {
		t.Fatalf("the first listener answered %q", body)
	}

	second := freeAddr(t)
	if err := s.Swap(context.Background(), second); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	if s.Addr() != second {
		t.Fatalf("the address is %q after the swap, want %q", s.Addr(), second)
	}
	if body := get(t, second); body != "ok" {
		t.Fatalf("the new listener answered %q", body)
	}

	// The old socket goes once it has drained. It holds nothing here, so this
	// is the ordinary case rather than the one below. Timed off a channel
	// rather than a wall-clock deadline: one package in this tree reads the
	// clock, and a test is not it.
	deadline := time.After(5 * time.Second)
	for {
		if _, err := dial(first); err != nil {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the old listener is still accepting connections")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// The whole reason the new socket is bound first: an address nothing can bind
// must not take the deployment offline.
func TestASwapThatCannotBindKeepsTheOldListener(t *testing.T) {
	s := testServe(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok")) //nolint:errcheck // a test handler with nowhere to report to.
	})
	before := s.Addr()

	// A port already held by something else, which is the ordinary way an
	// operator gets this wrong.
	held, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatalf("holding a port: %v", lerr)
	}
	defer func() {
		if cerr := held.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	if err := s.Swap(context.Background(), held.Addr().String()); err == nil {
		t.Fatal("a swap onto a held port was accepted")
	}
	if s.Addr() != before {
		t.Fatalf("a failed swap moved the address to %q", s.Addr())
	}
	if body := get(t, before); body != "ok" {
		t.Fatalf("the old listener answered %q after a failed swap", body)
	}
}

// A request in flight when the address moves finishes on the socket it
// arrived on. Without the drain it would be cut, which is what makes a bind
// change something an administrator avoids doing while anybody is using it.
func TestARequestInFlightSurvivesTheSwap(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	s := testServe(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(entered)
		<-release
		_, _ = w.Write([]byte("finished")) //nolint:errcheck // a test handler with nowhere to report to.
	})
	first := s.Addr()

	type result struct {
		body string
		err  error
	}
	done := make(chan result, 1)
	task.Go(context.Background(), "in-flight request", func() {
		body, err := getErr(first)
		done <- result{body: body, err: err}
	})

	// The request is in the handler before the swap: the header is already
	// written, so the connection is one the drain has to wait for.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached the handler")
	}

	if err := s.Swap(context.Background(), freeAddr(t)); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	close(release)

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("the in-flight request failed across the swap: %v", r.err)
		}
		if r.body != "finished" {
			t.Fatalf("the in-flight request answered %q", r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the in-flight request never finished")
	}
}

// Swapping to the address already in service is not a swap. Rebinding it
// would mean closing the only listener and hoping the new bind wins the race
// against anything else on the machine.
func TestSwapToTheSameAddressDoesNothing(t *testing.T) {
	s := testServe(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok")) //nolint:errcheck // a test handler with nowhere to report to.
	})
	addr := s.Addr()
	if err := s.Swap(context.Background(), addr); err != nil {
		t.Fatalf("Swap to the same address: %v", err)
	}
	if body := get(t, addr); body != "ok" {
		t.Fatalf("the listener answered %q", body)
	}
}

// freeAddr is a loopback address nothing is on, released before it is
// returned. A race with something else on the machine is possible and is what
// port zero costs; the alternative is a fixed port two runs would collide on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	addr := ln.Addr().String()
	if cerr := ln.Close(); cerr != nil {
		t.Fatalf("releasing the port: %v", cerr)
	}
	return addr
}

func testClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			//nolint:gosec // G402 reads the field: the certificate is this test's own, minted per run.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}
}

func get(t *testing.T, addr string) string {
	t.Helper()
	body, err := getErr(addr)
	if err != nil {
		t.Fatalf("GET %s: %v", addr, err)
	}
	return body
}

func getErr(addr string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/", nil) //nolint:gosec // G704 reads the variable: the address is this test's own listener.
	if err != nil {
		return "", err
	}
	resp, derr := testClient().Do(req)
	if derr != nil {
		return "", derr
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // the body is drained below.
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(body), rerr
}

// dial reports whether anything is accepting on addr.
func dial(addr string) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// selfSignedForTest mints a certificate for 127.0.0.1, through the same
// generator the product uses.
func selfSignedForTest(t *testing.T) tls.Certificate {
	t.Helper()
	certPEM, keyPEM, err := generateSelfSigned("localhost")
	if err != nil {
		t.Fatalf("generating a certificate: %v", err)
	}
	cert, perr := tls.X509KeyPair(certPEM, keyPEM)
	if perr != nil {
		t.Fatalf("parsing the certificate: %v", perr)
	}
	return cert
}

// testLogger keeps the swap's own lines out of a passing run's output.
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
