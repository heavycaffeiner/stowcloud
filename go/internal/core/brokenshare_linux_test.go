//go:build linux

package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"golang.org/x/sys/unix"
)

// A share whose backing folder goes away.
//
// The failure this covers is quiet by construction: a share root is an open
// descriptor, and a descriptor outlives the directory it names. Unmount the
// disk under a running server and nothing re-checks anything, so the share
// fails one request at a time with no way to tell which share is at fault, and
// the share itself stays in the list looking fine.
//
// Unmounting for real needs privileges this test does not have on the host, so
// it runs in a re-executed child that owns a user namespace, the same shape
// the nested-mount move proof uses.

const brokenShareMarker = "SC_TEST_BROKEN_SHARE"

// brokenShareProved is what the child prints once it has seen the share go
// broken and come back. The parent requires it, because a child that skipped
// also exits zero.
const brokenShareProved = "BROKEN-SHARE-DETECTED"

func TestAnUnmountedShareGoesBrokenAndComesBack(t *testing.T) {
	if dir := os.Getenv(brokenShareMarker); dir != "" {
		runBrokenShareChild(t, dir)
		return
	}

	dir := t.TempDir()
	r, _, err := vfs.RegisterShareRoot(1, dir, vfs.DefaultSharePolicy())
	if err != nil {
		t.Skipf("the temporary directory is on a filesystem this build refuses: %v", err)
	}
	if cerr := r.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run", "^"+t.Name()+"$", "-test.v") //nolint:gosec // G204 reads the variable: the command is this test binary re-executing itself.
	cmd.Env = append(os.Environ(), brokenShareMarker+"="+dir)
	cmd.SysProcAttr = &unix.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
	}

	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "SKIP-NO-NAMESPACE") {
		t.Skipf("this host does not allow an unprivileged user namespace:\n%s", out)
	}
	if err != nil {
		t.Fatalf("the child failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), brokenShareProved) {
		t.Fatalf("the child did not report the proof:\n%s", out)
	}
}

func runBrokenShareChild(t *testing.T, dir string) {
	t.Helper()

	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Skip("SKIP-NO-NAMESPACE: cannot make the mount tree private")
	}

	// The share is the mount point, so unmounting is exactly the operator
	// pulling the disk out from under a running server.
	host := filepath.Join(dir, "vol")
	if err := os.MkdirAll(host, 0o755); err != nil { //nolint:gosec // G703 traces the path from the environment: this test's own temporary directory.
		t.Fatalf("mkdir: %v", err)
	}
	if err := unix.Mount("tmpfs", host, "tmpfs", 0, ""); err != nil {
		t.Skipf("SKIP-NO-NAMESPACE: cannot mount inside the namespace: %v", err)
	}
	mounted := true
	defer func() {
		if mounted {
			if uerr := unix.Unmount(host, unix.MNT_DETACH); uerr != nil {
				t.Errorf("unmounting: %v", uerr)
			}
		}
	}()
	if err := os.WriteFile(filepath.Join(host, "a.txt"), []byte("payload"), 0o644); err != nil { //nolint:gosec // G703 as above.
		t.Fatalf("writing: %v", err)
	}

	c := nestedMoveCore(t, host)

	// Working, to begin with.
	if _, err := c.Resolve(UserID(42), mustVpath(t, "docs/a.txt"), acl.Read); err != nil {
		t.Fatalf("the share did not work before the unmount: %v", err)
	}
	if broke, _ := c.ProbeShares(ctx()); len(broke) != 0 {
		t.Fatalf("a healthy share was reported broken: %+v", broke)
	}

	// The disk goes away.
	if err := unix.Unmount(host, unix.MNT_DETACH); err != nil {
		t.Fatalf("unmounting: %v", err)
	}
	mounted = false

	broke, _ := c.ProbeShares(ctx())
	if len(broke) != 1 {
		t.Fatalf("the probe did not notice the unmount: %+v", broke)
	}
	if broke[0].Name != "docs" {
		t.Errorf("the wrong share broke: %+v", broke[0])
	}

	// Still listed, which is the whole point: dropped, it is indistinguishable
	// from a share somebody deleted.
	defs := c.Shares()
	if len(defs) != 1 || defs[0].BrokenReason == "" {
		t.Fatalf("a broken share is not listed with a reason: %+v", defs)
	}

	// And a request against it says so rather than reporting a path that is
	// perfectly good as missing.
	_, rerr := c.Resolve(UserID(42), mustVpath(t, "docs/a.txt"), acl.Read)
	if !errors.Is(rerr, ErrShareBroken) {
		t.Fatalf("resolving a broken share answered %v, want the broken error", rerr)
	}
	var sb *ShareBrokenError
	if !errors.As(rerr, &sb) || sb.Share != "docs" {
		t.Errorf("the error does not name the share: %v", rerr)
	}
	// Emphatically not not-found: telling somebody their folder does not exist
	// when a drive did not come back sends them looking in the wrong place.
	if errors.Is(rerr, ErrNotFound) {
		t.Error("a broken share reported its contents as missing")
	}

	// The disk comes back, and the share has to start working again without
	// anybody pressing anything.
	if err := unix.Mount("tmpfs", host, "tmpfs", 0, ""); err != nil {
		t.Fatalf("remounting: %v", err)
	}
	mounted = true
	if err := os.WriteFile(filepath.Join(host, "a.txt"), []byte("payload"), 0o644); err != nil { //nolint:gosec // G703 as above.
		t.Fatalf("rewriting: %v", err)
	}

	_, healed := c.ProbeShares(ctx())
	if len(healed) != 1 {
		t.Fatalf("the probe did not notice the remount: %+v", healed)
	}
	if _, err := c.Resolve(UserID(42), mustVpath(t, "docs/a.txt"), acl.Read); err != nil {
		t.Fatalf("the share did not work after the remount: %v", err)
	}
	if defs := c.Shares(); defs[0].BrokenReason != "" {
		t.Errorf("the share is still marked broken: %+v", defs[0])
	}
	t.Log(brokenShareProved)
}

