//go:build linux

package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"golang.org/x/sys/unix"
)

// The nested-mount move proof.
//
// A move whose destination directory is a separate mount inside the share
// cannot be a rename: the kernel answers EXDEV. Proving the decision is made
// against the destination directory rather than the share root needs a real
// mount, which needs privileges this test does not have on the host. A user
// namespace grants them inside itself, so the work runs in a re-executed child
// that owns one, the way the vfs package proves its own nested-mount rule.

const nestedMoveMarker = "SC_TEST_NESTED_MOVE"

// nestedMoveProved is what the child prints once it has placed the mount and
// seen the copy. The parent requires it, because a child that skipped also
// exits zero and also prints a pass.
const nestedMoveProved = "NESTED-MOVE-COPIED"

func TestMoveIntoANestedMountCopiesRatherThanFailing(t *testing.T) {
	if dir := os.Getenv(nestedMoveMarker); dir != "" {
		runNestedMoveChild(t, dir)
		return
	}

	dir := t.TempDir()
	// The share root has to be on a filesystem this build admits, or the child
	// proves nothing about moves.
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
	cmd.Env = append(os.Environ(), nestedMoveMarker+"="+dir)
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
	if !strings.Contains(string(out), nestedMoveProved) {
		t.Fatalf("the child did not report the proof:\n%s", out)
	}
}

func runNestedMoveChild(t *testing.T, dir string) {
	t.Helper()

	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Skip("SKIP-NO-NAMESPACE: cannot make the mount tree private")
	}

	nested := filepath.Join(dir, "vol")
	if err := os.MkdirAll(nested, 0o755); err != nil { //nolint:gosec // G703 traces the path from the environment: it is this test's own temporary directory, handed to its own child.
		t.Fatalf("mkdir: %v", err)
	}
	// tmpfs is admitted by the gate and is genuinely a separate device, which
	// is the whole point: the share root's device is the wrong answer for
	// anything inside it.
	if err := unix.Mount("tmpfs", nested, "tmpfs", 0, ""); err != nil {
		t.Skipf("SKIP-NO-NAMESPACE: cannot mount inside the namespace: %v", err)
	}
	defer func() {
		if uerr := unix.Unmount(nested, unix.MNT_DETACH); uerr != nil {
			t.Errorf("unmounting: %v", uerr)
		}
	}()

	const body = "payload"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(body), 0o644); err != nil { //nolint:gosec // G703 traces the path from the environment: this test's own temporary directory.
		t.Fatalf("writing the source: %v", err)
	}

	c := nestedMoveCore(t, dir)
	from, err := c.Resolve(UserID(42), mustVpath(t, "docs/a.txt"), acl.Move)
	if err != nil {
		t.Fatalf("resolving the source: %v", err)
	}
	to, err := c.Resolve(UserID(42), mustVpath(t, "docs/vol/a.txt"), acl.Create)
	if err != nil {
		t.Fatalf("resolving the destination: %v", err)
	}

	// The preflight and the move must agree, because the UI warns from the
	// first and the second is what actually runs.
	if !c.WouldCopy(from, to) {
		t.Fatal("WouldCopy answered false for a destination on another device")
	}
	res, err := c.Move(ctx(), from, to, MoveOpts{})
	if err != nil {
		t.Fatalf("moving into a nested mount: %v", err)
	}
	if !res.WillCopy {
		t.Fatal("the move reported a rename across a real device boundary")
	}
	if res.Moved {
		t.Fatal("the move reported a plain rename")
	}

	got, err := os.ReadFile(filepath.Join(nested, "a.txt")) //nolint:gosec // G703 traces the path from the environment: this test's own temporary directory.
	if err != nil {
		t.Fatalf("reading the destination: %v", err)
	}
	if string(got) != body {
		t.Fatalf("the destination holds %q, want %q", got, body)
	}
	//nolint:gosec // G703 reads the variable: this test's own temporary directory and a constant name.
	if _, serr := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(serr) {
		t.Fatalf("the source survived the move: %v", serr)
	}
	t.Log(nestedMoveProved)
}

// nestedMoveCore builds a Core whose one share is host, with a grant that can
// move within it. It is separate from testCore because the share root here is
// the directory the child mounted into.
func nestedMoveCore(t *testing.T, host string) *Core {
	t.Helper()
	s, err := store.Open(t.TempDir(), store.Options{Clock: testClock()})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})

	ev := acl.NewEvaluator()
	c, err := New(s, Options{ACL: ev, Clock: testClock()})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	if err := c.RegisterShare(ctx(), ShareDef{
		ID: 1, Name: "docs", Host: host, Policy: vfs.DefaultSharePolicy(),
	}); err != nil {
		t.Fatalf("RegisterShare: %v", err)
	}
	if err := insertTestUser(s, 42); err != nil {
		t.Fatalf("creating the test user: %v", err)
	}
	g := acl.Grant{
		User: 42, Share: 1, Subpath: acl.NewPath(),
		Allow:   acl.Read | acl.Write | acl.Create | acl.Delete | acl.Rename | acl.Move,
		Inherit: true, Label: "docs",
	}
	if err := insertGrant(s, g, 1); err != nil {
		t.Fatalf("inserting the grant: %v", err)
	}
	if err := ev.LoadFromState(ctx(), s.State().SQL()); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}
	return c
}
