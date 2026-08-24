// Package jail is the process sandbox: a Landlock domain, two seccomp filters,
// and the restrict-then-exec sequence that makes the domain cover every thread
// the Go runtime starts.
//
// Every file here except this one is Linux-only, and there is no portable
// stand-in. A second implementation of a security boundary is a second
// implementation that is never the one that ships.
package jail

import (
	"errors"
	"fmt"
	"strings"
)

// Policy is what an operator asked for, and it is a policy rather than an
// outcome: without a required mode, a sandbox is a thing that usually happens.
type Policy uint8

const (
	// Required refuses to start when a step cannot be applied. The default in
	// the shipped image.
	Required Policy = iota
	// Preferred reports a step that could not be applied as a named
	// degradation and keeps running. The default for a bare-metal install,
	// where an older kernel is a legitimate state the operator may not control.
	Preferred
	// Off attempts nothing, and says so, which is how "I know, and I accept it"
	// is expressible without a code change.
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

// ParsePolicy is the trust boundary for the configured value.
func ParsePolicy(s string) (Policy, error) {
	switch s {
	case "required":
		return Required, nil
	case "preferred":
		return Preferred, nil
	case "off":
		return Off, nil
	}
	return 0, fmt.Errorf("hardening %q is not a policy; the values are \"required\", \"preferred\" and \"off\"", s)
}

// The refusals this package answers with.
var (
	// ErrHardeningRefused is a step that could not be applied under Required.
	ErrHardeningRefused = errors.New("a hardening step could not be applied and the policy is required")

	// ErrArchUnsupported is an architecture with no verified syscall mapping. A
	// filter that waves a number through because it read it under the wrong ABI
	// is worse than no filter, because it is believed.
	ErrArchUnsupported = errors.New("no verified syscall mapping for this architecture")

	// ErrNoProc is /proc unmounted, so the binary's own path is unknown. There
	// is no guess from argv[0]: exec'ing the wrong file is worse than not
	// starting.
	ErrNoProc = errors.New("/proc is not mounted, so the binary's own path is unknown")
)

// StepStatus is one layer's outcome.
type StepStatus struct {
	Name    string
	Applied bool
	// Err is why it was not applied. Kept whole so an operator sees the errno
	// rather than a category.
	Err error
}

// Status is what the health endpoint reports. A degradation is named here
// rather than left in a startup log, because a log line scrolls away and a
// health field does not.
type Status struct {
	Policy Policy
	Kernel string
	Steps  []StepStatus
}

// Degraded reports a policy that asked for something it did not get.
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

// firstUnapplied is the step a refusal names.
func (s Status) firstUnapplied() (StepStatus, bool) {
	for _, st := range s.Steps {
		if !st.Applied {
			return st, true
		}
	}
	return StepStatus{}, false
}
