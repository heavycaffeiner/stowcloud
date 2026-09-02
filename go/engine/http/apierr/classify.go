// Linux only, because it classifies errors from services that are Linux only.
//go:build linux

// Package apierr is the presentation tier's one error classifier and the
// native JSON envelope.
//
// The old tree carried three complete errors.Is ladders, one per protocol, and
// they drifted: the same service sentinel could become a 404 on one surface and
// a 403 on another, which is a disclosure difference nobody chose. Here a
// sentinel is recognised once, in Classify, and each protocol renders the
// resulting class through a thin adapter that never repeats the ladder.
//
// The classification lives in presentation rather than in service on purpose.
// The core is a sentinel taxonomy with no HTTP concepts, and putting a second
// numeric taxonomy beside it, purely to save presentation code, would give the
// domain a vocabulary it has no use for.
package apierr

import (
	"errors"
	"fmt"
	"strings"
)

// Class is what an error means, independent of the protocol reporting it.
//
// A closed enumeration: an unrecognised error becomes Internal rather than
// something more specific, because guessing produces a status that leaks what
// the guess was.
type Class uint16

const (
	// Internal is an unrecognised error or an infrastructure failure. It is the
	// zero value so an unmapped error is reported as a server fault rather than
	// as whichever class happens to sit first.
	Internal Class = iota

	// Malformed is syntax: bad encoding, bad XML, a header that will not parse.
	Malformed
	// Unprocessable is input that parsed and then violated a named constraint.
	Unprocessable
	// BodyTooLarge is the outer bound on a request body.
	BodyTooLarge
	// LimitExceeded is a configured structural bound the same request would
	// cross again: a body with too many parts, a walk too deep.
	LimitExceeded
	// ResourceExhausted is a bound the caller is momentarily over and which
	// clears as their other work finishes. Separate from LimitExceeded
	// because the two want opposite things from a client: this one is worth
	// waiting out, and a 422 told every client to give up on it instead.
	ResourceExhausted

	// AuthRequired is no usable credential.
	AuthRequired
	// AuthInvalid is a credential that was presented and failed. Expired and
	// forged are never distinguished: the difference is an oracle.
	AuthInvalid
	// AccountDisabled is a known principal that may not authenticate.
	AccountDisabled
	// RateLimited is a request or login-flow rate bound.
	RateLimited

	// Hidden is missing or denied where existence must not be disclosed. Every
	// member renders identically, which is the point of the class.
	Hidden
	// Denied is a caller who may know the resource exists and lacks this
	// action on it.
	Denied
	// NotFound is ordinary absence on a surface where absence is not a secret.
	NotFound
	// Gone is an expired or consumed public capability.
	Gone

	// Conflict is a current state that conflicts with the action.
	Conflict
	// Exists is a no-clobber create that collided.
	Exists
	// NotEmpty is a collection that had to be empty.
	NotEmpty
	// Precondition is a validator or submitted state that failed.
	Precondition
	// Locked is a write with no acceptable lock token.
	Locked
	// RangeNotSatisfiable is a syntactically valid range naming nothing inside
	// the file. Distinct from Unprocessable because the answer carries the
	// real size, which is what lets a client ask again correctly.
	RangeNotSatisfiable

	// NoSpace is a physical floor or a quota.
	NoSpace
	// ShareUnavailable is a registered share whose backing is not there now.
	ShareUnavailable

	// LastAdmin is a change that would leave the deployment unadministrable.
	LastAdmin
	// WeakPassword is the password floor refusing.
	WeakPassword
	// NameTaken is a unique account or group name colliding.
	NameTaken

	// SubsystemUnavailable is an optional service this build does not have.
	SubsystemUnavailable
	// NotImplemented is a recognised operation this build does not supply.
	NotImplemented
	// BadGateway is a protocol target naming another origin or server.
	BadGateway

	// The setup gate's outcomes.
	SetupComplete
	SetupExpired
	SetupInvalidToken

	// Login flow v2's outcomes.
	FlowUnknown
	FlowPending
	FlowApproved
	FlowTooSoon
)

// classNames is what a class prints as, for logs and for test failures. It is
// not the wire code: the REST adapter owns that.
func (c Class) String() string {
	if int(c) < len(classNames()) {
		return classNames()[c]
	}
	return fmt.Sprintf("class(%d)", uint16(c))
}

func classNames() []string {
	return []string{
		"internal", "malformed", "unprocessable", "body-too-large", "limit-exceeded",
		"resource-exhausted",
		"auth-required", "auth-invalid", "account-disabled", "rate-limited",
		"hidden", "denied", "not-found", "gone",
		"conflict", "exists", "not-empty", "precondition", "locked",
		"range-not-satisfiable",
		"no-space", "share-unavailable",
		"last-admin", "weak-password", "name-taken",
		"subsystem-unavailable", "not-implemented", "bad-gateway",
		"setup-complete", "setup-expired", "setup-invalid-token",
		"flow-unknown", "flow-pending", "flow-approved", "flow-too-soon",
	}
}

