//go:build linux

package jail

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// reexecMarker is set on the image RestrictAndReexec produces, so the sequence
// runs exactly once and a missing marker afterwards is a bug rather than a
// loop.
const reexecMarker = "SC_REEXEC"

// fdSweepMax bounds the fallback descriptor sweep. Higher than any descriptor
// the parent plausibly holds, low enough that the loop is instant.
const fdSweepMax = 65536

// firstSealedFD is the lowest descriptor SealDescriptors closes. Standard in,
// out and error are 0 through 2, and the worker's control socket is 3, so the
// seal starts above all four.
const firstSealedFD = 4

// Reexeced reports whether marker says this process is already the image
// RestrictAndReexec produced.
func Reexeced(marker string) bool { return os.Getenv(marker) == "1" }

// ReexecMarker is the environment variable the sequence marks its own image
// with, so a caller assembling a worker can clear it deliberately.
func ReexecMarker() string { return reexecMarker }

// RestrictAndReexec applies spec to the calling thread and then replaces the
// process image, so that every thread of the new process inherits the domain.
// It does not return on success.
//
// The re-exec is the whole mechanism. landlock_restrict_self restricts the
// calling thread, a thread created afterwards inherits the domain of the thread
// that created it, and the Go runtime has already started several threads
// before main runs. Calling it from a goroutine restricts whichever thread that
// goroutine happened to be on, leaves every other one unrestricted, and returns
// success: the process then reports itself as sandboxed and is not. A Landlock
// domain survives execve, and after execve the process has exactly one thread
// carrying it.
//
// The caller must hold the OS thread across this call.
func RestrictAndReexec(spec Spec, marker string) error {
	// Before the domain exists, because reading it afterwards may be exactly
	// what the domain forbids.
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoProc, err)
	}
	if len(os.Args) == 0 {
		return fmt.Errorf("%w: this process has no argv to re-exec with", ErrNoProc)
	}

	// The binary's own path is granted here rather than left to the caller. A
	// domain built without it makes the exec below fail with EACCES and the
	// process die, and a sequence that can be assembled wrong in one place is
	// one that will be.
	spec.GrantBeneath = append(spec.GrantBeneath, Grant{Path: self, Access: readExecute})

	if rerr := restrict(spec); rerr != nil {
		return rerr
	}

	env := append(os.Environ(), marker+"=1")
	// unix.Exec replaces the process image, so nothing after it runs.
	return unix.Exec(self, os.Args, env)
}

// SealDescriptors closes everything above the worker's control socket.
//
// It matters as much as the filters. The parent is a file server, so the table
// a worker is born holding contains listening sockets, open share roots and
// database handles. RLIMIT_NOFILE caps how many new descriptors the worker may
// obtain and does nothing about the ones it inherited, and os/exec's CLOEXEC
// defaults cover most of them, which is not a security answer. This makes it
// all of them, verifiably.
func SealDescriptors() error {
	if err := unix.CloseRange(firstSealedFD, ^uint(0), 0); err == nil {
		return nil
	}
	// close_range needs 5.9 and the product's floor is 5.6, so the sweep is a
	// real path rather than a formality. EBADF on a descriptor this process
	// does not hold is the expected and harmless result, which is why the
	// close error is not propagated: there is nothing for a caller to do about
	// a descriptor that was already absent.
	for fd := firstSealedFD; fd < fdSweepMax; fd++ {
		//nolint:errcheck // EBADF on an absent descriptor is the expected result; see above.
		_ = unix.Close(fd)
	}
	return nil
}
