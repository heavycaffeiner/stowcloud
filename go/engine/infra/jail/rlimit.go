//go:build linux

package jail

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Limits define the resource bounds the decoder operates under. They form the
// graceful half of the jail: the syscall filter governs what the worker may do,
// and these govern how much.
//
// RLIMIT_AS carries the most weight, serving as the kernel-level backstop the
// in-process pixel ceiling has always been said to have. An in-process bound is
// enforced by the decoder itself, and a decoder exploit is precisely the
// situation where that enforcement stops mattering.
//
// RLIMIT_NPROC is deliberately absent. It reads as a per-process bound on
// children and is neither: the kernel counts every task the *user* owns,
// across every process on the machine, and a thread counts the same as a
// process. A worker set to zero could not add an OS thread and died with
// "fatal error: newosproc" the first time its scheduler wanted one; any
// non-zero value is measured against the rest of the user's machine, so it
// bounds nothing about this worker. Forking is refused by the seccomp gate,
// which kills any clone without CLONE_THREAD, and that is a bound on the
// worker itself.
type Limits struct {
	AddressSpaceBytes uint64
	CPUSeconds        uint64
	OpenFiles         uint64
}

func DefaultLimits() Limits {
	return Limits{
		AddressSpaceBytes: defaultAddressSpaceBytes,
		CPUSeconds:        10,
		OpenFiles:         16,
	}
}

// defaultAddressSpaceBytes is measured rather than chosen, and the measurement
// is what makes it this large.
//
// RLIMIT_AS bounds the whole address space, not resident memory, and the Go
// runtime reserves far more of it than it ever touches: arena and heap
// reservations alone put the floor above 1 GiB, so a process under it dies at
// startup or on its first allocation rather than on a large image.
//
// Measured on this tree's decoders: a Go process encoding a PNG fails under
// 1 GiB and succeeds at 1.5 GiB, and the worst case the decode ceiling admits,
// 64 Mpx decoded as RGBA and re-encoded, also fits at 1.5 GiB. Two gigabytes is
// that floor with headroom.
//
// It is still a real backstop. The graceful pixel ceiling refuses a bomb long
// before this, so what remains for the kernel is the case that ceiling cannot
// see: a decoder exploit allocating on its own account, where an in-process
// bound has already stopped counting.
const defaultAddressSpaceBytes = 2 << 30

// ApplyLimits sets soft and hard limits together for each bound, so nothing
// inside the worker can raise one again.
func ApplyLimits(l Limits) error {
	for _, r := range []struct {
		name     string
		resource int
		value    uint64
	}{
		{"RLIMIT_AS", unix.RLIMIT_AS, l.AddressSpaceBytes},
		{"RLIMIT_CPU", unix.RLIMIT_CPU, l.CPUSeconds},
		{"RLIMIT_NOFILE", unix.RLIMIT_NOFILE, l.OpenFiles},
		// Core dumps disabled, since a decoder's address space contains
		// somebody's file.
		{"RLIMIT_CORE", unix.RLIMIT_CORE, 0},
	} {
		lim := unix.Rlimit{Cur: r.value, Max: r.value}
		if err := unix.Setrlimit(r.resource, &lim); err != nil {
			return fmt.Errorf("setrlimit(%s, %d): %w", r.name, r.value, err)
		}
	}
	return nil
}
