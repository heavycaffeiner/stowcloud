//go:build linux

package jail

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"golang.org/x/sys/unix"
)

// Classic BPF opcodes taken from linux/bpf_common.h. Each appears as the OR of
// its named components so a reader can verify it against the header directly;
// several of those components are legitimately zero.
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

// Field offsets in struct seccomp_data. The syscall number is at 0 and the
// arch at 4, and reading the arch first is what makes a number mean anything.
const (
	offNr   = 0
	offArch = 4
)

// The two AUDIT_ARCH values for which this build holds a verified syscall
// mapping.
const (
	auditArchAmd64 = 0xC000003E
	auditArchArm64 = 0xC00000B7

	// Under x86-64 the x32 ABI reports an identical AUDIT_ARCH while setting
	// this bit in the syscall number, so an x32 task would otherwise be checked
	// against x86-64 numbers. The entire range is refused.
	x32SyscallBit = 0x40000000
)

// FilterKind selects one of the policies. All are flat syscall number lists
// with no argument inspection, which is what keeps each readable in one screen.
type FilterKind uint8

const (
	// FilterProcess holds the server's deny-list. It answers EPERM instead of
	// killing, since it backs up Landlock and an unprivileged uid rather than
	// standing as a sandbox by itself.
	FilterProcess FilterKind = iota

	// FilterWorker holds the decoder's allow-list. Anything absent kills the
	// process, which is the intended result: a crafted input that kills a
	// decoder costs a single thumbnail.
	FilterWorker

	// FilterWorkerAudit is the allow-list with logging substituted for the kill.
	// It is how the shipped list gets measured instead of guessed: the worker
	// runs under it across a corpus of real images, and the audit log identifies
	// every syscall the list omits.
	//
	// It never ships. A filter that logs rather than kills is not a sandbox, and
	// its only caller is the measurement command.
	FilterWorkerAudit

	// FilterWorkerTrap is the allow-list with SIGSYS substituted for the kill.
	//
	// It exists because SECCOMP_RET_LOG writes into the kernel audit log, which
	// requires root to read. On a machine where auditd owns that log, a
	// measurement run as an ordinary user returns a clean result meaning only
	// that it could not see the records. A trap arrives at the process itself,
	// so the measurement captures what it actually observed rather than what it
	// was allowed to read, and it needs no privilege.
	//
	// Also never shipped, for the same reason: a trap that a handler absorbs is
	// not a sandbox.
	FilterWorkerTrap
)

// archProfileFor uses a table instead of a build tag, because an architecture
// without an entry must produce a runtime refusal rather than a compile error
// nobody encounters until they attempt that build. Accepting the name as an
// argument is what allows the refusal to be exercised from the two architectures
// that do have entries; otherwise it is a branch nothing ever reaches.
func archProfileFor(goarch string) (auditArch uint32, rejectX32 bool, ok bool) {
	switch goarch {
	case "amd64":
		return auditArchAmd64, true, true
	case "arm64":
		return auditArchArm64, false, true
	}
	return 0, false, false
}

// deniedSyscalls holds the server's list. Every entry is a route out of this
// process or into another, and none has any legitimate caller here.
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

// allowedSyscalls holds the decoder's list.
//
// The omissions carry the security argument, and each is intentional: without
// openat or openat2 no file is ever opened by name; without socket or connect
// there is no network; without clone or execve no new process appears; ptrace is
// absent as well. recvmsg and sendmsg appear because they are the mechanism by
// which a job and its two descriptors arrive in the first place.
//
// The list comes from measurement rather than reasoning. A real worker was
// exercised over a real socket against the committed corpus while traced. The
// union of its steady state and decode phase is thirteen calls, all present
// here. The rest cover startup and teardown that the trace observed before the
// filter is installed or during shutdown, retained so a decode that grows the
// heap or receives a signal is not killed over it.
//
// Three entries emerged from measurement rather than estimation, and none would
// have been predicted:
//
//   - fcntl, issued by os.NewFile as F_GETFL against each descriptor a job
//     brings, to determine whether it is readable or writable.
//   - epoll_pwait and getpid, used by the Go runtime's scheduler and signal
//     path even with a single thread.
//   - epoll_create1, eventfd2 and epoll_ctl, which construct the network
//     poller. The runtime builds it lazily on whichever job first parks a
//     goroutine against the socket, so a worker handles its first few jobs and
//     is then killed. An earlier measurement recorded epoll_pwait without these,
//     capturing the wait without its setup because the poller was already
//     running before that trace started.
//
// clone is deliberately excluded, and the measurement is what makes exclusion
// viable. Every clone the trace recorded carried CLONE_THREAD, meaning the
// runtime was adding an OS thread rather than forking. A worker unable to clone
// is equally unable to fork, which is the property this list secures.
//
// GOMAXPROCS=1 does not imply a single OS thread. It caps goroutines executing
// Go code rather than threads, and the runtime keeps roughly five for its own
// purposes. What makes clone's absence workable is that none of them starts
// after the filter is installed, not that they are absent.
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

		// Found by measurement and missing from the estimate; see above.
		unix.SYS_FCNTL,
		unix.SYS_EPOLL_PWAIT,
		unix.SYS_GETPID,
		unix.SYS_EPOLL_CREATE1,
		unix.SYS_EVENTFD2,
		unix.SYS_EPOLL_CTL,

		// prctl, issued by the runtime as PR_SET_VMA_ANON_NAME so its own
		// mappings are identifiable in /proc/pid/maps. Enabled by default
		// from Go 1.26 and triggered whenever the heap grows, so a worker
		// decoding a larger image than its predecessor would be killed over it.
		//
		// It confers nothing: PR_SET_VMA merely renames a mapping this process
		// already holds.
		unix.SYS_PRCTL,
	}
}

