//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// closeQuietly closes what a test opened. The failure is discarded on purpose:
// every caller here is finished with the connection, and a close that fails
// tells the test nothing about the behaviour it is asserting.
func closeQuietly(c io.Closer) {
	_ = c.Close() //nolint:errcheck // see above: the caller is done and a failure here proves nothing.
}

// stubHandler answers with whatever a test planted.
type stubHandler struct {
	mu      sync.Mutex
	apply   Report
	last    Report
	applies int
}

func (h *stubHandler) Apply(context.Context) Report {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.applies++
	return h.apply
}

func (h *stubHandler) Last() Report {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

func (h *stubHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.applies
}

// serve starts an agent on a socket in a temp directory and returns its path.
func serve(t *testing.T, h Handler) string {
	t.Helper()
	dir := t.TempDir()
	socket := filepath.Join(dir, "agent.sock")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	task.Go(ctx, "smb agent under test", func() {
		done <- Serve(ctx, socket, h, dir, clock.System(), quiet())
	})

	// The listener is bound asynchronously, so wait for the socket to accept
	// rather than sleeping for a fixed period.
	clk := clock.System()
	deadline := clk.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", socket); err == nil {
			closeQuietly(c)
			break
		}
		if clk.Now().After(deadline) {
			cancel()
			t.Fatal("the agent never started listening")
		}
		time.Sleep(2 * time.Millisecond)
	}

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after cancellation")
		}
	})
	return socket
}

// ask sends raw bytes and returns the raw answer, so a test can drive the
// protocol without going through the client.
func ask(t *testing.T, socket string, req []byte) string {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(conn)
	if derr := conn.SetDeadline(clock.System().Now().Add(10 * time.Second)); derr != nil {
		t.Fatal(derr)
	}
	if _, werr := conn.Write(req); werr != nil {
		t.Fatal(werr)
	}
	answer, rerr := io.ReadAll(io.LimitReader(conn, MaxReportBytes))
	if rerr != nil {
		t.Fatal(rerr)
	}
	return string(answer)
}

