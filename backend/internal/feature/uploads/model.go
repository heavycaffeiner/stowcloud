// Builds only on Linux, where the types it names are openat2 handles beneath.
//go:build linux

// Package upload is the resumable-upload state machine every protocol drives.
// The TUS surface, the chunked compatibility surface and the native API all
// create sessions, append bytes and finalize through one engine.
package upload

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/stowcloud/transfer"
)

// Neutral session identity, lifecycle, specification and checksum contracts
// come from the public transfer module. Product-only fields stay in Session.
type SessionID = transfer.SessionID
type SessionState = transfer.SessionState
type SpoolMode = transfer.SpoolMode
type Algo = transfer.Algo
type Checksum = transfer.Checksum
type Verify = transfer.Verify
type Meta = transfer.Meta
type SessionSpec = transfer.SessionSpec

const (
	StateReceiving       = transfer.StateReceiving
	StateFinalizing      = transfer.StateFinalizing
	StateDone            = transfer.StateDone
	StateAborted         = transfer.StateAborted
	StateExpired         = transfer.StateExpired
	SpoolOffsetAddressed = transfer.SpoolOffsetAddressed
	SpoolNameOrdered     = transfer.SpoolNameOrdered
	AlgoCRC32C           = transfer.AlgoCRC32C
	AlgoBLAKE3           = transfer.AlgoBLAKE3
)

// StateNames exposes the public lifecycle names to the presentation layer.
func StateNames() map[string]bool { return transfer.StateNames() }

// ParseAlgo and ParseChecksum preserve the upload package's product error
// vocabulary while delegating parsing and validation to transfer.
func ParseAlgo(s string) (Algo, error) {
	a, err := transfer.ParseAlgo(s)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnknownAlgo, err)
	}
	return a, nil
}

func ParseChecksum(s string) (Checksum, error) {
	c, err := transfer.ParseChecksum(s)
	if err != nil {
		return Checksum{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	return c, nil
}

// Session presents one product upload, combining public lifecycle data with
// ACL, destination and progress details that are intentionally application-
// specific.
type Session struct {
	ID    SessionID
	User  core.UserID
	Share core.ShareID
	// Dest names the share-relative destination the file publishes to.
	Dest  vfs.SafePath
	State SessionState
	// and zero in every other case.
	Offset   uint64
	TotalLen *uint64
	// Received is how many bytes have actually landed, which is not the offset
	// once a random-access client has written past a hole.
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

func partName(id SessionID) string     { return ".scpart-" + id.String() }
func spoolDirName(id SessionID) string { return ".scpart-" + id.String() + ".d" }
func cacheDirName(id SessionID) string { return ".scpart-" + id.String() + ".c" }

// publication is atomic only within one directory.
func partPath(dest vfs.SafePath, name string) (vfs.SafePath, error) {
	return dest.Parent().JoinControl(name)
}

// chunkFileName is fixed-width hex of the ordinal, keeping lexical and numeric
// order identical without parsing directory listings.
func chunkFileName(n uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], n)
	return ".scpart-" + hex.EncodeToString(b[:])
}
