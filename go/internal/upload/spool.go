//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The name-ordered spool mode. The difference from the offset-addressed one is
// the ordering rule and nothing else: a chunk here carries a name rather than
// an offset, so where it lands is decided by what has already been assembled
// rather than by what the client said.
//
// The mode is named for what it does rather than for the client that needs it.
// This package does not know which protocol created a session, and that is the
// isolation principle holding at the place it would be easiest to break.

// PutNamed writes one named chunk.
//
// A chunk whose name is the one expected next is appended straight to the part
// file and the write head advances. Anything else is spooled to a file of its
// own and waits, and each append drains whatever successors have arrived in
// the meantime.
func (e *Engine) PutNamed(
	ctx context.Context, root *vfs.ShareRoot, id SessionID, user core.UserID,
	name uint32, body io.Reader,
) error {
	unlock := e.lockRow(id)
	defer unlock()

	r, err := e.load(ctx, id)
	if err != nil {
		return err
	}
	if err := requireOwner(r, user); err != nil {
		return err
	}
	if err := e.requireReceiving(r); err != nil {
		return err
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
		// The fast path: this is the chunk the assembly is waiting for, so it
		// goes straight onto the end of the part file and nothing is spooled.
		if err := e.appendToPart(root, r, id, body); err != nil {
			return err
		}
		r.sess.NextName++
		if err := e.drainSpool(ctx, root, r, false); err != nil {
			return err
		}
	} else {
		if err := e.spoolChunk(root, r, name, body); err != nil {
			return err
		}
		if !slices.Contains(r.sess.SpooledNames, name) {
			if len(r.sess.SpooledNames) >= limits.UploadSpooledNames {
				return &ExhaustedError{Limit: "out-of-order chunks held for this session"}
			}
			r.sess.SpooledNames = append(r.sess.SpooledNames, name)
		}
	}

	// The row is written after every byte is on disk, which is the same
	// ordering rule the offset-addressed path keeps: a crash between the two
	// under-reports what arrived and the client resends it.
	r.sess.ExpiresNs = e.expiry()
	return e.save(ctx, r)
}

// appendToPart writes a chunk body at the current write head and advances it.
func (e *Engine) appendToPart(root *vfs.ShareRoot, r *row, id SessionID, body io.Reader) error {
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
	n, _, werr := e.writeBody(f, head, body, r, nil)
	if werr != nil {
		return werr
	}
	written, nerr := num.Narrow[int64](n)
	if nerr != nil {
		return nerr
	}
	r.sess.WriteHead += written
	return nil
}

// spoolChunk writes an out-of-order chunk to a file of its own inside the
// session's spool directory.
//
// A repeated name is a client retry and overwrites idempotently: the chunk is
// the same bytes and refusing it would strand a client that lost the response
// rather than the request.
func (e *Engine) spoolChunk(root *vfs.ShareRoot, r *row, name uint32, body io.Reader) error {
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
		// The client re-sent a chunk name it already sent, which is a retry
		// after a lost response. The old file is removed and made again rather
		// than reopened for writing: reopening would be a second writable
		// descriptor on a read path, and the part-file handle is the only one
		// of those in the tree. Unlinking first also means a partial write
		// from the abandoned attempt cannot survive underneath a shorter one.
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

	// The floor does not apply to a spooled chunk: it is measured against the
	// assembled file, and a name-ordered client does not choose its own
	// offsets to be measured at.
	if _, _, werr := e.writeBody(f, 0, body, nil, nil); werr != nil {
		return werr
	}
	return f.SyncData()
}

// drainSpool merges spooled chunks onto the part file in ascending name order,
// which is what "assembled in the order of their names" means.
//
// strict decides what a gap is. Mid-upload a missing name is ordinary: the
// chunk is still in flight and draining simply stops. At assembly it is a
// refusal, because there is nothing left to wait for.
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
		if err := e.mergeChunk(root, r, next); err != nil {
			return err
		}
		r.sess.NextName++
		r.sess.SpooledNames = slices.DeleteFunc(r.sess.SpooledNames,
			func(n uint32) bool { return n == next })
		if err := e.save(ctx, r); err != nil {
			return err
		}
	}
}

// mergeChunk copies one spooled chunk onto the end of the part file with
// copy_file_range and removes it.
//
// Never a userspace buffer, so a 50 GiB file does not pass through this
// process's heap. The length comes from a stat rather than an oversized
// sentinel: some kernels reject an implausible length with EINVAL, which is
// not one of the fall-back errnos and correctly so, because an EINVAL here
// would be a real bug rather than a portability gap.
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
		// The chunk is already in the part file, so a removal that failed is
		// an unlistable orphan for the sweep rather than a failed assembly.
		e.log.Warn("a merged upload chunk could not be removed; the sweep will collect it",
			"error", uerr)
	}
	return nil
}

// ListChunks reports the chunk names a name-ordered session holds: everything
// already assembled, plus everything still spooled.
func (e *Engine) ListChunks(ctx context.Context, id SessionID, user core.UserID) ([]uint32, error) {
	r, err := e.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := requireOwner(r, user); err != nil {
		return nil, err
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
