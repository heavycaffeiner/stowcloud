// Package jail provides the process sandbox: a Landlock domain, two seccomp
// filters, POSIX rlimits, and the restrict-then-exec sequence that extends the
// domain across every thread the Go runtime creates.
//
// Apart from this file everything here is Linux-only, with no portable
// substitute. A second implementation of a security boundary is a second
// implementation that never ships.
package jail

import (
	"errors"
	"fmt"
	"strings"
)

// Policy records what an operator requested. It expresses intent rather than
// result: absent a required mode, a sandbox is merely something that usually
// occurs.
type Policy uint8

const (
	// Required aborts startup when any step cannot be applied. This is the
	// default in the shipped image.
	Required Policy = iota
	// Preferred records an unapplied step as a named degradation and continues.
	// This is the default for a bare-metal install, where an older kernel is a
	// legitimate condition the operator may not govern.
	Preferred
	// Off attempts nothing and reports as much, giving an operator a way to
	// accept the absence deliberately without editing code.
	Off
)

func (p Policy) String() string {
	switch p {
	case Preferred:
		return "preferred"
	case Off:
		return "off"
	}
	return "required"
}

// PolicyNames lists every policy this build supports, so the settings screen can
// render the available choices. It is transmitted rather than compiled into the
// client, because a client holding its own copy would offer policies the server
// does not implement.
func PolicyNames() []string { return []string{"required", "preferred", "off"} }

// ParsePolicy is the trust boundary for the configured value. Three spellings
// and nothing else: a name that is almost right is a policy the operator
// believes they configured.
func ParsePolicy(s string) (Policy, error) {
	switch s {
	case "required":
		return Required, nil
	case "preferred":
		return Preferred, nil
	case "off":
		return Off, nil
	}
	return 0, fmt.Errorf("hardening %q is not a policy; the values are %q, %q and %q",
		s, "required", "preferred", "off")
}

// The rejections this package produces.
var (
	// ErrHardeningRefused reports a step that failed to apply under Required.
	ErrHardeningRefused = errors.New("a hardening step could not be applied and the policy is required")

	// ErrArchUnsupported reports an architecture lacking a verified syscall
	// mapping. A filter that admits a number because it interpreted it under the
	// wrong ABI is worse than no filter at all, precisely because it is
	// trusted.
	ErrArchUnsupported = errors.New("no verified syscall mapping for this architecture")

	// ErrNoProc reports /proc unmounted, leaving the binary's own path unknown.
	// Nothing is inferred from argv[0], since exec'ing the wrong file is worse
	// than declining to start.
	ErrNoProc = errors.New("/proc is not mounted, so the binary's own path is unknown")
)

// StepStatus records the outcome for a single layer.
type StepStatus struct {
	Name    string
	Applied bool
	// Err explains why it was not applied, preserved intact so an operator sees
	// the errno instead of a category.
	Err error
}

// Status is what the health endpoint publishes. Degradations appear here rather
// than only in a startup log, because log lines scroll out of reach while a
// health field remains.
type Status struct {
	Policy Policy
	Kernel string
	Steps  []StepStatus
}

// LandlockApplied reports whether the filesystem domain is genuinely active.
//
// One decision outside this package depends on it: whether an ungranted path
// remains reachable. A domain that was never installed restricts nothing, so a
// share added beneath any parent works without a restart.
func (s Status) LandlockApplied() bool {
	for _, st := range s.Steps {
		if st.Name == StepLandlock {
			return st.Applied
		}
	}
	return false
}

// The step names, so the reporter and the reader agree on one spelling.
const (
	StepLandlock = "landlock"
	StepSeccomp  = "seccomp"
)

// Degraded reports a policy whose request went unfulfilled.
func (s Status) Degraded() bool {
	if s.Policy == Off {
		return false
	}
	for _, st := range s.Steps {
		if !st.Applied {
			return true
		}
	}
	return false
}

func (s Status) String() string {
	lines := make([]string, 0, len(s.Steps)+1)
	lines = append(lines, fmt.Sprintf("hardening %s on kernel %s", s.Policy, s.Kernel))
	for _, st := range s.Steps {
		if st.Applied {
			lines = append(lines, fmt.Sprintf("  %-10s applied", st.Name))
			continue
		}
		lines = append(lines, fmt.Sprintf("  %-10s NOT applied: %v", st.Name, st.Err))
	}
	return strings.Join(lines, "\n") + "\n"
}

// firstUnapplied identifies the step a rejection points at.
func (s Status) firstUnapplied() (StepStatus, bool) {
	for _, st := range s.Steps {
		if !st.Applied {
			return st, true
		}
	}
	return StepStatus{}, false
}
