//go:build linux

package smbpublish

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
)

// The publisher and the sidecar, both real, over a real socket.
//
// This is the check the whole channel exists for. Before it, the server wrote
// files and learned nothing: a rejected configuration, a share path that does
// not exist where the daemon runs, or an import that produced no credential all
// looked identical to success. Each half passing its own tests would not catch
// the two being wired to different directories, which is exactly the failure
// that is invisible until a client cannot connect.

func TestAPublishReachesTheSidecarAndComesBackWithItsAnswer(t *testing.T) {
	// Looked up on the path rather than at a fixed location, because the
	// distributions disagree about where it lives.
	if _, err := exec.LookPath("testparm"); err != nil {
		t.Skip("the configuration validator is not installed here, and the sidecar refuses to promote without it; the sidecar image has it")
	}

	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	socket := filepath.Join(dir, "agent.sock")

	// The sidecar, pointed at the same directory the publisher writes.
	paths := smbagent.DefaultPaths()
	paths.ConfigDir = configDir
	paths.StateDir = filepath.Join(dir, "state")
	paths.SmbConf = filepath.Join(dir, "smb.conf")
	paths.Passwd = filepath.Join(dir, "passwd")
	paths.Group = filepath.Join(dir, "group")

	if err := os.WriteFile(paths.Passwd, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Group, []byte("root:x:0:\nscsvc:x:1000:\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	agent := smbagent.NewAgent(paths, smbagent.Mode{Kind: smbagent.ModeSupervise}, quiet, clock.System())
	// The agent starts a daemon, and this test owns it. Leaving it running
	// outlives the test binary, which is what the first run of this test did.
	t.Cleanup(func() {
		if err := agent.Shutdown(); err != nil {
			t.Errorf("the daemon did not stop: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	smbagent.ServeInBackground(ctx, socket, agent, configDir, quiet)
	waitForSocket(t, socket)

	report, err := Publish(ctx, Deps{
		Auth: &fakeAccounts{}, ConfigDir: configDir, Socket: socket,
	}, enabledConfig())
	if err != nil {
		t.Fatalf("the publish did not reach the sidecar: %v", err)
	}

	// The sidecar answered about its own namespace, which is the whole point:
	// these are values this side cannot see.
	if report.Interfaces == "" {
		t.Error("the sidecar reported no bind line, so nothing was applied")
	}
	if !strings.HasPrefix(report.Interfaces, "lo") {
		t.Errorf("the bind line is %q, want loopback leading it", report.Interfaces)
	}
	if report.Smbd == "" {
		t.Error("the sidecar reported no action")
	}
}

// Turning it off travels the same channel, and the sidecar says it stopped.
func TestTurningItOffReachesTheSidecar(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	socket := filepath.Join(dir, "agent.sock")

	paths := smbagent.DefaultPaths()
	paths.ConfigDir = configDir
	paths.StateDir = filepath.Join(dir, "state")
	paths.SmbConf = filepath.Join(dir, "smb.conf")
	paths.Passwd = filepath.Join(dir, "passwd")
	paths.Group = filepath.Join(dir, "group")

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	agent := smbagent.NewAgent(paths, smbagent.Mode{Kind: smbagent.ModeSupervise}, quiet, clock.System())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	smbagent.ServeInBackground(ctx, socket, agent, configDir, quiet)
	waitForSocket(t, socket)

	off := enabledConfig()
	off.Enabled = false
	report, err := Publish(ctx, Deps{Auth: &fakeAccounts{}, ConfigDir: configDir, Socket: socket}, off)
	if err != nil {
		t.Fatalf("turning it off did not reach the sidecar: %v", err)
	}
	if report.Smbd != smbagent.ActionStopped {
		t.Fatalf("the sidecar reported %q, want the daemon stopped", report.Smbd)
	}
}

// A sidecar that is not there is reported as itself. The files are written
// either way, so the poll on the other side still applies them; what is lost
// is the answer, and the caller is told that rather than shown a success it
// did not get.
func TestASidecarThatIsNotThereIsReportedNotHidden(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")

	_, err := Publish(context.Background(), Deps{
		Auth: &fakeAccounts{}, ConfigDir: configDir,
		Socket: filepath.Join(dir, "absent.sock"),
	}, enabledConfig())
	if err == nil {
		t.Fatal("an unreachable sidecar reported success")
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("the failure reads as %q, which does not say the answer is what was lost", err)
	}
	// The files are there regardless, so the sidecar's own poll still applies
	// them when it comes back.
	if _, serr := os.Stat(filepath.Join(configDir, "smb.conf")); serr != nil {
		t.Error("nothing was written, so the poll has nothing to pick up either")
	}
}

func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	const tries = 500
	for range tries {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the sidecar's control socket never appeared")
}
