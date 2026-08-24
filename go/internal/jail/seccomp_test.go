//go:build linux

package jail

import (
	"errors"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func kinds() []FilterKind { return []FilterKind{FilterProcess, FilterWorker} }

func mustAssemble(t *testing.T, kind FilterKind) []unix.SockFilter {
	t.Helper()
	prog, err := assemble(kind)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return prog
}

// The arch is loaded and compared before the syscall number is ever read. A
// number is only meaningful together with the ABI that issued it, so a filter
// that compares numbers without pinning the arch is comparing numbers that mean
// something else.
func TestArchIsCheckedBeforeAnySyscallNumberIsTrusted(t *testing.T) {
	wantArch, _, ok := archProfile()
	if !ok {
		t.Skip("this architecture has no verified mapping, which is a refusal rather than a filter")
	}
	for _, kind := range kinds() {
		prog := mustAssemble(t, kind)
		if prog[0].Code != opLoad || prog[0].K != offArch {
			t.Fatalf("kind %d: instruction 0 is %+v, want a load of seccomp_data.arch", kind, prog[0])
		}
		if prog[1].Code != opJumpEqual || prog[1].K != wantArch {
			t.Fatalf("kind %d: instruction 1 is %+v, want a compare against AUDIT_ARCH", kind, prog[1])
		}
		if prog[2].Code != opLoad || prog[2].K != offNr {
			t.Fatalf("kind %d: instruction 2 is %+v, want a load of seccomp_data.nr", kind, prog[2])
		}
	}
}

// A mismatched arch is killed under both policies.
//
// The server's own default action is ALLOW, so a mismatch that landed on the
// policy's default rather than on a dedicated refusal would wave through
// exactly the task the arch check exists to catch. That is F1 with the check
// present and pointed at the wrong instruction, which is the version a reader
// would not notice.
func TestAMismatchedArchIsKilledUnderBothPolicies(t *testing.T) {
	for _, kind := range kinds() {
		prog := mustAssemble(t, kind)
		target := 1 + 1 + int(prog[1].Jf)
		if target >= len(prog) {
			t.Fatalf("kind %d: the arch mismatch jumps out of the program", kind)
		}
		if prog[target].K != unix.SECCOMP_RET_KILL_PROCESS {
			t.Fatalf("kind %d: an arch mismatch lands on %#x, want a kill", kind, prog[target].K)
		}
	}
}

// F1. The x32 ABI reports the same AUDIT_ARCH with a bit set on the number, so
// an x32 task would otherwise be matched against x86-64 numbers.
func TestX32IsRejectedBeforeAnyCompare(t *testing.T) {
	_, rejectX32, ok := archProfile()
	if !ok {
		t.Skip("no verified mapping for this architecture")
	}
	if !rejectX32 {
		t.Skip("the x32 range only exists on amd64")
	}
	for _, kind := range kinds() {
		prog := mustAssemble(t, kind)
		if prog[3].Code != opJumpGE || prog[3].K != x32SyscallBit {
			t.Fatalf("kind %d: instruction 3 is %+v, want the x32 range rejection", kind, prog[3])
		}
		target := 3 + 1 + int(prog[3].Jt)
		if target >= len(prog) {
			t.Fatalf("kind %d: the x32 rejection jumps out of the program", kind)
		}
		if prog[target].K != unix.SECCOMP_RET_KILL_PROCESS {
			t.Fatalf("kind %d: the x32 rejection lands on %#x, want a kill", kind, prog[target].K)
		}
	}
}

// An off-by-one in a jump is a filter that allows what it meant to refuse, and
// there is no way to notice at runtime.
func TestEveryJumpLandsInsideTheProgram(t *testing.T) {
	for _, kind := range kinds() {
		prog := mustAssemble(t, kind)
		for i, ins := range prog {
			if ins.Code&bpfJmp != bpfJmp {
				continue
			}
			for _, off := range []uint8{ins.Jt, ins.Jf} {
				target := i + 1 + int(off)
				if target >= len(prog) {
					t.Fatalf("kind %d: instruction %d jumps to %d, past the %d instruction program",
						kind, i, target, len(prog))
				}
			}
		}
	}
}

// Every listed syscall jumps to the matched action and a miss falls through to
// the next compare rather than jumping anywhere.
func TestEveryListedSyscallJumpsToTheMatchedAction(t *testing.T) {
	_, rejectX32, ok := archProfile()
	if !ok {
		t.Skip("no verified mapping for this architecture")
	}
	prologue := 3
	if rejectX32 {
		prologue = 4
	}

	for _, kind := range kinds() {
		list := deniedSyscalls()
		if kind == FilterWorker {
			list = allowedSyscalls()
		}
		prog := mustAssemble(t, kind)
		matchedIdx := len(prog) - 2
		for i, nr := range list {
			ins := prog[prologue+i]
			if int64(ins.K) != int64(nr) {
				t.Fatalf("kind %d: compare %d is against %d, want %d", kind, i, ins.K, nr)
			}
			if got := prologue + i + 1 + int(ins.Jt); got != matchedIdx {
				t.Fatalf("kind %d: compare %d lands on %d, want the matched action at %d",
					kind, i, got, matchedIdx)
			}
			if ins.Jf != 0 {
				t.Fatalf("kind %d: compare %d jumps on a miss instead of falling through", kind, i)
			}
		}
	}
}

// The terminal instructions are the last three, in the order the jumps assume:
// the policy's default, the policy's matched action, and the ABI refusal.
func TestTheTerminalActionsAreWhereTheJumpsExpect(t *testing.T) {
	worker := mustAssemble(t, FilterWorker)
	n := len(worker)
	if worker[n-3].Code != opReturn || worker[n-3].K != unix.SECCOMP_RET_KILL_PROCESS {
		t.Fatalf("the worker's default action is %+v, want a kill", worker[n-3])
	}
	if worker[n-2].Code != opReturn || worker[n-2].K != unix.SECCOMP_RET_ALLOW {
		t.Fatalf("the worker's matched action is %+v, want allow", worker[n-2])
	}

	process := mustAssemble(t, FilterProcess)
	n = len(process)
	if process[n-3].K != unix.SECCOMP_RET_ALLOW {
		t.Fatalf("the server's default action is %#x, want allow", process[n-3].K)
	}
	if process[n-2].K != unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM) {
		t.Fatalf("the server's matched action is %#x, want EPERM", process[n-2].K)
	}

	for _, kind := range kinds() {
		prog := mustAssemble(t, kind)
		last := prog[len(prog)-1]
		if last.Code != opReturn || last.K != unix.SECCOMP_RET_KILL_PROCESS {
			t.Fatalf("kind %d: the ABI refusal is %+v, want a kill", kind, last)
		}
	}
}