// Visibility is what the caller is allowed to learn about a resource it was
// refused.
//
// The boundary chooses it, never the error text: a handler serving a path the
// caller reached through an authorised parent may say "denied", and one serving
// a path the caller guessed may not, and only the handler knows which it is.
type Visibility uint8

const (
	// VisibilityHidden folds absence and denial together, so a caller cannot
	// tell a file it may not see from one that is not there.
	VisibilityHidden Visibility = iota
	// VisibilityKnown allows a denial to be reported as one, because the
	// caller already learned the resource exists through a parent it may read.
	VisibilityKnown
)

// Arg is one safe substitution for a rendered message.
//
// Safe is the whole contract: a configured bound, an ETag, a share's display
// label, a fixed reason token, a minimum length. Never a host path, a
// credential, a lower-layer message, or the value the client sent that was
// refused.
type Arg struct {
	Name  string
	Value string
}

// Classified is an error reduced to what a protocol adapter needs.
type Classified struct {
	Class Class
	// Key is the message catalogue key the client renders. The adapters carry
	// it through unchanged; nothing here formats a human sentence.
	Key  string
	Args []Arg
}

// classifier is one sentinel and the class it means.
//
// A slice rather than a map because errors.Is is not equality: a wrapped error
// matches its sentinel, and order decides which of two overlapping sentinels
// wins. The order here is specific-before-general.
type classifier struct {
	err   error
	class Class
	key   string
}

// Classify reduces any error to a class, once.
//
// This is the only place in the presentation tier that consults a service
// sentinel. The protocol adapters take the result; a second ladder anywhere
// else is the drift this package exists to end.
func Classify(err error, visibility Visibility) Classified {
	if err == nil {
		return Classified{Class: Internal, Key: "internal"}
	}

	// A classification the caller already made travels through unchanged: a
	// protocol parser reports what it found rather than re-deriving it.
	var already *ClassifiedError
	if errors.As(err, &already) {
		return applyVisibility(already.Classified, visibility)
	}

	// A request error the handlers construct, which carries its own class.
	var req *RequestError
	if errors.As(err, &req) {
		return applyVisibility(Classified{Class: req.Class, Key: req.Key, Args: req.Args}, visibility)
	}

	for _, c := range sentinels() {
		if errors.Is(err, c.err) {
			return applyVisibility(Classified{Class: c.class, Key: c.key}, visibility)
		}
	}
	// Unrecognised. Internal rather than a guess: a guessed class produces a
	// status that tells the caller what was guessed.
	return Classified{Class: Internal, Key: "internal"}
}

// applyVisibility folds a denial into Hidden where the caller must not learn
// the resource exists.
//
// One place rather than at each call site, so the existence rule cannot be
// applied on one surface and forgotten on another.
func applyVisibility(c Classified, v Visibility) Classified {
	if v != VisibilityHidden {
		return c
	}
	switch c.Class {
	case Denied, NotFound:
		return Classified{Class: Hidden, Key: "fs.not_found"}
	}
	return c
}

// ClassifiedError carries a class a protocol parser already determined.
//
// A DAV or OCS parser knows what it found and has no service sentinel to
// report it with. Wrapping the class rather than inventing a sentinel keeps the
// ladder in one place.
type ClassifiedError struct {
	Classified
}

func (e *ClassifiedError) Error() string {
	return "apierr: " + e.Class.String()
}

// Classify returns the carried classification, so this type satisfies the same
// shape the adapters read.
func (e *ClassifiedError) Unwrap() error { return nil }

// AsClassified wraps a class as an error, for a parser reporting what it found.
func AsClassified(class Class, key string, args ...Arg) error {
	return &ClassifiedError{Classified{Class: class, Key: key, Args: args}}
}

// RequestError is a refusal a handler constructs about its own input.
//
// It carries a semantic class rather than an HTTP status: the REST adapter
// chooses 400 or 422 or 502, and the DAV adapter chooses its own, from the same
// value. Storing a status here is what made the old tree's request errors
// unusable on any surface but one.
type RequestError struct {
	Class Class
	Key   string
	Args  []Arg
}

func (e *RequestError) Error() string { return "apierr: " + e.Key }

// BadRequest is malformed input: it did not parse, or a required field is
// absent.
//
// field is the field's name and never its value. The value is what the client
// sent, and echoing it into a message is how a refusal becomes a reflection.
func BadRequest(key, field string) error {
	return &RequestError{Class: Malformed, Key: key, Args: fieldArg(field)}
}

// UnprocessableInput is input that parsed and then violated a constraint.
//
// Named for the input rather than the class because the class already owns the
// bare word, and a constructor shadowing its own class reads as a mistake.
func UnprocessableInput(key, field string) error {
	return &RequestError{Class: Unprocessable, Key: key, Args: fieldArg(field)}
}

// BadGatewayError is a protocol target naming another origin or server.
func BadGatewayError(key, field string) error {
	return &RequestError{Class: BadGateway, Key: key, Args: fieldArg(field)}
}

func fieldArg(field string) []Arg {
	if strings.TrimSpace(field) == "" {
		return nil
	}
	return []Arg{{Name: "field", Value: field}}
}
