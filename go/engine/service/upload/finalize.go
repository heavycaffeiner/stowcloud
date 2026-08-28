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

// Finalize verifies and publishes.
//
// The durable rename is the commit point and everything is arranged around
// that one fact. A failure before it leaves the session resumable and the
// part file on disk. After it the upload is complete even if cleanup has to
// be retried, because a filesystem commit cannot be rolled back and reporting
// one as a resumable upload whose destination already exists is worse than
// the debt.
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
	// The destination the session was created against is the one it publishes
	// to. A resolution naming somewhere else is a different file, and
	// honouring it would publish through a permission check that looked at
	// another path. This is checked before anything is touched.
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

// publish moves the part file onto the destination durably.
//
// It goes through the core's own publish path rather than renaming here, so
// the mode and ownership transplant, the quota charge, the cache
// invalidation and the journal row are the same ones every other write in the
// product gets.
func (e *Engine) publish(
	ctx context.Context, r core.Resolved, rw *row, part vfs.SafePath, total uint64,
) (core.Entry, error) {
	root := r.Root()

	if err := e.checkIfMatch(root, r.Path(), rw.sess.IfMatch); err != nil {
		return core.Entry{}, err
	}
	// The descriptor goes before the rename, so nothing depends on the
	// semantics of renaming a file that is still open. The sync here
	// guarantees the bytes; the directory sync the publish does afterwards is
	// what guarantees anyone can find them.
	if err := e.syncAndClose(root, rw, part); err != nil {
		return core.Entry{}, err
	}

	if rw.sess.MtimeNs != nil {
		if err := root.SetTimes(part, *rw.sess.MtimeNs); err != nil {
			// A timestamp the client asked for and did not get is worth a line
			// and not a failed upload: the bytes are right either way.
			e.log.Warn("could not apply the client's modification time to an upload",
				"dest", rw.sess.Dest, "error", err)
		}
	}
	return e.core.PublishPart(ctx, r, part, total)
}

// syncAndClose makes the uploaded bytes durable and gives up the descriptor.
func (e *Engine) syncAndClose(root *vfs.ShareRoot, rw *row, part vfs.SafePath) error {
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

// checkIfMatch applies the client's validator to the destination as it
// stands, not to what was there when the session opened.
//
// A supplied validator can never pass, and that is the file-token rule rather
// than a shortcut here: the token is derived from metadata and is always weak,
// and a weak validator cannot satisfy a strong comparison. An explicit
// unconditional retry is the way past.
func (e *Engine) checkIfMatch(root *vfs.ShareRoot, dest vfs.SafePath, ifMatch string) error {
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

// Assemble is the name-ordered path's finalize: it merges whatever chunks are
// still spooled, in ascending name order, then publishes through the same
// path every other session takes.
//
// total is what the caller's own protocol declared. The wording stays
// protocol-neutral because this package does not know which protocol created
// the session, and the layer that read the header is the one that can name
// it.
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
	if head != total {
		return core.Entry{}, fmt.Errorf("%w: %d bytes assembled against a declared total of %d",
			ErrBadRequest, head, total)
	}

	// The assembled file is contiguous by construction, so the set is the one
	// range covering it. Writing it now is what makes the completeness check
	// in finalize mean the same thing for both modes.
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
	// The spool directory is empty by now and its removal is best effort: an
	// orphan under the reserved prefix is unlistable and the sweep takes it.
	if dir, derr := e.spoolDirOf(rw); derr == nil {
		if rerr := r.Root().Rmdir(dir); rerr != nil && !errors.Is(rerr, vfs.ErrNotFound) {
			e.log.Warn("an upload spool directory survived assembly; the sweep will collect it",
				"session", id.String(), "error", rerr)
		}
	}
	return entry, nil
}
