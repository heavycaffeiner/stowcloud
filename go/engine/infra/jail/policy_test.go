package jail

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// ParsePolicy is the trust boundary for a configured value, so it takes the
// three spellings and refuses everything else. A name that is almost right is
// a policy the operator believes they configured.
func TestParsePolicyTakesThreeSpellingsAndRefusesTheRest(t *testing.T) {
	for name, want := range map[string]Policy{
		"required":  Required,
		"preferred": Preferred,
		"off":       Off,
	} {
		got, err := ParsePolicy(name)
		if err != nil {
			t.Errorf("ParsePolicy(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("ParsePolicy(%q) = %v, want %v", name, got, want)
		}
		if got.String() != name {
			t.Errorf("%v.String() = %q, want %q", got, got.String(), name)
		}
	}

	for _, bad := range []string{
		"", "Required", "REQUIRED", "require", "requiredd", "on", "true", "1",
		"disabled", " off", "off ", "prefered",
	} {
		if _, err := ParsePolicy(bad); err == nil {
			t.Errorf("ParsePolicy(%q) was accepted", bad)
		}
	}
}

// The names are sent to the client rather than compiled into it, so a client
// cannot offer a policy the server does not have.
func TestPolicyNamesMatchWhatParses(t *testing.T) {
	names := PolicyNames()
	if len(names) != 3 {
		t.Fatalf("PolicyNames returned %d entries: %v", len(names), names)
	}
	for _, n := range names {
		if _, err := ParsePolicy(n); err != nil {
			t.Errorf("PolicyNames offers %q, which does not parse: %v", n, err)
		}
	}
	// An unknown policy value still renders as something rather than empty.
	if got := Policy(200).String(); got != "required" {
		t.Errorf("an out-of-range policy rendered as %q", got)
	}
}

// A degradation is named in the status rather than left in a startup log,
// because a log line scrolls away and a health field does not.
func TestStatusReportsDegradationAndTheDomainState(t *testing.T) {
	applied := Status{
		Policy: Required,
		Steps: []StepStatus{
			{Name: StepLandlock, Applied: true},
			{Name: StepSeccomp, Applied: true},
		},
	}
	if applied.Degraded() {
		t.Error("a fully applied status reported itself degraded")
	}
	if !applied.LandlockApplied() {
		t.Error("LandlockApplied is false when the step applied")
	}

	partial := Status{
		Policy: Preferred,
		Steps: []StepStatus{
			{Name: StepLandlock, Err: errors.New("no ABI")},
			{Name: StepSeccomp, Applied: true},
		},
	}
	if !partial.Degraded() {
		t.Error("a status with an unapplied step is not degraded")
	}
	if partial.LandlockApplied() {
		t.Error("LandlockApplied is true when the step did not apply")
	}
	if !strings.Contains(partial.String(), "NOT applied") {
		t.Errorf("the rendering hides the degradation: %q", partial.String())
	}

	// Off asked for nothing, so it did not fail to get it.
	off := Status{Policy: Off}
	if off.Degraded() {
		t.Error("the off policy reported itself degraded")
	}
	// A status with no landlock step at all reports the domain absent rather
	// than assuming it.
	if (Status{Steps: []StepStatus{{Name: StepSeccomp, Applied: true}}}).LandlockApplied() {
		t.Error("LandlockApplied is true with no landlock step recorded")
	}
}

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
