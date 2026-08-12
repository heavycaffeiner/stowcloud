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
)

// archProfile is a table rather than a build tag, because an architecture with
// no entry has to be a refusal at runtime and not a compile error nobody sees
// until they try to build for it.
func archProfile() (auditArch uint32, rejectX32 bool, ok bool) {
	switch runtime.GOARCH {
	case "amd64":
		return auditArchAmd64, true, true
	case "arm64":
		return auditArchArm64, false, true
	}
	return 0, false, false
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
// The five entries the Rust worker did not need are scheduler and signal
// plumbing a Go runtime cannot start without. This list is the estimate the
// design carries; the shipped one is produced by running the worker under
// SECCOMP_RET_LOG against a corpus of real images and reading the audit log,
// which needs a decoder that does not exist yet.
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
	auditArch, rejectX32, ok := archProfile()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrArchUnsupported, runtime.GOARCH)
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
