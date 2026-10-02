package jail

import (
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

	// An unknown policy value still renders as something rather than empty.
	if got := Policy(200).String(); got != "required" {
		t.Errorf("an out-of-range policy rendered as %q", got)
	}
}
