//go:build linux

package jail

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"golang.org/x/sys/unix"
)

// Classic BPF opcodes, from linux/bpf_common.h. Each is spelled as the OR of
// its named parts so it can be checked against the header by eye; several of
// those parts are genuinely zero there.
const (
	bpfLd  = 0x00
	bpfW   = 0x00
	bpfAbs = 0x20
	bpfJmp = 0x05
	bpfJeq = 0x10
	bpfJge = 0x30
	bpfK   = 0x00
	bpfRet = 0x06

	opLoad      = bpfLd | bpfW | bpfAbs
	opJumpEqual = bpfJmp | bpfJeq | bpfK
	opJumpGE    = bpfJmp | bpfJge | bpfK
	opReturn    = bpfRet | bpfK
)

// Field offsets in struct seccomp_data. arch is at 4 and the syscall number is
// at 0, and reading them in that order is the whole of D4.
const (
	offNr   = 0
	offArch = 4
)

// The two AUDIT_ARCH values this build has a verified syscall mapping for.
const (
	auditArchAmd64 = 0xC000003E
	auditArchArm64 = 0xC00000B7

	// On x86-64 the x32 ABI reports the same AUDIT_ARCH with this bit set on
	// the syscall number, so an x32 task would otherwise be matched against
	// x86-64 numbers. The whole range is rejected.
	x32SyscallBit = 0x40000000
)

// FilterKind selects one of the two policies. Both are flat syscall number
// lists with no argument inspection, which is what keeps each readable in one
// screen.
type FilterKind uint8

const (
	// FilterProcess is the server's deny-list. It returns EPERM rather than
	// killing, because it is a second line of defence beside Landlock and an
	// unprivileged uid, not a sandbox on its own.
	FilterProcess FilterKind = iota

	// FilterWorker is the decoder's allow-list. Everything not on it kills the
	// process, which is the designed outcome: a crafted input that kills a
	// decoder costs one thumbnail.
	FilterWorker

	// FilterWorkerAudit is the allow-list with the kill replaced by a log
	// record. It is how the shipped list is measured rather than guessed:
	// the worker runs under it against a corpus of real images, and the audit
	// log names every syscall the list is missing.
	//
	// It is never shipped. A filter that logs instead of killing is not a
	// sandbox, and the one caller is the measurement command.
	FilterWorkerAudit

	// FilterWorkerTrap is the allow-list with the kill replaced by SIGSYS.
	//
	// It exists because SECCOMP_RET_LOG writes to the kernel audit log, which
	// needs root to read: on a machine where auditd owns it, a measurement run
	// as an ordinary user gets a clean result that means only that it could not
	// see the records. A trap is delivered to the process itself, so the
	// measurement records what it observed rather than what it was permitted to
	// read, and it needs no privilege.
	//
	// Never shipped either, for the same reason: a trap a handler swallows is
	// not a sandbox.
	FilterWorkerTrap
)

// archProfileFor is a table rather than a build tag, because an architecture
// with no entry has to be a refusal at runtime and not a compile error nobody
// sees until they try to build for it. Taking the name as an argument is what
// lets the refusal be tested from the two architectures that do have entries,
// which is otherwise a branch nothing ever runs.
func archProfileFor(goarch string) (auditArch uint32, rejectX32 bool, ok bool) {
	switch goarch {
	case "amd64":
		return auditArchAmd64, true, true
	case "arm64":
		return auditArchArm64, false, true
	}
	return 0, false, false
}

func archProfile() (auditArch uint32, rejectX32 bool, ok bool) {
	return archProfileFor(runtime.GOARCH)
}

// deniedSyscalls is the server's list. Each one is a way out of the process or
// into another one, and none of them has a legitimate caller here.
func deniedSyscalls() []int {
	return []int{
		unix.SYS_PTRACE,
		unix.SYS_PROCESS_VM_READV,
		unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_MOUNT,
		unix.SYS_KEXEC_LOAD,
		unix.SYS_KEXEC_FILE_LOAD,
		unix.SYS_BPF,
		unix.SYS_USERFAULTFD,
	}
}