// AllowedSyscalls exposes the worker's list to the command that measures it.
//
// Exported so the audit run and the list under test cannot diverge: measuring
// against a duplicate of the list establishes nothing about the one that
// ships.
func AllowedSyscalls() []int { return allowedSyscalls() }

func stmt(code uint16, k uint32) unix.SockFilter {
	return unix.SockFilter{Code: code, K: k}
}

func jump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// assembleFor constructs the program.
//
//	0                load seccomp_data.arch
//	1                arch mismatch          -> kill
//	2                load seccomp_data.nr
//	3                nr >= X32_SYSCALL_BIT  -> kill   (amd64 only)
//	prologue..+n-1   one comparison per listed syscall
//	prologue+n       this policy's default action
//	prologue+n+1     this policy's action on a match
//	prologue+n+2     kill, where an unexpected ABI arrives
//
// The architecture check leads, and not for tidiness. A syscall number carries
// meaning only alongside the ABI that issued it, so a filter comparing numbers
// without pinning the architecture is comparing numbers that denote something
// else entirely.
//
// The refusal occupies its own instruction rather than relying on the policy's
// default action, which is what separates the two problems this addresses. The
// server's default action is ALLOW, so directing an ABI mismatch there would
// admit precisely the task the check exists to catch. An unexpected ABI is
// killed under every policy.
//
// BPF jump offsets are unsigned 8-bit values relative to the following
// instruction, so the longest jump must remain below 256. These list lengths
// leave room to spare; this verifies rather than assuming nobody extends one.
func assembleFor(kind FilterKind, goarch string) ([]unix.SockFilter, error) {
	auditArch, rejectX32, ok := archProfileFor(goarch)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrArchUnsupported, goarch)
	}

	var list []int
	var matched, unmatched uint32
	switch kind {
	case FilterProcess:
		// Listed numbers are refused; everything else proceeds.
		list = deniedSyscalls()
		matched = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
		unmatched = unix.SECCOMP_RET_ALLOW
	case FilterWorker:
		// Listed numbers proceed; everything else kills the process.
		list = allowedSyscalls()
		matched = unix.SECCOMP_RET_ALLOW
		unmatched = unix.SECCOMP_RET_KILL_PROCESS
	case FilterWorkerAudit:
		// The same list, except an unlisted call is logged and then permitted,
		// so a single run surfaces every missing entry instead of just the
		// first.
		list = allowedSyscalls()
		matched = unix.SECCOMP_RET_ALLOW
		unmatched = unix.SECCOMP_RET_LOG
	case FilterWorkerTrap:
		// The same list, with an unlisted call raising SIGSYS on the calling
		// thread. The handler notes the number and execution continues.
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

	// All jumps land on one of the three terminal instructions, so the longest
	// is the span from the first jump to the final instruction.
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
		x32, xerr := offsetTo(refuseIdx, 3)
		if xerr != nil {
			return nil, xerr
		}
		prog = append(prog, jump(opJumpGE, x32SyscallBit, x32, 0))
	}

	for i, nr := range list {
		here := prologue + i
		off, oerr := offsetTo(matchedIdx, here)
		if oerr != nil {
			return nil, oerr
		}
		number, nerr := num.Narrow[uint32](nr)
		if nerr != nil {
			return nil, fmt.Errorf("syscall number %d: %w", nr, nerr)
		}
		// A non-match falls through to the following compare and never jumps.
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

func assemble(kind FilterKind) ([]unix.SockFilter, error) {
	return assembleFor(kind, runtime.GOARCH)
}

// offsetTo gives the jump distance between the instructions at from and to,
// which BPF measures starting from the instruction after the jump.
func offsetTo(to, from int) (uint8, error) {
	d := to - from - 1
	if d < 0 {
		return 0, fmt.Errorf("a backward BPF jump from %d to %d, which the verifier refuses", from, to)
	}
	return num.Narrow[uint8](d)
}

// InstallSeccomp builds and applies the policy for kind using TSYNC, so it
// reaches every thread the Go runtime has already created rather than only
// whichever one this goroutine currently occupies.
//
// The effect lasts for the process's lifetime, and in the worker it must be the
// final action the jail takes.
func InstallSeccomp(kind FilterKind) error {
	prog, err := assemble(kind)
	if err != nil {
		return err
	}

	// A precondition for any unprivileged process installing a filter.
	if perr := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); perr != nil {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", perr)
	}

	length, err := num.Narrow[uint16](len(prog))
	if err != nil {
		return fmt.Errorf("seccomp program length: %w", err)
	}
	fprog := unix.SockFprog{Len: length, Filter: &prog[0]}

	// TSYNC either applies the filter across all threads or fails atomically,
	// a guarantee Landlock provides no counterpart to.
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP,
		uintptr(unix.SECCOMP_SET_MODE_FILTER),
		uintptr(unix.SECCOMP_FILTER_FLAG_TSYNC),
		//nolint:gosec // seccomp has no x/sys wrapper and the kernel wants a sock_fprog pointer.
		uintptr(unsafe.Pointer(&fprog)))
	runtime.KeepAlive(prog)
	runtime.KeepAlive(fprog)
	if errno != 0 {
		return fmt.Errorf("seccomp(SECCOMP_SET_MODE_FILTER, TSYNC): %w", errno)
	}
	return nil
}
