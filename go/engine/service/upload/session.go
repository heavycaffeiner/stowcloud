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

	// The part file is created and sized immediately via an exclusive create
	// and a sparse truncate, so nothing is copied and the destination directory
	// carries exactly one unlistable entry throughout the session's life.
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
		// The floor is captured once rather than read per chunk, so an
		// administrator changing it mid-upload cannot retroactively invalidate
		// a chunk that was legal when sent.
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

// createPart creates the part file and sparsely sizes it to a declared length.
func (e *Engine) createPart(root vfs.Root, part vfs.SafePath, total *uint64) (*vfs.File, error) {
	f, err := root.CreatePart(part)
	if err != nil {
		// A not-found here is the destination's directory, not a session: this
		// runs before any session exists. The general mapper turns it into
		// ErrNotFound, which reads as "no such upload session" and names the
		// wrong thing entirely.
		if errors.Is(err, vfs.ErrNotFound) {
			return nil, ErrDestMissing
		}
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

// discardPart closes and deletes a part file left over from a failed creation.
func (e *Engine) discardPart(root vfs.Root, part vfs.SafePath, f *vfs.File) error {
	err := f.Close()
	if uerr := root.Unlink(part); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
		err = errors.Join(err, uerr)
	}
	return err
}

// Get returns a single session, restricted to its owning account.
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

// SetLength provides a deferred length, required by finalize and needed by the
// interval set before it can report completeness.
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
	// Everything already written must fit within the length being declared, or
	// the session would count as complete over bytes beyond its own end.
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
	// Performed outside the row lock, since stopping a merger means waiting on a
	// step that acquires it.
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
	// Eligible for the sweep at once, and the sweep is what claims the part
	// file.
	r.sess.ExpiresNs = e.clk.Nanos()
	if serr := e.save(ctx, r); serr != nil {
		unlock()
		return serr
	}
	// The cache is released now rather than at the sweep. Its contents can never
	// be completed, and the spool is the small volume, so retaining a cancelled
	// upload's window there for a day is exactly what fills it.
	e.releaseCache(r.sess.CacheDir)
	e.closeHandle(id)
	unlock()
	e.forgetRow(id)
	return nil
}
