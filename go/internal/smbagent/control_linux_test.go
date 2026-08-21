//go:build linux

package smbagent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
)

// The channel is checked by running both halves, because the whole reason it
// exists is that writing files and hoping told the server nothing. A test that
// only encodes and decodes the types would not have caught either half being
// wired to the wrong path.

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// listen starts a real agent on a real socket and returns its path.
func listen(t *testing.T) (string, *Agent) {
	t.Helper()

	dir := t.TempDir()
	socket := filepath.Join(dir, "agent.sock")
	paths := DefaultPaths()
	paths.ConfigDir = filepath.Join(dir, "config")
	paths.StateDir = filepath.Join(dir, "state")
	paths.SmbConf = filepath.Join(dir, "smb.conf")
	paths.Passwd = filepath.Join(dir, "passwd")
	paths.Group = filepath.Join(dir, "group")

	agent := NewAgent(paths, Mode{Kind: ModeSupervise}, quietLog(), clock.System())

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ServeInBackground(ctx, socket, agent, paths.ConfigDir, quietLog())

	// The listener binds on another goroutine, so the socket is waited for
	// rather than assumed. Counted rather than timed, because reading the wall
	// clock is what the gate refuses everywhere outside the clock package.
	const tries = 500
	for range tries {
		if _, err := os.Stat(socket); err == nil {
			return socket, agent
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the control socket never appeared")
	return "", nil
}

// A status request travels both halves and comes back as a report.
func TestAStatusRequestGetsAnAnswer(t *testing.T) {
	socket, agent := listen(t)

	want := Report{OK: true, Shares: []string{"Share"}, Interfaces: "lo eth0", Smbd: ActionReloaded}
	agent.mu.Lock()
	agent.last = want
	agent.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	got, err := Status(ctx, socket)
	if err != nil {
		t.Fatalf("the status request failed: %v", err)
	}
	if !got.OK || got.Interfaces != "lo eth0" || got.Smbd != ActionReloaded {
		t.Fatalf("the report came back as %+v, want %+v", got, want)
	}
	if len(got.Shares) != 1 || got.Shares[0] != "Share" {
		t.Errorf("the shares came back as %v", got.Shares)
	}
}

// An apply with no rendered configuration is the off switch, and the agent
// says so rather than reporting a failure.
func TestAnApplyWithNothingRenderedReadsAsTurnedOff(t *testing.T) {
	socket, _ := listen(t)

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	got, err := Apply(ctx, socket)
	if err != nil {
		t.Fatalf("the apply request failed: %v", err)
	}
	if got.Smbd != ActionStopped {
		t.Fatalf("the agent reported %q, want the daemon stopped", got.Smbd)
	}
	if !got.OK {
		t.Errorf("turning SMB off reported as a failure: %s", got.Error)
	}
}

// Nothing listening is its own answer, because a deployment with no sidecar is
// a legitimate configuration rather than a fault.
func TestNothingListeningIsItsOwnAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := Apply(ctx, filepath.Join(t.TempDir(), "absent.sock"))
	if !errors.Is(err, ErrNotListening) {
		t.Fatalf("error = %v, want it reported as nothing listening", err)
	}
}

// A request the agent cannot read is refused with a report, not by dropping
// the connection: the server is waiting on an answer either way.
func TestAnUnintelligibleRequestIsAnswered(t *testing.T) {
	socket, _ := listen(t)

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck // a test connection.

	if _, err := conn.Write([]byte("this is not a request\n")); err != nil {
		t.Fatal(err)
	}
	body, rerr := io.ReadAll(conn)
	if rerr != nil {
		t.Fatalf("the agent dropped the connection instead of answering: %v", rerr)
	}
	if !strings.Contains(string(body), `"ok":false`) {
		t.Fatalf("the answer was %q, want a refusal", body)
	}
}

// A client that connects and says nothing must not hold the loop, or one stuck
// caller stops every later apply.
func TestASilentClientDoesNotHoldTheLoop(t *testing.T) {
	socket, _ := listen(t)

	stuck, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.Close() //nolint:errcheck // a test connection.

	// The next request has to be answered while the first one is still open.
	// The exchange bound is a few seconds, so this waits longer than that.
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout+5*time.Second)
	defer cancel()

	if _, err := Status(ctx, socket); err != nil {
		t.Fatalf("a silent client blocked the next request: %v", err)
	}
}
