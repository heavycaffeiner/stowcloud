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
)

// Finalize verifies the upload and publishes it.
//
// The durable rename is the commit point, and everything else is arranged
// around that single fact. Failing before it leaves the session resumable with
// the part file still on disk. Once past it the upload counts as complete even
// if cleanup needs retrying, because a filesystem commit cannot be undone and
// presenting one as a resumable upload whose destination already exists is worse
// than carrying the debt.
func (e *Engine) Finalize(ctx context.Context, r core.Resolved, id SessionID) (core.Entry, error) {
	// The cache drains before the row lock is taken: draining needs that lock
	// itself, and the merger has to be stopped before the part file is synced
	// and closed under it.
	if err := e.drainCache(ctx, id); err != nil {
		return core.Entry{}, err
	}
	unlock := e.lockRow(id)
	defer unlock()
	return e.finalize(ctx, r, id)
}

// finalize is the shared path both spool modes converge on. The caller holds
// the row lock.
func (e *Engine) finalize(ctx context.Context, r core.Resolved, id SessionID) (core.Entry, error) {
	rw, err := e.load(ctx, id)
	if err != nil {
		return core.Entry{}, err
	}
	if oerr := requireOwner(rw, r.User()); oerr != nil {
		return core.Entry{}, oerr
	}
	if perr := r.Require(acl.Write | acl.Create); perr != nil {
		return core.Entry{}, perr
	}
	dest, err := rw.dest()
	if err != nil {
		return core.Entry{}, err
	}
	// Publication targets the destination the session was created against. A
	// resolution pointing elsewhere describes a different file, and honouring it
	// would publish through a permission check performed on another path. This
	// is verified before anything is modified.
	if !dest.Equal(r.Path()) {
		return core.Entry{}, fmt.Errorf("%w: this session publishes to %s",
			ErrBadRequest, rw.sess.Dest)
	}

	total, declared := rw.totalLen()
	if !declared {
		return core.Entry{}, fmt.Errorf("%w: this session never declared a length", ErrBadRequest)
	}
	if !rw.set.IsComplete(total) {
		// The refusal names what is missing, so the client resends the holes
		// rather than the file.
		return core.Entry{}, &IncompleteError{Missing: rw.set.Missing(total)}
	}

	// A session that is publishing is not receiving, and the sweep leaves a
	// finalizing session alone: a long assembly must not be collected halfway
	// through its own publish.
	if rw.sess.State != int64(StateFinalizing) {
		rw.sess.State = int64(StateFinalizing)
		if serr := e.save(ctx, rw); serr != nil {
			return core.Entry{}, serr
		}
	}

	part, err := e.partPathOf(rw)
	if err != nil {
		return core.Entry{}, err
	}
	root := r.Root()

	if v := rw.verify(); v != nil {
		f, herr := e.handleFor(root, id, part)
		if herr != nil {
			return core.Entry{}, herr
		}
		if verr := VerifyWholeFile(f, *v, total); verr != nil {
			// The session stays and the part file stays on disk. The client's
			// declared digest does not match what landed, and it is the client
			// that knows whether to resend a range or start again; discarding
			// its bytes here decides that for it.
			return core.Entry{}, verr
		}
	}

	entry, err := e.publish(ctx, r, rw, part, total)
	if err != nil {
		return core.Entry{}, err
	}

	// Everything from here is after the commit point, so none of it can fail
	// the upload. A surviving session row is cleanup debt the sweep collects.
	e.releaseCache(rw.sess.CacheDir)
	e.closeHandle(id)
	e.forgetRow(id)
	if derr := e.state.DeleteUploadSession(ctx, id.Bytes()); derr != nil {
		e.log.Warn("an upload published but its session row survived; the sweep will collect it",
			"session", id.String(), "error", derr)
	}
	return entry, nil
}

// publish durably moves the part file onto the destination.
//
// It routes through the core's publish path rather than renaming locally, so the
// mode and ownership transplant, the quota charge, the cache invalidation and
// the journal row all match what every other write in the product receives.
func (e *Engine) publish(
	ctx context.Context, r core.Resolved, rw *row, part vfs.SafePath, total uint64,
) (core.Entry, error) {
	root := r.Root()

	if err := e.checkIfMatch(root, r.Path(), rw.sess.IfMatch); err != nil {
		return core.Entry{}, err
	}
	// The descriptor is released before the rename, so nothing relies on the
	// semantics of renaming a file that remains open. This sync secures the
	// bytes, while the directory sync performed later by publish is what makes
	// them findable.
	if err := e.syncAndClose(root, rw, part); err != nil {
		return core.Entry{}, err
	}

	if rw.sess.MtimeNs != nil {
		if err := root.SetTimes(part, *rw.sess.MtimeNs); err != nil {
			// A timestamp the client requested and did not receive merits a log
			// line rather than a failed upload, since the bytes are correct
			// regardless.
			e.log.Warn("could not apply the client's modification time to an upload",
				"dest", rw.sess.Dest, "error", err)
		}
	}
	return e.core.PublishPart(ctx, r, part, total, rw.sess.IfMatch)
}

