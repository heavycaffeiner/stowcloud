//go:build linux

// The escape proof. This package's claim is that the kernel confines a
// share, not that its own checks do, and every case below asserts the
// specific errno-derived sentinel the kernel produced rather than merely
// that an operation failed. A string check that happens to also refuse the
// same input would keep passing after the real enforcement broke, which is
// worse than no test at all.
package vfs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// share opens host as a share root under policy without running admission,
// so a test can pick any policy without an admission verdict getting in the
// way.
func share(t *testing.T, host string, policy SharePolicy) *ShareRoot {
	t.Helper()
	r, err := OpenShareRoot(1, host, policy)
	if err != nil {
		t.Fatalf("open share root %s: %v", host, err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close share root: %v", err)
		}
	})
	return r
}

func denyPolicy() SharePolicy { return DefaultSharePolicy() }

func withinPolicy() SharePolicy {
	p := DefaultSharePolicy()
	p.Symlink = SymlinkWithinShare
	return p
}

func mustParse(t *testing.T, s string) SafePath {
	t.Helper()
	p, err := ParseSafePath(s)
	if err != nil {
		t.Fatalf("ParseSafePath(%q): %v", s, err)
	}
	return p
}

// mustJoinControl builds a control-prefixed path directly, since a control
// name carries the reserved prefix that only JoinControl, not the ordinary
// parser, is permitted to produce.
func mustJoinControl(t *testing.T, parent SafePath, name string) SafePath {
	t.Helper()
	p, err := parent.JoinControl(name)
	if err != nil {
		t.Fatalf("JoinControl(%q): %v", name, err)
	}
	return p
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readAll(t *testing.T, r *ShareRoot, p SafePath) (string, error) {
	t.Helper()
	f, err := r.OpenRead(p, IntentRead)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	buf := make([]byte, 256)
	n, err := f.ReadAt(buf, 0)
	if n == 0 && err != nil && !isEOF(err) {
		return "", err
	}
	return string(buf[:n]), nil
}

// Item 1. The traversal table refuses before any syscall runs at all.
func TestEscapeRejectedBeforeAnySyscall(t *testing.T) {
	for _, in := range []string{"..", "a/../..", "../etc/passwd", "/etc/passwd", "//etc"} {
		if _, err := ParseSafePath(in); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("ParseSafePath(%q) = %v, want ErrInvalidName", in, err)
		}
	}
}

