//go:build linux

package jail

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The combination that matters most: the strict policy on a kernel with no
// Landlock, which has to refuse to start.
//
// A second kernel cannot be booted here, so the kernel is made to look like one
// without it. A seccomp filter that answers the Landlock syscall with ENOSYS
// is indistinguishable from a kernel that does not implement it, because
// ENOSYS is exactly what such a kernel returns, and the availability probe has
// no other way to ask.
//
// The filter is installed in a re-executed child, since it cannot be removed
// once installed and the rest of this package's tests need the real answer.

const noLandlockMarker = "SC_TEST_NO_LANDLOCK"

// blockLandlock installs a filter that answers the Landlock syscall with
// ENOSYS and leaves every other syscall alone.
//
// Written by hand rather than through this package's own policy builder: the
// point is to simulate a kernel, not to exercise the code being tested.
func blockLandlock() error {
	// A classic BPF program: check the architecture, then the syscall number.
	// Comparing a syscall number without first checking the architecture is
	// the defect the real filter's own tests exist for, and it would be just
	// as wrong here.
	const (
		archOffset = 4
		nrOffset   = 0
	)
	prog := []unix.SockFilter{
		// Load the architecture and refuse to guess if it is not this one.
		{Code: 0x20, K: archOffset},
		{Code: 0x15, Jt: 0, Jf: 3, K: uint32(unix.AUDIT_ARCH_X86_64)},
		// Load the syscall number.
		{Code: 0x20, K: nrOffset},
		// The Landlock syscall answers ENOSYS.
		{Code: 0x15, Jt: 1, Jf: 0, K: unix.SYS_LANDLOCK_CREATE_RULESET},
		// Everything else, including a foreign architecture, is allowed: this
		// filter simulates a missing feature and is not a sandbox.
		{Code: 0x06, K: 0x7fff0000},
		{Code: 0x06, K: 0x00050000 | uint32(unix.ENOSYS)},
	}
	if len(prog) > 4096 {
		// A classic filter is bounded by the kernel at far more than this, and
		// the bound is here so the conversion below cannot be the thing that
		// truncates it.
		return errors.New("the filter is too long to describe")
	}
	fprog := &unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]} //nolint:gosec // the bound directly above is what makes this fit.

	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP,
		uintptr(unix.SECCOMP_SET_MODE_FILTER), 0, uintptr(unsafe.Pointer(fprog))) //nolint:gosec // G103: the kernel takes this structure by address; there is no other calling convention.
	if errno != 0 {
		return errno
	}
	return nil
}

// TestRequiredRefusesWithoutLandlock is the proof.
func TestRequiredRefusesWithoutLandlock(t *testing.T) {
	if os.Getenv(noLandlockMarker) == "1" {
		runWithoutLandlock(t)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run", "^"+t.Name()+"$", "-test.v") //nolint:gosec // G204 reads the variable: this test binary re-executing itself.
	cmd.Env = append(os.Environ(), noLandlockMarker+"=1")

	out, cerr := cmd.CombinedOutput()
	if strings.Contains(string(out), "SKIP-NO-SECCOMP") {
		t.Skipf("this host does not allow an unprivileged seccomp filter:\n%s", out)
	}
	if cerr != nil {
		t.Fatalf("the child failed: %v\n%s", cerr, out)
	}
	// The child says so explicitly, because a child that skipped also exits
	// zero and also prints a pass.
	if !strings.Contains(string(out), noLandlockProved) {
		t.Fatalf("the child did not report the proof:\n%s", out)
	}
}

// noLandlockProved is printed only after the refusal has actually been seen.
const noLandlockProved = "REFUSED-WITHOUT-LANDLOCK"

func runWithoutLandlock(t *testing.T) {
	t.Helper()

	// Landlock is available before the filter, or this proves nothing.
	if ok, err := available(); !ok || err != nil {
		t.Skipf("SKIP-NO-SECCOMP: this host has no Landlock to remove: %v", err)
	}
	if err := blockLandlock(); err != nil {
		t.Skipf("SKIP-NO-SECCOMP: cannot install the filter: %v", err)
	}
	// And now it is not.
	// The error is not consulted: what matters is that the probe no longer
	// reports Landlock as available, whichever way it says so.
	if ok, herr := available(); ok {
		t.Fatalf("the filter did not hide Landlock, so the rest proves nothing: %v", herr)
	}

	// The strict policy refuses to start, which is the property.
	st, err := apply(Required, Spec{}, kernelSteps())
	if !errors.Is(err, ErrHardeningRefused) {
		t.Fatalf("the strict policy gave %v, want a refusal", err)
	}
	step, ok := st.firstUnapplied()
	if !ok || step.Name != "landlock" {
		t.Fatalf("the refusal names %+v, want the landlock step", step)
	}

	// The permissive policy keeps running and reports the degradation, which
	// is the difference between the two and the whole reason both exist.
	pst, perr := apply(Preferred, Spec{}, kernelSteps())
	if perr != nil {
		t.Fatalf("the permissive policy refused: %v", perr)
	}
	if !pst.Degraded() {
		t.Fatal("the permissive policy reported no degradation, so a missing layer is invisible")
	}

	// And the policy that attempts nothing reports nothing, rather than
	// reporting a degradation it never tried for.
	ost, oerr := apply(Off, Spec{}, kernelSteps())
	if oerr != nil {
		t.Fatalf("the disabled policy refused: %v", oerr)
	}
	if ost.Degraded() {
		t.Fatal("the disabled policy reported a degradation it never attempted")
	}

	t.Log(noLandlockProved)
}
