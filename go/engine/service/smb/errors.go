// Package smb renders the configuration file smbd reads, and refuses what it
// cannot represent.
//
// The generated file is a trust boundary in the outbound direction. Every value
// interpolated into it comes from the operator's configuration or from account
// names, and one that cannot be represented safely is refused rather than
// escaped: the format has no escape that survives its own line-continuation and
// variable-substitution rules, so an escaping scheme here would be a guess about
// somebody else's parser.
//
// A share name is not where anyone should be discovering that Samba's parser has
// opinions.
package smb

import (
	"errors"
	"fmt"
)

// Rendering's rejection reasons. Each one identifies the offending value: an
// operator who mistyped something can correct the value, but can do nothing
// with a bare "invalid configuration".

// ErrBindRefused covers a globally routable pin lacking the opt-in, and a
// pinned value that is not an address in the first place.
var ErrBindRefused = errors.New("smb: the pinned interface is refused")

// ErrUnsafeValue marks a value with no safe representation in the configuration
// file.
var ErrUnsafeValue = errors.New("smb: the value cannot be represented safely")

// BindError reports an interface pin that was rejected.
type BindError struct {
	Value  string
	Reason string
}

func (e *BindError) Error() string {
	return fmt.Sprintf("smb.interfaces: %q is refused: %s", e.Value, e.Reason)
}

func (e *BindError) Is(target error) bool { return target == ErrBindRefused }

// UnsafeError reports a value that cannot be safely interpolated.
type UnsafeError struct {
	// Field identifies the setting the value came from, directing the rejection
	// at the configuration rather than at the output file.
	Field  string
	Value  string
	Reason string
}

func (e *UnsafeError) Error() string {
	return fmt.Sprintf("smb: %s %q is refused: %s", e.Field, e.Value, e.Reason)
}

func (e *UnsafeError) Is(target error) bool { return target == ErrUnsafeValue }
