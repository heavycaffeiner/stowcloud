package capability

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ErrReadAtUnsupported is returned by ReadAt when the materialized reader does
// not provide random access. Callers can instead use the standard Reader and
// Seeker methods exposed by the lease.
var ErrReadAtUnsupported = errors.New("storage capability: random access unsupported")

// Materialized is a caller-owned, seekable representation of backend data.
// The lease owns its cleanup callback: callers must call Release exactly when
// finished, and may safely call it more than once. The callback runs at most
// once, and every call returns the callback's result.
type Materialized struct {
	Reader io.ReadSeeker
	Size   int64

	once       sync.Once
	release    func() error
	releaseErr error
}

// NewMaterialized creates a materialized lease. Reader and release may be nil
// only when a backend has no corresponding resource; a nil release is treated
// as an already-clean lease.
func NewMaterialized(reader io.ReadSeeker, size int64, release func() error) *Materialized {
	return &Materialized{Reader: reader, Size: size, release: release}
}

func (m *Materialized) Read(p []byte) (int, error) {
	if m == nil || m.Reader == nil {
		return 0, io.EOF
	}
	return m.Reader.Read(p)
}

func (m *Materialized) Seek(offset int64, whence int) (int64, error) {
	if m == nil || m.Reader == nil {
		return 0, io.ErrClosedPipe
	}
	return m.Reader.Seek(offset, whence)
}

// ReadAt forwards random access when the underlying reader supports it.
func (m *Materialized) ReadAt(p []byte, offset int64) (int, error) {
	if m == nil || m.Reader == nil {
		return 0, io.EOF
	}
	if r, ok := m.Reader.(io.ReaderAt); ok {
		return r.ReadAt(p, offset)
	}
	return 0, ErrReadAtUnsupported
}

// Release returns the materialized resource to its owner. It is idempotent;
// the owner callback is invoked once even if multiple cleanup paths race.
func (m *Materialized) Release() error {
	if m == nil {
		return nil
	}
	m.once.Do(func() {
		if m.release != nil {
			m.releaseErr = m.release()
		}
	})
	return m.releaseErr
}

// HealthChecker is an optional health probe. A failed type assertion means the
// backend does not expose health information, not that it is unhealthy.
type HealthChecker interface {
	Health(ctx context.Context) Health
}
