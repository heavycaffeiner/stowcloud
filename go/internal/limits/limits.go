// Package limits holds every bound in this tree. A bound is a constant here or
// it is a magic number somewhere else, and a magic number is one a caller can
// widen without anyone seeing the diff.
//
// The premise these exist for: the directories this product serves are written
// by other programs, so no input's size is ours to assume.
package limits

import (
	"errors"
	"fmt"
	"time"
)

// Request and body bounds.
const (
	// RequestBody is the ceiling on a general request body. Refuses with 413.
	RequestBody = 1 << 20

	// RequestBodyXML is the ceiling on an XML body, which is parsed rather
	// than streamed to disk and so costs more per byte. Refuses with 413.
	RequestBodyXML = 256 << 10
)

// XML scanner bounds. Each refuses with 400.
const (
	XMLElements    = 10_000
	XMLDepth       = 64
	XMLElementName = 256
)

// Path bounds. Each refuses with 400.
const (
	PathComponents = 256
	PathBytes      = 4 << 10
	NameBytes      = 255
)

// DirEntriesBuffered is the ceiling on a directory read that materialises its
// entries. Refuses with ErrTooLarge. A caller that cannot accept the bound
// streams instead.
const DirEntriesBuffered = 100_000

// Concurrency and per-user bounds.
const (
	// ConcurrentRequestsDefault is the default for a configurable bound.
	// Refuses with 503.
	ConcurrentRequestsDefault = 512

	// UploadsInFlightPerUser refuses with 429.
	UploadsInFlightPerUser = 32

	// UploadSessionsPerUser refuses with 429.
	UploadSessionsPerUser = 256

	// DavLocksPerUser refuses with 507.
	DavLocksPerUser = 256
)

// Search bounds. The storage-dependent pairs are two constants rather than one
// parameter, because a caller that picks the number is a caller that can widen
// it.
const (
	// SearchResults truncates rather than refusing, and the response says so.
	SearchResults = 1_000

	// ConcurrentSearchesSSD and ConcurrentSearchesRotational refuse with 503.
	ConcurrentSearchesSSD        = 4
	ConcurrentSearchesRotational = 2

	// SearchWalkDeadlineSSD and SearchWalkDeadlineRotational end the walk with
	// a partial result, flagged in the response.
	SearchWalkDeadlineSSD        = 3 * time.Second
	SearchWalkDeadlineRotational = 8 * time.Second
)

// JournalRowsPerAccount is enforced by deleting the oldest rows inside the
// upsert's own transaction, so the bound holds even if a writer crashes.
const JournalRowsPerAccount = 1_000

// ArchiveEntriesListed truncates rather than refusing, and the response says so.
const ArchiveEntriesListed = 10_000

// WorkerWireMessage is the ceiling on one message on the preview worker's
// socket. A peer that exceeds it is killed rather than answered: it is the
// least trusted process in the system.
const WorkerWireMessage = 8 << 10

// The preview decoder's source-pixel ceiling is not here. It is derived per
// preset from the decoder's own memory behaviour rather than fixed, so it
// lives with the preview decode limits.

// ErrTooLarge is what every bound in this package refuses with.
var ErrTooLarge = errors.New("limit exceeded")

// Exceeded names the limit that refused. "Too large" without the bound and the
// value is a refusal a caller cannot act on.
type Exceeded struct {
	Limit string
	Bound int64
	Got   int64
}

func (e *Exceeded) Error() string {
	return fmt.Sprintf("%s: %d exceeds the limit of %d", e.Limit, e.Got, e.Bound)
}

// Is reports ErrTooLarge so callers match the sentinel and read the fields.
func (e *Exceeded) Is(target error) bool { return target == ErrTooLarge }

// Exceed builds the refusal for a named limit.
func Exceed(limit string, bound, got int64) error {
	return &Exceeded{Limit: limit, Bound: bound, Got: got}
}
