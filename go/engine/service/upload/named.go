//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The name-ordered spool mode. The difference from the offset-addressed one
// is the ordering rule and nothing else: a chunk here carries a name rather
// than an offset, so where it lands is decided by what has already been
// assembled rather than by what the client said.

// PutNamed writes a single named chunk.
//
// When a chunk's name matches the one expected next it is appended directly to
// the part file and the write head moves forward. Everything else goes to its
// own spooled file to wait, and each append drains whichever successors arrived
// in the interim.
func (e *Engine) PutNamed(
	ctx context.Context, root *vfs.ShareRoot, id SessionID, user core.UserID,
	name uint32, body io.Reader, sum *Checksum,
) error {
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
	if r.mode() != SpoolNameOrdered {
		return fmt.Errorf("%w: this session is offset-addressed", ErrBadRequest)
	}
	if name == 0 {
		return fmt.Errorf("%w: a chunk name starts at one", ErrBadRequest)
	}

	next, nerr := num.Narrow[uint32](r.sess.NextName)
	if nerr != nil {
		return nerr
	}

	if name == next {
		// The chunk the assembly is waiting for goes straight onto the end of
		// the part file and nothing is spooled.
		if aerr := e.appendToPart(root, r, id, body, sum); aerr != nil {
			return aerr
		}
		r.sess.NextName++
		if derr := e.drainSpool(ctx, root, r, false); derr != nil {
			return derr
		}
	} else {
		if serr := e.spoolChunk(root, r, name, body, sum); serr != nil {
			return serr
		}
		if !slices.Contains(r.sess.SpooledNames, name) {
			if len(r.sess.SpooledNames) >= limits.UploadSpooledNames {
				return &ExhaustedError{Limit: "out-of-order chunks held for this session"}
			}
			r.sess.SpooledNames = append(r.sess.SpooledNames, name)
		}
	}

	// The row is written only once every byte reaches disk, matching the
	// offset-addressed path's ordering rule. A crash in between under-states
	// what arrived, and the client sends it again.
	r.sess.ExpiresNs = e.expiry()
	return e.save(ctx, r)
}

// appendToPart writes a chunk body at the current write head and moves it
// forward.
func (e *Engine) appendToPart(
	root *vfs.ShareRoot, r *row, id SessionID, body io.Reader, sum *Checksum,
) error {
	part, err := e.partPathOf(r)
	if err != nil {
		return err
	}
	f, err := e.handleFor(root, id, part)
	if err != nil {
		return err
	}
	head, herr := num.Narrow[uint64](r.sess.WriteHead)
	if herr != nil {
		return herr
	}
	n, digest, werr := e.writeBody(f, head, body, r, sum)
	if werr != nil {
		return werr
	}
	// The write head does not move over a chunk whose digest does not match, so
	// the client resends the same name rather than the next one. The bytes are
	// already on disk past the head and the resend overwrites them.
	if sum != nil && digest != nil && !constantTimeEqual(digest, sum.Digest) {
		return fmt.Errorf("%w: the %s digest does not match the %d bytes received",
			ErrChecksum, sum.Algo, n)
	}
	written, nerr := num.Narrow[int64](n)
	if nerr != nil {
		return nerr
	}
	r.sess.WriteHead += written
	return nil
}

// spoolChunk writes an out-of-order chunk into its own file within the session's
// spool directory.
//
// A repeated name indicates a client retry and overwrites idempotently, since
// the chunk carries identical bytes. Rejecting it would strand a client that
// lost the response rather than the request.
func (e *Engine) spoolChunk(
	root *vfs.ShareRoot, r *row, name uint32, body io.Reader, sum *Checksum,
) error {
	dir, err := e.spoolDirOf(r)
	if err != nil {
		return err
	}
	if merr := root.Mkdir(dir); merr != nil && !errors.Is(merr, vfs.ErrExists) {
		return mapVFSErr(merr)
	}
	file, err := dir.JoinControl(chunkFileName(name))
	if err != nil {
		return err
	}

	f, err := root.CreatePart(file)
	if errors.Is(err, vfs.ErrExists) {
		// The client resent a name it had already sent, a retry following a lost
		// response. The previous file is deleted and recreated rather than
		// reopened for writing, because reopening would introduce a second
		// writable descriptor on a read path and the part-file handle is the
		// only such descriptor in the tree. Unlinking first also ensures a
		// partial write from the abandoned attempt cannot persist beneath a
		// shorter one.
		if uerr := root.Unlink(file); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			return mapVFSErr(uerr)
		}
		f, err = root.CreatePart(file)
	}
	if err != nil {
		return mapVFSErr(err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			e.log.Warn("closing a spooled upload chunk failed", "error", cerr)
		}
	}()

	// The floor does not govern a spooled chunk, since it is measured against the
	// assembled file and a name-ordered client does not select the offsets it
	// would be measured at.
	n, digest, werr := e.writeBody(f, 0, body, nil, sum)
	if werr != nil {
		return werr
	}
	// A chunk that fails its digest is not recorded as spooled, so the name
	// stays absent from the session and the client sends it again. The file it
	// wrote is left for the resend to overwrite or the sweep to remove.
	if sum != nil && digest != nil && !constantTimeEqual(digest, sum.Digest) {
		return fmt.Errorf("%w: the %s digest does not match the %d bytes received",
			ErrChecksum, sum.Algo, n)
	}
	return f.SyncData()
}

