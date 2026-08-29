//go:build linux

package server

import (
	"slices"
	"strings"
	"testing"
)

// The real sequence is a valid one. If this fails, the shipped order broke.
func TestTheStartupSequenceIsValid(t *testing.T) {
	if err := ValidateStartup(StartupSequence()); err != nil {
		t.Fatalf("the shipped startup order is invalid: %v", err)
	}
}

// Every step that outlives its own frame must follow the sandbox. Moving any
// one of them earlier has to be refused, named by the step that escaped.
//
// This is the rule the whole table exists for. A descriptor opened before the
// confinement keeps working after it, so an early step hands out exactly what
// the sandbox was there to withhold.
func TestNothingLongLivedOpensBeforeTheSandbox(t *testing.T) {
	base := StartupSequence()

	for _, step := range afterSandbox() {
		moved := hoistBefore(base, step, StepApplySandbox)
		if slices.Equal(moved, base) {
			t.Fatalf("%s was not actually moved, so the case proves nothing", step)
		}

		err := ValidateStartup(moved)
		if err == nil {
			t.Errorf("%s ran before the sandbox and was accepted", step)
			continue
		}
		if !strings.Contains(err.Error(), step.String()) {
			t.Errorf("%s escaped the sandbox but the refusal does not name it: %v", step, err)
		}
		if !strings.Contains(err.Error(), "escapes the sandbox") {
			t.Errorf("%s: the refusal does not say what went wrong: %v", step, err)
		}
	}
}

// Removing the sandbox entirely is refused, and says so. Without this a
// sequence with no confinement at all would satisfy every ordering rule
// vacuously, since nothing can run before a step that is not there.
func TestASequenceWithNoSandboxIsRefused(t *testing.T) {
	var without []StartupStep
	for _, s := range StartupSequence() {
		if s != StepApplySandbox {
			without = append(without, s)
		}
	}

	err := ValidateStartup(without)
	if err == nil {
		t.Fatal("a sequence that never confines anything was accepted")
	}
	if !strings.Contains(err.Error(), "nothing is confined") {
		t.Errorf("the refusal does not say the process is unconfined: %v", err)
	}
}

// The hardening read is the one step that must precede the sandbox: the
// sandbox grants the share host parents, and it cannot grant a path it has not
// read. Pushing the read after the sandbox is refused.
func TestTheHardeningReadCannotFollowTheSandbox(t *testing.T) {
	moved := hoistBefore(StartupSequence(), StepApplySandbox, StepDeriveHardening)

	err := ValidateStartup(moved)
	if err == nil {
		t.Fatal("the sandbox was applied before reading what it must grant")
	}
	if !strings.Contains(err.Error(), "has not read") {
		t.Errorf("the refusal does not explain the dependency: %v", err)
	}
}

// The resolver refusal comes before anything resolves a path. A kernel without
// it resolves under a race, and every path check below is then decided against
// a tree that can change between the check and the use.
func TestPathsAreNotResolvedBeforeTheResolverIsRequired(t *testing.T) {
	for _, step := range []StartupStep{StepDeriveHardening, StepOpenServices} {
		moved := hoistBefore(StartupSequence(), step, StepRequireResolver)
		if slices.Equal(moved, StartupSequence()) {
			t.Fatalf("%s was not moved", step)
		}

		err := ValidateStartup(moved)
		if err == nil {
			t.Errorf("%s resolved paths before the resolver was required", step)
			continue
		}
		if !strings.Contains(err.Error(), "may race") {
			t.Errorf("%s: the refusal does not name the race: %v", step, err)
		}
	}
}

// A malformed table is refused rather than half-checked: a missing step, a
// repeated one, an unnamed slot, and a value outside the enum.
func TestAMalformedSequenceIsRefused(t *testing.T) {
	full := StartupSequence()

	cases := []struct {
		name  string
		steps []StartupStep
		say   string
	}{
		{"empty", nil, "empty"},
		{"missing a step", full[:len(full)-1], "Serve is missing"},
		{"a repeated step", append(slices.Clone(full), StepServe), "more than once"},
		{"an unnamed slot", append(slices.Clone(full), StepUnsetStartup), "does not name a step"},
		{"past the enum", append(slices.Clone(full), StartupStep(200)), "not a step"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateStartup(c.steps)
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.say) {
				t.Errorf("%s: want a refusal mentioning %q, got %v", c.name, c.say, err)
			}
		})
	}
}

// Every step in the shipped sequence has a name. An unnamed step in a refusal
// tells an operator a number and nothing they can act on.
func TestEveryStepIsNamed(t *testing.T) {
	for _, s := range StartupSequence() {
		name := s.String()
		if name == "" || strings.HasPrefix(name, "StartupStep(") || name == "unset" {
			t.Errorf("the step at %d has no usable name: %q", uint8(s), name)
		}
	}
}

// hoistBefore returns the sequence with step moved to just before anchor.
func hoistBefore(steps []StartupStep, step, anchor StartupStep) []StartupStep {
	out := make([]StartupStep, 0, len(steps))
	for _, s := range steps {
		if s == step {
			continue
		}
		if s == anchor {
			out = append(out, step)
		}
		out = append(out, s)
	}
	return out
}