func TestApplyRoundTrip(t *testing.T) {
	want := Report{OK: true, Shares: []string{"docs"}, Interfaces: "lo eth0", Smbd: ActionReloaded}
	h := &stubHandler{apply: want}
	socket := serve(t, h)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	got, err := Apply(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || len(got.Shares) != 1 || got.Shares[0] != "docs" || got.Smbd != ActionReloaded {
		t.Errorf("the report did not round-trip: %+v", got)
	}
	if h.count() != 1 {
		t.Errorf("the handler ran %d applies, want 1", h.count())
	}
}

// Status repeats the previous report rather than performing a pass, which is
// what makes it safe for a status screen to poll.
func TestStatusDoesNotApply(t *testing.T) {
	h := &stubHandler{last: Report{OK: true, Smbd: ActionUnchanged}}
	socket := serve(t, h)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if _, err := Status(ctx, socket); err != nil {
		t.Fatal(err)
	}
	if h.count() != 0 {
		t.Errorf("a status request performed %d applies, want 0", h.count())
	}
}

// The bound this file exists for.
//
// A bufio.Reader does not limit a line: its buffer sizes one fill, while
// ReadString grows its own result until it finds the delimiter. A peer that
// sends no newline is therefore bounded by the peer. This drives a real socket
// with far more than the limit and requires the agent to refuse rather than
// accumulate it.
func TestAnOversizedRequestIsRefused(t *testing.T) {
	h := &stubHandler{apply: Report{OK: true}}
	socket := serve(t, h)

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(conn)
	if derr := conn.SetDeadline(clock.System().Now().Add(20 * time.Second)); derr != nil {
		t.Fatal(derr)
	}

	// 8 MiB with no newline, which is 2048 times the request bound.
	const flood = 8 << 20
	chunk := []byte(strings.Repeat("x", 1<<16))
	task.Go(t.Context(), "smb agent flood writer", func() {
		for sent := 0; sent < flood; sent += len(chunk) {
			if _, werr := conn.Write(chunk); werr != nil {
				return
			}
		}
	})

	answer, rerr := io.ReadAll(io.LimitReader(conn, MaxReportBytes))
	if rerr != nil && len(answer) == 0 {
		t.Fatalf("the agent sent nothing: %v", rerr)
	}

	var report Report
	if uerr := json.Unmarshal([]byte(strings.TrimSpace(string(answer))), &report); uerr != nil {
		t.Fatalf("the answer is not a report: %q", answer)
	}
	if report.Smbd != ActionFailed {
		t.Errorf("an oversized request produced %q, want a refusal", report.Smbd)
	}
	if !strings.Contains(report.Error, "exceeded") {
		t.Errorf("the refusal does not name the bound: %q", report.Error)
	}
	if h.count() != 0 {
		t.Errorf("an oversized request reached the handler %d times", h.count())
	}
}

// A truncated line must not be parsed. Whichever prefix happens to be valid
// JSON is not the request the peer sent.
func TestATruncatedRequestIsNotParsed(t *testing.T) {
	h := &stubHandler{apply: Report{OK: true}}
	socket := serve(t, h)

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(conn)
	if derr := conn.SetDeadline(clock.System().Now().Add(10 * time.Second)); derr != nil {
		t.Fatal(derr)
	}

	// A valid request followed by padding past the bound, with no newline. A
	// reader that stopped at the buffer and parsed what it held would find an
	// apply request and run a pass.
	//
	// The write is allowed to fail partway. The agent refuses as soon as it has
	// read its limit and closes, which resets the rest of the write; that reset
	// is the refusal working rather than a test failure, so what is asserted is
	// the answer and the handler count.
	req := []byte(`{"op":"apply"}` + strings.Repeat(" ", MaxRequestBytes*4))
	task.Go(t.Context(), "smb agent truncation writer", func() {
		_, _ = conn.Write(req) //nolint:errcheck // the refusal closes the socket mid-write; that reset is the assertion.
	})

	answer, rerr := io.ReadAll(io.LimitReader(conn, MaxReportBytes))
	if len(answer) == 0 {
		t.Fatalf("the agent sent nothing: %v", rerr)
	}

	var report Report
	if uerr := json.Unmarshal([]byte(strings.TrimSpace(string(answer))), &report); uerr != nil {
		t.Fatalf("the answer is not a report: %q", answer)
	}
	if report.Smbd != ActionFailed {
		t.Errorf("a truncated request produced %q, want a refusal", report.Smbd)
	}
	if h.count() != 0 {
		t.Errorf("a truncated request reached the handler %d times", h.count())
	}
}

func TestMalformedAndUnknownRequestsRefuse(t *testing.T) {
	h := &stubHandler{apply: Report{OK: true}}
	socket := serve(t, h)

	for _, c := range []struct{ name, body string }{
		{"not json", "this is not json\n"},
		{"unknown op", `{"op":"destroy"}` + "\n"},
		{"empty object", `{}` + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var report Report
			answer := ask(t, socket, []byte(c.body))
			if err := json.Unmarshal([]byte(strings.TrimSpace(answer)), &report); err != nil {
				t.Fatalf("the answer is not a report: %q", answer)
			}
			if report.Smbd != ActionFailed {
				t.Errorf("%s produced %q, want a refusal", c.name, report.Smbd)
			}
		})
	}
	if h.count() != 0 {
		t.Errorf("a malformed request reached the handler %d times", h.count())
	}
}

// One request per connection. A second request on the same connection is not
// answered, because the agent has already closed it.
func TestOneRequestPerConnection(t *testing.T) {
	h := &stubHandler{apply: Report{OK: true}}
	socket := serve(t, h)

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(conn)
	if derr := conn.SetDeadline(clock.System().Now().Add(10 * time.Second)); derr != nil {
		t.Fatal(derr)
	}

	if _, werr := conn.Write([]byte(`{"op":"apply"}` + "\n" + `{"op":"apply"}` + "\n")); werr != nil {
		t.Fatal(werr)
	}
	answer, _ := io.ReadAll(io.LimitReader(conn, MaxReportBytes)) //nolint:errcheck // a reset after one answer is the behaviour under test.
	if strings.Count(strings.TrimSpace(string(answer)), "\n") != 0 {
		t.Errorf("more than one answer arrived on one connection: %q", answer)
	}
	if h.count() != 1 {
		t.Errorf("two requests on one connection produced %d applies, want 1", h.count())
	}
}