// allowedSyscalls is the decoder's list.
//
// What is absent is the proof obligation, and each absence is deliberate: no
// openat and no openat2, so no file by name ever; no socket and no connect, so
// no network; no clone and no execve, so no new process; no ptrace. recvmsg and
// sendmsg are on it because they are how a job and its two descriptors arrive
// at all.
//
// This list is measured, not reasoned about. A real worker was driven over a
// real socket against the committed corpus and traced; the union of its steady
// state and the decode phase is thirteen calls, and every one of them is here.
// The remainder are startup and teardown the trace showed before the filter is
// installed or on the way out, kept because a decode that grows the heap or
// takes a signal must not be killed for it.
//
// Three came from the measurement rather than the estimate, and none would
// have been guessed:
//
//   - fcntl, which os.NewFile issues as F_GETFL on each descriptor a job
//     arrives with, to learn whether it is readable or writable.
//   - epoll_pwait and getpid, which the Go runtime's scheduler and its
//     signal path use even with one thread.
//   - epoll_create1, eventfd2 and epoll_ctl, which are the network poller
//     being built. The runtime does that lazily, on whichever job first parks
//     a goroutine on the socket, so a worker survives its first few and then
//     is killed. The original measurement had epoll_pwait without them, which
//     is the wait without the setup: it had captured a poller that was already
//     running before the trace began.
//
// clone is deliberately absent and the measurement is what makes that
// affordable. Every clone the trace showed carried CLONE_THREAD, so it was the
// runtime adding an OS thread rather than a fork. A worker that cannot clone
// cannot fork either, which is the property this list is for.
//
// GOMAXPROCS=1 does not mean one OS thread, and an earlier version of this
// note claimed it did. It bounds goroutines running Go code, not threads: the
// runtime still has five or so for its own work. What makes clone's absence
// hold is that none of them is started after the filter is installed, not that
// they do not exist.
func allowedSyscalls() []int {
	return []int{
		unix.SYS_READ,
		unix.SYS_PREAD64,
		unix.SYS_WRITE,
		unix.SYS_PWRITE64,
		unix.SYS_MMAP,
		unix.SYS_MUNMAP,
		unix.SYS_MREMAP,
		unix.SYS_BRK,
		unix.SYS_CLOSE,
		unix.SYS_FSTAT,
		unix.SYS_LSEEK,
		unix.SYS_FUTEX,
		unix.SYS_SCHED_YIELD,
		unix.SYS_EXIT,
		unix.SYS_EXIT_GROUP,
		unix.SYS_RT_SIGRETURN,
		unix.SYS_RT_SIGPROCMASK,
		unix.SYS_MADVISE,
		unix.SYS_GETRANDOM,
		unix.SYS_CLOCK_GETTIME,
		unix.SYS_RECVMSG,
		unix.SYS_SENDMSG,
		unix.SYS_NANOSLEEP,
		unix.SYS_SIGALTSTACK,
		unix.SYS_RT_SIGACTION,
		unix.SYS_GETTID,
		unix.SYS_TGKILL,

		// Measured, and absent from the estimate. See the note above.
		unix.SYS_FCNTL,
		unix.SYS_EPOLL_PWAIT,
		unix.SYS_GETPID,
		unix.SYS_EPOLL_CREATE1,
		unix.SYS_EVENTFD2,
		unix.SYS_EPOLL_CTL,

		// prctl, which the runtime issues as PR_SET_VMA_ANON_NAME to label its
		// own mappings so they are identifiable in /proc/pid/maps. It is on by
		// default from Go 1.26 and happens whenever the heap grows, so a
		// worker decoding a larger image than the last one is killed for it.
		//
		// It grants nothing: PR_SET_VMA only renames a mapping this process
		// already owns.
		unix.SYS_PRCTL,
	}
}

func stmt(code uint16, k uint32) unix.SockFilter {
	return unix.SockFilter{Code: code, K: k}
}

func jump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// assemble builds the program.
//
//	0                  A = seccomp_data.arch
//	1                  if A != AUDIT_ARCH      -> kill
//	2                  A = seccomp_data.nr
//	3                  if A >= X32_SYSCALL_BIT -> kill   (amd64 only)
//	prologue..+n-1     one compare per listed syscall
//	prologue+n         the default action for this policy
//	prologue+n+1       the matched action for this policy
//	prologue+n+2       kill, where an unexpected ABI lands
//
// The arch check comes first and it is not decoration. A syscall number is only
// meaningful together with the ABI that issued it, so a filter matching numbers
// without pinning the arch is matching numbers that mean something else.
//
// The refusal is its own instruction rather than the policy's default action,
// and that is the difference between the two things this fixes. The server's
// default action is ALLOW, so pointing an ABI mismatch at it would wave through
// exactly the task the check exists to catch. An unexpected ABI is killed under
// both policies.
//
// BPF jump offsets are unsigned 8-bit and relative to the following
// instruction, so the longest jump has to stay under 256. There is headroom at
// these list lengths; this checks rather than trusting that nobody grows one.
func assemble(kind FilterKind) ([]unix.SockFilter, error) {
	return assembleFor(kind, runtime.GOARCH)
}

