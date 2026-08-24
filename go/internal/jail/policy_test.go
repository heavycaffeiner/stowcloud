package jail

import "testing"

func TestParsePolicy(t *testing.T) {
	for in, want := range map[string]Policy{
		"required":  Required,
		"preferred": Preferred,
		"off":       Off,
	} {
		got, err := ParsePolicy(in)
		if err != nil {
			t.Fatalf("ParsePolicy(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("ParsePolicy(%q) = %v", in, got)
		}
		if got.String() != in {
			t.Fatalf("%v renders as %q", got, got.String())
		}
	}
}

// An unknown value is a refusal rather than a warning and a default, because
// the default it would fall back to is the one an operator did not ask for.
func TestParsePolicyRefusesAnythingElse(t *testing.T) {
	for _, in := range []string{"", "Required", "yes", "true", "on", "strict"} {
		if _, err := ParsePolicy(in); err == nil {
			t.Fatalf("ParsePolicy(%q) was accepted", in)
		}
	}
}

func TestStatusNamesTheFirstUnappliedStep(t *testing.T) {
	st := Status{
		Policy: Required,
		Steps: []StepStatus{
			{Name: "landlock", Applied: true},
			{Name: "seccomp"},
		},
	}
	step, ok := st.firstUnapplied()
	if !ok || step.Name != "seccomp" {
		t.Fatalf("firstUnapplied = %+v, %v", step, ok)
	}
	if !st.Degraded() {
		t.Fatal("a step that was not applied is a degradation")
	}
}
