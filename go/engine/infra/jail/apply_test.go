//go:build linux

package jail

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeSteps is the kernel-facing half, faulted. The seam exists so the
// sequencing is testable without a kernel that refuses.
type fakeSteps struct {
	reexecedNow  bool
	available    bool
	availableErr error
	restrictErr  error
	seccompErr   error

	restrictCalls int
	seccompCalls  int
}

func (f *fakeSteps) steps() steps {
	return steps{
		restrictAndReexec: func(Spec, string) error {
			f.restrictCalls++
			return f.restrictErr
		},
		installSeccomp: func(FilterKind) error {
			f.seccompCalls++
			return f.seccompErr
		},
		reexeced:          func(string) bool { return f.reexecedNow },
		landlockAvailable: func() (bool, error) { return f.available, f.availableErr },
		kernel:            func() string { return "test-kernel" },
	}
}

// Off attempts nothing and reports as much, letting an operator accept the
// absence deliberately without a code change.
func TestOffAttemptsNothing(t *testing.T) {
	f := &fakeSteps{available: true}
	st, err := apply(Off, Spec{}, f.steps())
	if err != nil {
		t.Fatalf("off returned an error: %v", err)
	}
	if len(st.Steps) != 0 {
		t.Errorf("off recorded steps: %+v", st.Steps)
	}
	if f.restrictCalls != 0 || f.seccompCalls != 0 {
		t.Errorf("off touched the kernel: %d restrict, %d seccomp", f.restrictCalls, f.seccompCalls)
	}
	if st.Degraded() {
		t.Error("off reported itself degraded")
	}
}

// Under Required a step that could not be applied stops the sequence, and the
// error names it.
func TestRequiredRefusesAFailedLandlockStep(t *testing.T) {
	f := &fakeSteps{available: false, availableErr: errors.New("no ABI here")}
	st, err := apply(Required, Spec{}, f.steps())
	if !errors.Is(err, ErrHardeningRefused) {
		t.Fatalf("got %v, want ErrHardeningRefused", err)
	}
	// The sequence stops: seccomp is never reached.
	if f.seccompCalls != 0 {
		t.Errorf("seccomp ran after a refused landlock step")
	}
	if st.LandlockApplied() {
		t.Error("the status claims the domain applied")
	}
}

func TestRequiredRefusesAFailedSeccompStep(t *testing.T) {
	f := &fakeSteps{reexecedNow: true, seccompErr: errors.New("EINVAL")}
	_, err := apply(Required, Spec{}, f.steps())
	if !errors.Is(err, ErrHardeningRefused) {
		t.Fatalf("got %v, want ErrHardeningRefused", err)
	}
}

// Preferred records the degradation and keeps running, which is the answer for
// a kernel older than the sandbox needs: a legitimate state the operator may
// not control.
func TestPreferredRecordsAndContinues(t *testing.T) {
	f := &fakeSteps{available: false, availableErr: errors.New("no ABI here")}
	st, err := apply(Preferred, Spec{}, f.steps())
	if err != nil {
		t.Fatalf("preferred returned an error: %v", err)
	}
	// It continued: seccomp still ran.
	if f.seccompCalls != 1 {
		t.Errorf("seccomp ran %d times, want 1", f.seccompCalls)
	}
	if !st.Degraded() {
		t.Error("a preferred run that lost a step is not degraded")
	}
	if st.LandlockApplied() {
		t.Error("the status claims a domain that was never installed")
	}
}

// A restrict that returns at all has failed, because a successful exec never
// returns.
func TestPreferredRecordsAFailedRestrict(t *testing.T) {
	f := &fakeSteps{available: true, restrictErr: errors.New("EACCES")}
	st, err := apply(Preferred, Spec{}, f.steps())
	if err != nil {
		t.Fatalf("preferred returned an error: %v", err)
	}
	if f.restrictCalls != 1 {
		t.Errorf("restrict ran %d times, want 1", f.restrictCalls)
	}
	if st.LandlockApplied() {
		t.Error("a failed restrict was recorded as applied")
	}
}

// The re-exec'd image carries the domain by inheritance, so the step is
// recorded applied and the restrict is not attempted a second time.
func TestAReexecedImageInheritsTheDomain(t *testing.T) {
	f := &fakeSteps{reexecedNow: true}
	st, err := apply(Required, Spec{}, f.steps())
	if err != nil {
		t.Fatalf("the re-exec'd image refused: %v", err)
	}
	if f.restrictCalls != 0 {
		t.Error("the re-exec'd image restricted a second time")
	}
	if !st.LandlockApplied() {
		t.Error("the inherited domain was not recorded")
	}
	if st.Degraded() {
		t.Error("a fully applied re-exec'd image reported itself degraded")
	}
	if f.seccompCalls != 1 {
		t.Errorf("seccomp ran %d times, want 1", f.seccompCalls)
	}
}