func assembleFor(kind FilterKind, goarch string) ([]unix.SockFilter, error) {
	auditArch, rejectX32, ok := archProfileFor(goarch)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrArchUnsupported, goarch)
	}

	var list []int
	var matched, unmatched uint32
	switch kind {
	case FilterProcess:
		// A listed number is denied and everything else runs.
		list = deniedSyscalls()
		matched = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
		unmatched = unix.SECCOMP_RET_ALLOW
	case FilterWorker:
		// A listed number runs and everything else kills the process.
		list = allowedSyscalls()
		matched = unix.SECCOMP_RET_ALLOW
		unmatched = unix.SECCOMP_RET_KILL_PROCESS
	case FilterWorkerAudit:
		// The same list, but an unlisted call is logged and then allowed, so
		// one run finds every missing entry rather than the first.
		list = allowedSyscalls()
		matched = unix.SECCOMP_RET_ALLOW
		unmatched = unix.SECCOMP_RET_LOG
	case FilterWorkerTrap:
		// The same list, with an unlisted call raising SIGSYS in the calling
		// thread. The handler records the number and the run continues.
		list = allowedSyscalls()
		matched = unix.SECCOMP_RET_ALLOW
		unmatched = unix.SECCOMP_RET_TRAP
	default:
		return nil, fmt.Errorf("unknown seccomp filter kind %d", kind)
	}

	prologue := 3
	if rejectX32 {
		prologue = 4
	}
	n := len(list)
	unmatchedIdx := prologue + n
	matchedIdx := unmatchedIdx + 1
	refuseIdx := matchedIdx + 1

	// Every jump targets one of the three terminal instructions, so the longest
	// one is the reach from the first jump to the last instruction.
	if refuseIdx > 1+int(^uint8(0)) {
		return nil, fmt.Errorf("a %d entry seccomp list does not fit in 8-bit BPF jump offsets", n)
	}

	prog := make([]unix.SockFilter, 0, refuseIdx+1)
	prog = append(prog, stmt(opLoad, offArch))

	archMiss, err := offsetTo(refuseIdx, 1)
	if err != nil {
		return nil, err
	}
	prog = append(prog, jump(opJumpEqual, auditArch, 0, archMiss))
	prog = append(prog, stmt(opLoad, offNr))

	if rejectX32 {
		x32, err := offsetTo(refuseIdx, 3)
		if err != nil {
			return nil, err
		}
		prog = append(prog, jump(opJumpGE, x32SyscallBit, x32, 0))
	}

	for i, nr := range list {
		here := prologue + i
		off, err := offsetTo(matchedIdx, here)
		if err != nil {
			return nil, err
		}
		number, err := num.Narrow[uint32](nr)
		if err != nil {
			return nil, fmt.Errorf("syscall number %d: %w", nr, err)
		}
		// A non-match falls through to the next compare, never jumps.
		prog = append(prog, jump(opJumpEqual, number, off, 0))
	}

	prog = append(prog, stmt(opReturn, unmatched))
	prog = append(prog, stmt(opReturn, matched))
	prog = append(prog, stmt(opReturn, unix.SECCOMP_RET_KILL_PROCESS))
	if len(prog) != refuseIdx+1 {
		return nil, fmt.Errorf("assembled %d instructions, expected %d", len(prog), refuseIdx+1)
	}
	return prog, nil
}

// offsetTo is the jump distance from the instruction at from to the one at to,
// which BPF counts from the instruction after the jump.
func offsetTo(to, from int) (uint8, error) {
	d := to - from - 1
	if d < 0 {
		return 0, fmt.Errorf("a backward BPF jump from %d to %d, which the verifier refuses", from, to)
	}
	return num.Narrow[uint8](d)
}

// InstallSeccomp assembles and installs the policy for kind with TSYNC, so it
// covers every thread the Go runtime has already started rather than whichever
// one this goroutine happens to be on.
//
// Irreversible for the life of the process, and for the worker it has to be the
// last thing the jail does.
func InstallSeccomp(kind FilterKind) error {
	prog, err := assemble(kind)
	if err != nil {
		return err
	}

	// Required before an unprivileged process may install a filter at all.
	if perr := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); perr != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", perr)
	}

	length, err := num.Narrow[uint16](len(prog))
	if err != nil {
		return fmt.Errorf("seccomp program length: %w", err)
	}
	fprog := unix.SockFprog{Len: length, Filter: &prog[0]}

	// TSYNC applies the filter to every thread or fails atomically, which is
	// the answer Landlock has no equivalent of.
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP,
		uintptr(unix.SECCOMP_SET_MODE_FILTER),
		uintptr(unix.SECCOMP_FILTER_FLAG_TSYNC),
		//nolint:gosec // the kernel takes a sock_fprog pointer; there is no wrapper for seccomp.
		uintptr(unsafe.Pointer(&fprog)))
	runtime.KeepAlive(prog)
	if errno != 0 {
		return fmt.Errorf("seccomp(SECCOMP_SET_MODE_FILTER, TSYNC): %w", errno)
	}
	return nil
}

// AllowedSyscalls is the worker's list, for the command that measures it.
//
// Exported so the audit run and the list it checks cannot drift apart: a
// measurement against a copy of the list proves nothing about the one that
// ships.
func AllowedSyscalls() []int { return allowedSyscalls() }