// syncAndClose flushes the uploaded bytes to durable storage and releases the
// descriptor.
func (e *Engine) syncAndClose(root vfs.Root, rw *row, part vfs.SafePath) error {
	id, err := rw.id()
	if err != nil {
		return err
	}
	f, err := e.handleFor(root, id, part)
	if err != nil {
		return err
	}
	if serr := f.SyncData(); serr != nil {
		return mapVFSErr(serr)
	}
	e.closeHandle(id)
	return nil
}

// checkIfMatch evaluates the client's validator against the destination's
// current state rather than its state when the session opened.
//
// A supplied validator can never succeed, which follows from the file-token rule
// instead of being a shortcut taken here: the token derives from metadata and is
// always weak, and a weak validator cannot satisfy a strong comparison. Getting
// past it requires an explicit unconditional retry.
func (e *Engine) checkIfMatch(root vfs.Root, dest vfs.SafePath, ifMatch string) error {
	if ifMatch == "" {
		return nil
	}
	st, err := root.Stat(dest)
	if errors.Is(err, vfs.ErrNotFound) {
		return fmt.Errorf("%w: nothing is at the destination", core.ErrPrecondition)
	}
	if err != nil {
		return mapVFSErr(err)
	}
	current, _ := core.FileETag(st)
	return fmt.Errorf("%w: the destination's current token is %s", core.ErrPrecondition, current)
}

// Assemble finalizes the name-ordered path by merging whatever chunks remain
// spooled in ascending name order, then publishing through the same route every
// other session uses.
//
// total carries whatever the caller's protocol declared. The terminology stays
// protocol-neutral because this package cannot tell which protocol created the
// session; only the layer that parsed the header can name it.
func (e *Engine) Assemble(
	ctx context.Context, r core.Resolved, id SessionID, total uint64, mtimeNs *int64,
) (core.Entry, error) {
	unlock := e.lockRow(id)
	defer unlock()

	rw, err := e.load(ctx, id)
	if err != nil {
		return core.Entry{}, err
	}
	if oerr := requireOwner(rw, r.User()); oerr != nil {
		return core.Entry{}, oerr
	}
	if rw.mode() != SpoolNameOrdered {
		return core.Entry{}, fmt.Errorf("%w: this session is offset-addressed", ErrBadRequest)
	}

	if derr := e.drainSpool(ctx, r.Root(), rw, true); derr != nil {
		return core.Entry{}, derr
	}

	head, herr := num.Narrow[uint64](rw.sess.WriteHead)
	if herr != nil {
		return core.Entry{}, herr
	}
	if total > 0 && head != total {
		return core.Entry{}, fmt.Errorf("%w: %d bytes assembled against a declared total of %d",
			ErrBadRequest, head, total)
	}
	if total == 0 {
		total = head
	}

	// Assembly produces a contiguous file by construction, so the set reduces to
	// the single range covering it. Writing that now is what gives finalize's
	// completeness check identical meaning across both modes.
	declared, nerr := num.Narrow[int64](total)
	if nerr != nil {
		return core.Entry{}, nerr
	}
	rw.sess.TotalLen = &declared
	if mtimeNs != nil {
		rw.sess.MtimeNs = mtimeNs
	}
	rw.set = FullIntervalSet(total)
	if serr := e.save(ctx, rw); serr != nil {
		return core.Entry{}, serr
	}
	if cerr := e.commitRange(ctx, rw); cerr != nil {
		return core.Entry{}, cerr
	}

	entry, err := e.finalize(ctx, r, id)
	if err != nil {
		return core.Entry{}, err
	}
	// The spool directory is empty at this point and removing it is best effort:
	// an orphan beneath the reserved prefix stays unlistable and the sweep
	// collects it.
	if dir, derr := e.spoolDirOf(rw); derr == nil {
		if rerr := r.Root().Rmdir(dir); rerr != nil && !errors.Is(rerr, vfs.ErrNotFound) {
			e.log.Warn("an upload spool directory survived assembly; the sweep will collect it",
				"session", id.String(), "error", rerr)
		}
	}
	return entry, nil
}
