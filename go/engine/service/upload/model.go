// Builds only on Linux, where the types it names are openat2 handles beneath.
//go:build linux

// Package upload is the resumable-upload state machine every protocol
// drives. The TUS surface, the chunked compatibility surface and the native
// API all create sessions, append bytes and finalize through one engine.
//
// The engine does not know which protocol created a session. The spool-mode
// names say what a mode does, never which client wants it, and that
// isolation is load-bearing: it is what keeps one protocol's quirk from
// becoming a branch in the write path every other protocol also takes.
package upload

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// sessionIDBytes is how much entropy a session handle carries.
//
// The id is the whole of an upload URL, so it is the one thing standing
// between a request and somebody else's upload, on top of the owner check
// every call also applies: possession of a URL is addressing, not
// authorization.
const sessionIDBytes = 16

// SessionID is an opaque session handle that cannot be guessed.
type SessionID [sessionIDBytes]byte

// NewSessionID mints one from the system random source.
func NewSessionID() (SessionID, error) {
	var id SessionID
	if _, err := rand.Read(id[:]); err != nil {
		return SessionID{}, fmt.Errorf("naming an upload session: %w", err)
	}
	return id, nil
}

// String is the spelling a client sees, which is also what the part file is
// named after.
func (id SessionID) String() string { return base64.RawURLEncoding.EncodeToString(id[:]) }

// Bytes gives the on-disk representation.
func (id SessionID) Bytes() []byte { return id[:] }

// ParseSessionID reads the wire spelling.
//
// It is a trust boundary: the value arrives in a URL, so a wrong length is
// refused rather than padded, and the refusal is the same one an unknown
// session gets.
func ParseSessionID(s string) (SessionID, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != sessionIDBytes {
		return SessionID{}, fmt.Errorf("%w: a session id is %d base64url-encoded bytes",
			ErrNotFound, sessionIDBytes)
	}
	var id SessionID
	copy(id[:], b)
	return id, nil
}

func sessionIDFromBytes(b []byte) (SessionID, error) {
	if len(b) != sessionIDBytes {
		return SessionID{}, fmt.Errorf("a stored session id is %d bytes, not %d",
			len(b), sessionIDBytes)
	}
	var id SessionID
	copy(id[:], b)
	return id, nil
}

// SessionState marks a session's stage of life.
type SessionState int64

const (
	// StateReceiving marks a session still taking bytes.
	StateReceiving SessionState = iota
	// StateFinalizing is one that has begun publishing. It is a real
	// transition rather than a declared-and-never-set value: a session that
	// is publishing is not receiving, and must not be swept mid-publish.
	StateFinalizing
	// StateDone marks one whose bytes have reached the destination.
	StateDone
	// StateAborted is one a client terminated.
	StateAborted
	// StateExpired marks one beyond its lifetime. It is computed from the clock
	// instead of stored, so expiry requires no writer.
	StateExpired
)

// StateName is the wire name of a session's state.
//
// Names rather than the stored numbers, which may only be appended to. The
// presentation tier has no other way to say what a session is doing.
func (s SessionState) StateName() string {
	switch s {
	case StateReceiving:
		return "receiving"
	case StateFinalizing:
		return "finalizing"
	case StateDone:
		return "done"
	case StateAborted:
		return "aborted"
	case StateExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// Terminal reports whether a session will take no more bytes.
//
// Decided here rather than by a client comparing state names, because a state
// added later would otherwise be read as still live by every client written
// before it.
func (s SessionState) Terminal() bool { return !s.live() }

// StateNames lists every state name with whether it is terminal, so a tier
// that cannot read the stored numbers can check its own answer against this.
func StateNames() map[string]bool {
	return map[string]bool{
		"receiving":  false,
		"finalizing": false,
		"done":       true,
		"aborted":    true,
		"expired":    true,
	}
}

// live reports whether a state is one the sweep leaves alone. Receiving and
// finalizing are both live: an assembly that takes minutes is not an
// abandoned session.
func (s SessionState) live() bool { return s == StateReceiving || s == StateFinalizing }

// ModeName is the wire name of a spool mode.
func (m SpoolMode) ModeName() string {
	switch m {
	case SpoolOffsetAddressed:
		return "offset"
	case SpoolNameOrdered:
		return "named"
	default:
		return "unknown"
	}
}

// SpoolMode determines how chunks map onto the part file.
type SpoolMode int64

const (
	// SpoolOffsetAddressed places each chunk at the client-supplied offset, so
	// no assembly is needed.
	SpoolOffsetAddressed SpoolMode = iota
	// SpoolNameOrdered gives each chunk its own file and assembles them during
	// finalize in ascending name order, serving protocols that carry no
	// offsets.
	SpoolNameOrdered
)

// Algo names a checksum algorithm.
//
// Both are client-facing: each is a value a client puts in an upload
// checksum header, which is why neither can be swapped for whatever the
// standard library happens to offer.
type Algo int64

const (
	// AlgoCRC32C is CRC32 using the Castagnoli polynomial from the standard
	// library.
	AlgoCRC32C Algo = iota
	// AlgoBLAKE3 is the pure-Go module the directory ETag already uses, so a
	// build with no C toolchain has nothing to fall back from.
	AlgoBLAKE3
)

// String is the wire spelling, lowercase, as the protocol advertises it.
func (a Algo) String() string {
	if a == AlgoBLAKE3 {
		return "blake3"
	}
	return "crc32c"
}

// ParseAlgo reads the wire spelling.
//
// An algorithm this server does not offer is refused rather than defaulted:
// defaulting would verify a chunk against a digest the client never
// computed, which is a check that cannot fail.
func ParseAlgo(s string) (Algo, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "crc32c":
		return AlgoCRC32C, nil
	case "blake3":
		return AlgoBLAKE3, nil
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownAlgo, s)
}

// Algorithms lists what the server advertises, ordered by preference.
func Algorithms() []Algo { return []Algo{AlgoCRC32C, AlgoBLAKE3} }

// Checksum is one digest and the algorithm that produced it.
//
// The pair travels together because an algorithm with nothing to compare
// against is the shape that shipped once and could never fail.
type Checksum struct {
	Algo   Algo
	Digest []byte
}

// ParseChecksum reads an upload checksum value: an algorithm name, a space,
// and the digest in base64.
func ParseChecksum(s string) (Checksum, error) {
	name, digest, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok {
		return Checksum{}, fmt.Errorf("%w: expected an algorithm and a digest", ErrBadRequest)
	}
	algo, err := ParseAlgo(name)
	if err != nil {
		return Checksum{}, err
	}
	raw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(digest))
	if derr != nil {
		return Checksum{}, fmt.Errorf("%w: the digest is not base64", ErrBadRequest)
	}
	if lerr := checkDigestLen(algo, len(raw)); lerr != nil {
		return Checksum{}, lerr
	}
	return Checksum{Algo: algo, Digest: raw}, nil
}

