//go:build linux

package worker

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The proof.
//
// A security claim that cannot be executed is a comment, so the worker accepts
// a probe message and attempts, from inside the finished jail, the things the
// jail is supposed to prevent.
//
// The probes grant nothing. They run after every restriction is in place, and
// their only possible outcomes are that the kernel refused and that the kernel
// killed the process. A probe reporting success is a test failure.

// spinIterations is long enough to outlast any deadline a test sets and short
// enough to end on its own if nothing interrupts it.
const spinIterations = 20_000_000_000

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
	// ProbeReportLimits reads this process's own rlimits back, so the
	// jailproof suite can assert the address-space bound is really set rather
	// than assuming ApplyLimits was called.
	ProbeReportLimits
	// ProbeCountDescriptors reports the highest descriptor this process still
	// holds, so the suite can assert the seal closed what the worker inherited.
	ProbeCountDescriptors
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
	case ProbeReportLimits:
		return "report rlimits"
	case ProbeCountDescriptors:
		return "count descriptors"
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
	// OutcomeCompleted is ping, spin and the two reporting probes, which are
	// not refusals.
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
		return probeOpen()
	case ProbeCreateSocket:
		return probeSocket()
	case ProbeFork:
		return probeFork()
	case ProbeSpin:
		return probeSpin()
	case ProbeReportLimits:
		return probeLimits()
	case ProbeCountDescriptors:
		return probeDescriptors()
	}
	return OutcomeSucceeded, "an unknown probe did nothing"
}

func probeOpen() (ProbeOutcome, string) {
	// The raw syscall, not os.Open. openat is not on the allow-list, so the
	// expected outcome is a kill; on a kernel where only Landlock applied, it
	// is EACCES.
	path := pathPtr("/etc/passwd")
	if path == nil {
		return OutcomeRefused, "the probe path could not be built"
	}
	// AT_FDCWD is negative by definition and travels as its bit pattern, and
	// the path is a NUL-terminated buffer because a raw openat is the only way
	// to reach the syscall the filter is meant to refuse.
	atFDCWD := unix.AT_FDCWD
	cwd := uintptr(atFDCWD)              //nolint:gosec // G115: see above.
	buf := uintptr(unsafe.Pointer(path)) //nolint:gosec // G103: see above.
	fd, _, errno := unix.Syscall6(unix.SYS_OPENAT, cwd, buf,
		uintptr(unix.O_RDONLY), 0, 0, 0)
	if errno != 0 {
		return OutcomeRefused, fmt.Sprintf("openat: %v", errno)
	}
	//nolint:errcheck // the probe already failed by succeeding.
	_ = unix.Close(int(fd))
	return OutcomeSucceeded, "openat returned a descriptor"
}

func probeSocket() (ProbeOutcome, string) {
	fd, _, errno := unix.Syscall(unix.SYS_SOCKET,
		uintptr(unix.AF_INET), uintptr(unix.SOCK_STREAM), 0)
	if errno != 0 {
		return OutcomeRefused, fmt.Sprintf("socket: %v", errno)
	}
	//nolint:errcheck // as above.
	_ = unix.Close(int(fd))
	return OutcomeSucceeded, "socket returned a descriptor"
}

func probeFork() (ProbeOutcome, string) {
	// clone directly, because fork has no Go form and the runtime would never
	// issue a bare one. This is the call the filter is meant to kill; where it
	// is not killed, RLIMIT_NPROC gives EAGAIN.
	pid, _, errno := unix.Syscall6(unix.SYS_CLONE,
		uintptr(unix.SIGCHLD), 0, 0, 0, 0, 0)
	if errno != 0 {
		return OutcomeRefused, fmt.Sprintf("clone: %v", errno)
	}
	if pid == 0 {
		// The child of a fork the jail was supposed to prevent. It must not run
		// any Go code: the runtime's locks are held by threads that do not
		// exist here.
		unix.Exit(0)
	}
	return OutcomeSucceeded, fmt.Sprintf("clone returned pid %d", pid)
}

func probeSpin() (ProbeOutcome, string) {
	// Burns CPU so the parent's deadline has something to interrupt.
	//
	// A bounded loop rather than a clock: this runs inside the jail, where
	// nothing is injectable. The count only has to outlast a test deadline,
	// not measure anything.
	sink := 0
	for i := range spinIterations {
		sink += i * i
		if sink == -1 {
			// Never true; it stops the loop being optimised away.
			return OutcomeCompleted, "unreachable"
		}
	}
	return OutcomeCompleted, "the spin finished, so nothing interrupted it"
}

// probeLimits reports the address-space bound this process runs under.
//
// It is what makes the wired ApplyLimits verifiable from outside: the comments
// have always claimed RLIMIT_AS backstops the decode ceiling, and this is the
// worker saying what the kernel actually gave it.
//
// The value is captured at startup, before the seccomp filter is installed,
// rather than read here. getrlimit is not on the measured allow list, and
// adding a syscall so a probe can run would widen the filter to make a test
// pass, which is the wrong direction for the thing being proved.
func probeLimits() (ProbeOutcome, string) {
	l := capturedLimits.Load()
	if l == nil {
		return OutcomeCompleted, "no limits were captured at startup"
	}
	return OutcomeCompleted, fmt.Sprintf("as=%d nofile=%d nproc=%d", l.as, l.nofile, l.nproc)
}

// startupLimits is what the worker read about itself before the filter closed
// the call that reads it.
type startupLimits struct{ as, nofile, nproc uint64 }

//nolint:gochecknoglobals // the worker is one process with one jail; the capture happens once at startup.
var capturedLimits atomic.Pointer[startupLimits]

// captureLimits records this process's rlimits, called from Run after
// ApplyLimits and before the seccomp filter.
func captureLimits() {
	var as, nofile, nproc unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_AS, &as); err != nil {
		return
	}
	//nolint:errcheck // a limit that cannot be read stays zero, which the probe reports.
	_ = unix.Getrlimit(unix.RLIMIT_NOFILE, &nofile)
	//nolint:errcheck // as above.
	_ = unix.Getrlimit(unix.RLIMIT_NPROC, &nproc)
	capturedLimits.Store(&startupLimits{as: as.Cur, nofile: nofile.Cur, nproc: nproc.Cur})
}

// probeDescriptors reports the highest descriptor this process still holds.
//
// It is what makes the wired SealDescriptors verifiable: os/exec's CLOEXEC
// defaults cover most inherited descriptors, and "most" is not a security
// answer, so the suite asserts nothing beyond the control socket survived.
//
// fstat rather than a directory read, because opening /proc/self/fd needs
// openat, which the filter refuses by design.
func probeDescriptors() (ProbeOutcome, string) {
	highest := -1
	count := 0
	for fd := range probeFDScan {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			continue
		}
		count++
		highest = fd
	}
	return OutcomeCompleted, fmt.Sprintf("open=%d highest=%d", count, highest)
}

// probeFDScan bounds the descriptor scan. Well above the handful a sealed
// worker holds, and small enough that the loop is instant under a filter that
// allows fstat and nothing else.
const probeFDScan = 256

// pathPtr is a NUL-terminated path for a raw syscall.
func pathPtr(s string) *byte {
	b, err := unix.BytePtrFromString(s)
	if err != nil {
		return nil
	}
	return b
}
