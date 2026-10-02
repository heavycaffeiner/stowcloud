//go:build linux

package jail

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// reexecMarker is placed on the image RestrictAndReexec produces, so the
// sequence executes exactly once and a marker missing afterwards indicates a bug
// instead of causing a loop.
const reexecMarker = "SC_REEXEC"

// fdSweepMax limits the descriptor sweep. It exceeds any descriptor the parent
// could plausibly hold while keeping the sweep near ten milliseconds.
const fdSweepMax = 65536

// firstSealedFD is the lowest descriptor SealDescriptors closes. Standard in,
// out and error are 0 through 2, and the worker's control socket is 3, so the
// seal starts above all four.
const firstSealedFD = 4

// Reexeced reports whether the marker indicates this process is already the
// image RestrictAndReexec produced.
func Reexeced(marker string) bool { return os.Getenv(marker) == "1" }

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

// SealDescriptors closes every descriptor above the worker's control socket
// that this process inherited.
//
// This carries as much weight as the filters. The parent is a file server, so
// the table a worker inherits at birth holds listening sockets, open share roots
// and database handles. RLIMIT_NOFILE bounds how many new descriptors the worker
// can acquire while doing nothing about inherited ones, and os/exec's CLOEXEC
// defaults cover most but not all of them, which is not a security guarantee.
//
// A descriptor with FD_CLOEXEC set cannot have survived an execve, so it was
// opened by this image and is kept. The Go runtime opens its poller, the
// poller's eventfd and the cgroup CPU limit before main; closing them leaves the
// scheduler waiting on a dead descriptor, or on a job's file once the number is
// reused.
func SealDescriptors() error {
	for fd := firstSealedFD; fd < fdSweepMax; fd++ {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		//nolint:errcheck // the descriptor was open a moment ago, and a failed close leaves nothing to retry.
		_ = unix.Close(fd)
	}
	// Nothing of the runtime's sits this high. close_range needs 5.9 while the
	// product's minimum is 5.6, where the sweep above is the bound.
	if err := unix.CloseRange(fdSweepMax, ^uint(0), 0); err != nil && !errors.Is(err, unix.ENOSYS) {
		return fmt.Errorf("closing descriptors from %d: %w", fdSweepMax, err)
	}
	return nil
}
