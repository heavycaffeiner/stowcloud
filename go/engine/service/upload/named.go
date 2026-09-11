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
	ctx context.Context, root vfs.Root, id SessionID, user core.UserID,
	name uint32, body io.Reader, sum *Checksum,
) error {
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
	if serr := e.requireReceiving(r); serr != nil {
		unlock()
		return serr
	}
	if r.mode() != SpoolNameOrdered {
		unlock()
		return fmt.Errorf("%w: this session is offset-addressed", ErrBadRequest)
	}
	if name == 0 {
		unlock()
		return fmt.Errorf("%w: a chunk name starts at one", ErrBadRequest)
	}

	next, nerr := num.Narrow[uint32](r.sess.NextName)
	if nerr != nil {
		unlock()
		return nerr
	}
	isNext := name == next
	if !isNext && !slices.Contains(r.sess.SpooledNames, name) &&
		len(r.sess.SpooledNames) >= limits.UploadSpooledNames {
		unlock()
		return &ExhaustedError{Limit: "out-of-order chunks held for this session"}
	}
	lease, admitted := e.admitWriter(id)
	if !admitted {
		unlock()
		return ErrSessionState
	}

	part, err := e.partPathOf(r)
	if err != nil {
		lease.release()
		unlock()
		return err
	}
	logical := uint64(0)
	stageDir := part.Parent()
	if isNext {
		logical, err = num.Narrow[uint64](r.sess.WriteHead)
		if err != nil {
			lease.release()
			unlock()
			return err
		}
	} else {
		stageDir, err = e.spoolDirOf(r)
		if err != nil {
			lease.release()
			unlock()
			return err
		}
		if merr := root.Mkdir(stageDir); merr != nil && !errors.Is(merr, vfs.ErrExists) {
			lease.release()
			unlock()
			return mapVFSErr(merr)
		}
	}
	declared := false
	if _, declared = r.totalLen(); !declared {
		// Deferred-length named sessions still need a per-member bound. The
		// captured session chunk size is the protocol's advertised maximum,
		// and the aggregate bound below caps a run of held members.
		maxChunk, merr := num.Narrow[uint64](r.sess.ChunkSize)
		if merr != nil || maxChunk == 0 {
			maxChunk = limits.UploadChunkSizeDefault
		}
		if maxChunk < uint64(1<<63-1) {
			body = io.LimitReader(body, int64(maxChunk)+1)
		}
	}
	unlock()
	defer lease.release()

	stage, n, _, werr := e.stageBody(root, stageDir, logical, body, r, sum)
	if werr != nil {
		return werr
	}
	cleanup := func() {
		if uerr := root.Unlink(stage); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("a named upload staging file could not be removed", "error", uerr)
		}
	}
	defer cleanup()

	if !declared {
		maxChunk, merr := num.Narrow[uint64](r.sess.ChunkSize)
		if merr != nil || maxChunk == 0 {
			maxChunk = limits.UploadChunkSizeDefault
		}
		if n > maxChunk {
			return fmt.Errorf("%w: named chunk %d has %d bytes, maximum is %d",
				ErrTooLarge, name, n, maxChunk)
		}
	}
	if !lease.valid() {
		return ErrSessionState
	}

	relock := e.lockRow(id)
	defer relock()
	fresh, err := e.load(ctx, id)
	if err != nil {
		return err
	}
	if !lease.valid() {
		return ErrSessionState
	}
	if serr := e.requireReceiving(fresh); serr != nil {
		return serr
	}
	if fresh.mode() != SpoolNameOrdered {
		return fmt.Errorf("%w: this session is offset-addressed", ErrBadRequest)
	}
	currentNext, nerr := num.Narrow[uint32](fresh.sess.NextName)
	if nerr != nil {
		return nerr
	}
	if name < currentNext {
		// A response may have been lost after this name was assembled. The
		// only safe idempotent result is a no-op: never overwrite an accepted
		// range with a retry whose original bytes are no longer staged.
		return nil
	}
	if name == currentNext {
		head, herr := num.Narrow[uint64](fresh.sess.WriteHead)
		if herr != nil {
			return herr
		}
		if total, ok := fresh.totalLen(); ok {
			if head > total || n > total-head {
				return fmt.Errorf("%w: named chunk %d exceeds the declared length of %d",
					ErrTooLarge, name, total)
			}
		}
		if err := e.commitStagedAt(root, id, part, stage, head, n); err != nil {
			return err
		}
		advanced, aerr := num.Narrow[int64](n)
		if aerr != nil {
			return aerr
		}
		fresh.sess.WriteHead += advanced
		fresh.sess.NextName++
		if err := e.save(ctx, fresh); err != nil {
			return err
		}
		if err := e.drainSpool(ctx, root, fresh, false); err != nil {
			return err
		}
		return nil
	}

	held, herr := e.namedHeldBytes(root, fresh, name)
	if herr != nil {
		return herr
	}
	if n > ^uint64(0)-held {
		return fmt.Errorf("%w: named chunks overflow the session byte bound", ErrTooLarge)
	}
	if total, ok := fresh.totalLen(); ok {
		if held+n > total {
			return fmt.Errorf("%w: named chunks total %d exceeds the declared length of %d",
				ErrTooLarge, held+n, total)
		}
	} else if held+n > uint64(limits.UploadReservedBytesPerUser) {
		return &ExhaustedError{Limit: "unassembled named upload bytes"}
	}

	dir, derr := e.spoolDirOf(fresh)
	if derr != nil {
		return derr
	}
	file, ferr := dir.JoinControl(chunkFileName(name))
	if ferr != nil {
		return ferr
	}
	if rerr := root.Rename(stage, file, false); rerr != nil {
		return mapVFSErr(rerr)
	}
	if !slices.Contains(fresh.sess.SpooledNames, name) {
		fresh.sess.SpooledNames = append(fresh.sess.SpooledNames, name)
	}
	fresh.sess.ExpiresNs = e.expiry()
	return e.save(ctx, fresh)
}

