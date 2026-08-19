package upload

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// sessionIDBytes is how much entropy a session handle carries. The id is the
// whole of a TUS URL, so it is the one thing standing between a request and
// somebody else's upload, on top of the owner check every call also applies.
const sessionIDBytes = 16

// SessionID is an opaque, unguessable session handle.
type SessionID [sessionIDBytes]byte

// NewSessionID mints one from the system CSPRNG.
func NewSessionID() (SessionID, error) {
	var id SessionID
	if _, err := rand.Read(id[:]); err != nil {
		return SessionID{}, fmt.Errorf("naming an upload session: %w", err)
	}
	return id, nil
}

// String is the base64url spelling a client sees, which is also what the part
// file is named after.
func (id SessionID) String() string {
	return base64.RawURLEncoding.EncodeToString(id[:])
}

// Bytes is the storage spelling.
func (id SessionID) Bytes() []byte { return id[:] }

// ParseSessionID reads the wire spelling. It is a trust boundary: the value
// arrives in a URL, so a wrong length is refused rather than padded.
func ParseSessionID(s string) (SessionID, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
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
		return SessionID{}, fmt.Errorf("a stored session id is %d bytes, not %d", len(b), sessionIDBytes)
	}
	var id SessionID
	copy(id[:], b)
	return id, nil
}

// SessionState is where one session is in its life.
type SessionState int64

const (
	// StateReceiving is a session still accepting bytes.
	StateReceiving SessionState = iota
	// StateFinalizing is one that has begun publishing.
	StateFinalizing
	// StateDone is one whose bytes are at the destination.
	StateDone
	// StateAborted is one a client terminated. Its part file is the sweep's.
	StateAborted
	// StateExpired is one past its lifetime. It is derived from the clock
	// rather than stored, so a session does not need a writer to expire.
	StateExpired
)

// SpoolMode is how chunks map onto the part file.
//
// The names say what each one does rather than which client needs it, which is
// the protocol-isolation principle holding at the place it would be easiest to
// break: this package does not know which protocol created a session.
type SpoolMode int64

const (
	// SpoolOffsetAddressed writes each chunk at the offset the client gave,
	// so nothing has to be assembled.
	SpoolOffsetAddressed SpoolMode = iota
	// SpoolNameOrdered writes each chunk to a file of its own and assembles
	// them at finalize in ascending name order. It is for a chunked upload
	// that carries no offsets, only names.
	SpoolNameOrdered
)

// Algo names a checksum algorithm. Both are client-facing: each is a value a
// client puts in a TUS Upload-Checksum header, which is why neither can be
// swapped for whatever the standard library happens to offer.
type Algo int64

const (
	// AlgoCRC32C is CRC32 with the Castagnoli polynomial, standard library.
	AlgoCRC32C Algo = iota
	// AlgoBLAKE3 is the module the directory ETag already uses.
	AlgoBLAKE3
)

// String is the wire spelling, lowercase, as TUS advertises it.
func (a Algo) String() string {
	if a == AlgoBLAKE3 {
		return "blake3"
	}
	return "crc32c"
}

// ParseAlgo reads the wire spelling. An algorithm this server does not offer
// is refused rather than defaulted, because defaulting would verify a chunk
// against a digest the client never computed.
func ParseAlgo(s string) (Algo, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "crc32c":
		return AlgoCRC32C, nil
	case "blake3":
		return AlgoBLAKE3, nil
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownAlgo, s)
}

// Algorithms is what the server advertises, in the order it prefers them.
func Algorithms() []Algo { return []Algo{AlgoCRC32C, AlgoBLAKE3} }

// Checksum is one digest and the algorithm that produced it. The pair travels
// together because an algorithm with nothing to compare against is the shape
// that shipped once and could never fail.
type Checksum struct {
	Algo   Algo
	Digest []byte
}

// ParseChecksum reads a TUS Upload-Checksum value: an algorithm name, a space,
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
	if err := checkDigestLen(algo, len(raw)); err != nil {
		return Checksum{}, err
	}
	return Checksum{Algo: algo, Digest: raw}, nil
}

// checkDigestLen refuses a digest that cannot be the algorithm's output. A
// short one would otherwise be compared against a truncation of the real
// digest and pass.
func checkDigestLen(a Algo, n int) error {
	want := digestLen(a)
	if n != want {
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

// Verify is the opt-in whole-file check at finalize: an algorithm and the
// digest the caller expects the finished file to hash to.
type Verify struct {
	Algo   Algo
	Digest []byte
}

// Meta is what a client says about the file it is sending. None of it decides
// where the bytes land; that is the destination the session was resolved to.
type Meta struct {
	Filename     string
	MtimeNs      *int64
	Mime         string
	RelativePath string
	// Verify is nil for a session that asked for no whole-file check.
	Verify *Verify
}

// SessionSpec is what Create is asked to open.
type SessionSpec struct {
	// TotalLen is nil for a deferred length, which the client supplies later
	// and finalize requires.
	TotalLen *uint64
	// RandomAccess lets chunks arrive at any offset. Without it a chunk has to
	// land at the resumable offset, which is what a plain TUS client does.
	RandomAccess bool
	// IfMatch is the destination's token as the client last saw it.
	IfMatch string
	Mode    SpoolMode
	Meta    Meta
}

// Session is one upload as a caller sees it.
type Session struct {
	ID    SessionID
	User  core.UserID
	Share core.ShareID
	// Dest is the share-relative destination the file publishes to.
	Dest  vfs.SharePath
	State SessionState
	// Offset is the resumable offset: the end of the first range when the set
	// starts at zero, and zero otherwise.
	Offset   uint64
	TotalLen *uint64
	// Received is how many bytes have actually landed, which is not the offset
	// once a random-access client has written past a hole.
	Received     uint64
	ChunkSize    uint64
	RunCount     int
	RandomAccess bool
	Mode         SpoolMode
	ExpiresNs    int64
}

// partName is the reserved control name a session's bytes accumulate under.
//
// It is ".scpart-{id}" and nothing else. An earlier revision of the design
// disguised it as ".{basename}.scpart-{id}" to get past component validation,
// which defeated the reserved-name filter: part files then showed up in
// ordinary listings, in the web UI and to WebDAV clients, for the duration of
// every upload.
func partName(id SessionID) string { return ".scpart-" + id.String() }

// spoolDirName is the directory a name-ordered session's chunks wait in. It
// carries the same reserved prefix, so it is unlistable for its whole life.
func spoolDirName(id SessionID) string { return ".scpart-" + id.String() + ".d" }

// partPath is the part file's path: the destination's own directory, because
// the rename that publishes it is only atomic within one directory.
func partPath(dest vfs.SafePath, name string) (vfs.SafePath, error) {
	return dest.Parent().JoinControl(name)
}

// chunkFileName is the name one spooled chunk takes inside the spool
// directory. Hex of the ordinal, fixed width, so the on-disk order and the
// numeric order are the same and a listing needs no parsing to be sorted.
//
// It carries the control prefix even though the spool directory it sits in
// already does. The prefix is what every listing filters on, so a chunk that
// outlived its directory being removed is still unlistable rather than
// depending on where it happens to be.
func chunkFileName(n uint32) string {
	var b [4]byte
	b[0] = byte(n >> 24)
	b[1] = byte(n >> 16)
	b[2] = byte(n >> 8)
	b[3] = byte(n)
	return ".scpart-" + hex.EncodeToString(b[:])
}

// ErrUnknownAlgo is a checksum algorithm this server does not offer.
var ErrUnknownAlgo = errors.New("unknown checksum algorithm")
