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

	// SearchWalkDepth stops a walk descending forever. The path layer already
	// refuses a symlink out of a share, so this bounds a genuinely deep tree
	// rather than a loop.
	SearchWalkDepth = 64

	// OIDCFlowLifetime bounds how long a link flow may sit between the
	// redirect out and the callback back. Long enough for a person to sign in
	// at the provider and answer a prompt, short enough that a state value
	// lifted from a log is not usable later.
	OIDCFlowLifetime = 10 * time.Minute

	// CorpusScanEntries bounds the measurement an index estimate runs on.
	// Reaching it does not refuse: the statistics describe a real sample, and
	// the answer says it is partial rather than reporting the fraction it saw
	// as the whole.
	CorpusScanEntries = 5_000_000

	// SearchQueryBytes caps a query string. It is folded and split into
	// trigrams, so an unbounded one is work a client chooses.
	SearchQueryBytes = 1 << 10
)

// JournalRowsPerAccount is enforced by deleting the oldest rows inside the
// upsert's own transaction, so the bound holds even if a writer crashes.
const JournalRowsPerAccount = 1_000

// BatchPaths bounds how many paths one delete, move or copy may name.
//
// Each is resolved and acted on inside the request, so an unbounded list is an
// unbounded request. The bound is generous against what a person selects in a
// file manager and small against what a script could send.
const BatchPaths = 1_000

// ArchiveEntriesListed truncates rather than refusing, and the response says so.
const ArchiveEntriesListed = 10_000

// The bounds on what one archive request may pack.
//
// ArchiveEntriesListed bounds how many paths a request may name and says
// nothing about what those paths contain, so one path naming a large tree is
// an unbounded response. These two bound the walk itself.
//
// Neither refuses. The status is committed on the first byte, so the archive
// ends early and carries an entry saying it was cut: a caller receives a valid
// archive that says it is short rather than a stream that stops without
// explanation.
const (
	// ArchivePackedEntries bounds how many entries one archive holds,
	// directories included.
	ArchivePackedEntries = 200_000

	// ArchivePackedBytes bounds the file bytes one archive copies. It is the
	// bound that matters for a connection's lifetime: entries are cheap and
	// bytes are the wait.
	ArchivePackedBytes = 32 << 30
)

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

	// DavIfLists caps the parenthesised lists in one If header, and
	// DavIfConditions the terms inside one list. Both are attacker-chosen
	// counts in a header, so both are bounded rather than trusted.
	DavIfLists      = 256
	DavIfConditions = 256

	// DavIfTokenLength caps one coded URL or entity tag inside an If header.
	DavIfTokenLength = 2048

	// DavInfinityEntries is the collection size above which Depth: infinity is
	// refused with 507 instead of attempted. The honest refusal beats the one
	// that arrives after ten minutes.
	DavInfinityEntries = 100_000
)

// OIDC bounds. Every one of these is counted against what an identity provider
// sent, which is a party this server trusts to authenticate and does not trust
// to be well behaved.
const (
	// OIDCResponseBytes is the ceiling on one response body, counted while the
	// body arrives rather than after it is buffered: the point is to bound what
	// is allocated, and a bound checked at the end has already allocated.
	// A discovery document is a couple of kilobytes and a key set with a dozen
	// keys is under ten, so this leaves three orders of magnitude of headroom.
	OIDCResponseBytes = 256 << 10

	// OIDCRequestTimeout is the ceiling on one outbound request, connect and
	// handshake included. Every caller sits inside a redirect a person is
	// waiting on, so the useful ceiling is shorter than their patience rather
	// than long enough for any provider.
	OIDCRequestTimeout = 10 * time.Second

	// OIDCConnectTimeout bounds the connect alone, so a provider that accepts
	// a connection and then stalls is not indistinguishable from one that never
	// answers.
	OIDCConnectTimeout = 5 * time.Second

	// OIDCJWKSKeys bounds the keys one key set may carry. Each is parsed into a
	// public key, so an unbounded set is work and memory the provider chooses.
	OIDCJWKSKeys = 32

	// OIDCTokenBytes bounds one compact token. It arrives inside a response
	// already bounded above, but it is also decoded and parsed, so it carries
	// its own.
	OIDCTokenBytes = 16 << 10

	// OIDCClockSkew is how far outside its own validity window a token may sit
	// and still be accepted, which covers ordinary clock drift between two
	// machines and nothing more.
	OIDCClockSkew = 2 * time.Minute

	// OIDCDiscoveryTTL bounds how long a discovery document is reused. A
	// document cached without a bound is a provider's key rotation this server
	// never notices.
	OIDCDiscoveryTTL = time.Hour

	// OIDCJWKSTTL is the same bound for the key set.
	OIDCJWKSTTL = time.Hour

	// OIDCFlowTTL is how long an authorization attempt may stay open. It is the
	// window in which a stolen state value is worth anything.
	OIDCFlowTTL = 10 * time.Minute
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