// A share whose path is missing at startup is registered broken rather than
// dropped. Dropped, it was absent from the admin list and from every user's
// roots, so a drive that did not come back looked exactly like a share
// somebody had deleted, with the only trace a line on the health endpoint.
func TestAShareWithAMissingPathIsStillListed(t *testing.T) {
	dir := t.TempDir()
	c := nestedMoveCore(t, dir)

	// Registering a path that is not there is the startup case.
	missing := filepath.Join(dir, "gone")
	err := c.RegisterShare(ctx(), ShareDef{
		ID: 2, Name: "archive", Host: missing, Policy: vfs.DefaultSharePolicy(),
	})
	if err == nil {
		t.Fatal("registering a missing path succeeded")
	}
	c.RegisterBroken(ShareDef{ID: 2, Name: "archive", Host: missing}, err)

	var found bool
	for _, def := range c.Shares() {
		if def.Name == "archive" {
			found = true
			if def.BrokenReason != "missing" {
				t.Errorf("reason = %q, want missing", def.BrokenReason)
			}
		}
	}
	if !found {
		t.Fatalf("the broken share is not listed: %+v", c.Shares())
	}
	// No root to hand out, because a nil one would move the failure to
	// whoever dereferenced it.
	if _, ok := c.ShareRoot(2); ok {
		t.Error("a broken share handed out a root")
	}

	// The path comes back and the retry picks it up.
	if merr := os.MkdirAll(missing, 0o755); merr != nil { //nolint:gosec // G703 traces this test's own temporary directory.
		t.Fatalf("mkdir: %v", merr)
	}
	def, rerr := c.RetryShare(ctx(), 2)
	if rerr != nil {
		t.Fatalf("the retry failed after the path came back: %v", rerr)
	}
	if def.BrokenReason != "" {
		t.Errorf("the retried share is still broken: %+v", def)
	}
	if _, ok := c.ShareRoot(2); !ok {
		t.Error("the retried share has no root")
	}
}

// Removing a broken share has to work, and it is the repair that matters most:
// nothing re-probes a share that no longer exists, so a delete that failed
// would leave the deployment degraded over it forever. It answered 500,
// because unregistering closed a root a broken share does not have.
func TestABrokenShareCanBeRemoved(t *testing.T) {
	dir := t.TempDir()
	c := nestedMoveCore(t, dir)

	missing := filepath.Join(dir, "gone")
	err := c.RegisterShare(ctx(), ShareDef{
		ID: 2, Name: "archive", Host: missing, Policy: vfs.DefaultSharePolicy(),
	})
	if err == nil {
		t.Fatal("registering a missing path succeeded")
	}
	c.RegisterBroken(ShareDef{ID: 2, Name: "archive", Host: missing}, err)

	if derr := c.DeleteShare(ctx(), 2); derr != nil {
		t.Fatalf("removing a broken share: %v", derr)
	}
	for _, def := range c.Shares() {
		if def.Name == "archive" {
			t.Fatalf("the removed share is still listed: %+v", def)
		}
	}
}
