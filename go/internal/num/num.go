// Package num holds the one integer conversion in this tree that is allowed to
// exist. Every other conversion between differently sized integer types is a
// gate failure, because Go's own conversion truncates in silence and the value
// that did not fit is the only thing worth knowing.
package num

import (
	"errors"
	"fmt"
)

// Integer is hand-written rather than taken from golang.org/x/exp/constraints:
// four lines against a dependency.
type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// ErrNarrow is what a conversion that did not fit refuses with.
var ErrNarrow = errors.New("value does not fit the target integer width")

// RangeError carries the value that did not fit. A truncation reported without
// the number is a bug report nobody can act on.
type RangeError struct {
	Value string
	From  string
	To    string
}

func (e *RangeError) Error() string {
	return fmt.Sprintf("%s: %s (%s) does not fit %s", ErrNarrow.Error(), e.Value, e.From, e.To)
}

// Is reports ErrNarrow so callers match the sentinel and read the fields.
func (e *RangeError) Is(target error) bool { return target == ErrNarrow }

// Narrow converts between integer widths and reports the value that did not fit
// rather than truncating it.
//
// The round trip catches a value too wide for the target; the sign comparison
// catches the case the round trip cannot see, where a negative value and an
// unsigned target convert back to the same bits.
func Narrow[To, From Integer](v From) (To, error) {
	t := To(v)
	if From(t) != v || (v < 0) != (t < 0) {
		return 0, &RangeError{
			Value: fmt.Sprintf("%d", v),
			From:  fmt.Sprintf("%T", v),
			To:    fmt.Sprintf("%T", t),
		}
	}
	return t, nil
}
