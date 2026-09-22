package capability

import (
	"context"
	"io"
	"time"
)

// Kind identifies the shape of an entry. Backends should use KindOther when
// they know an entry exists but cannot classify it without another request.
type Kind uint8

const (
	KindOther Kind = iota
	KindFile
	KindDirectory
)

func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindDirectory:
		return "directory"
	default:
		return "other"
	}
}

func (k Kind) IsDir() bool { return k == KindDirectory }

// Entry is the backend-neutral description returned by a hierarchy read.
// Size is meaningful for files; ModTime may be the zero value when unavailable.
type Entry struct {
	Path    Path
	Kind    Kind
	Size    int64
	ModTime time.Time
}

// ReadHierarchy is the minimum read-only storage contract. The returned
// reader is owned by the caller and must be closed. ReadDir returns a bounded
// snapshot; implementations may return an error rather than silently omit
// entries when the backend cannot produce a complete snapshot.
type ReadHierarchy interface {
	Stat(ctx context.Context, path Path) (Entry, error)
	ReadDir(ctx context.Context, path Path) ([]Entry, error)
	OpenRead(ctx context.Context, path Path) (io.ReadCloser, error)
}

// HealthStatus describes whether a backend can currently answer requests.
type HealthStatus uint8

const (
	HealthUnknown HealthStatus = iota
	HealthOK
	HealthDegraded
	HealthFailing
)

func (s HealthStatus) String() string {
	switch s {
	case HealthOK:
		return "ok"
	case HealthDegraded:
		return "degraded"
	case HealthFailing:
		return "failing"
	default:
		return "unknown"
	}
}

// Health is a point-in-time backend status. Err is optional detail and is not
// interpreted by this package.
type Health struct {
	Status HealthStatus
	Err    error
}

// Renamer moves one entry to another path. It is deliberately not an atomic
// rename promise: object stores commonly implement this as copy then delete.
type Renamer interface {
	Rename(ctx context.Context, from, to Path) error
}

// RandomAccessWriter opens a file-like writer supporting io.WriterAt. The
// returned writer belongs to the caller, who must close it when it also
// implements io.Closer. Implementations may reject random access.
type RandomAccessWriter interface {
	OpenWriteAt(ctx context.Context, path Path) (io.WriterAt, error)
}

// MultipartPart identifies one uploaded part. Part numbers are one-based.
type MultipartPart struct {
	Number int
	ETag   string
	Size   int64
}

// MultipartStore is an optional multipart upload capability. The upload ID
// belongs to the caller until Complete or Abort consumes it.
type MultipartStore interface {
	BeginMultipart(ctx context.Context, path Path, size int64) (uploadID string, err error)
	UploadPart(ctx context.Context, uploadID string, number int, body io.Reader) (MultipartPart, error)
	CompleteMultipart(ctx context.Context, uploadID string, parts []MultipartPart) error
	AbortMultipart(ctx context.Context, uploadID string) error
}

// Space reports capacity figures. Unknown values are represented by zero only
// when the backend documents that zero means unknown; callers must not infer
// local-disk semantics from these values.
type Space struct {
	Total uint64
	Free  uint64
}

// SpaceReporter optionally reports capacity for a hierarchy path.
type SpaceReporter interface {
	Space(ctx context.Context, path Path) (Space, error)
}

// Event reports a potentially changed path. A backend may coalesce events;
// consumers must re-read the hierarchy rather than treating this as a journal.
type Event struct {
	Path Path
	Kind Kind
}

// Watchable optionally observes changes below a path. The returned channel is
// closed by the backend when the watch ends. The caller owns cancelation of ctx.
type Watchable interface {
	Watch(ctx context.Context, path Path) (<-chan Event, error)
}

// Materializer optionally provides a local, seekable representation of an
// entry. The returned lease is owned by the caller and must be released.
type Materializer interface {
	Materialize(ctx context.Context, path Path) (*Materialized, error)
}
