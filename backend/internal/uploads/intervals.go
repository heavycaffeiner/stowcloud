//go:build linux

package uploads

import (
	"github.com/heavycaffeiner/stowcloud/backend/internal/uploads/limits"
	"github.com/stowcloud/transfer"
)

// Range and IntervalSet remain named by the upload package for current callers
// and persisted-row conversion. Their implementation is transport-neutral.
type Range = transfer.Range
type IntervalSet = transfer.IntervalSet

// NewIntervalSet returns an upload-bounded received set.
func NewIntervalSet() *IntervalSet {
	return transfer.NewIntervalSet(transfer.WithMaxRuns(limits.UploadIntervalRuns))
}

// FullIntervalSet returns a wholly received upload-bounded set.
func FullIntervalSet(length uint64) *IntervalSet {
	return transfer.FullIntervalSet(length, transfer.WithMaxRuns(limits.UploadIntervalRuns))
}

// LoadIntervalSet rebuilds a set from stored rows using the upload run bound.
func LoadIntervalSet(rows []Range) (*IntervalSet, error) {
	return transfer.LoadIntervalSet(rows, transfer.WithMaxRuns(limits.UploadIntervalRuns))
}
