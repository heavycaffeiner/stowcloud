//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The offset-addressed write path.
//
// The ordering rule admits no exceptions: the disk write finishes before the
// range is committed. Crashing between the two leaves the set reporting the
// shorter prefix, so the client resends identical bytes at the same offset and
// they land the same way. Inverting the order would let the set claim bytes
// never durably written, producing silent corruption rather than a slow
// upload.

// PatchAt writes the body at off within the session's part file and records the
// range. It is the sole writer for an offset-addressed session.
//
// The row lock protects the bookkeeping and never the body. This is a named
// regression: holding it across the body read serialized concurrent chunks, and
// over a multiplexed connection that deadlocks instead of queuing. Blocked
// handlers never read their streams, the connection's flow-control window fills,
// and the chunk holding the lock cannot receive its own body. Every upload
// stalled after its first chunk.
func (e *Engine) PatchAt(
	ctx context.Context, root vfs.Root, id SessionID, user core.UserID,
	off uint64, body io.Reader, sum *Checksum,
) (uint64, error) {
	unlockChunk := e.lockChunk(id, off)
	defer unlockChunk()

	unlock := e.lockRow(id)
	r, err := e.load(ctx, id)
	if err != nil {
		unlock()
		return 0, err
	}
	if oerr := requireOwner(r, user); oerr != nil {
		unlock()
		return 0, oerr
	}
	if serr := e.requireReceiving(r); serr != nil {
		unlock()
		return 0, serr
	}
	if r.mode() != SpoolOffsetAddressed {
		unlock()
		return 0, fmt.Errorf("%w: this session is name-ordered", ErrBadRequest)
	}
	if verr := validateOffset(r, off); verr != nil {
		unlock()
		return 0, verr
	}

	part, err := e.partPathOf(r)
	if err != nil {
		unlock()
		return 0, err
	}
	cached := r.cached() && e.cache != nil
	var f *vfs.File
	if !cached {
		if f, err = e.handleFor(root, id, part); err != nil {
			unlock()
			return 0, err
		}
	}
	unlock()

	// Writing and hashing happen in a single pass. Nothing buffers the whole
	// chunk, so a per-chunk checksum costs a hasher rather than a copy.
	var (
		n      uint64
		digest []byte
		werr   error
	)
	if cached {
		n, digest, werr = e.patchCached(ctx, root, r, id, part, off, body, sum)
	} else {
		n, digest, werr = e.writeBody(f, off, body, r, sum)
	}
	if werr != nil {
		// A cancellation closes the part file while chunks of the same session
		// are still writing, and this is what those writes hit. The session is
		// gone on purpose, so it is reported as gone: answering with a server
		// fault made clients read a deliberate cancellation as one and retry.
		if errors.Is(werr, os.ErrClosed) {
			return 0, ErrNotFound
		}
		return 0, werr
	}

	// Checksum verification precedes recording the range. A failing chunk leaves
	// the set unchanged, so the client resends that same range instead of
	// resuming beyond a gap it believes is filled. The bytes already on disk are
	// simply overwritten by the resend.
	if sum != nil && digest != nil && !constantTimeEqual(digest, sum.Digest) {
		return 0, fmt.Errorf("%w: the %s digest does not match the %d bytes received",
			ErrChecksum, sum.Algo, n)
	}

	// Reacquired to record what arrived. The row is re-read while holding the
	// lock instead of reusing the earlier copy, because another chunk of this
	// same file has very likely committed its own range meanwhile, and writing
	// back a stale set would discard it.
	relock := e.lockRow(id)
	defer relock()

	fresh, err := e.load(ctx, id)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		if ierr := fresh.set.Insert(off, off+n); ierr != nil {
			return 0, ierr
		}
	}
	if cerr := e.commitRange(ctx, fresh); cerr != nil {
		return 0, cerr
	}
	return fresh.set.ContiguousPrefix(), nil
}

// writeBody streams body into f at off, hashing as it goes when the client
// supplied a checksum.
func (e *Engine) writeBody(
	f *vfs.File, off uint64, body io.Reader, r *row, sum *Checksum,
) (uint64, []byte, error) {
	return e.writeBodyAt(f, off, off, body, r, sum)
}

// writeBodyAt is writeBody with its two offsets kept apart: the position the
// bytes are written to, and the position they occupy in the finished file.
//
// Only one caller sees them diverge. A cached chunk lives in its own file with
// bytes beginning at zero, whereas the declared length and the chunk floor
// constrain the assembled file and must be measured against the offset the
// client supplied.
//
// r is nil for a write no session rule applies to, which is a spooled chunk:
// it is measured against the assembled file, and a name-ordered client does
// not choose the offsets those rules are about.
func (e *Engine) writeBodyAt(
	f *vfs.File, at, logical uint64, body io.Reader, r *row, sum *Checksum,
) (uint64, []byte, error) {
	var hasher *streamHasher
	if sum != nil {
		if err := checkDigestLen(sum.Algo, len(sum.Digest)); err != nil {
			return 0, nil, err
		}
		hasher = newStreamHasher(sum.Algo)
	}

	buf := make([]byte, writeBufBytes)
	var written uint64
	for {
		read, rerr := body.Read(buf)
		if read > 0 {
			got, nerr := num.Narrow[uint64](read)
			if nerr != nil {
				return written, nil, nerr
			}
			// The declared length is checked as bytes arrive rather than from
			// a header, since a header merely asserts while this observes. A
			// body exceeding the declaration is rejected before anything is
			// written past the file's end.
			if cerr := checkWithinDeclared(r, logical, written+got); cerr != nil {
				return written, nil, cerr
			}
			if werr := writeAllAt(f, buf[:read], at+written); werr != nil {
				return written, nil, werr
			}
			if hasher != nil {
				hasher.write(buf[:read])
			}
			written += got
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return written, nil, rerr
		}
		if read == 0 {
			break
		}
	}
	if ferr := checkChunkFloor(r, logical, written); ferr != nil {
		return written, nil, ferr
	}
	if hasher == nil {
		return written, nil, nil
	}
	return written, hasher.sum(), nil
}

// writeAllAt loops over a short write, which the underlying call is allowed to
// produce even on success.
//
// A zero-length write with no error is a failure rather than a retry: it means
// the file cannot take the bytes, and looping on it spins forever.
func writeAllAt(f *vfs.File, b []byte, off uint64) error {
	for len(b) > 0 {
		at, err := num.Narrow[int64](off)
		if err != nil {
			return err
		}
		n, werr := f.WriteAt(b, at)
		if n == 0 && werr == nil {
			return errors.New("the part file accepted no bytes and reported no error")
		}
		if werr != nil {
			return mapVFSErr(werr)
		}
		wrote, nerr := num.Narrow[uint64](n)
		if nerr != nil {
			return nerr
		}
		b = b[n:]
		off += wrote
	}
	return nil
}
