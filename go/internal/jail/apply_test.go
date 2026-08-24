//go:build linux

package jail

import (
	"errors"
	"strings"
	"testing"
)

// faulted builds a steps set whose kernel calls are replaced, so the refusal
// path can be exercised without a kernel that refuses.
func faulted(landlockErr, seccompErr error, reexeced bool) steps {
	return steps{
		restrictAndReexec: func(Spec, string) error { return landlockErr },
		installSeccomp:    func(FilterKind) error { return seccompErr },
		reexeced:          func(string) bool { return reexeced },
		landlockAvailable: func() (bool, error) { return landlockErr == nil, landlockErr },
		kernel:            func() string { return "6.12.0-test" },
	}
}

// F2 and D3. Today the result is logged and dropped, so a container that
// silently refuses both layers starts identically to one that enforces both.
// This is the test that fails without the required mode existing.
func TestRequiredRefusesToStartWhenAStepCannotBeApplied(t *testing.T) {
	probeFault := errors.New("landlock_create_ruleset: function not implemented")
	st, err := apply(Required, Spec{}, faulted(probeFault, nil, false))
	if !errors.Is(err, ErrHardeningRefused) {
		t.Fatalf("apply = %v, want ErrHardeningRefused", err)
	}
	if !errors.Is(err, probeFault) {
		t.Fatalf("the refusal dropped the reason: %v", err)
	}
	if !st.Degraded() {
		t.Fatal("the status does not report the degradation it refused over")
	}

	var out strings.Builder
	if code := Refuse(&out, st); code != 78 {
		t.Fatalf("exit code = %d, want 78", code)
	}
	msg := out.String()
	for _, want := range []string{"landlock", "not implemented", "6.12.0-test", "preferred"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal does not name %q:\n%s", want, msg)
		}
	}
}

func TestRequiredRefusesOnTheSeccompStepToo(t *testing.T) {
	filterFault := errors.New("seccomp: operation not permitted")
	st, err := apply(Required, Spec{}, faulted(nil, filterFault, true))
	if !errors.Is(err, ErrHardeningRefused) || !errors.Is(err, filterFault) {
		t.Fatalf("apply = %v, want a refusal naming the seccomp failure", err)
	}
	step, ok := st.firstUnapplied()
	if !ok || step.Name != "seccomp" {
		t.Fatalf("the refusal names %+v, want the seccomp step", step)
	}
}

// A downgrade is loud. Preferred keeps running and reports the layer it did not
// get, in a field a health endpoint can read rather than only in a startup log.
func TestPreferredDegradesAndSaysSo(t *testing.T) {
	probeFault := errors.New("landlock is not available on this kernel")
	st, err := apply(Preferred, Spec{}, faulted(probeFault, nil, false))
	if err != nil {
		t.Fatalf("preferred returned an error: %v", err)
	}
	if !st.Degraded() {
		t.Fatal("a missing layer was not reported as a degradation")
	}
	if !strings.Contains(st.String(), "landlock") {
		t.Fatalf("the status does not name the missing layer:\n%s", st)
	}
	// The layer that could be applied still was.
	var seccomp StepStatus
	for _, s := range st.Steps {
		if s.Name == "seccomp" {
			seccomp = s
		}
	}
	if !seccomp.Applied {
		t.Fatal("a failure in one layer skipped the other")
	}
}

// Off attempts nothing and says so, which is how "I know, and I accept it" is
// expressible without a code change.
func TestOffAttemptsNothing(t *testing.T) {
	attempted := false
	s := faulted(nil, nil, false)
	s.installSeccomp = func(FilterKind) error { attempted = true; return nil }
	s.restrictAndReexec = func(Spec, string) error { attempted = true; return nil }

	st, err := apply(Off, Spec{}, s)
	if err != nil {
		t.Fatalf("off returned an error: %v", err)
	}
	if attempted {
		t.Fatal("off attempted a step")
	}
	if st.Degraded() {
		t.Fatal("off is not a degradation, it is a decision")
	}
	if len(st.Steps) != 0 {
		t.Fatalf("off reported steps: %v", st.Steps)
	}
}

// The image produced by the re-exec carries the domain by inheritance, so it
// must not try to build a second one.
func TestTheReexecedImageDoesNotRestrictAgain(t *testing.T) {
	restricted := false
	s := faulted(nil, nil, true)
	s.restrictAndReexec = func(Spec, string) error { restricted = true; return nil }

	st, err := apply(Required, Spec{}, s)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if restricted {
		t.Fatal("the re-exec'd image built a second domain")
	}
	if st.Degraded() {
		t.Fatalf("the re-exec'd image reported a degradation:\n%s", st)
	}
	if len(st.Steps) != 2 {
		t.Fatalf("steps = %v, want landlock and seccomp", st.Steps)
	}
}

func TestSuccessIsNotADegradation(t *testing.T) {
	st, err := apply(Preferred, Spec{}, faulted(nil, nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if st.Degraded() {
		t.Fatalf("everything applied and the status says degraded:\n%s", st)
	}
}