// Item 2. A symlink inside the share pointing outside it.
func TestEscapeSymlinkOutOfTheShareUnderDeny(t *testing.T) {
	outside := t.TempDir()
	host := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "outside")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(host, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := readAll(t, share(t, host, denyPolicy()), mustParse(t, "link"))
	if !errors.Is(err, ErrSymlinkDenied) {
		t.Fatalf("read = %q, %v, want ErrSymlinkDenied", got, err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("ErrSymlinkDenied must stay distinct from ErrNotFound")
	}
}

// Item 3. Under SymlinkWithinShare the same absolute target is rebased
// against the share root rather than followed to the real location, and
// nothing outside the share is ever read.
func TestEscapeSymlinkOutOfTheShareUnderWithinShare(t *testing.T) {
	outside := t.TempDir()
	host := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "outside")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(host, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := readAll(t, share(t, host, withinPolicy()), mustParse(t, "link"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("read = %q, %v, want ErrNotFound", got, err)
	}
	if got == "outside" {
		t.Fatal("the symlink escaped the share")
	}
}

// Item 4. The positive half of item 3: a link to /secret.txt reads the
// share's own secret.txt, proving the rebasing itself is correct and not
// merely a refusal in disguise.
func TestWithinShareRebasesAnAbsoluteTargetIntoTheShare(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "secret.txt"), "inside")
	if err := os.Symlink("/secret.txt", filepath.Join(host, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := readAll(t, share(t, host, withinPolicy()), mustParse(t, "link"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "inside" {
		t.Fatalf("read = %q, want the share's own file", got)
	}
}

// Item 5. The same two outcomes reproduced for a symlink standing in for an
// intermediate directory component rather than the leaf.
func TestEscapeThroughASymlinkedDirectoryComponent(t *testing.T) {
	outside := t.TempDir()
	host := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "outside")
	if err := os.Symlink(outside, filepath.Join(host, "dirlink")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := readAll(t, share(t, host, denyPolicy()), mustParse(t, "dirlink/secret.txt"))
	if !errors.Is(err, ErrSymlinkDenied) {
		t.Fatalf("deny: read = %q, %v, want ErrSymlinkDenied", got, err)
	}

	got, err = readAll(t, share(t, host, withinPolicy()), mustParse(t, "dirlink/secret.txt"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("within share: read = %q, %v, want ErrNotFound", got, err)
	}
}

// Item 6. A magic link cannot be demonstrated through an ordinary symlink,
// since an absolute target is already refused by RESOLVE_BENEATH before
// RESOLVE_NO_MAGICLINKS ever matters. /proc/self/fd holds a magic link that
// is beneath the share root, so BENEATH alone has nothing to say about it.
//
// Two controls turn this into a proof rather than an observation: with no
// resolve flags at all the same open reads the real file, showing the
// target is genuinely reachable, and with RESOLVE_BENEATH alone the open
// still refuses but with EXDEV rather than ELOOP, showing the ELOOP in the
// real case is specifically RESOLVE_NO_MAGICLINKS's doing.
func TestEscapeThroughAMagicLink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.txt")
	write(t, target, "through the magic link")
	held, err := os.Open(target)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer closeAfter(held, "magic link target")

	number, err := withFd(held, func(fd int) (int, error) { return fd, nil })
	if err != nil {
		t.Fatal(err)
	}
	name := strconv.Itoa(number)

	policy := DefaultSharePolicy()
	policy.Symlink = SymlinkFollow
	r := share(t, "/proc/self", policy)

	if _, openErr := r.OpenRead(mustParse(t, "fd/"+name), IntentRead); !errors.Is(openErr, ErrSymlinkDenied) {
		t.Fatalf("opening a magic link = %v, want ErrSymlinkDenied from ELOOP", openErr)
	}

	parent, err := r.resolveDir([]string{"fd"})
	if err != nil {
		t.Fatalf("resolve /proc/self/fd: %v", err)
	}
	defer closeAfter(parent, "magic link parent")

	// Control one: no resolve restriction at all. If this fails the refusal
	// above proves nothing about what was blocked.
	open, err := openat2Raw(parent, name, unix.O_RDONLY|unix.O_CLOEXEC, 0, 0)
	if err != nil {
		t.Fatalf("the unrestricted control open failed (%v)", err)
	}
	buf := make([]byte, 64)
	n, rerr := open.ReadAt(buf, 0)
	closeAfter(open, "magic link control")
	if n == 0 && rerr != nil && !isEOF(rerr) {
		t.Fatalf("the unrestricted control read failed: %v", rerr)
	}
	if string(buf[:n]) != "through the magic link" {
		t.Fatalf("the control read %q, so it did not follow the link", buf[:n])
	}

	// Control two: RESOLVE_BENEATH alone refuses with a different errno,
	// which is what attributes the ELOOP above to RESOLVE_NO_MAGICLINKS
	// specifically.
	if _, err := openat2Raw(parent, name, unix.O_RDONLY|unix.O_CLOEXEC, 0, unix.RESOLVE_BENEATH); !errors.Is(err, unix.EXDEV) {
		t.Fatalf("RESOLVE_BENEATH alone = %v, want EXDEV", err)
	}
}

// Item 7. A component swapped out for a symlink between two calls. There is
// no window here to race: a descriptor already resolved keeps naming what
// it resolved to, unaffected by the swap, and a fresh resolution afterward
// refuses the new symlink rather than following it.
func TestEscapeBySwappingAComponentBetweenCalls(t *testing.T) {
	outside := t.TempDir()
	host := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "outside")

	inner := filepath.Join(host, "a", "b")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, filepath.Join(inner, "secret.txt"), "inside")

	r := share(t, host, denyPolicy())

	held, err := r.resolveDir([]string{"a", "b"})
	if err != nil {
		t.Fatalf("resolve a/b: %v", err)
	}
	defer closeAfter(held, "held parent")
	before, err := statOf(held)
	if err != nil {
		t.Fatalf("stat the held descriptor: %v", err)
	}

	if rmErr := os.Remove(filepath.Join(inner, "secret.txt")); rmErr != nil {
		t.Fatalf("remove: %v", rmErr)
	}
	if rmdirErr := os.Remove(inner); rmdirErr != nil {
		t.Fatalf("rmdir: %v", rmdirErr)
	}
	if symErr := os.Symlink(outside, inner); symErr != nil {
		t.Fatalf("symlink: %v", symErr)
	}

	after, err := statOf(held)
	if err != nil {
		t.Fatalf("stat the held descriptor after the swap: %v", err)
	}
	if after.Ino != before.Ino || after.Dev != before.Dev {
		t.Fatal("a descriptor already resolved followed the swap")
	}

	got, err := readAll(t, r, mustParse(t, "a/b/secret.txt"))
	if !errors.Is(err, ErrSymlinkDenied) {
		t.Fatalf("a fresh resolution = %q, %v, want ErrSymlinkDenied", got, err)
	}
}

const mountBoundaryEnv = "SC_VFS_MOUNT_BOUNDARY"
const mountBoundarySkip = "SC-MOUNT-BOUNDARY-SKIP"
const mountBoundaryProved = "SC-MOUNT-BOUNDARY-PROVED"

// Item 8. A share opted out of crossing a mount boundary (CrossMount:
// false) refuses both Stat and ReadDir into a real mount placed below the
// root, before anything under the mount is exposed; the same layout with
// CrossMount: true resolves ordinarily. Mounting needs privilege, so the
// work happens in a re-executed child under a private user and mount
// namespace, and the parent distinguishes a real failure from a host that
// simply disallows the namespace.
func TestEscapeAcrossAMountBoundary(t *testing.T) {
	if os.Getenv(mountBoundaryEnv) == "1" {
		mountBoundaryChild(t)
		return
	}
	// The re-exec filter below is a fixed string, not built from t.Name(), so
	// a rename of this test has to update it here too; this catches a rename
	// that forgot.
	if got := "^" + t.Name() + "$"; got != mountBoundaryTestFilter {
		t.Fatalf("mountBoundaryTestFilter = %q, want %q", mountBoundaryTestFilter, got)
	}

	out, err := reexecInNamespaceForTest()
	output := string(out)
	if strings.Contains(output, mountBoundarySkip) {
		t.Skipf("this host does not allow building the boundary:\n%s", output)
	}
	if err != nil {
		t.Fatalf("the mount-boundary child failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, mountBoundaryProved) {
		t.Fatalf("the child did not report the proof:\n%s", output)
	}
}

// mountBoundaryTestFilter names this exact test as a compile-time constant,
// not a computed value, since this test has no subtests: the filter passed
// to the re-executed binary is always this one fixed string.
const mountBoundaryTestFilter = "^TestEscapeAcrossAMountBoundary$"

// reexecInNamespaceForTest re-executes this test binary, filtered to
// exactly TestEscapeAcrossAMountBoundary, inside a private user and mount
// namespace mapped to the current uid and gid, and returns its combined
// output.
func reexecInNamespaceForTest() ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return reexecCommand(exe).CombinedOutput()
}

// reexecCommand builds the re-exec command. exe is a function parameter
// rather than a local variable, so it names this process's own resolved
// path at the point exec.Command reads it; the test filter is the fixed
// mountBoundaryTestFilter constant, never a value built from input this
// process does not already trust.
func reexecCommand(exe string) *exec.Cmd {
	cmd := exec.Command(exe, "-test.run", mountBoundaryTestFilter, "-test.v")
	cmd.Env = append(os.Environ(), mountBoundaryEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
	}
	return cmd
}

func mountBoundaryChild(t *testing.T) {
	t.Helper()
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Logf("%s: cannot privatize the mount namespace: %v", mountBoundarySkip, err)
		return
	}

	host := t.TempDir()
	target := filepath.Join(host, "mnt")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := unix.Mount("tmpfs", target, "tmpfs", 0, ""); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Logf("%s: cannot mount inside the namespace: %v", mountBoundarySkip, err)
			return
		}
		t.Fatalf("mount inside the share: %v", err)
	}
	defer func() {
		if err := unix.Unmount(target, 0); err != nil {
			t.Errorf("unmount: %v", err)
		}
	}()
	write(t, filepath.Join(target, "x.txt"), "on the other filesystem")

	policy := DefaultSharePolicy()
	policy.CrossMount = false
	r := share(t, host, policy)

	if _, err := r.Stat(mustParse(t, "mnt")); !errors.Is(err, ErrCrossDevice) {
		t.Fatalf("stat across the boundary = %v, want ErrCrossDevice", err)
	}
	if _, err := r.ReadDir(mustParse(t, "mnt"), HideReserved); !errors.Is(err, ErrCrossDevice) {
		t.Fatalf("readdir across the boundary = %v, want ErrCrossDevice", err)
	}

	policy.CrossMount = true
	crossing := share(t, host, policy)
	if _, err := crossing.Stat(mustParse(t, "mnt/x.txt")); err != nil {
		t.Fatalf("stat with crossing allowed: %v", err)
	}

	t.Log(mountBoundaryProved)
}

// Item 10. The complementary case: a name present under only its NFD
// spelling is found by both an NFD and an NFC lookup.
func TestOnlyOneSpellingOnDiskIsFoundByBoth(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, nfdSpelling), "decomposed")

	r := share(t, host, denyPolicy())
	for _, spelling := range []string{nfdSpelling, nfcSpelling} {
		got, err := readAll(t, r, mustParse(t, spelling))
		if err != nil {
			t.Fatalf("looking up %q: %v", spelling, err)
		}
		if got != "decomposed" {
			t.Fatalf("looking up %q read %q", spelling, got)
		}
	}
}
