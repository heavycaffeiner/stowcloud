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

// Upload bounds.
const (
	// UploadIntervalRuns caps the disjoint received ranges one session may
	// hold. A sequential upload reaches one and a parallel one a handful;
	// only a client fragmenting on purpose approaches this, and at that point
	// the insert is refused rather than letting the set grow without end.
	UploadIntervalRuns = 4096

	// UploadReservedBytesPerUser bounds the declared length of everything one
	// account has in flight. A declared length reserves a sparse part file, so
	// without this an account can promise the disk away without writing a
	// byte. Refuses with 429.
	UploadReservedBytesPerUser = 100 << 30

	// UploadFreeSpaceMargin is what a session's declared length must leave
	// behind on the destination filesystem. Refuses with 507.
	UploadFreeSpaceMargin = 2 << 30

	// UploadSpooledNames caps the out-of-order chunk names one name-ordered
	// session may hold before its predecessor arrives.
	UploadSpooledNames = 4096
)

// UploadSessionTTL is how long a session survives without a write. The sweep
// takes the part file after it, so it is also the grace period an orphan is
// measured against.
const UploadSessionTTL = 24 * time.Hour

// The chunk floor and default. The floor is the hard one: neither the config
// file nor an administrator may set a live minimum below it, which is what
// keeps a misconfiguration from turning every upload into a per-byte request.
// The other two are seeds the config file and then an admin override replace.
const (
	UploadChunkFloor       = 5 << 20
	UploadChunkMinDefault  = 5 << 20
	UploadChunkSizeDefault = 10 << 20
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

// WebDAV bounds. Every one of these is counted against a body a stranger sent,
// inside the scanner loop rather than after a document is built: a bound
// checked after the fact is a bound the allocation already went past.
const (
	// DavElements caps the elements in one request body. Refuses with 400.
	DavElements = 10_000

	// DavDepth caps element nesting. The scanner counts it rather than
	// trusting the parser's own recursion to run out first.
	DavDepth = 64

	// DavNameLength caps an element's local name in bytes. A name is a map key
	// and a response field, so an unbounded one is memory a client chooses.
	DavNameLength = 256

	// DavTextBytes caps the text accumulated for one property value. Text
	// arrives in fragments and is joined, so the join is what needs the bound.
	DavTextBytes = 64 << 10

	// DavPropsPerResource bounds the dead properties one resource may carry.
	DavPropsPerResource = 256

	// DavInfinityEntries is the collection size above which Depth: infinity is
	// refused with 507 instead of attempted. The honest refusal beats the one
	// that arrives after ten minutes.
	DavInfinityEntries = 100_000
)

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
