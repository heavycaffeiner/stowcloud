//go:build linux

package jail

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// Limits are the resource bounds the decoder runs under. They are the graceful
// half of the jail: the syscall filter decides what the worker may do, and
// these decide how much of it.
type Limits struct {
	AddressSpaceBytes uint64
	CPUSeconds        uint64
	OpenFiles         uint64
	ChildProcesses    uint64
}

func DefaultLimits() Limits {
	return Limits{
		AddressSpaceBytes: 512 << 20,
		CPUSeconds:        10,
		OpenFiles:         16,
		ChildProcesses:    0,
	}
}

// ApplyLimits sets both the soft and the hard limit for each, so nothing in the
// worker can raise one back.
func ApplyLimits(l Limits) error {
	for _, r := range []struct {
		name     string
		resource int
		value    uint64
	}{
		{"RLIMIT_AS", unix.RLIMIT_AS, l.AddressSpaceBytes},
		{"RLIMIT_CPU", unix.RLIMIT_CPU, l.CPUSeconds},
		{"RLIMIT_NOFILE", unix.RLIMIT_NOFILE, l.OpenFiles},
		{"RLIMIT_NPROC", unix.RLIMIT_NPROC, l.ChildProcesses},
		// No core dumps: a decoder's address space holds someone's file.
		{"RLIMIT_CORE", unix.RLIMIT_CORE, 0},
	} {
		lim := unix.Rlimit{Cur: r.value, Max: r.value}
		if err := unix.Setrlimit(r.resource, &lim); err != nil {
			return fmt.Errorf("setrlimit(%s, %d): %w", r.name, r.value, err)
		}
	}
	return nil
}