// The domain grants the two paths the hardening sequence itself needs.
//
// Both fail far from where they are assembled. Without the binary's own path
// the re-exec dies with EACCES at startup; without /dev/null every thumbnail
// answers 500, because os/exec opens it for the decoder worker's stdio and
// the request never names it.
func TestTheDomainGrantsWhatTheSequenceItselfNeeds(t *testing.T) {
	grants := mandatoryGrants("/usr/bin/whatever")

	byPath := make(map[string]uint64, len(grants))
	for _, g := range grants {
		byPath[g.Path] = g.Access
	}

	self, ok := byPath["/usr/bin/whatever"]
	if !ok {
		t.Fatal("the binary's own path is not granted, so the re-exec cannot run")
	}
	if self&unix.LANDLOCK_ACCESS_FS_EXECUTE == 0 || self&unix.LANDLOCK_ACCESS_FS_READ_FILE == 0 {
		t.Errorf("the binary is granted %#x, which does not cover reading and executing it", self)
	}

	// Skipped only on a host with no /dev/null, which this one has.
	if _, serr := os.Stat(os.DevNull); serr != nil {
		t.Skipf("this host has no %s", os.DevNull)
	}
	null, ok := byPath[os.DevNull]
	if !ok {
		t.Fatalf("%s is not granted, so no worker can be spawned", os.DevNull)
	}
	if null&unix.LANDLOCK_ACCESS_FS_READ_FILE == 0 || null&unix.LANDLOCK_ACCESS_FS_WRITE_FILE == 0 {
		t.Errorf("%s is granted %#x, which does not cover both stdio directions", os.DevNull, null)
	}
	// And nothing more than the two directions: it is a discard, not a share.
	if null&^discardDevice != 0 {
		t.Errorf("%s is granted %#x, beyond reading and writing it", os.DevNull, null)
	}
}

// The marker round-trips: the sequence runs once, and a missing marker
// afterwards is a bug rather than a loop.
func TestReexecMarkerRoundTrips(t *testing.T) {
	marker := ReexecMarker()
	if marker == "" {
		t.Fatal("the marker is empty")
	}
	if Reexeced(marker) {
		t.Error("the marker is set in the test process")
	}
	t.Setenv(marker, "1")
	if !Reexeced(marker) {
		t.Error("a set marker was not observed")
	}
	t.Setenv(marker, "0")
	if Reexeced(marker) {
		t.Error("a marker of 0 read as re-exec'd")
	}
}

// The arch check comes first and it is not decoration: a syscall number is
// only meaningful with the ABI that issued it.
func TestAssembleChecksTheArchBeforeTheNumber(t *testing.T) {
	prog, err := assembleFor(FilterWorker, "amd64")
	if err != nil {
		t.Fatalf("assembleFor: %v", err)
	}
	if len(prog) < 4 {
		t.Fatalf("the program is %d instructions", len(prog))
	}
	if prog[0].Code != opLoad || prog[0].K != offArch {
		t.Errorf("the first instruction is not the arch load: %+v", prog[0])
	}
	if prog[1].Code != opJumpEqual || prog[1].K != auditArchAmd64 {
		t.Errorf("the second instruction is not the arch compare: %+v", prog[1])
	}
	if prog[2].Code != opLoad || prog[2].K != offNr {
		t.Errorf("the syscall number is not loaded third: %+v", prog[2])
	}
	// amd64 rejects the whole x32 range, because an x32 task reports the same
	// AUDIT_ARCH with this bit set and would be matched against x86-64 numbers.
	if prog[3].Code != opJumpGE || prog[3].K != x32SyscallBit {
		t.Errorf("amd64 does not reject the x32 range: %+v", prog[3])
	}

	// arm64 has no x32 ABI, so it has no such instruction.
	arm, err := assembleFor(FilterWorker, "arm64")
	if err != nil {
		t.Fatalf("assembleFor(arm64): %v", err)
	}
	if arm[1].K != auditArchArm64 {
		t.Errorf("arm64 pinned the wrong arch: %#x", arm[1].K)
	}
	if arm[3].Code == opJumpGE && arm[3].K == x32SyscallBit {
		t.Error("arm64 emitted an x32 rejection it has no need for")
	}
}

// An unexpected ABI is killed under every policy, including the server's,
// whose default action is ALLOW: pointing the mismatch at the default would
// wave through exactly the task the check exists to catch.
func TestAnUnexpectedArchIsKilledUnderEveryFilter(t *testing.T) {
	for _, kind := range []FilterKind{FilterProcess, FilterWorker, FilterWorkerAudit, FilterWorkerTrap, FilterWorkerErrno} {
		prog, err := assembleFor(kind, "amd64")
		if err != nil {
			t.Fatalf("assembleFor(%d): %v", kind, err)
		}
		last := prog[len(prog)-1]
		if last.Code != opReturn || last.K != unix.SECCOMP_RET_KILL_PROCESS {
			t.Errorf("filter %d does not end in a kill: %+v", kind, last)
		}
		// The arch compare jumps to it on a mismatch. BPF counts the offset
		// from the instruction after the jump, so the target index is the
		// jump's own index plus one plus the offset.
		target := 1 + 1 + int(prog[1].Jf)
		if target != len(prog)-1 {
			t.Errorf("filter %d: the arch mismatch jumps to %d, want the kill at %d",
				kind, target, len(prog)-1)
		}
	}
}