// namedHeldBytes is the durable byte count that a name-ordered session would
// assemble. Existing data for the replaced name is excluded so an idempotent
// retry is charged by its replacement size, not twice.
func (e *Engine) namedHeldBytes(
	root vfs.Root, r *row, replacing uint32,
) (uint64, error) {
	head, err := num.Narrow[uint64](r.sess.WriteHead)
	if err != nil {
		return 0, err
	}
	held := head
	for _, name := range r.sess.SpooledNames {
		if name == replacing {
			continue
		}
		size, serr := e.spooledChunkSize(root, r, name)
		if serr != nil {
			return 0, serr
		}
		if size > ^uint64(0)-held {
			return 0, fmt.Errorf("%w: named chunks overflow the session byte bound", ErrTooLarge)
		}
		held += size
	}
	return held, nil
}

// drainSpool merges spooled chunks into the part file in ascending name order,
// which is precisely what assembling by name means.
//
// strict determines how a gap is treated. During an upload a missing name is
// unremarkable: the chunk remains in flight and draining simply halts. At
// assembly time it becomes a rejection, because nothing further is coming.
func (e *Engine) drainSpool(ctx context.Context, root vfs.Root, r *row, strict bool) error {
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
func (e *Engine) mergeChunk(root vfs.Root, r *row, name uint32) error {
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
	if total, ok := r.totalLen(); ok {
		if head > total || st.Size > total-head {
			return fmt.Errorf("%w: assembling chunk %d would exceed the declared length of %d",
				ErrTooLarge, name, total)
		}
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
		// The chunk already sits in the part file, so a failed removal leaves
		// an unlistable orphan for the sweep instead of a failed assembly.
		e.log.Warn("a merged upload chunk could not be removed; the sweep will collect it",
			"error", uerr)
	}
	return nil
}

// Chunk is one member of a name-ordered session, as a listing reports it.
type Chunk struct {
	// Name is the member's number.
	Name uint32
	// Size is how many bytes that member contributed.
	Size uint64
}

// ListChunks returns the members a name-ordered session holds, covering both
// what is already assembled and what remains spooled.
//
// The assembled prefix comes back as a single member carrying the whole of it,
// named for the last chunk merged. Merging copies a chunk onto the end of the
// part file and unlinks it, so the boundaries inside that run no longer exist
// to report; what does survive is the write head, and that is the figure a
// resuming client needs. It sums the members it is shown to decide where to
// start and takes the highest name to decide what to call the next chunk, so
// both answers are right while no member's size is invented.
func (e *Engine) ListChunks(
	ctx context.Context, root vfs.Root, id SessionID, user core.UserID,
) ([]Chunk, error) {
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
	head, herr := num.Narrow[uint64](r.sess.WriteHead)
	if herr != nil {
		return nil, herr
	}

	out := make([]Chunk, 0, len(r.sess.SpooledNames)+1)
	if next > 1 {
		out = append(out, Chunk{Name: next - 1, Size: head})
	}
	for _, n := range r.sess.SpooledNames {
		if next > 1 && n == next-1 {
			// Already covered by the assembled run above; counting it twice
			// would have the client resume past bytes it never sent.
			continue
		}
		size, serr := e.spooledChunkSize(root, r, n)
		if serr != nil {
			return nil, serr
		}
		out = append(out, Chunk{Name: n, Size: size})
	}
	slices.SortFunc(out, func(a, b Chunk) int { return int(a.Name) - int(b.Name) })
	return out, nil
}

// spooledChunkSize measures one out-of-order member still awaiting its
// predecessor. A chunk whose file has gone answers zero rather than failing
// the listing: the client resends that name, which is the recovery either way.
func (e *Engine) spooledChunkSize(root vfs.Root, r *row, name uint32) (uint64, error) {
	dir, err := e.spoolDirOf(r)
	if err != nil {
		return 0, err
	}
	file, err := dir.JoinControl(chunkFileName(name))
	if err != nil {
		return 0, err
	}
	st, serr := root.Stat(file)
	if serr != nil {
		return 0, nil
	}
	return st.Size, nil
}
