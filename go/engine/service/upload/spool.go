//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"
	"io"

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
	lease, admitted := e.admitWriter(id)
	if !admitted {
		unlock()
		return 0, ErrSessionState
	}

	part, err := e.partPathOf(r)
	if err != nil {
		lease.release()
		unlock()
		return 0, err
	}
	cached := r.cached() && e.cache != nil
	unlock()
	defer lease.release()

	// Every body is staged and validated before it can touch an accepted
	// range. This is what makes a rejected replacement observationally
	var (
		stage vfs.SafePath
		n     uint64
		werr  error
	)
	if cached {
		stage, n, _, werr = e.patchCached(ctx, root, r, id, part, off, body, sum)
	} else {
		stage, n, _, werr = e.stageBody(
			root, part.Parent(), off, body, r, sum,
		)
	}
	if werr != nil {
		return 0, werr
	}
	cleanup := func() {
		if stage.IsRoot() {
			return
		}
		cacheRoot := root
		if cached {
			cacheRoot = e.cache.root
		}
		if uerr := cacheRoot.Unlink(stage); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("an upload staging file could not be removed", "error", uerr)
		}
	}
	defer cleanup()

	if !lease.valid() {
		return 0, ErrSessionState
	}

	// The row is re-read only after the body has been validated. The barrier
	// makes this state check meaningful: a finalizer may not turn the session
	// terminal while this writer still owns its admission slot.
	relock := e.lockRow(id)
	defer relock()
	fresh, err := e.load(ctx, id)
	if err != nil {
		return 0, err
	}
	if !lease.valid() {
		return 0, ErrSessionState
	}
	if serr := e.requireReceiving(fresh); serr != nil {
		return 0, serr
	}
	var nextSet *IntervalSet
	if n > 0 {
		nextSet, err = LoadIntervalSet(fresh.set.Runs())
		if err != nil {
			return 0, err
		}
		if ierr := nextSet.Insert(off, off+n); ierr != nil {
			return 0, ierr
		}
	} else {
		nextSet = fresh.set
	}
	if n > 0 {
		if cached {
			if cerr := e.commitCached(stage, fresh, off, n); cerr != nil {
				return 0, cerr
			}
		} else if cerr := e.commitStagedAt(root, id, part, stage, off, n); cerr != nil {
			return 0, cerr
		}
	}
	fresh.set = nextSet
	if cerr := e.commitRange(ctx, fresh); cerr != nil {
		return 0, cerr
	}
	if cached {
		e.mergerFor(id).nudge()
	}
	return fresh.set.ContiguousPrefix(), nil
}

// stageBody writes one validated body to a control file in the destination
// directory. The file is never given a discoverable final name until the
// checksum and all stream bounds have passed.
func (e *Engine) stageBody(
	root vfs.Root, dir vfs.SafePath, logical uint64, body io.Reader,
	r *row, sum *Checksum,
) (vfs.SafePath, uint64, []byte, error) {
	name, err := cacheStagingName()
	if err != nil {
		return vfs.SafePath{}, 0, nil, err
	}
	stage, err := dir.JoinControl(name)
	if err != nil {
		return vfs.SafePath{}, 0, nil, err
	}
	f, err := root.CreatePart(stage)
	if err != nil {
		return vfs.SafePath{}, 0, nil, mapVFSErr(err)
	}
	closed := false
	done := false
	defer func() {
		if !closed {
			if cerr := f.Close(); cerr != nil {
				e.log.Warn("closing an upload staging file failed", "error", cerr)
			}
		}
		if done {
			return
		}
		if uerr := root.Unlink(stage); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("an upload staging file could not be removed", "error", uerr)
		}
	}()

	n, digest, werr := e.writeBodyAt(f, 0, logical, body, r, sum)
	if werr != nil {
		return stage, n, digest, werr
	}
	if derr := verifyChunkChecksum(sum, digest, n); derr != nil {
		return stage, n, digest, derr
	}
	if serr := f.SyncData(); serr != nil {
		return stage, n, digest, mapVFSErr(serr)
	}
	if cerr := f.Close(); cerr != nil {
		return stage, n, digest, mapVFSErr(cerr)
	}
	closed = true
	done = true
	return stage, n, digest, nil
}

// commitStagedAt copies a validated chunk onto the part file and makes that
// replacement durable before the interval transaction records it.
func (e *Engine) commitStagedAt(
	root vfs.Root, id SessionID, part, stage vfs.SafePath, off, n uint64,
) error {
	src, err := root.OpenRead(stage, vfs.IntentRead)
	if err != nil {
		return mapVFSErr(err)
	}
	defer func() {
		if cerr := src.Close(); cerr != nil {
			e.log.Warn("closing an upload staging source failed", "error", cerr)
		}
	}()
	dst, err := e.handleFor(root, id, part)
	if err != nil {
		return err
	}
	copied, err := vfs.CopyRange(src, 0, dst, off, n)
	if err != nil {
		return mapVFSErr(err)
	}
	if copied != n {
		return fmt.Errorf("staging copy moved %d of %d bytes", copied, n)
	}
	if err := dst.SyncData(); err != nil {
		return mapVFSErr(err)
	}
	if err := root.Unlink(stage); err != nil && !errors.Is(err, vfs.ErrNotFound) {
		e.log.Warn("an upload staging file could not be removed", "error", err)
	}
	return nil
}

func verifyChunkChecksum(sum *Checksum, digest []byte, n uint64) error {
	if sum == nil || digest == nil || constantTimeEqual(digest, sum.Digest) {
		return nil
	}
	return fmt.Errorf("%w: the %s digest does not match the %d bytes received",
		ErrChecksum, sum.Algo, n)
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
