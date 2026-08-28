// Linux only, because it fronts services that are Linux only.
//go:build linux

// Package middleware is the request chain, expressed as an ordered table
// rather than a sequence of registration calls scattered across assembly.
//
// Source order is request order. A step's position is the whole of its
// contract with its neighbours: TrustedProxy resolves the client address that
// RateLimit keys on, HostAndOriginBoundary decides which origin's routes exist
// before Auth reads a cookie, and ErrorMapper sits innermost so a handler's
// error cannot escape past the one thing that renders it.
//
// Naming the order in one value makes it testable. A replay test walks the
// table and records entry and exit around every step, which is the only way a
// reordering shows up as a failure rather than as a subtly different response.
package middleware

import (
	"fmt"
	"slices"
	"strings"
)

// Step names one link in the chain.
type Step uint8

const (
	// StepUnset is the zero value and never appears in a chain. A table entry
	// that failed to name its step must not silently become the first one.
	StepUnset Step = iota

	// StepRequestID stamps the identifier every later step and every log line
	// for this request carries.
	StepRequestID

	// StepTrustedProxy resolves the client address from the peer and the
	// forwarding headers. Everything downstream that says "the client" means
	// what this step decided.
	StepTrustedProxy

	// StepHostAndOriginBoundary admits the Host and settles the origin. It is
	// one step rather than a host gate and a separate origin check because the
	// first-boot origin bypass is only safe behind the private-network host
	// gate, and two steps can be mounted apart.
	StepHostAndOriginBoundary

	// StepSecurityHeaders sets the response headers that do not depend on the
	// outcome, early enough that an error rendered downstream still carries
	// them.
	StepSecurityHeaders

	// StepRateLimit spends the resolved client's budget. After the boundary,
	// so a request to a host this deployment does not serve cannot consume it.
	StepRateLimit

	// StepBodyLimit bounds the request body by the route's declared class,
	// before anything reads it.
	StepBodyLimit

	// StepAuth resolves the credential and the principal.
	StepAuth

	// StepCSRF checks the token on a mutating cookie-authenticated request. It
	// follows Auth because whether the request is cookie-authenticated is what
	// Auth just determined.
	StepCSRF

	// StepACLScope narrows the principal's permissions to the route's
	// requirement.
	StepACLScope

	// StepAuditSink records the event. It wraps ErrorMapper so the recorded
	// status is the one actually sent.
	StepAuditSink

	// StepErrorMapper renders a handler error as a response. Innermost: every
	// error from a handler passes through exactly one mapper.
	StepErrorMapper
)

// Chain is the request order, and the source order here is that order.
//
// Read top to bottom, this is the sentence the server enforces: identify the
// request, decide who is asking, decide whether this deployment serves them,
// bound what they may spend and send, decide what they may do, then run the
// handler and render whatever it returns.
func Chain() []Step {
	return []Step{
		StepRequestID,
		StepTrustedProxy,
		StepHostAndOriginBoundary,
		StepSecurityHeaders,
		StepRateLimit,
		StepBodyLimit,
		StepAuth,
		StepCSRF,
		StepACLScope,
		StepAuditSink,
		StepErrorMapper,
	}
}

// String is the step's name in a diagnostic or a replay record.
func (s Step) String() string {
	switch s {
	case StepRequestID:
		return "RequestID"
	case StepTrustedProxy:
		return "TrustedProxy"
	case StepHostAndOriginBoundary:
		return "HostAndOriginBoundary"
	case StepSecurityHeaders:
		return "SecurityHeaders"
	case StepRateLimit:
		return "RateLimit"
	case StepBodyLimit:
		return "BodyLimit"
	case StepAuth:
		return "Auth"
	case StepCSRF:
		return "CSRF"
	case StepACLScope:
		return "ACLScope"
	case StepAuditSink:
		return "AuditSink"
	case StepErrorMapper:
		return "ErrorMapper"
	case StepUnset:
		return "unset"
	default:
		return fmt.Sprintf("Step(%d)", uint8(s))
	}
}

// ValidateChain reports every way a chain is not one, at once.
//
// All the problems rather than the first, because a chain is assembled once at
// startup and an operator fixing one error only to meet the next is a worse
// experience than reading them together.
func ValidateChain(steps []Step) error {
	var problems []string

	if len(steps) == 0 {
		return fmt.Errorf("middleware: the chain is empty")
	}

	seen := map[Step]bool{}
	for i, s := range steps {
		switch {
		case s == StepUnset:
			problems = append(problems,
				fmt.Sprintf("position %d does not name a step", i))
		case s > StepErrorMapper:
			problems = append(problems,
				fmt.Sprintf("position %d names %s, which is not a step", i, s))
		case seen[s]:
			problems = append(problems,
				fmt.Sprintf("%s appears more than once", s))
		}
		seen[s] = true
	}

	// The two ordering rules the document states as load-bearing, checked
	// rather than trusted to the source order of the table above. A future
	// edit to Chain that moved either one would otherwise change behaviour
	// silently: the audit would record a status that later changed, or a
	// handler error would escape the mapper.
	// Absent and misplaced are the same defect from the caller's side: an
	// error leaves the chain unrendered either way. Checking only the position
	// would pass a chain with no mapper at all, which is the more complete
	// version of the failure.
	if at, ok := indexOf(steps, StepErrorMapper); !ok || at != len(steps)-1 {
		problems = append(problems,
			"ErrorMapper is not innermost, so a handler error can escape it")
	}
	if a, aok := indexOf(steps, StepAuditSink); aok {
		if e, eok := indexOf(steps, StepErrorMapper); eok && a > e {
			problems = append(problems,
				"AuditSink is inside ErrorMapper, so it records a status that is not the one sent")
		}
	}
	if p, pok := indexOf(steps, StepTrustedProxy); pok {
		if r, rok := indexOf(steps, StepRateLimit); rok && p > r {
			problems = append(problems,
				"RateLimit precedes TrustedProxy, so it keys on an address that is not the client")
		}
	}
	if h, hok := indexOf(steps, StepHostAndOriginBoundary); hok {
		if a, aok := indexOf(steps, StepAuth); aok && h > a {
			problems = append(problems,
				"Auth precedes HostAndOriginBoundary, so a credential is read for a host this deployment does not serve")
		}
	}
	if a, aok := indexOf(steps, StepAuth); aok {
		if c, cok := indexOf(steps, StepCSRF); cok && a > c {
			problems = append(problems,
				"CSRF precedes Auth, so it cannot know whether the request is cookie-authenticated")
		}
	}

	if len(problems) == 0 {
		return nil
	}
	slices.Sort(problems)
	return fmt.Errorf("middleware: %s", strings.Join(problems, "; "))
}

func indexOf(steps []Step, want Step) (int, bool) {
	for i, s := range steps {
		if s == want {
			return i, true
		}
	}
	return 0, false
}
