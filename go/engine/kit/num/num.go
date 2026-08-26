// Package num provides the single sanctioned way to convert an integer from
// one width to another. A plain Go conversion truncates without complaint;
// this package refuses instead and reports the value that would have been
// lost.
package num

import (
	"errors"
	"fmt"
)

// Integer lists the built-in integer kinds, written out here so the package
// needs no dependency beyond the standard library.
type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// ErrNarrow is the sentinel a caller matches with errors.Is.
var ErrNarrow = errors.New("num: value out of range for target type")

// RangeError describes a conversion that did not fit. Value holds the
// original number as text so the failure is legible without a type switch on
// From.
type RangeError struct {
	Value string
	From  string
	To    string
}

func (e *RangeError) Error() string {
	return fmt.Sprintf("num: %s (%s) out of range for %s", e.Value, e.From, e.To)
}

// Is lets errors.Is(err, ErrNarrow) succeed for any RangeError.
func (e *RangeError) Is(target error) bool {
	return target == ErrNarrow
}

// Narrow converts v to type To, or reports why it could not.
//
// Converting forward and then back to From must reproduce v; that alone
// catches a value too large or too small for To. It is not enough on its own:
// a negative From value narrowed into an unsigned To can land on a bit
// pattern that converts back to the same negative number, so the sign of the
// input and the sign of the result are also compared directly.
func Narrow[To, From Integer](v From) (To, error) {
	out := To(v)
	roundTrip := From(out)
	signFlip := (v < 0) != (out < 0)
	if roundTrip != v || signFlip {
		var zero To
		return zero, &RangeError{
			Value: fmt.Sprintf("%d", v),
			From:  fmt.Sprintf("%T", v),
			To:    fmt.Sprintf("%T", out),
		}
	}
	return out, nil
}
