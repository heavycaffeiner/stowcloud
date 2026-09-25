//go:build linux

// The tests here reach Refuse and AllowedSyscalls, which live in files this
// package only builds on Linux. Without the tag the Windows test binary
// fails to compile, which is a broken build rather than a skipped test.

package jail

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The refusal names the step, the errno and the kernel, because "hardening
// failed" tells an operator nothing they can act on.
func TestRefuseNamesWhatCouldNotBeApplied(t *testing.T) {
	var sb strings.Builder
	code := Refuse(&sb, Status{
		Policy: Required,
		Kernel: "6.1.0-test",
		Steps:  []StepStatus{{Name: StepLandlock, Err: errors.New("ENOSYS from the kernel")}},
	})
	if code == 0 {
		t.Error("Refuse returned a success exit code")
	}
	out := sb.String()
	for _, want := range []string{StepLandlock, "ENOSYS from the kernel", "6.1.0-test", "preferred"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, out)
		}
	}
}

// A status with nothing unapplied still yields the configuration exit code
// rather than reporting success from a refusal path.
func TestRefuseWithNothingUnapplied(t *testing.T) {
	var sb strings.Builder
	if code := Refuse(&sb, Status{Policy: Required}); code == 0 {
		t.Error("Refuse returned zero for a status with no failing step")
	}
}

// The allow list is measured rather than reasoned about, and what is absent is
// the proof obligation: no way to open a file by name, reach the network, or
// make another process.
func TestTheWorkerListOmitsTheCallsItIsDefinedBy(t *testing.T) {
	allowed := AllowedSyscalls()
	if len(allowed) == 0 {
		t.Fatal("the worker allow list is empty")
	}

	// Named by number rather than by constant so this test states the
	// obligation independently of the list it checks.
	forbidden := map[string]int{
		"openat":  257,
		"openat2": 437,
		"socket":  41,
		"connect": 42,
		"clone":   56,
		"execve":  59,
		"ptrace":  101,
	}
	for name, nr := range forbidden {
		if slices.Contains(allowed, nr) {
			t.Errorf("the worker allow list contains %s (%d), which it is defined by not having", name, nr)
		}
	}

	// The two that must be there, because they are how a job and its
	// descriptors arrive at all.
	for name, nr := range map[string]int{"recvmsg": 47, "sendmsg": 46} {
		if !slices.Contains(allowed, nr) {
			t.Errorf("the worker allow list is missing %s (%d), so no job could arrive", name, nr)
		}
	}
}

// Exported so a measurement run and the list that ships cannot drift apart: a
// measurement against a copy proves nothing about the real one.
func TestAllowedSyscallsIsACopyOfOneList(t *testing.T) {
	a, b := AllowedSyscalls(), AllowedSyscalls()
	if !slices.Equal(a, b) {
		t.Error("two reads of the allow list disagree")
	}
	a[0] = -1
	if AllowedSyscalls()[0] == -1 {
		t.Error("a caller mutated the shipped list")
	}
}