// The client's own bound, which is the mirror of the agent's.
func TestTheClientRefusesAnOversizedReport(t *testing.T) {
	// A server that answers with far more than the report bound and no
	// newline, which is what a compromised or wedged agent looks like.
	dir := t.TempDir()
	socket := filepath.Join(dir, "flood.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(ln)

	task.Go(t.Context(), "smb oversized report writer", func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer closeQuietly(conn)
		chunk := []byte(strings.Repeat("x", 1<<16))
		for sent := 0; sent < 4*MaxReportBytes; sent += len(chunk) {
			if _, werr := conn.Write(chunk); werr != nil {
				return
			}
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	_, derr := Apply(ctx, socket)
	if derr == nil {
		t.Fatal("an oversized report was accepted")
	}
	if !errors.Is(derr, ErrProtocol) {
		t.Errorf("the refusal is %v, want a protocol error", derr)
	}
}

// An absent agent is a distinct answer from a broken one, because a deployment
// with no SMB sidecar is a legitimate configuration.
func TestAnAbsentAgentIsNotAFailure(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "nothing.sock")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, err := Apply(ctx, socket)
	if !errors.Is(err, ErrNotListening) {
		t.Errorf("a missing socket answered %v, want ErrNotListening", err)
	}
}

// A socket that exists with nobody accepting is the same answer as no socket.
func TestARefusedConnectionReadsAsAbsent(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "dead.sock")

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	// Closing the listener leaves the socket file behind, which is exactly the
	// state a killed agent leaves.
	if cerr := ln.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if _, aerr := Apply(ctx, socket); !errors.Is(aerr, ErrNotListening) {
		t.Errorf("a dead socket answered %v, want ErrNotListening", aerr)
	}
}

// The deadline reaches the connection, so an agent that accepts and then falls
// silent cannot hold a settings-screen request open.
func TestTheDeadlineReachesTheConnection(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "silent.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(ln)

	accepted := make(chan net.Conn, 1)
	task.Go(t.Context(), "smb silent agent", func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		// Accept and say nothing at all.
		accepted <- conn
	})

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	clk := clock.System()
	start := clk.Now()
	_, aerr := Apply(ctx, socket)
	elapsed := clk.Since(start)

	if aerr == nil {
		t.Fatal("a silent agent answered successfully")
	}
	if elapsed > 5*time.Second {
		t.Errorf("the call took %v, so the deadline did not reach the connection", elapsed)
	}
	if conn := <-accepted; conn != nil {
		closeQuietly(conn)
	}
}

// A killed agent leaves its socket file behind, and the next start has to bind
// anyway or the channel is lost until somebody removes the file by hand.
func TestAStaleSocketFileDoesNotBlockTheBind(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "agent.sock")
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	task.Go(ctx, "smb agent over a stale socket", func() {
		done <- Serve(ctx, socket, &stubHandler{}, dir, clock.System(), quiet())
	})

	clk := clock.System()
	deadline := clk.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", socket); err == nil {
			closeQuietly(c)
			break
		}
		if clk.Now().After(deadline) {
			t.Fatal("the agent did not bind over a stale socket file")
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve returned %v", err)
	}
}

func TestFailedReportShape(t *testing.T) {
	r := FailedReport("the validator said no")
	if r.OK {
		t.Error("a failed report reports OK")
	}
	if r.Smbd != ActionFailed {
		t.Errorf("the action is %q", r.Smbd)
	}
	if r.Error != "the validator said no" {
		t.Errorf("the reason is %q", r.Error)
	}
}
