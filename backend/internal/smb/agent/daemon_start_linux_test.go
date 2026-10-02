//go:build linux

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A daemon that exits as soon as it starts is reported as not started.
//
// smbd does exactly this when it cannot bind its port, which is every attempt
// in a rootless container: the supervisor reported a successful start on each
// one while the daemon died on each one, and the restart loop turned that into
// a start every two seconds that read as success. A fork that succeeded is not
// a daemon that is serving.
func TestAStartIsNotReportedUntilTheDaemonSurvivesIt(t *testing.T) {
	// A stand-in for smbd that fails the way a bind failure does: immediately,
	// with a non-zero status. Binding a privileged port to reproduce the real
	// thing needs privileges this test does not have and does not need.
	dir := t.TempDir()
	stub := filepath.Join(dir, "smbd")
	script := "#!/bin/sh\necho 'open_socket_in failed: Permission denied' >&2\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub daemon: %v", err)
	}
	// Prepended, not replaced: the stub is a shell script, so the real PATH
	// still has to resolve /bin/sh for it to run at all.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := NewSmbd(quiet())
	err := s.Start()
	if err == nil {
		t.Fatal("a daemon that exited immediately was reported as started")
	}
	if s.Running() {
		t.Error("the supervisor still believes a dead daemon is running")
	}
}

// A daemon that keeps running is reported as started.
//
// The other half: a supervisor that called every start a failure would pass
// the test above and never serve anything.
func TestAStartIsReportedWhenTheDaemonKeepsRunning(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "smbd")
	script := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stub daemon: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s := NewSmbd(quiet())
	if err := s.Start(); err != nil {
		t.Fatalf("a daemon that kept running was reported as failed: %v", err)
	}
	t.Cleanup(func() {
		if serr := s.Stop(); serr != nil {
			t.Errorf("stopping the stub daemon: %v", serr)
		}
	})
	if !s.Running() {
		t.Error("a running daemon is not reported as running")
	}
	if _, err := exec.LookPath("smbd"); err != nil {
		t.Fatalf("the stub was not on PATH, so this proved nothing: %v", err)
	}
}
