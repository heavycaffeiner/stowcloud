// Package limits collects the size, count and time bounds this tree checks
// against input it does not control. Any such bound belongs here as a named
// constant; left as a literal elsewhere it becomes invisible to review and
// free for a caller to raise without anyone noticing.
//
// Constants are grouped by which layer enforces them: protocol bounds at the
// presentation edge, service bounds inside a subsystem's own logic, and
// domain or persistence bounds against what is stored.
package limits

import (
	"errors"
	"fmt"
	"time"
)

// Section 1: protocol bounds, enforced at the presentation edge before a
// request body is trusted.

const (
	// RequestBody is the general request body ceiling. Refuses with 413.
	RequestBody = 1 << 20

	// RequestBodyXML is lower than RequestBody because an XML body is parsed
	// in memory instead of streamed to disk. Refuses with 413.
	RequestBodyXML = 256 << 10
)

// XML scanner bounds, each refusing with 400.
const (
	XMLElements    = 10_000
	XMLDepth       = 64
	XMLElementName = 256
)

// WebDAV request scanner bounds. The scanner charges these against a body
// as it reads, not after building a document from it, so an oversized input
// never gets the chance to be fully allocated first.
const (
	// DavElements, DavDepth and DavNameLength refuse with 400.
	DavElements   = 10_000
	DavDepth      = 64
	DavNameLength = 256

	// DavTextBytes caps one property value's accumulated text, since text
	// arrives in fragments that get joined before the bound would otherwise
	// see the full size. Refuses with 400.
	DavTextBytes = 64 << 10

	// DavIfLists and DavIfConditions bound the parenthesised lists and the
	// terms inside them in one If header. Refuse with 400.
	DavIfLists      = 256
	DavIfConditions = 256

	// DavIfTokenLength caps one entity tag or coded URL inside an If header.
	// Refuses with 400.
	DavIfTokenLength = 2048

	// DavInfinityEntries is the collection size past which a Depth: infinity
	// request is refused with 507 up front instead of attempted.
	DavInfinityEntries = 100_000
)

// BatchPaths bounds how many paths one delete, move or copy request may
// name, since each is resolved inside that request.
const BatchPaths = 1_000

// ArchiveEntriesListed truncates the listing rather than refusing; the
// response marks it as truncated.
const ArchiveEntriesListed = 10_000

// ArchivePackedEntries and ArchivePackedBytes bound what one archive request
// actually walks and copies, as opposed to ArchiveEntriesListed, which
// bounds only the paths named and nothing about what they contain. An
// archive's response status is already committed by the time either bound
// is reached, so neither refuses; the stream ends early and the archive
// carries an entry marking it incomplete.
const (
	ArchivePackedEntries = 200_000
	ArchivePackedBytes   = 32 << 30
)

// ConcurrentRequestsDefault is the default ceiling on requests served at
// once, and a configurable one. Refuses with 503.
const ConcurrentRequestsDefault = 512

// Section 2: service bounds, enforced by a subsystem against its own
// running state rather than a single request.

// Upload bounds.
const (
	// UploadIntervalRuns caps the disjoint byte ranges one session tracks as
	// received. A normal upload needs one or a few; only a client
	// deliberately fragmenting its writes gets near this.
	UploadIntervalRuns = 4096

	// UploadReservedBytesPerUser caps the sum of declared lengths across an
	// account's sessions, because a declared length reserves a sparse file
	// on disk before any byte of it is written. Refuses with 429.
	UploadReservedBytesPerUser = 100 << 30

	// UploadFreeSpaceMargin is the free space a session's declared length
	// must leave on the destination filesystem. Refuses with 507.
	UploadFreeSpaceMargin = 2 << 30

	// UploadsInFlightPerUser and UploadSessionsPerUser both refuse with 429
	// once an account crosses them.
	UploadsInFlightPerUser = 32
	UploadSessionsPerUser  = 256

	// UploadSpooledNames caps the chunks one name-ordered session holds
	// aside while waiting for an earlier chunk to arrive.
	UploadSpooledNames = 4096
)

// UploadSessionTTL is how long an idle session survives before its part
// file is swept, and so the grace period before it counts as abandoned.
const UploadSessionTTL = 24 * time.Hour

// UploadChunkFloor is the one chunk-size bound neither the config file nor
// an admin override may lower, which stops a bad configuration from
// turning uploads into a per-byte request storm. UploadChunkMinDefault and
// UploadChunkSizeDefault are only starting values those two may replace.
const (
	UploadChunkFloor       = 5 << 20
	UploadChunkMinDefault  = 5 << 20
	UploadChunkSizeDefault = 10 << 20
)

