//go:build linux

package vfs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The nested-mount proof.
//
// A supported root does not bless an unsupported mount below it. Proving that
// needs a real mount of a real filesystem the gate refuses, which needs
// privileges this test does not have on the host. A user namespace grants them
// inside itself, so the work runs in a re-executed child that owns one.
//
// The child does the mounting and the assertion, because a mount made in a
// namespace is only visible from inside it.

// nestedMountMarker names the child. It carries the directory to work in.
const nestedMountMarker = "SC_TEST_NESTED_MOUNT"

// nestedMountProved is what the child prints once it has actually placed the
// mount and seen the refusal. The parent requires it, because a child that
// skipped also exits zero and also prints a pass.
const nestedMountProved = "NESTED-MOUNT-REFUSED"

// TestAnUnsupportedNestedMountFailsClosed places a filesystem the gate refuses
// below an admitted root and proves traversal into it is refused.
func TestAnUnsupportedNestedMountFailsClosed(t *testing.T) {
	if dir := os.Getenv(nestedMountMarker); dir != "" {
		// This is the child, inside its own namespace.
		runNestedMountChild(t, dir)
		return
	}

	dir := t.TempDir()
	// The parent's temporary directory is what the child mounts into, so it
	// has to be one this build admits in the first place.
	r, _, err := RegisterShareRoot(1, dir, DefaultSharePolicy())
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
	cmd.Env = append(os.Environ(), nestedMountMarker+"="+dir)
	// A user namespace is what makes the mount possible without privileges on
	// the host, and a private propagation keeps it from escaping into the
	// host's mount table.
	cmd.SysProcAttr = &unix.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
		// The mount namespace is made private by the kernel for the child, and
		// the uid map is what makes the child hold the privileges the mount
		// needs inside it.
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
	}

	out, err := cmd.CombinedOutput()
	// A child that skipped exits zero and prints PASS, so a check for either
	// alone is a gate that reports success whatever happened inside. The skip
	// marker is looked for first, and on both paths.
	if strings.Contains(string(out), "SKIP-NO-NAMESPACE") {
		t.Skipf("this host does not allow an unprivileged user namespace:\n%s", out)
	}
	if err != nil {
		t.Fatalf("the child failed: %v\n%s", err, out)
	}
	// And the child says so explicitly, so a run that skipped for some reason
	// this does not know about cannot be read as a pass either.
	if !strings.Contains(string(out), nestedMountProved) {
		t.Fatalf("the child did not report the proof:\n%s", out)
	}
}

// runNestedMountChild mounts a refused filesystem below the root and checks
// that the gate refuses traversal into it.
func runNestedMountChild(t *testing.T, dir string) {
	t.Helper()

	// A private propagation, so nothing here reaches the host's mount table.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Skip("SKIP-NO-NAMESPACE: cannot make the mount tree private")
	}

	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil { //nolint:gosec // G703 traces the path from the environment: it is this test's own temporary directory, handed to its own child.
		t.Fatalf("mkdir: %v", err)
	}

	// ramfs is a filesystem this build has no name for, which is exactly the
	// fail-closed case: unknown rather than named-and-refused.
	if err := unix.Mount("ramfs", nested, "ramfs", 0, ""); err != nil {
		t.Skipf("SKIP-NO-NAMESPACE: cannot mount inside the namespace: %v", err)
	}
	defer func() {
		if uerr := unix.Unmount(nested, unix.MNT_DETACH); uerr != nil {
			t.Errorf("unmounting: %v", uerr)
		}
	}()

	// The mount really is a different filesystem, or the test proves nothing.
	var sfs unix.Statfs_t
	if err := unix.Statfs(nested, &sfs); err != nil {
		t.Fatalf("statfs: %v", err)
	}
	if adm, _ := AdmitFsType(FsType(sfs.Type)); adm.OK { //nolint:gosec // the kernel's own magic value, read back from the mount this test made.
		t.Skipf("this host's ramfs reports magic %#x, which this build admits", uint64(sfs.Type)) //nolint:gosec // the same value, in a message.
	}

	// The share root itself is still admitted.
	policy := DefaultSharePolicy()
	policy.CrossMount = true
	r, _, err := RegisterShareRoot(1, dir, policy)
	if err != nil {
		t.Fatalf("the share root was refused: %v", err)
	}
	defer func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	// And traversal into the nested mount is refused, before anything on it is
	// exposed.
	p, err := ParseSafePath("nested")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	_, err = r.Stat(p)
	if !errors.Is(err, ErrUnsupportedFilesystem) {
		t.Fatalf("traversal into an unsupported nested mount gave %v, want a refusal", err)
	}
	// Reading the mount as a directory is refused too, so no entry on it is
	// exposed either.
	if _, rerr := r.ReadDir(p, HideReserved); !errors.Is(rerr, ErrUnsupportedFilesystem) {
		t.Fatalf("reading an unsupported nested mount gave %v, want a refusal", rerr)
	}
	// The refusal names the mount path, which is not the share root the
	// operator configured and so is the thing they have to go and look at.
	if !strings.Contains(err.Error(), "nested") {
		t.Fatalf("the refusal does not name the mount: %v", err)
	}
	t.Log(nestedMountProved)
}
