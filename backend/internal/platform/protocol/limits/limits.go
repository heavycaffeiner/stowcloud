// Package limits defines bounds enforced at protocol boundaries.
package limits

import (
	"errors"
	"fmt"
)

const (
	ServerBodyLimit    = 128 << 20
	RequestBody        = 1 << 20
	RequestBodyXML     = 256 << 10
	XMLElements        = 10_000
	XMLDepth           = 64
	XMLElementName     = 256
	DavElements        = 10_000
	DavDepth           = 64
	DavNameLength      = 256
	DavTextBytes       = 64 << 10
	DavIfLists         = 256
	DavIfConditions    = 256
	DavIfTokenLength   = 2048
	DavInfinityEntries = 100_000
	BatchPaths         = 1_000
)

var ErrTooLarge = errors.New("limit exceeded")

type Exceeded struct {
	Limit string
	Bound int64
	Got   int64
}

func (e *Exceeded) Error() string {
	return fmt.Sprintf("%s: %d exceeds the limit of %d", e.Limit, e.Got, e.Bound)
}
func (e *Exceeded) Is(target error) bool { return target == ErrTooLarge }
func Exceed(limit string, bound, got int64) error {
	return &Exceeded{Limit: limit, Bound: bound, Got: got}
}
