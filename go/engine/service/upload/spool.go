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
// The ordering rule is not negotiable: the disk write completes before the
// range is committed. A crash between the two leaves the set reporting the
// smaller prefix, so the client resends the same bytes at the same offset and
// they land identically. Reversing it would let the set claim bytes that were
// never durably written, which is silent corruption rather than a slow
// upload.

// PatchAt writes the body at off in the session's part file and records the
// range. It is the only writer for an offset-addressed session.
//
// The row lock covers the bookkeeping and never the body. That is a named
// regression: holding it across the body read serialized concurrent chunks,
// and over a multiplexed connection that deadlocks rather than queues.
// Blocked handlers never read their streams, the connection's flow-control
// window fills, and the chunk holding the lock cannot receive its own body.
// Every upload stopped after its first chunk.
func (e *Engine) PatchAt(
	ctx context.Context, root *vfs.ShareRoot, id SessionID, user core.UserID,
	off uint64, body io.Reader, sum *Checksum,
) (uint64, error) {
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

	// The body is written and hashed in one pass. Nothing accumulates the
	// whole chunk, so a per-chunk checksum costs a hasher and not a copy.
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

	// The checksum is verified before the range is recorded. A chunk that
	// fails leaves the set untouched, so the client resends the same range
	// rather than resuming past a hole it believes is filled. The bytes are
	// already on disk and are simply overwritten by the resend.
	if sum != nil && digest != nil && !constantTimeEqual(digest, sum.Digest) {
		return 0, fmt.Errorf("%w: the %s digest does not match the %d bytes received",
			ErrChecksum, sum.Algo, n)
	}

	// Retaken to record what landed. The row is re-read under the lock rather
	// than reusing the copy from above: another chunk of this same file has
	// very likely recorded its own range in the meantime, and writing back a
	// stale set would drop it.
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

// writeBodyAt is writeBody with the two offsets separated: where the bytes go,
// and where they belong in the finished file.
//
// They differ for exactly one caller. A cached chunk is a file of its own and
// its bytes start at zero in it, while the declared length and the chunk floor
// are rules about the assembled file and have to be measured against the
// offset the client sent.
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
			// The declared length is enforced as the bytes arrive rather than
			// from a header, because a header is a claim and this is the
			// stream. A body longer than declared is refused before it is
			// written past the end of the file.
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
