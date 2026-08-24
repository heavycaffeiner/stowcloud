//go:build linux

// The escape proof. The claim this package makes is that the kernel confines a
// share, rather than that this code's own checks do, and that is a claim which
// can be executed.
//
// Every case below could be made to pass by string validation, and string
// validation is exactly what the design refuses, so each one asserts the error
// the kernel produced and not merely that the operation failed. A proof that
// passes for the wrong reason is worse than none.
package vfs

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// share opens a temporary directory as a share root under policy.
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

// Case 1 and 2. Rejected at parse, before any syscall is issued at all.
func TestEscapeRejectedBeforeAnySyscall(t *testing.T) {
	for _, in := range []string{"..", "a/../..", "../etc/passwd", "/etc/passwd", "//etc"} {
		if _, err := ParseSafePath(in); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("ParseSafePath(%q) = %v, want ErrInvalidName", in, err)
		}
	}
}

// Case 3. A symlink inside the share pointing at a file outside it.
func TestEscapeSymlinkOutOfTheShare(t *testing.T) {
	outside := t.TempDir()
	host := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "outside")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(host, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Under the default policy the kernel reports ELOOP, which this package
	// keeps distinct from "not found" because they are different facts.
	got, err := readAll(t, share(t, host, denyPolicy()), mustParse(t, "link"))
	if !errors.Is(err, ErrSymlinkDenied) {
		t.Fatalf("deny: read = %q, %v, want ErrSymlinkDenied", got, err)
	}

	// Under WithinShare the absolute target is reinterpreted against the share
	// root, so it names something that does not exist inside it.
	got, err = readAll(t, share(t, host, withinPolicy()), mustParse(t, "link"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("within share: read = %q, %v, want ErrNotFound", got, err)
	}
	if got == "outside" {
		t.Fatal("within share: the symlink escaped")
	}
}

// Case 3, the other half of what WithinShare means: an absolute target is
// rebased into the share rather than refused, so a link to /x reads the share's
// own x.
func TestWithinShareRebasesRatherThanEscaping(t *testing.T) {
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

// Case 4. Traversal through a symlinked directory, which is the same refusal
// applied to an intermediate component rather than the leaf.
func TestEscapeThroughASymlinkedDirectory(t *testing.T) {
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

// Case 5, new work. A magic link is what RESOLVE_NO_MAGICLINKS exists for, and
// it cannot be demonstrated through an ordinary symlink: an absolute target is
// refused by RESOLVE_BENEATH first, so a test written that way would pass
// without the flag being present at all.
//
// The share root here is /proc/self, so a magic link under fd/ is beneath the
// root and BENEATH has nothing to say about reaching it. The two controls are
// what make this a proof rather than an observation: with no resolve flags the
// same open succeeds and reads the file, and with BENEATH alone it is refused
// with a different errno. Only the ELOOP is attributable to NO_MAGICLINKS.
func TestEscapeThroughAMagicLink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.txt")
	write(t, target, "through the magic link")
	held, err := os.Open(target)
	if err != nil {
		t.Fatalf("open the target: %v", err)
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

	if _, oerr := r.OpenRead(mustParse(t, "fd/"+name), IntentRead); !errors.Is(oerr, ErrSymlinkDenied) {
		t.Fatalf("opening a magic link = %v, want ErrSymlinkDenied from ELOOP", oerr)
	}

	parent, err := r.resolveDir([]string{"fd"})
	if err != nil {
		t.Fatalf("resolve /proc/self/fd: %v", err)
	}
	defer closeAfter(parent, "magic link parent")

	// Control one: no resolve restrictions at all. The link is followed and the
	// file behind it reads, so the target is reachable and readable.
	open, err := openat2(parent, name, unix.O_RDONLY|unix.O_CLOEXEC, 0, 0)
	if err != nil {
		t.Fatalf("the unrestricted control open failed (%v), so the refusal above proves nothing", err)
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

	// Control two: BENEATH alone. It also refuses, with EXDEV rather than
	// ELOOP, which is what makes the errno above the flag's own answer.
	if _, err := openat2(parent, name, unix.O_RDONLY|unix.O_CLOEXEC, 0, unix.RESOLVE_BENEATH); !errors.Is(err, unix.EXDEV) {
		t.Fatalf("BENEATH alone = %v, want EXDEV; the two flags are no longer distinguishable", err)
	}
}

// Case 6, new work. A component replaced by a symlink between two calls.
//
// There is no window to race, and this asserts both halves of that: a
// descriptor already resolved still names what it resolved to, and a fresh
// resolution of the same path refuses rather than following the new symlink.
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

	// Swap the component out from under it.
	if rerr := os.Remove(filepath.Join(inner, "secret.txt")); rerr != nil {
		t.Fatalf("remove: %v", rerr)
	}
	if rerr := os.Remove(inner); rerr != nil {
		t.Fatalf("rmdir: %v", rerr)
	}
	if serr := os.Symlink(outside, inner); serr != nil {
		t.Fatalf("symlink: %v", serr)
	}

	after, err := statOf(held)
	if err != nil {
		t.Fatalf("stat the held descriptor after the swap: %v", err)
	}
	if after.Ino != before.Ino || after.Dev != before.Dev {
		t.Fatal("a descriptor that was already resolved followed the swap")
	}

	got, err := readAll(t, r, mustParse(t, "a/b/secret.txt"))
	if !errors.Is(err, ErrSymlinkDenied) {
		t.Fatalf("a fresh resolution = %q, %v, want ErrSymlinkDenied", got, err)
	}
}

// mountProofEnv marks the child that runs case 7 inside its own namespaces.
const mountProofEnv = "SC_MOUNT_PROOF"

// What the child prints when the kernel refuses the mount, for the parent to
// recognise. A sentinel rather than a matched errno string, because the child
// communicates over its output.
const mountProofDenied = "SC_MOUNT_PROOF: the kernel refused the mount"

// Case 7, new work. A path crossing a mount boundary inside the share, with the
// share opted out of crossing one.
//
// Mounting needs privilege, and the answer is a private user and mount
// namespace rather than a skip: this case is required on the Linux job, and a
// row of the proof that only runs when somebody remembers to be root is a row
// that stops running. The child does the mounting; the namespace dies with it,
// so nothing survives into the host's mount table.
func TestEscapeAcrossAMountBoundary(t *testing.T) {
	if os.Getenv(mountProofEnv) == "1" || os.Geteuid() == 0 {
		// Already privileged, or already the child. Either way the boundary can
		// be built here.
		mountBoundaryProof(t)
		return
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("this test re-executes itself and cannot find its own path: %v", err)
	}
	cmd := exec.Command(self, "-test.run", "^"+t.Name()+"$", "-test.v") //nolint:gosec // this binary re-executing itself.
	cmd.Env = append(os.Environ(), mountProofEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	// The child reports a denied mount by printing this, so it is checked
	// before the error: a policy that forbids the mount is not a failure of
	// what is under test. The clone succeeds and the mount inside it does not,
	// so the outer error is a plain exit status with no errno to match on.
	if bytes.Contains(out, []byte(mountProofDenied)) {
		t.Skip("this kernel does not allow mounting inside an unprivileged user namespace, so the boundary cannot be built")
	}
	if err == nil {
		return
	}
	if errors.Is(err, unix.EPERM) {
		// A kernel that forbids an unprivileged user namespace outright, which
		// some distributions do by policy. Here the clone itself fails, so the
		// child never runs and prints nothing.
		t.Skipf("this kernel does not allow an unprivileged user namespace, so the boundary cannot be built: %v", err)
	}
	t.Fatalf("the mount-boundary child failed: %v\n%s", err, out)
}

func mountBoundaryProof(t *testing.T) {
	host := t.TempDir()
	target := filepath.Join(host, "mnt")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := unix.Mount("tmpfs", target, "tmpfs", 0, ""); err != nil {
		// A user namespace can exist and still not be allowed to mount inside
		// it: AppArmor on Ubuntu denies it, and GitHub's runners are that. The
		// parent reads this line and skips, because a boundary that cannot be
		// built is not the same as one that does not hold.
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Log(mountProofDenied)
			t.SkipNow()
		}
		t.Fatalf("mount a filesystem inside the share: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(target, 0); err != nil {
			t.Errorf("unmount: %v", err)
		}
	})
	write(t, filepath.Join(target, "x.txt"), "on the other filesystem")

	policy := DefaultSharePolicy()
	policy.CrossMount = false
	r := share(t, host, policy)

	if _, err := r.Stat(mustParse(t, "mnt")); !errors.Is(err, ErrCrossDevice) {
		t.Fatalf("stat across the boundary = %v, want ErrCrossDevice", err)
	}
	if _, err := r.Stat(mustParse(t, "mnt/x.txt")); !errors.Is(err, ErrCrossDevice) {
		t.Fatalf("stat through the boundary = %v, want ErrCrossDevice", err)
	}

	// Opted in, the same path is ordinary. A share is one directory tree and
	// not one filesystem, so crossing into a RAID array mounted under media/ is
	// what a user expects rather than a fault.
	policy.CrossMount = true
	if _, err := share(t, host, policy).Stat(mustParse(t, "mnt/x.txt")); err != nil {
		t.Fatalf("stat with crossing allowed: %v", err)
	}
}

// Case 8. A name spelled in the other Unicode form, because the candidate loop
// is not an escape: it finds what another program wrote rather than reaching
// outside anything.
func TestTheOtherUnicodeFormIsFoundRatherThanRefused(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, nfdName), "decomposed")
	write(t, filepath.Join(host, nfcName), "precomposed")

	r := share(t, host, denyPolicy())

	got, err := readAll(t, r, mustParse(t, nfcName))
	if err != nil {
		t.Fatalf("reading the precomposed spelling: %v", err)
	}
	if got != "precomposed" {
		t.Fatalf("read %q, want the file that spelling names", got)
	}
	got, err = readAll(t, r, mustParse(t, nfdName))
	if err != nil {
		t.Fatalf("reading the decomposed spelling: %v", err)
	}
	if got != "decomposed" {
		t.Fatalf("read %q, want the file that spelling names", got)
	}
}

// The candidate loop's other half: a name on disk in one form is found by the
// other, which is the whole reason it exists.
func TestOnlyOneSpellingOnDiskIsFoundByBoth(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, nfdName), "decomposed")

	r := share(t, host, denyPolicy())
	for _, spelling := range []string{nfdName, nfcName} {
		got, err := readAll(t, r, mustParse(t, spelling))
		if err != nil {
			t.Fatalf("looking up %q: %v", spelling, err)
		}
		if got != "decomposed" {
			t.Fatalf("looking up %q read %q", spelling, got)
		}
	}
}
