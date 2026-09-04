//go:build linux

package jail

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// reexecMarker is placed on the image RestrictAndReexec produces, so the
// sequence executes exactly once and a marker missing afterwards indicates a bug
// instead of causing a loop.
const reexecMarker = "SC_REEXEC"

// fdSweepMax limits the fallback descriptor sweep. It exceeds any descriptor the
// parent could plausibly hold while staying small enough for the loop to finish
// instantly.
const fdSweepMax = 65536

// firstSealedFD is the lowest descriptor SealDescriptors closes. Standard in,
// out and error are 0 through 2, and the worker's control socket is 3, so the
// seal starts above all four.
const firstSealedFD = 4

// Reexeced reports whether the marker indicates this process is already the
// image RestrictAndReexec produced.
func Reexeced(marker string) bool { return os.Getenv(marker) == "1" }

// ReexecMarker is the environment variable the sequence marks its own image
// with, so a caller assembling a worker can clear it deliberately.
func ReexecMarker() string { return reexecMarker }

// RestrictAndReexec applies spec to the calling thread and then swaps out the
// process image, letting every thread of the resulting process inherit the
// domain.
// On success it does not return.
//
// The re-exec is the entire mechanism. landlock_restrict_self constrains only
// the calling thread; threads created later inherit the domain of whichever
// thread created them; and the Go runtime has already spawned several threads
// before main begins. Invoking it from a goroutine restricts whatever thread
// that goroutine occupied, leaves the rest unconstrained, and still reports
// success, so the process claims to be sandboxed while it is not. A Landlock
// domain survives execve, and afterwards the process has exactly one thread
// carrying it.
//
// The caller must remain locked to the OS thread for the duration.
func RestrictAndReexec(spec Spec, marker string) error {
	// Read before the domain exists, since reading it afterwards may be
	// precisely what the domain prohibits.
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoProc, err)
	}
	if len(os.Args) == 0 {
		return fmt.Errorf("%w: this process has no argv to re-exec with", ErrNoProc)
	}

	spec.GrantBeneath = append(spec.GrantBeneath, mandatoryGrants(self)...)

	if rerr := restrict(spec); rerr != nil {
		return rerr
	}

	env := append(os.Environ(), marker+"=1")
	// unix.Exec replaces the process image, so nothing following it executes.
	return unix.Exec(self, os.Args, env)
}

// mandatoryGrants are the paths the sequence itself needs, granted here rather
// than left to the caller. A sequence that can be assembled incorrectly in one
// place eventually will be, and each of these fails somewhere far from the
// assembly:
//
//   - The binary's own path. Without it the re-exec below fails with EACCES
//     and the process dies at startup.
//   - /dev/null, which os/exec opens for any child stream a caller left nil.
//     The server spawns its decoder worker that way, so a domain without it
//     answers every thumbnail 500 over a path no request named. Measured:
//     "starting a worker: open /dev/null: permission denied". The grant
//     confers nothing: a read gives EOF and a write is discarded.
//
// A host with no /dev/null is not this server's to fix and not worth refusing
// to boot over, so the grant is skipped and a worker fails as it would have.
func mandatoryGrants(self string) []Grant {
	grants := []Grant{{Path: self, Access: readExecute}}
	if _, err := os.Stat(os.DevNull); err == nil {
		grants = append(grants, Grant{Path: os.DevNull, Access: discardDevice})
	}
	return grants
}

// SealDescriptors closes every descriptor above the worker's control socket.
//
// This carries as much weight as the filters. The parent is a file server, so
// the table a worker inherits at birth holds listening sockets, open share roots
// and database handles. RLIMIT_NOFILE bounds how many new descriptors the worker
// can acquire while doing nothing about inherited ones, and os/exec's CLOEXEC
// defaults cover most but not all of them, which is not a security guarantee.
// This covers all of them, verifiably.
func SealDescriptors() error {
	if err := unix.CloseRange(firstSealedFD, ^uint(0), 0); err == nil {
		return nil
	}
	// close_range requires 5.9 while the product's minimum is 5.6, making the
	// sweep a genuine code path rather than a formality. EBADF on a descriptor
	// this process never held is the expected and harmless outcome, which is why
	// the close error is not propagated: a caller can do nothing about a
	// descriptor that was already gone.
	for fd := firstSealedFD; fd < fdSweepMax; fd++ {
		//nolint:errcheck // EBADF on an absent descriptor is the expected result; see above.
		_ = unix.Close(fd)
	}
	return nil
}