// drainSpool merges spooled chunks into the part file in ascending name order,
// which is precisely what assembling by name means.
//
// strict determines how a gap is treated. During an upload a missing name is
// unremarkable: the chunk remains in flight and draining simply halts. At
// assembly time it becomes a rejection, because nothing further is coming.
func (e *Engine) drainSpool(ctx context.Context, root *vfs.ShareRoot, r *row, strict bool) error {
	for {
		if len(r.sess.SpooledNames) == 0 {
			return nil
		}
		next, nerr := num.Narrow[uint32](r.sess.NextName)
		if nerr != nil {
			return nerr
		}
		if !slices.Contains(r.sess.SpooledNames, next) {
			if !strict {
				return nil
			}
			smallest := slices.Min(r.sess.SpooledNames)
			return fmt.Errorf("%w: chunk %d is missing and %d is the next one held",
				ErrIncomplete, next, smallest)
		}
		if merr := e.mergeChunk(root, r, next); merr != nil {
			return merr
		}
		r.sess.NextName++
		r.sess.SpooledNames = slices.DeleteFunc(r.sess.SpooledNames,
			func(n uint32) bool { return n == next })
		// Saved per chunk rather than once at the end, so assembly is
		// idempotent over a crash: a re-run starts from the cursor the row
		// keeps and a chunk already copied is not copied twice.
		if serr := e.save(ctx, r); serr != nil {
			return serr
		}
	}
}

// mergeChunk copies one spooled chunk onto the end of the part file and
// removes it.
//
// The copy is a kernel-side range copy and never a userspace buffer, so a
// large file does not pass through this process's heap. The length comes from
// a stat rather than an oversized sentinel: some kernels refuse an
// implausible length, and that refusal would be a real bug rather than a
// portability gap.
func (e *Engine) mergeChunk(root *vfs.ShareRoot, r *row, name uint32) error {
	id, err := r.id()
	if err != nil {
		return err
	}
	dir, err := e.spoolDirOf(r)
	if err != nil {
		return err
	}
	file, err := dir.JoinControl(chunkFileName(name))
	if err != nil {
		return err
	}
	part, err := e.partPathOf(r)
	if err != nil {
		return err
	}

	src, err := root.OpenRead(file, vfs.IntentRead)
	if err != nil {
		return mapVFSErr(err)
	}
	defer func() {
		if cerr := src.Close(); cerr != nil {
			e.log.Warn("closing a spooled upload chunk failed", "error", cerr)
		}
	}()

	st, serr := src.Stat()
	if serr != nil {
		return mapVFSErr(serr)
	}
	dst, err := e.handleFor(root, id, part)
	if err != nil {
		return err
	}
	head, herr := num.Narrow[uint64](r.sess.WriteHead)
	if herr != nil {
		return herr
	}
	copied, cerr := vfs.CopyRange(src, 0, dst, head, st.Size)
	if cerr != nil {
		return mapVFSErr(cerr)
	}
	if copied != st.Size {
		return fmt.Errorf("assembling chunk %d: copied %d of %d bytes", name, copied, st.Size)
	}
	written, nerr := num.Narrow[int64](copied)
	if nerr != nil {
		return nerr
	}
	r.sess.WriteHead += written

	if uerr := root.Unlink(file); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
		// The chunk already sits in the part file, so a failed removal leaves an
		// unlistable orphan for the sweep instead of a failed assembly.
		e.log.Warn("a merged upload chunk could not be removed; the sweep will collect it",
			"error", uerr)
	}
	return nil
}

// ListChunks returns the chunk names a name-ordered session holds, covering both
// what is already assembled and what remains spooled.
func (e *Engine) ListChunks(ctx context.Context, id SessionID, user core.UserID) ([]uint32, error) {
	r, err := e.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if oerr := requireOwner(r, user); oerr != nil {
		return nil, oerr
	}
	if r.mode() != SpoolNameOrdered {
		return nil, nil
	}
	next, nerr := num.Narrow[uint32](r.sess.NextName)
	if nerr != nil {
		return nil, nerr
	}
	out := make([]uint32, 0, int(next)+len(r.sess.SpooledNames))
	for n := uint32(1); n < next; n++ {
		out = append(out, n)
	}
	out = append(out, r.sess.SpooledNames...)
	slices.Sort(out)
	return slices.Compact(out), nil
}
