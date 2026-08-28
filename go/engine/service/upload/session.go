//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Create opens a session against a resolved destination.
func (e *Engine) Create(ctx context.Context, r core.Resolved, spec SessionSpec) (Session, error) {
	if err := r.Require(acl.Write | acl.Create); err != nil {
		return Session{}, err
	}
	dest := r.Path()
	if dest.IsRoot() {
		return Session{}, fmt.Errorf("%w: the destination has no file name", ErrBadRequest)
	}
	if err := e.checkAccountLimits(ctx, r.User(), spec.TotalLen); err != nil {
		return Session{}, err
	}
	if err := e.checkFreeSpace(r.Root(), dest.Parent(), spec.TotalLen); err != nil {
		return Session{}, err
	}
	if v := spec.Meta.Verify; v != nil {
		if err := checkDigestLen(v.Algo, len(v.Digest)); err != nil {
			return Session{}, err
		}
	}

	id, err := NewSessionID()
	if err != nil {
		return Session{}, err
	}
	part, err := partPath(dest, partName(id))
	if err != nil {
		return Session{}, err
	}

	// The part file is created and sized up front: an exclusive create plus a
	// sparse truncate, so nothing is copied and the destination directory
	// holds exactly one unlistable entry for the session's whole life.
	f, err := e.createPart(r.Root(), part, spec.TotalLen)
	if err != nil {
		return Session{}, err
	}

	sess, err := e.newRow(id, r, dest, spec)
	if err != nil {
		return Session{}, errors.Join(err, e.discardPart(r.Root(), part, f))
	}
	if err := e.state.CreateUploadSession(ctx, sess); err != nil {
		// The row is what makes the part file findable. Without one the file is
		// an orphan the sweep would have to notice, so it goes now.
		return Session{}, errors.Join(err, e.discardPart(r.Root(), part, f))
	}
	// The directory is recorded before the first byte arrives and outlives the
	// session. An orphan is a part file whose row is gone, so the rows cannot
	// be what tells the sweep where to look. A failure here costs the sweep
	// its record, not the upload.
	if terr := e.state.TouchUploadDir(ctx, int64(r.Share()), dest.Parent().String()); terr != nil {
		e.log.Warn("could not record the directory an upload writes into; "+
			"the sweep may miss an orphan there",
			"dir", dest.Parent().String(), "error", terr)
	}

	e.putHandle(id, f)
	rw := &row{sess: sess, set: NewIntervalSet()}
	if rw.cached() {
		if cerr := e.startMerger(rw); cerr != nil {
			return Session{}, cerr
		}
	}
	return e.session(rw)
}

// newRow builds the stored session from what Create was asked for.
func (e *Engine) newRow(
	id SessionID, r core.Resolved, dest vfs.SafePath, spec SessionSpec,
) (state.UploadSession, error) {
	minAtCreation, chunkSize := e.settings.Snapshot()
	floor, ferr := num.Narrow[int64](minAtCreation)
	size, serr := num.Narrow[int64](chunkSize)
	if ferr != nil || serr != nil {
		return state.UploadSession{}, fmt.Errorf("%w: a chunk setting does not fit", ErrBadRequest)
	}

	sess := state.UploadSession{
		ID:       id.Bytes(),
		User:     int64(r.User()),
		Share:    int64(r.Share()),
		Dest:     dest.String(),
		PartName: partName(id),
		Mode:     int64(spec.Mode),
		// The floor is snapshotted rather than read live on every chunk: an
		// administrator moving it mid-upload must not retroactively make a
		// chunk that was legal when it was sent illegal now.
		ChunkMinAtCreation: floor,
		ChunkSize:          size,
		RandomAccess:       spec.RandomAccess,
		NextName:           1,
		IfMatch:            spec.IfMatch,
		Filename:           spec.Meta.Filename,
		MtimeNs:            spec.Meta.MtimeNs,
		Mime:               spec.Meta.Mime,
		RelativePath:       spec.Meta.RelativePath,
		CreatedNs:          e.clk.Nanos(),
		ExpiresNs:          e.expiry(),
		State:              int64(StateReceiving),
	}
	if spec.Mode == SpoolNameOrdered {
		sess.SpoolDir = spoolDirName(id)
	}
	// The mode is fixed here and read from the row afterwards. A switch
	// flipped mid-upload must not change where a session in flight looks for
	// its bytes: they are in one place or the other and no setting moves them.
	//
	// Name-ordered sessions are excluded: they already spool out-of-order
	// chunks to files of their own, so the cache would be a second staging
	// layer under the first.
	if e.cacheEnabled() && spec.Mode == SpoolOffsetAddressed {
		sess.CacheDir = cacheDirName(id)
	}
	if spec.TotalLen != nil {
		n, nerr := num.Narrow[int64](*spec.TotalLen)
		if nerr != nil {
			return state.UploadSession{}, fmt.Errorf("%w: the declared length does not fit", ErrBadRequest)
		}
		sess.TotalLen = &n
	}
	if v := spec.Meta.Verify; v != nil {
		algo := int64(v.Algo)
		sess.Verify = &algo
		sess.VerifyDigest = v.Digest
	}
	return sess, nil
}