// The absences are the proof obligations. If one of these ever appears on the
// worker's list, everything the jail claims stops meaning what it says.
func TestTheWorkerListDoesNotCarryWhatTheJailExistsToDeny(t *testing.T) {
	allowed := allowedSyscalls()
	for _, denied := range []struct {
		name string
		nr   int
	}{
		{"openat", unix.SYS_OPENAT},
		{"openat2", unix.SYS_OPENAT2},
		{"socket", unix.SYS_SOCKET},
		{"connect", unix.SYS_CONNECT},
		{"clone", unix.SYS_CLONE},
		{"execve", unix.SYS_EXECVE},
		{"ptrace", unix.SYS_PTRACE},
		{"mprotect", unix.SYS_MPROTECT},
		{"statx", unix.SYS_STATX},
	} {
		if slices.Contains(allowed, denied.nr) {
			t.Fatalf("%s is on the worker's allow-list", denied.name)
		}
	}
}

// F3, both halves.
//
// An earlier process filter was an empty list on anything but x86_64, and
// an empty list short-circuited to a warning. The image publishes linux/arm64,
// so a published image ran with no process-level syscall filter and the only
// signal was one log line.
//
// The architecture is a parameter here rather than the one this test happens to
// run on, because otherwise the refusal branch is unreachable from either
// architecture that ships and the test would report a skip forever.
func TestEveryPublishedArchitectureHasAVerifiedMapping(t *testing.T) {
	for _, arch := range []struct {
		goarch    string
		auditArch uint32
		rejectX32 bool
	}{
		{"amd64", auditArchAmd64, true},
		{"arm64", auditArchArm64, false},
	} {
		got, x32, ok := archProfileFor(arch.goarch)
		if !ok {
			t.Fatalf("%s is published and has no verified mapping", arch.goarch)
		}
		if got != arch.auditArch || x32 != arch.rejectX32 {
			t.Fatalf("%s maps to %#x x32=%v", arch.goarch, got, x32)
		}
		for _, kind := range kinds() {
			prog, err := assembleFor(kind, arch.goarch)
			if err != nil {
				t.Fatalf("%s kind %d: %v", arch.goarch, kind, err)
			}
			if len(prog) < 4 {
				t.Fatalf("%s kind %d assembled %d instructions, which cannot be a filter",
					arch.goarch, kind, len(prog))
			}
		}
	}
}

func TestAnUnmappedArchitectureRefusesRatherThanEmittingAnEmptyFilter(t *testing.T) {
	for _, goarch := range []string{"riscv64", "ppc64le", "s390x", "386", ""} {
		if _, _, ok := archProfileFor(goarch); ok {
			t.Fatalf("%q reports a mapping this build has not verified", goarch)
		}
		for _, kind := range kinds() {
			if _, err := assembleFor(kind, goarch); !errors.Is(err, ErrArchUnsupported) {
				t.Fatalf("%q kind %d assembled a filter anyway: %v", goarch, kind, err)
			}
		}
	}
}

func TestOffsetToRefusesABackwardJump(t *testing.T) {
	if _, err := offsetTo(2, 5); err == nil {
		t.Fatal("a backward jump was accepted")
	}
	got, err := offsetTo(10, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got != 5 {
		t.Fatalf("offset = %d, want 5", got)
	}
}

func TestAnUnknownFilterKindIsRefused(t *testing.T) {
	if _, err := assemble(FilterKind(99)); err == nil {
		t.Fatal("an unknown filter kind assembled something")
	}
}