// String is the wire spelling of a checksum, which is what a client sends.
func (c Checksum) String() string {
	return c.Algo.String() + " " + base64.StdEncoding.EncodeToString(c.Digest)
}

// checkDigestLen rejects a digest that could not be the algorithm's output.
// Without this, a short one would be compared against a truncation of the real
// digest and succeed.
func checkDigestLen(a Algo, n int) error {
	if want := digestLen(a); n != want {
		return fmt.Errorf("%w: a %s digest is %d bytes, not %d", ErrBadRequest, a, want, n)
	}
	return nil
}

func digestLen(a Algo) int {
	if a == AlgoBLAKE3 {
		return 32
	}
	return 4
}

// Verify is the opt-in whole-file check at finalize.
//
// It carries the algorithm and the expected digest as one value, because the
// shape that shipped before carried only a selector: verification computed a
// digest, logged it, and could never fail whatever arrived on disk.
type Verify struct {
	Algo   Algo
	Digest []byte
}

// Meta is what a client says about the file it is sending. None of it
// decides where the bytes land; that is the destination the session was
// resolved to.
type Meta struct {
	Filename     string
	MtimeNs      *int64
	Mime         string
	RelativePath string
	// Verify is nil where a session requested no whole-file check.
	Verify *Verify
}

// SessionSpec describes what Create is asked to open.
type SessionSpec struct {
	// TotalLen is nil when the length is deferred, supplied by the client later
	// and demanded by finalize.
	TotalLen *uint64
	// RandomAccess lets chunks arrive at any offset. Without it a chunk has
	// to land at the resumable offset.
	RandomAccess bool
	// IfMatch holds the destination's token as of the client's last view.
	IfMatch string
	Mode    SpoolMode
	Meta    Meta
}

// Session presents a single upload as a caller sees it.
type Session struct {
	ID    SessionID
	User  core.UserID
	Share core.ShareID
	// Dest names the share-relative destination the file publishes to.
	Dest  vfs.SafePath
	State SessionState
	// Offset gives the resumable position: the end of the first range when the
	// set begins at zero, and zero in every other case.
	Offset   uint64
	TotalLen *uint64
	// Received is how many bytes have actually landed, which is not the
	// offset once a random-access client has written past a hole. Both
	// travel: one is what a resume needs and the other is what a progress
	// bar shows.
	Received     uint64
	ChunkSize    uint64
	RunCount     int
	RandomAccess bool
	Mode         SpoolMode
	Cached       bool
	ExpiresNs    int64
}

// Alias is a client-chosen transfer id bound to a session.
type Alias struct {
	Session SessionID
	Share   core.ShareID
	Dest    string
}

// partName gives the reserved control name a session's bytes accumulate under.
//
// The form is ".scpart-{id}" exclusively. An earlier design disguised it as
// ".{basename}.scpart-{id}" to slip past component validation, which broke the
// reserved-name filter: part files then showed up in ordinary listings, in the
// web interface and to sync clients, throughout every upload.
func partName(id SessionID) string { return ".scpart-" + id.String() }

// spoolDirName is the directory a name-ordered session's chunks wait in.
func spoolDirName(id SessionID) string { return ".scpart-" + id.String() + ".d" }

// cacheDirName is the directory a cached session's chunks land in, inside
// the spool.
//
// It carries the reserved prefix even though nothing lists the spool: the
// prefix is what every filter in this tree keys on, and a control file that
// outlives its directory has to stay unlistable wherever it ends up.
func cacheDirName(id SessionID) string { return ".scpart-" + id.String() + ".c" }

// partPath gives the part file's location, which is the destination's own
// directory, because the publishing rename is atomic only within a single
// directory.
func partPath(dest vfs.SafePath, name string) (vfs.SafePath, error) {
	return dest.Parent().JoinControl(name)
}

// chunkFileName is the name one spooled chunk takes inside its directory:
// fixed-width hex of the ordinal, so the on-disk order and the numeric order
// agree and a listing needs no parsing to be sorted.
func chunkFileName(n uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], n)
	return ".scpart-" + hex.EncodeToString(b[:])
}