// Search bounds. The SSD/rotational pairs are separate constants rather
// than one setting, since a caller free to pick the number is a caller free
// to widen it.
const (
	// SearchResults truncates the result list rather than refusing; the
	// response marks it as truncated.
	SearchResults = 1_000

	// ConcurrentSearchesSSD and ConcurrentSearchesRotational refuse with
	// 503 once exceeded.
	ConcurrentSearchesSSD        = 4
	ConcurrentSearchesRotational = 2

	// SearchWalkDeadlineSSD and SearchWalkDeadlineRotational cut a walk off
	// with a partial result rather than letting it run unbounded.
	SearchWalkDeadlineSSD        = 3 * time.Second
	SearchWalkDeadlineRotational = 8 * time.Second

	// SearchWalkDepth stops descent into a pathologically deep tree. A
	// symlink escape is already refused by the path layer, so this is about
	// depth, not cycles.
	SearchWalkDepth = 64

	// SearchQueryBytes caps a query string before it is folded and split
	// into trigrams, work whose cost a client otherwise controls.
	SearchQueryBytes = 1 << 10

	// CorpusScanEntries bounds how much of the corpus an index estimate
	// samples. It does not refuse: the sample stays a real sample, and the
	// estimate reports itself as partial instead of treating what it saw as
	// the whole.
	CorpusScanEntries = 5_000_000
)

// OIDC bounds. These are all counted against traffic from an identity
// provider: trusted to authenticate users, not trusted to behave.
const (
	// OIDCResponseBytes bounds one response body, checked as bytes arrive
	// rather than after buffering, so the check limits what actually gets
	// allocated. A discovery document and a small key set both land well
	// under it.
	OIDCResponseBytes = 256 << 10

	// OIDCRequestTimeout bounds one outbound call end to end, including
	// connect and TLS handshake. It is set by the person waiting on the
	// redirect, not by how patient a provider could be.
	OIDCRequestTimeout = 10 * time.Second

	// OIDCConnectTimeout bounds the connect step alone, so a provider that
	// accepts a connection and then stalls fails distinctly from one that
	// never answers.
	OIDCConnectTimeout = 5 * time.Second

	// OIDCJWKSKeys bounds how many keys one key set carries; each gets
	// parsed into a public key, so this bounds provider-driven work.
	OIDCJWKSKeys = 32

	// OIDCTokenBytes bounds one compact token on its own, separately from
	// OIDCResponseBytes, because the token is decoded and parsed again once
	// it is out of the response.
	OIDCTokenBytes = 16 << 10

	// OIDCClockSkew is the slack allowed outside a token's own validity
	// window, sized for ordinary clock drift between two machines and
	// nothing more.
	OIDCClockSkew = 2 * time.Minute

	// OIDCDiscoveryTTL bounds how long a cached discovery document is
	// reused before a refetch, so a provider's key rotation is eventually
	// noticed.
	OIDCDiscoveryTTL = time.Hour

	// OIDCJWKSTTL is the equivalent cache bound for the key set.
	OIDCJWKSTTL = time.Hour

	// OIDCFlowLifetime bounds the window between redirecting a user out to
	// the provider and their return: enough for a person to sign in, short
	// enough that a leaked state value goes stale quickly.
	OIDCFlowLifetime = 10 * time.Minute

	// OIDCFlowTTL bounds how long an authorization attempt stays valid, the
	// same window in which a stolen state value could be replayed.
	OIDCFlowTTL = 10 * time.Minute
)

// WorkerWireMessage bounds one message on the preview worker's socket. The
// worker is the least trusted process here, so a peer over this size is
// killed rather than answered.
const WorkerWireMessage = 8 << 10

// Section 3: domain and persistence bounds, enforced against what this
// service actually stores.

// Path bounds, each refusing with 400.
const (
	PathComponents = 256
	PathBytes      = 4 << 10
	NameBytes      = 255
)

// DirEntriesBuffered bounds a directory read that collects its entries into
// memory at once. Refuses with ErrTooLarge; a caller unable to accept that
// reads the directory as a stream instead.
const DirEntriesBuffered = 100_000

// JournalRowsPerAccount holds by deleting the oldest rows as part of the
// same transaction that inserts new ones, so it stays true even if the
// writer crashes mid-batch.
const JournalRowsPerAccount = 1_000

const (
	// DavLocksPerUser rejects with status 507.
	DavLocksPerUser = 256

	// DavPropsPerResource bounds how many dead properties one resource may
	// carry.
	DavPropsPerResource = 256
)

// ErrTooLarge is the sentinel every bound in this package refuses with.
var ErrTooLarge = errors.New("limit exceeded")

// Exceeded carries the limit that refused plus the bound and the value that
// crossed it, since a bare "too large" gives a caller nothing to act on.
type Exceeded struct {
	Limit string
	Bound int64
	Got   int64
}

func (e *Exceeded) Error() string {
	return fmt.Sprintf("%s: %d exceeds the limit of %d", e.Limit, e.Got, e.Bound)
}

// Is matches ErrTooLarge, so a caller can check the sentinel with
// errors.Is and then unwrap the concrete type for the fields.
func (e *Exceeded) Is(target error) bool { return target == ErrTooLarge }

// Exceed constructs the refusal for a named limit.
func Exceed(limit string, bound, got int64) error {
	return &Exceeded{Limit: limit, Bound: bound, Got: got}
}
