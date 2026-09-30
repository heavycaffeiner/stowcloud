// Package limits defines bounds enforced by database-backed state.
package limits

import (
	"errors"
	"fmt"
)

const (
	JournalRowsPerAccount = 1_000
	AuditRetentionMaxRows = 100_000
	DavLocksPerUser       = 256
	DavPropsPerResource   = 256
	UploadIntervalRuns    = 4096
	UploadSpooledNames    = 4096
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