// createPart makes the part file and sizes it sparsely for a declared length.
func (e *Engine) createPart(root *vfs.ShareRoot, part vfs.SafePath, total *uint64) (*vfs.File, error) {
	f, err := root.CreatePart(part)
	if err != nil {
		return nil, mapVFSErr(err)
	}
	if total == nil {
		return f, nil
	}
	n, nerr := num.Narrow[int64](*total)
	if nerr != nil {
		return nil, errors.Join(
			fmt.Errorf("%w: the declared length does not fit", ErrBadRequest), f.Close())
	}
	if terr := f.Truncate(n); terr != nil {
		return nil, errors.Join(mapVFSErr(terr), f.Close())
	}
	return f, nil
}

// discardPart closes and removes a part file a failed creation left behind.
func (e *Engine) discardPart(root *vfs.ShareRoot, part vfs.SafePath, f *vfs.File) error {
	err := f.Close()
	if uerr := root.Unlink(part); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
		err = errors.Join(err, uerr)
	}
	return err
}

// Get reports one session, scoped to the account that owns it.
func (e *Engine) Get(ctx context.Context, id SessionID, user core.UserID) (Session, error) {
	r, err := e.load(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if oerr := requireOwner(r, user); oerr != nil {
		return Session{}, oerr
	}
	return e.session(r)
}

// Offset reports the resumable offset.
//
// A client that asks after a failed chunk gets the truth rather than the part
// file's size, which on a sparse file says where the last write landed and not
// what is in it.
func (e *Engine) Offset(ctx context.Context, id SessionID, user core.UserID) (uint64, error) {
	s, err := e.Get(ctx, id, user)
	if err != nil {
		return 0, err
	}
	return s.Offset, nil
}

// SetLength supplies a deferred length, which finalize requires and the
// interval set needs before it can report completeness.
func (e *Engine) SetLength(ctx context.Context, id SessionID, user core.UserID, total uint64) error {
	unlock := e.lockRow(id)
	defer unlock()

	r, err := e.load(ctx, id)
	if err != nil {
		return err
	}
	if oerr := requireOwner(r, user); oerr != nil {
		return oerr
	}
	if serr := e.requireReceiving(r); serr != nil {
		return serr
	}
	if have, declared := r.totalLen(); declared {
		if have != total {
			return fmt.Errorf("%w: this session already declared a length of %d", ErrBadRequest, have)
		}
		return nil
	}
	// Whatever has already landed has to fit inside the length being declared,
	// or the session would be complete over bytes past its own end.
	if received := r.set.Runs(); len(received) > 0 && received[len(received)-1].Hi > total {
		return fmt.Errorf("%w: %d bytes have already landed, past the declared length of %d",
			ErrBadRequest, received[len(received)-1].Hi, total)
	}
	n, nerr := num.Narrow[int64](total)
	if nerr != nil {
		return fmt.Errorf("%w: the declared length does not fit", ErrBadRequest)
	}
	r.sess.TotalLen = &n
	r.sess.ExpiresNs = e.expiry()
	return e.save(ctx, r)
}

// Abort terminates a session.
//
// The part file stays on disk for the sweep, which is what keeps a
// termination from racing a write already in flight. What does not stay is
// the bookkeeping lock: leaving it for the sweep meant an aborted session's
// mutex sat in the map for a day.
func (e *Engine) Abort(ctx context.Context, id SessionID, user core.UserID) error {
	// Outside the row lock, because stopping a merger means waiting for a step
	// that takes it.
	e.stopMerger(id)

	unlock := e.lockRow(id)
	r, err := e.load(ctx, id)
	if err != nil {
		unlock()
		return err
	}
	if oerr := requireOwner(r, user); oerr != nil {
		unlock()
		return oerr
	}
	if SessionState(r.sess.State) == StateDone {
		unlock()
		return ErrNotFound
	}
	r.sess.State = int64(StateAborted)
	// Immediately eligible for the sweep, which is what takes the part file.
	r.sess.ExpiresNs = e.clk.Nanos()
	if serr := e.save(ctx, r); serr != nil {
		unlock()
		return serr
	}
	// The cache goes now rather than at the sweep. Its contents can never be
	// finished, and the spool is the small volume: holding a cancelled
	// upload's window there for a day is what fills it.
	e.releaseCache(r.sess.CacheDir)
	e.closeHandle(id)
	unlock()
	e.forgetRow(id)
	return nil
}