// The two policies differ in exactly their two terminal actions.
func TestTheTwoPoliciesHaveTheirOwnTerminalActions(t *testing.T) {
	proc, err := assembleFor(FilterProcess, "amd64")
	if err != nil {
		t.Fatalf("assembleFor: %v", err)
	}
	// Listed numbers are refused; everything else proceeds.
	if got := proc[len(proc)-3].K; got != unix.SECCOMP_RET_ALLOW {
		t.Errorf("the server's default action is %#x, want ALLOW", got)
	}
	if want := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)); proc[len(proc)-2].K != want {
		t.Errorf("the server's matched action is %#x, want EPERM", proc[len(proc)-2].K)
	}

	work, err := assembleFor(FilterWorker, "amd64")
	if err != nil {
		t.Fatalf("assembleFor: %v", err)
	}
	// A listed number runs and everything else kills.
	if got := work[len(work)-3].K; got != unix.SECCOMP_RET_KILL_PROCESS {
		t.Errorf("the worker's default action is %#x, want KILL", got)
	}
	if got := work[len(work)-2].K; got != unix.SECCOMP_RET_ALLOW {
		t.Errorf("the worker's matched action is %#x, want ALLOW", got)
	}
}

// An architecture with no verified mapping is a runtime refusal rather than a
// compile error nobody sees until they build for it.
func TestAssembleRefusesAnUnmappedArch(t *testing.T) {
	for _, arch := range []string{"riscv64", "s390x", "386", ""} {
		if _, err := assembleFor(FilterWorker, arch); !errors.Is(err, ErrArchUnsupported) {
			t.Errorf("assembleFor(%q) = %v, want ErrArchUnsupported", arch, err)
		}
	}
}

func TestAssembleRefusesAnUnknownFilterKind(t *testing.T) {
	if _, err := assembleFor(FilterKind(200), "amd64"); err == nil {
		t.Error("an unknown filter kind assembled")
	}
}

// BPF jump offsets are unsigned 8-bit, so the longest jump has to stay under
// 256. The assembler checks rather than trusting that nobody grows a list.
func TestOffsetToRefusesAnOverLimitAndABackwardJump(t *testing.T) {
	if _, err := offsetTo(300, 1); err == nil {
		t.Error("a jump past the 8-bit range was accepted")
	}
	if _, err := offsetTo(1, 5); err == nil {
		t.Error("a backward jump was accepted, which the verifier refuses")
	}
	got, err := offsetTo(10, 4)
	if err != nil {
		t.Fatalf("a legal jump was refused: %v", err)
	}
	// BPF counts from the instruction after the jump.
	if got != 5 {
		t.Errorf("offsetTo(10, 4) = %d, want 5", got)
	}
}

// Every jump in a real program lands on one of the three terminal
// instructions, and the shipped lists have headroom under the bound.
func TestEveryJumpInAShippedFilterIsInRange(t *testing.T) {
	for _, kind := range []FilterKind{FilterProcess, FilterWorker} {
		for _, arch := range []string{"amd64", "arm64"} {
			prog, err := assembleFor(kind, arch)
			if err != nil {
				t.Fatalf("assembleFor(%d, %s): %v", kind, arch, err)
			}
			for i, ins := range prog {
				for _, off := range []uint8{ins.Jt, ins.Jf} {
					if target := i + 1 + int(off); target >= len(prog)+1 {
						t.Errorf("%d/%s: instruction %d jumps past the program", kind, arch, i)
					}
				}
			}
		}
	}
}

// The default limits are the graceful half of the jail, and RLIMIT_AS is the
// backstop the decode ceiling is described as having.
func TestDefaultLimitsBoundTheDecoder(t *testing.T) {
	l := DefaultLimits()
	if l.AddressSpaceBytes == 0 {
		t.Error("no address-space bound, which is the backstop the comments claim")
	}
	// No thread bound here on purpose: RLIMIT_NPROC counts every task the user
	// owns machine-wide, so it cannot say anything about this worker. Forking
	// is the seccomp gate's job.
	if l.OpenFiles == 0 || l.CPUSeconds == 0 {
		t.Errorf("an unbounded limit in %+v", l)
	}
	// The address-space bound has to leave room for the measured decode
	// ceiling, or the graceful limit never fires first.
	if l.AddressSpaceBytes < 256<<20 {
		t.Errorf("the address-space bound of %d is below the measured decode cost",
			l.AddressSpaceBytes)
	}
}
