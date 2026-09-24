// Package limits defines bounds on stored paths and buffered storage data.
package limits

import (
	"errors"
	"fmt"
)

const (
	PathComponents     = 256
	PathBytes          = 4 << 10
	NameBytes          = 255
	DirEntriesBuffered = 100_000
)

var ErrTooLarge = errors.New("limit exceeded")

type Exceeded struct {
	Limit      string
	Bound, Got int64
}

func (e *Exceeded) Error() string {
	return fmt.Sprintf("%s: %d exceeds the limit of %d", e.Limit, e.Got, e.Bound)
}
func (e *Exceeded) Is(target error) bool { return target == ErrTooLarge }
func Exceed(limit string, bound, got int64) error {
	return &Exceeded{Limit: limit, Bound: bound, Got: got}
}
