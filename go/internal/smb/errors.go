package smb

import (
	"errors"
	"fmt"
)

// The refusals rendering can produce. Each names the value that caused it,
// because an operator who made the typo can act on the value and cannot act
// on "invalid configuration".

// ErrBindRefused is a pinned interface that is global without the opt-in, or a
// pinned value that is not an address at all.
var ErrBindRefused = errors.New("smb: the pinned interface is refused")

// ErrUnsafeValue is a value that cannot be represented in the configuration
// file safely.
var ErrUnsafeValue = errors.New("smb: the value cannot be represented safely")

// BindError is a refused interface pin.
type BindError struct {
	Value  string
	Reason string
}

func (e *BindError) Error() string {
	return fmt.Sprintf("smb.interfaces: %q is refused: %s", e.Value, e.Reason)
}

func (e *BindError) Is(target error) bool { return target == ErrBindRefused }

// UnsafeError is a value that cannot be interpolated safely.
type UnsafeError struct {
	// Field names what carried the value, so the refusal points at the setting
	// rather than at the file.
	Field  string
	Value  string
	Reason string
}

func (e *UnsafeError) Error() string {
	return fmt.Sprintf("smb: %s %q is refused: %s", e.Field, e.Value, e.Reason)
}

func (e *UnsafeError) Is(target error) bool { return target == ErrUnsafeValue }
