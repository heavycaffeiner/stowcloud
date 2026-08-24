//go:build linux

package worker

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// spinIterations is long enough to outlast any deadline a test sets and short
// enough to end on its own if nothing interrupts it.
const spinIterations = 20_000_000_000

// The proof.
//
// A security claim that cannot be executed is a comment, so the worker accepts
// a probe message and attempts, from inside the finished jail, the things the
// jail is supposed to prevent.
//
// The probes grant nothing. They run after every restriction is in place, and
// their only possible outcomes are that the kernel refused and that the kernel
// killed the process. A probe reporting success is a test failure.

// Probe is one thing to attempt.
type Probe uint8

const (
	// ProbePing attempts nothing and proves the transport works, so a run
	// where every probe was killed can be told from one where the socket was
	// never connected.
	ProbePing Probe = iota
	// ProbeOpenEtcPasswd opens a file by name.
	ProbeOpenEtcPasswd
	// ProbeCreateSocket asks for a network socket.
	ProbeCreateSocket
	// ProbeFork asks for a new process.
	ProbeFork
	// ProbeSpin burns CPU so the parent can kill it mid-job.
	ProbeSpin
)

func (p Probe) String() string {
	switch p {
	case ProbePing:
		return "ping"
	case ProbeOpenEtcPasswd:
		return "open /etc/passwd"
	case ProbeCreateSocket:
		return "create a socket"
	case ProbeFork:
		return "fork"
	case ProbeSpin:
		return "spin"
	}
	return "unknown"
}

// ProbeOutcome is what happened.
type ProbeOutcome uint8

const (
	// OutcomeRefused is the kernel saying no, which is a pass.
	OutcomeRefused ProbeOutcome = iota
	// OutcomeSucceeded is the probe getting what it asked for, which is a
	// failure. A killed probe never reports at all: the process is gone and
	// the parent sees the socket close.
	OutcomeSucceeded
	// OutcomeCompleted is ping and spin, which are not refusals.
	OutcomeCompleted
)

// OutcomeFrom decodes an outcome the worker reported in a wire field.
//
// An out-of-range value is a worker that is not this build, so it reads as a
// success: the safe direction for a proof is the one that fails the test.
func OutcomeFrom(v uint16) ProbeOutcome {
	if v > uint16(OutcomeCompleted) {
		return OutcomeSucceeded
	}
	return ProbeOutcome(v)
}

func (o ProbeOutcome) String() string {
	switch o {
	case OutcomeRefused:
		return "refused"
	case OutcomeSucceeded:
		return "succeeded"
	case OutcomeCompleted:
		return "completed"
	}
	return "unknown"
}

// RunProbe attempts one probe and reports what the kernel said.
//
// Nothing here goes through the standard library's file or network API. The Go
// runtime may service those through a different path, or not issue the syscall
// at all, and a probe that did not reach the kernel proves nothing about a
// filter that sits in front of it.
func RunProbe(p Probe) (ProbeOutcome, string) {
	switch p {
	case ProbePing:
		return OutcomeCompleted, "the transport works"

	case ProbeOpenEtcPasswd:
		// The raw syscall, not os.Open. openat is not on the allow-list, so
		// the expected outcome is a kill; on a kernel where only Landlock
		// applied, it is EACCES.
		path := pathPtr("/etc/passwd")
		if path == nil {
			return OutcomeRefused, "the probe path could not be built"
		}
		// AT_FDCWD is negative by definition and travels as its bit pattern,
		// and the path is a NUL-terminated buffer because a raw openat is the
		// only way to reach the syscall the filter is meant to refuse.
		atFDCWD := unix.AT_FDCWD
		cwd := uintptr(atFDCWD)              //nolint:gosec // G115: see above.
		buf := uintptr(unsafe.Pointer(path)) //nolint:gosec // G103: see above.
		fd, _, errno := unix.Syscall6(unix.SYS_OPENAT, cwd, buf,
			uintptr(unix.O_RDONLY), 0, 0, 0)
		if errno != 0 {
			return OutcomeRefused, fmt.Sprintf("openat: %v", errno)
		}
		_ = unix.Close(int(fd)) //nolint:errcheck // the probe already failed by succeeding.
		return OutcomeSucceeded, "openat returned a descriptor"

	case ProbeCreateSocket:
		fd, _, errno := unix.Syscall(unix.SYS_SOCKET,
			uintptr(unix.AF_INET), uintptr(unix.SOCK_STREAM), 0)
		if errno != 0 {
			return OutcomeRefused, fmt.Sprintf("socket: %v", errno)
		}
		_ = unix.Close(int(fd)) //nolint:errcheck // as above.
		return OutcomeSucceeded, "socket returned a descriptor"

	case ProbeFork:
		// clone directly, because fork has no Go form and the runtime would
		// never issue a bare one. This is the call the filter is meant to
		// kill; where it is not killed, RLIMIT_NPROC gives EAGAIN.
		pid, _, errno := unix.Syscall6(unix.SYS_CLONE,
			uintptr(unix.SIGCHLD), 0, 0, 0, 0, 0)
		if errno != 0 {
			return OutcomeRefused, fmt.Sprintf("clone: %v", errno)
		}
		if pid == 0 {
			// The child of a fork the jail was supposed to prevent. It must
			// not run any Go code: the runtime's locks are held by threads
			// that do not exist here.
			unix.Exit(0)
		}
		return OutcomeSucceeded, fmt.Sprintf("clone returned pid %d", pid)

	case ProbeSpin:
		// Burns CPU so the parent's deadline has something to interrupt.
		//
		// A bounded loop rather than a clock: this runs inside the jail, where
		// nothing is injectable, and D8 keeps the wall clock in one package.
		// The count only has to outlast a test deadline, not measure anything.
		sink := 0
		for i := 0; i < spinIterations; i++ {
			sink += i * i
			if sink == -1 {
				// Never true; it stops the loop being optimised away.
				return OutcomeCompleted, "unreachable"
			}
		}
		return OutcomeCompleted, "the spin finished, so nothing interrupted it"
	}
	return OutcomeSucceeded, "an unknown probe did nothing"
}

// pathPtr is a NUL-terminated path for a raw syscall.
func pathPtr(s string) *byte {
	b, err := unix.BytePtrFromString(s)
	if err != nil {
		return nil
	}
	return b
}
