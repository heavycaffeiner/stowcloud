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

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"golang.org/x/sys/unix"
)

// A share root admitted on a supported filesystem does not bless whatever
// gets mounted below it afterward. Proving that needs a real mount of a
// filesystem this build refuses, which needs privilege this test process
// does not have on the host; a private user namespace grants that
// privilege inside itself, so the work runs in a re-executed copy of this
// test binary rather than in the parent.

const nestedMountEnv = "SC_VFS_NESTED_MOUNT"
const nestedMountSkip = "SC-NESTED-MOUNT-SKIP"
const nestedMountProved = "SC-NESTED-MOUNT-PROVED"

// nestedMountTestName is a compile-time constant rather than t.Name(), so
// the -test.run argument built from it is a fixed string a reviewer can
// read in full, not a value gosec has to trust was not shaped by a caller.
const nestedMountTestName = "TestNestedMountOfAnUnsupportedFilesystemFailsClosed"

// TestNestedMountOfAnUnsupportedFilesystemFailsClosed places ramfs, a type
// this build has no case for, below an admitted share root and proves that
// traversal into it, and only into it, is refused.
func TestNestedMountOfAnUnsupportedFilesystemFailsClosed(t *testing.T) {
	if os.Getenv(nestedMountEnv) == "1" {
		nestedMountChild(t)
		return
	}

	// /proc/self/exe, a fixed path rather than a variable built from
	// os.Executable(), is what re-executes this same test binary: it always
	// names the running process's own executable on Linux, so nothing here
	// passes a caller-influenced value to exec.Command.
	cmd := exec.Command("/proc/self/exe", "-test.run=^"+nestedMountTestName+"$", "-test.v")
	cmd.Env = append(os.Environ(), nestedMountEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
	}
	out, err := cmd.CombinedOutput()
	output := string(out)
	// A child that skipped exits zero and prints PASS, the same as a child
	// that actually proved the case, so the skip marker is checked first and
	// on both outcomes.
	if strings.Contains(output, nestedMountSkip) {
		t.Skipf("this host does not allow building the boundary:\n%s", output)
	}
	if err != nil {
		t.Fatalf("the child failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, nestedMountProved) {
		t.Fatalf("the child did not report the proof:\n%s", output)
	}
}

func nestedMountChild(t *testing.T) {
	t.Helper()

	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Logf("%s: cannot privatize the mount namespace: %v", nestedMountSkip, err)
		return
	}

	host := t.TempDir()
	nested := filepath.Join(host, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// ramfs is a filesystem this build has no case for, which is exactly the
	// fail-closed shape this test is after: unclassified, not classified
	// and refused by name.
	if err := unix.Mount("ramfs", nested, "ramfs", 0, ""); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Logf("%s: cannot mount ramfs inside the namespace: %v", nestedMountSkip, err)
			return
		}
		t.Fatalf("mount: %v", err)
	}
	defer func() {
		if err := unix.Unmount(nested, unix.MNT_DETACH); err != nil {
			t.Errorf("unmount: %v", err)
		}
	}()

	var sfs unix.Statfs_t
	if err := unix.Statfs(nested, &sfs); err != nil {
		t.Fatalf("statfs: %v", err)
	}
	// Statfs_t.Type is signed on this platform even though a magic number is
	// never negative; num.Narrow is this tree's one sanctioned way to cross
	// that width and signedness rather than a bare conversion.
	magic, err := num.Narrow[uint64](sfs.Type)
	if err != nil {
		t.Fatalf("the mount's magic number does not fit a uint64: %v", err)
	}
	if adm, _ := AdmitFsType(FsType(magic)); adm.OK {
		t.Skipf("%s: this host's ramfs reports a magic this build admits (%#x)", nestedMountSkip, magic)
	}

	policy := DefaultSharePolicy()
	policy.CrossMount = true
	r, adm, err := RegisterShareRoot(1, host, policy)
	if err != nil {
		t.Fatalf("the share root itself was refused: %v", err)
	}
	t.Cleanup(func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	if !adm.OK {
		t.Fatal("the share root was not admitted")
	}

	p, err := ParseSafePath("nested")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	_, statErr := r.Stat(p)
	if !errors.Is(statErr, ErrUnsupportedFilesystem) {
		t.Fatalf("stat into the nested mount = %v, want ErrUnsupportedFilesystem", statErr)
	}
	if !strings.Contains(statErr.Error(), "nested") {
		t.Fatalf("the refusal does not name the mount path: %v", statErr)
	}

	if _, err := r.ReadDir(p, HideReserved); !errors.Is(err, ErrUnsupportedFilesystem) {
		t.Fatalf("readdir into the nested mount = %v, want ErrUnsupportedFilesystem", err)
	}

	// The share root itself stays usable outside the refused subtree: one
	// unsupported nested mount is not an outage of the whole share.
	if err := r.Alive(); err != nil {
		t.Fatalf("the share root reports unhealthy after a nested refusal: %v", err)
	}

	t.Log(nestedMountProved)
}
