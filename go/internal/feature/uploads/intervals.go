//go:build linux

package upload

import (
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/transfer"
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
func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
func LoadIntervalSet(rows []Range) (*IntervalSet, error) {
	return transfer.LoadIntervalSet(rows, transfer.WithMaxRuns(limits.UploadIntervalRuns))
}
