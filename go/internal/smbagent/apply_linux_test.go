//go:build linux

package smbagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
)

// The apply cycle, run for real against the tools it drives.
//
// Skipped where those tools are not installed. That is not a gap this test can
// close: what it checks is the decision the agent makes and the file it
// promotes, and both need a validator that answers.

func requireTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"testparm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed here; the sidecar image has it", bin)
		}
	}
}

const applyConf = `[global]
  workgroup = WORKGROUP
  disable netbios = yes
  bind interfaces only = yes
  interfaces = lo
  hosts allow = 127.0.0.0/8 ::1/128

[Share]
  path = %s
`

// newAgent builds one pointed entirely at a temporary directory.
func newAgent(t *testing.T) (*Agent, Paths) {
	t.Helper()

	dir := t.TempDir()
	paths := DefaultPaths()
	paths.ConfigDir = filepath.Join(dir, "config")
	paths.StateDir = filepath.Join(dir, "state")
	paths.SmbConf = filepath.Join(dir, "smb.conf")
	paths.Passwd = filepath.Join(dir, "passwd")
	paths.Group = filepath.Join(dir, "group")

	if err := os.MkdirAll(paths.ConfigDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Passwd, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Group, []byte("root:x:0:\nscsvc:x:1000:\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	agent := NewAgent(paths, Mode{Kind: ModeSupervise}, quietLog(), clock.System())
	t.Cleanup(func() {
		if err := agent.Shutdown(); err != nil {
			t.Errorf("the daemon did not stop: %v", err)
		}
	})
	return agent, paths
}

func writeRendered(t *testing.T, paths Paths, sharePath string) {
	t.Helper()
	body := strings.Replace(applyConf, "%s", sharePath, 1)
	if err := os.WriteFile(paths.renderedConf(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The promoted file is the candidate, not the rendered one: the logging
// directives and the expanded scope are added on the way through.
func TestAnApplyPromotesTheCandidateNotTheRenderedFile(t *testing.T) {
	requireTools(t)
	agent, paths := newAgent(t)
	writeRendered(t, paths, t.TempDir())

	agent.Apply()

	promoted, err := os.ReadFile(paths.SmbConf)
	if err != nil {
		t.Fatalf("nothing was promoted: %v", err)
	}
	if !strings.Contains(string(promoted), "log level = 1 auth_audit:3") {
		t.Error("the promoted file carries no audit level, so a failed login is never logged and the ban daemon has nothing to read")
	}
	// The scope was expanded from this machine's own devices, so the closed
	// case the server rendered is not what the daemon runs.
	if !strings.HasPrefix(BoundInterfaces(string(promoted)), "lo") {
		t.Errorf("the promoted bind line is %q", BoundInterfaces(string(promoted)))
	}
}

// A share path that does not exist here is reported. The validator does not
// check it, and the symptom without this is a client being told the network
// name is invalid while every file on the server's side looks right.
func TestAShareWhosePathIsMissingIsReported(t *testing.T) {
	requireTools(t)
	agent, paths := newAgent(t)
	writeRendered(t, paths, "/this/path/does/not/exist")

	report := agent.Apply()
	if len(report.MissingPaths) != 1 {
		t.Fatalf("missing paths = %v, want the one that does not exist", report.MissingPaths)
	}
	if report.OK {
		t.Error("a share nobody can open reported as ok")
	}
	// Still promoted: one missing path does not stop the other shares working.
	if _, err := os.Stat(paths.SmbConf); err != nil {
		t.Error("a missing share path stopped the whole promotion")
	}
}

// A rejected candidate leaves the previous configuration running.
func TestARejectedCandidateKeepsWhatWasAlreadyServing(t *testing.T) {
	requireTools(t)
	agent, paths := newAgent(t)

	writeRendered(t, paths, t.TempDir())
	agent.Apply()
	good, err := os.ReadFile(paths.SmbConf)
	if err != nil {
		t.Fatal(err)
	}

	// Not a configuration at all, which the validator refuses.
	if err := os.WriteFile(paths.renderedConf(), []byte("this is not a configuration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := agent.Apply()
	if report.OK {
		t.Error("a rejected candidate reported as applied")
	}

	after, rerr := os.ReadFile(paths.SmbConf)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(after) != string(good) {
		t.Fatal("a rejected candidate replaced the configuration that was serving")
	}
}

// The distinction the agent exists for: a moved bind line needs the process
// replaced, because the daemon binds its sockets once at startup and a reload
// does not revisit them.
func TestAMovedBindLineIsARestartAndAnUnchangedOneIsNeither(t *testing.T) {
	if NeedsRestart("lo", "lo eth0") != true {
		t.Error("a widened bind line did not ask for a restart, so SMB would stay on loopback")
	}
	if NeedsRestart("lo eth0", "lo") != true {
		t.Error("a narrowed bind line did not ask for a restart, so SMB would stay bound where it was revoked")
	}
	if NeedsRestart("lo eth0", "lo eth0") != false {
		t.Error("an unchanged bind line asked for a restart, which drops every transfer in flight")
	}
}

// Applying the same state twice does nothing the second time, so a poll that
// finds no change does not restart the daemon every couple of seconds.
func TestApplyingTheSameStateTwiceIsUnchangedTheSecondTime(t *testing.T) {
	requireTools(t)
	agent, paths := newAgent(t)
	writeRendered(t, paths, t.TempDir())

	if first := agent.Apply(); first.Smbd != ActionStarted {
		t.Fatalf("the first apply reported %q, want the daemon started", first.Smbd)
	}
	if second := agent.Apply(); second.Smbd != ActionUnchanged {
		t.Fatalf("the second apply reported %q, want nothing to do", second.Smbd)
	}
}

// The fingerprint is what the poll compares, so an unchanged directory has to
// produce an unchanged one.
func TestTheFingerprintIsStableWhileNothingChanges(t *testing.T) {
	agent, paths := newAgent(t)
	writeRendered(t, paths, t.TempDir())

	first := agent.Fingerprint()
	for range 4 {
		if agent.Fingerprint() != first {
			t.Fatal("the fingerprint moved while nothing changed, so the poll would apply on every pass")
		}
	}
}

// Removing the rendered configuration is the off switch, and it takes the
// managed accounts with it: leaving them behind keeps a revoked credential
// working.
func TestRemovingTheRenderedConfigurationTearsDown(t *testing.T) {
	requireTools(t)
	agent, paths := newAgent(t)
	writeRendered(t, paths, t.TempDir())

	if err := os.WriteFile(paths.renderedPasswd(),
		[]byte("alice:x:2001:1000::/nonexistent:/sbin/nologin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent.Apply()

	after, err := os.ReadFile(paths.Passwd)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "alice") {
		t.Fatal("the account was never written, so the teardown below proves nothing")
	}

	if err := os.Remove(paths.renderedConf()); err != nil {
		t.Fatal(err)
	}
	report := agent.Apply()
	if report.Smbd != ActionStopped {
		t.Fatalf("the teardown reported %q, want the daemon stopped", report.Smbd)
	}

	gone, rerr := os.ReadFile(paths.Passwd)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(string(gone), "alice") {
		t.Error("a managed account survived the teardown, so its credential still works")
	}
	if !strings.Contains(string(gone), "root") {
		t.Error("an account this agent did not create was removed by the teardown")
	}
}

// An account colliding with a real one refuses the whole sync rather than
// adopting it: adopting gives that name a credential, and removing it later
// deletes a system user.
func TestACollidingAccountRefusesTheWholeSync(t *testing.T) {
	requireTools(t)
	agent, paths := newAgent(t)
	writeRendered(t, paths, t.TempDir())

	if err := os.WriteFile(paths.Passwd,
		[]byte("root:x:0:0:root:/root:/bin/bash\nalice:x:1001:1001:a real person:/home/alice:/bin/bash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.renderedPasswd(),
		[]byte("alice:x:2001:1000::/nonexistent:/sbin/nologin\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report := agent.Apply()
	if report.OK {
		t.Fatal("a colliding account was adopted")
	}
	if !strings.Contains(report.Error, "refusing to sync") {
		t.Errorf("the refusal reads as %q", report.Error)
	}
	// Nothing was promoted, because the roster cannot be applied at all.
	if _, err := os.Stat(paths.SmbConf); err == nil {
		t.Error("a refused sync still promoted a configuration")
	}
}
