//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// The merger: one goroutine per cached session in flight, draining contiguous
// cached data into the part file.

// cacheRetryAfter is what a refused chunk is told to wait. It is short
// because the thing it waits for is a disk write already in progress.
const cacheRetryAfter = 5 * time.Second

// cacheWaitMax limits how long a chunk waits on the merger. Hitting it means the
// merge is neither failing visibly nor progressing, which points at the
// destination rather than a full cache. The request is answered instead of being
// held open indefinitely.
const cacheWaitMax = 30 * time.Second

// mergeCopyMax caps a single merge step, so a larger chunk drains over several
// and the loop can observe cancellation between them. An unbounded step would be
// one copy a shutdown must sit through.
const mergeCopyMax = 64 << 20

// merger holds a session's drain: the goroutine copying contiguous cached data
// into the part file, plus the two channels writers use to reach it.
type merger struct {
	wake chan struct{}
	stop context.CancelFunc
	done chan struct{}

	// progress is closed and recreated at the end of each round, a round being
	// either one merge step or the point where the merger exhausts its work. A
	// writer waiting for room reads this channel before consulting the budget,
	// so a round completing in between wakes it instead of going unnoticed. That
	// ordering is what makes the protocol correct.
	mu       sync.Mutex
	progress chan struct{}
}

func newMerger(stop context.CancelFunc) *merger {
	return &merger{
		wake:     make(chan struct{}, 1),
		stop:     stop,
		done:     make(chan struct{}),
		progress: make(chan struct{}),
	}
}

// nudge prompts the merger to re-examine its work. It never blocks, since the
// channel holds a single token and one pending wake conveys as much as five.
func (m *merger) nudge() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// endRound signals a completed round, waking everything waiting for room.
func (m *merger) endRound() {
	m.mu.Lock()
	old := m.progress
	m.progress = make(chan struct{})
	m.mu.Unlock()
	close(old)
}

// watch returns the channel to await the next round on. Reading it before
// checking the budget is what eliminates the race.
func (m *merger) watch() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress
}

// wait blocks until this merger has left.
func (m *merger) wait() {
	m.stop()
	<-m.done
}

// startMerger yields the session's merger, launching it when not already
// running.
//
// It accepts a row rather than an id because an uncached session requires no
// merger whatsoever, and re-querying the store to discover that would cost a
// read per chunk.
func (e *Engine) startMerger(r *row) error {
	id, err := r.id()
	if err != nil {
		return err
	}
	if !r.cached() || e.cache == nil {
		return nil
	}
	e.mergerFor(id)
	return nil
}

func (e *Engine) mergerFor(id SessionID) *merger {
	e.mergersMu.Lock()
	defer e.mergersMu.Unlock()
	if m, ok := e.mergers[id]; ok {
		return m
	}
	ctx, cancel := context.WithCancel(e.mergeCtx)
	m := newMerger(cancel)
	e.mergers[id] = m
	task.Go(ctx, "upload cache merger", func() {
		defer close(m.done)
		e.mergeLoop(ctx, id, m)
	})
	return m
}

// stopMerger cancels a session's merger and waits for its exit.
//
// The wait is essential: the merger writes the part file, and finalize is about
// to sync, close and rename it. A merger still active during that sequence would
// be writing to a descriptor another goroutine is closing.
func (e *Engine) stopMerger(id SessionID) {
	e.mergersMu.Lock()
	m, ok := e.mergers[id]
	delete(e.mergers, id)
	e.mergersMu.Unlock()
	if !ok {
		return
	}
	m.wait()
}

// mergeLoop drains a session's cache until cancellation.
func (e *Engine) mergeLoop(ctx context.Context, id SessionID, m *merger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		}
		for {
			moved, err := e.mergeStep(ctx, id)
			if err != nil {
				// The session is gone: aborted, swept, or already published.
				// There is nothing left to drain and nothing to report.
				if errors.Is(err, ErrNotFound) {
					return
				}
				// A failed step is retried on the next chunk instead of being
				// spun on: the part file is untouched, the cache file remains,
				// and the frontier has not advanced. A cancelled context means
				// this merger is being stopped, which is not a failure.
				if !errors.Is(err, context.Canceled) {
					e.log.Warn("an upload cache merge step failed; "+
						"it is retried on the next chunk",
						"session", id.String(), "error", err)
				}
				break
			}
			if !moved {
				break
			}
			m.endRound()
		}
		// The round's end is announced regardless of whether anything moved.
		// Without it, a waiter that chose to sleep exactly as the merger ran out
		// of work would await a step that never arrives.
		m.endRound()
	}
}

// mergeStep copies the unmerged tail of one cached chunk into the part file and
// reports whether anything moved.
//
// The ordering carries the entire durability argument: copy, make the part file
// durable, record the frontier, then remove the cache file. Crashing between the
// copy and the record re-merges the same range, which is idempotent. Crashing
// between the record and the removal leaves a cache file wholly below the
// frontier, which the following step deletes without copying. No sequence here
// can lose a byte the client was told had arrived.
//
// Nothing beyond the committed contiguous prefix is ever merged. A cache file
// appears the moment its chunk is complete, which precedes recording the range
// and therefore precedes verifying its checksum. Merging one early would place
// bytes the client is about to resend into the part file, then answer that
// resend with a frontier already past them.
func (e *Engine) mergeStep(ctx context.Context, id SessionID) (bool, error) {
	unlock := e.lockRow(id)
	r, err := e.load(ctx, id)
	if err != nil {
		unlock()
		return false, err
	}
	if !r.cached() {
		unlock()
		return false, nil
	}
	dir, derr := cacheDirOf(r.sess.CacheDir)
	if derr != nil {
		unlock()
		return false, derr
	}
	frontier, ferr := num.Narrow[uint64](r.sess.CacheMerged)
	if ferr != nil {
		unlock()
		return false, ferr
	}
	share, ok := shareIDOf(r.sess.Share)
	if !ok {
		unlock()
		return false, errors.New("a cached session names a share id that does not fit")
	}
	root, ok := e.core.ShareRoot(share)
	if !ok {
		// The share is unregistered in this process, leaving the part file
		// unreachable. The cache retains its files and the frontier holds.
		unlock()
		return false, nil
	}
	part, perr := e.partPathOf(r)
	if perr != nil {
		unlock()
		return false, perr
	}
	chunks, cerr := e.cache.chunksOf(dir)
	if cerr != nil {
		unlock()
		return false, cerr
	}
	committed := r.set.ContiguousPrefix()
	unlock()

	next, ok := nextMergeable(chunks, frontier, committed)
	if !ok {
		return false, nil
	}

	end := min64(next.off+next.size, committed)
	if end > frontier {
		copied, copyErr := e.copyIntoPart(root, id, part, next, frontier, end)
		if copyErr != nil {
			return false, copyErr
		}
		end = frontier + copied
	}

	// Made durable at the destination before anything is assumed about it.
	if serr := e.syncPart(root, id, part); serr != nil {
		return false, serr
	}
	if end > frontier {
		moved, nerr := num.Narrow[int64](end)
		if nerr != nil {
			return false, nerr
		}
		if aerr := e.state.AdvanceUploadCacheMerged(ctx, id.Bytes(), moved); aerr != nil {
			return false, aerr
		}
	}
	// The cache file goes only once every byte of it is in the part file. A
	// step is bounded, so a chunk larger than that bound drains across several
	// of them, and deleting it after the first would throw away the tail the
	// frontier has not reached yet.
	if end < next.off+next.size {
		// A step that copied nothing and can delete nothing is not progress,
		// and reporting it as such spins the loop.
		return end > frontier, nil
	}
	if uerr := e.cache.root.Unlink(next.path); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
		// The bytes reached the part file and the frontier advanced, so a file
		// that refuses to go becomes an unlistable orphan for the sweep.
		e.log.Warn("a merged upload cache chunk could not be removed; "+
			"the sweep will collect it",
			"session", id.String(), "error", uerr)
	} else if n, nerr := num.Narrow[int64](next.size); nerr == nil {
		e.cache.used.Add(-n)
	}
	return true, nil
}

// nextMergeable is the chunk to merge at the frontier: the one with the
// lowest offset that starts at or below it, so no hole is ever skipped over,
// and whose bytes are inside the committed prefix.
func nextMergeable(chunks []cacheChunk, frontier, committed uint64) (cacheChunk, bool) {
	for _, ch := range chunks {
		if ch.off > frontier {
			continue
		}
		// Either it contributes something below the commit point, or it lies
		// wholly behind the frontier and is a remnant to delete.
		if committed > frontier || ch.off+ch.size <= frontier {
			return ch, true
		}
	}
	return cacheChunk{}, false
}

// copyIntoPart writes a chunk's unmerged tail into the part file at the
// frontier and never below it. Everything below is already durable there and
// belongs to the chunk writer rather than the merger.
func (e *Engine) copyIntoPart(
	root vfs.Root, id SessionID, part vfs.SafePath, ch cacheChunk, frontier, end uint64,
) (uint64, error) {
	src, err := e.cache.root.OpenRead(ch.path, vfs.IntentRead)
	if err != nil {
		return 0, mapVFSErr(err)
	}
	defer func() {
		if cerr := src.Close(); cerr != nil {
			e.log.Warn("closing a cached upload chunk failed", "error", cerr)
		}
	}()
	dst, herr := e.handleFor(root, id, part)
	if herr != nil {
		return 0, herr
	}

	from := frontier - ch.off
	want := end - frontier
	if limit := e.cache.stepMax(); want > limit {
		want = limit
	}
	copied, cerr := vfs.CopyRange(src, from, dst, frontier, want)
	if cerr != nil {
		return copied, mapVFSErr(cerr)
	}
	if copied == 0 {
		return 0, fmt.Errorf("merging a cached chunk at %d: the copy moved no bytes", ch.off)
	}
	return copied, nil
}

// syncPart flushes the part file's contents to durable storage.
func (e *Engine) syncPart(root vfs.Root, id SessionID, part vfs.SafePath) error {
	f, err := e.handleFor(root, id, part)
	if err != nil {
		return err
	}
	if serr := f.SyncData(); serr != nil {
		return mapVFSErr(serr)
	}
	return nil
}

// patchCached is the cached path's half of the write: make room, write the
// chunk to the spool, then ask the merger to drain it.
func (e *Engine) patchCached(
	ctx context.Context, root vfs.Root, r *row, id SessionID,
	part vfs.SafePath, off uint64, body io.Reader, sum *Checksum,
) (uint64, []byte, error) {
	m := e.mergerFor(id)
	// Opening happens here rather than inside the merger, so a failure to open
	// rejects the chunk instead of surfacing as a warning in a background loop
	// the client never observes.
	if _, herr := e.handleFor(root, id, part); herr != nil {
		return 0, nil, herr
	}
	if rerr := e.waitForRoom(ctx, m, id, off); rerr != nil {
		return 0, nil, rerr
	}
	n, digest, werr := e.writeCached(r, off, func(f *vfs.File) (uint64, []byte, error) {
		// Zero within the cache file, offset within the finished file. The
		// declared length and chunk floor constrain the file being assembled
		// rather than the staging file the bytes temporarily occupy.
		return e.writeBodyAt(f, 0, off, body, r, sum)
	})
	m.nudge()
	return n, digest, werr
}

// chunkWriter is the write half of a chunk, so the caller decides how the
// body is read and this file decides only where it lands.
type chunkWriter func(*vfs.File) (uint64, []byte, error)

// writeCached stores a chunk in the cache rather than the part file.
//
// Writing goes to a staging name, renamed into position once the chunk is
// complete and durable. This is not housekeeping: the merger runs concurrently
// and chooses what to copy from the directory listing, so a file bearing its
// final name before completion is one the merger can partially copy, advance its
// frontier beyond, and delete from under the writer. The staging name does not
// parse as a chunk, keeping it invisible to the merger, and renaming within a
// single directory is atomic.
func (e *Engine) writeCached(r *row, off uint64, body chunkWriter) (uint64, []byte, error) {
	dir, err := cacheDirOf(r.sess.CacheDir)
	if err != nil {
		return 0, nil, err
	}
	if merr := e.cache.root.Mkdir(dir); merr != nil && !errors.Is(merr, vfs.ErrExists) {
		return 0, nil, mapVFSErr(merr)
	}
	stagingName, serr := cacheStagingName()
	if serr != nil {
		return 0, nil, serr
	}
	staging, jerr := dir.JoinControl(stagingName)
	if jerr != nil {
		return 0, nil, jerr
	}
	file, jerr := dir.JoinControl(cacheChunkName(off))
	if jerr != nil {
		return 0, nil, jerr
	}

	f, cerr := e.cache.root.CreatePart(staging)
	if cerr != nil {
		return 0, nil, mapVFSErr(cerr)
	}
	done := false
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			e.log.Warn("closing a cached upload chunk failed", "error", closeErr)
		}
		if done {
			return
		}
		// A chunk that failed midway leaves nothing: it never acquired its final
		// name, so nothing will ever search for it.
		if uerr := e.cache.root.Unlink(staging); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("an abandoned cached upload chunk could not be removed", "error", uerr)
		}
	}()

	n, digest, werr := body(f)
	if werr != nil {
		return n, digest, werr
	}
	// Made durable in the cache before recording the range, matching the direct
	// path's ordering: crashing between the two under-reports what arrived and
	// the client resends it.
	if syncErr := f.SyncData(); syncErr != nil {
		return n, digest, mapVFSErr(syncErr)
	}
	// Replaced rather than rejected, since a repeated offset indicates a client
	// retry after a lost response carrying identical bytes.
	if rerr := e.cache.root.Rename(staging, file, false); rerr != nil {
		return n, digest, mapVFSErr(rerr)
	}
	done = true
	if size, nerr := num.Narrow[int64](n); nerr == nil {
		e.cache.used.Add(size)
	}
	return n, digest, nil
}

// waitForRoom blocks until the cache can accept another chunk, or reports that
// it never will.
//
// The budget is consulted before the write rather than during it, so a chunk may
// push the spool one chunk beyond the bound. That overshoot amounts to one chunk
// per client in flight; enforcing mid-body would mean discarding a half-written
// chunk and requesting it again.
//
// Three outcomes, each a distinct fact:
//
//   - Room exists. Nothing to decide.
//   - The merger holds contiguous data to drain, so room is on its way. Waiting
//     is correct here, and it is what limits a sequential client to the disk's
//     pace instead of letting it fill the spool.
//   - The merger holds none, meaning the bytes it lacks are ones this client has
//     not sent. Nothing the server does will free space. The single exception is
//     a chunk that would extend the contiguous region: rejecting it would reject
//     precisely the chunk that unblocks the merge, so it is accepted and the
//     overshoot tolerated. Everything else is rejected with a retry, making the
//     client back off rather than the server buffer an unbounded hole.
func (e *Engine) waitForRoom(ctx context.Context, m *merger, id SessionID, off uint64) error {
	for {
		// Read before consulting the budget: a round completing between the two
		// closes this channel, so the wait below returns immediately instead of
		// sleeping through the very progress it awaited.
		watch := m.watch()
		if e.cache.used.Load() < e.cache.budget() {
			return nil
		}
		r, err := e.load(ctx, id)
		if err != nil {
			return err
		}
		merged, nerr := num.Narrow[uint64](r.sess.CacheMerged)
		if nerr != nil {
			return nerr
		}
		prefix := r.set.ContiguousPrefix()
		if prefix <= merged {
			if off <= prefix {
				return nil
			}
			return &CacheFullError{RetryAfterSeconds: int(cacheRetryAfter.Seconds())}
		}
		m.nudge()
		select {
		case <-watch:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cacheWaitMax):
			// The merger had work and has completed none of it for this long,
			// so something is wrong at the destination: a vanished share, a
			// disk no longer responding. Keeping the request open beyond that
			// point serves the client worse than asking it to return later.
			return &CacheFullError{RetryAfterSeconds: int(cacheRetryAfter.Seconds())}
		}
	}
}

// drainCache merges whatever a cached session still holds and halts its merger,
// so finalize encounters a part file no one else is writing.
//
// Holes at this stage are not an error: finalize is what rejects an incomplete
// set, naming the absent ranges. This stops once the merger has nothing
// contiguous remaining rather than judging completeness itself.
func (e *Engine) drainCache(ctx context.Context, id SessionID) error {
	if e.cache == nil {
		return nil
	}
	e.stopMerger(id)
	for {
		moved, err := e.mergeStep(ctx, id)
		if err != nil {
			return err
		}
		if !moved {
			return nil
		}
	}
}

// releaseCache drops a session's cache directory and its merger.
//
// It is what an abort and the sweep call: the part file's fate is theirs to
// decide, and the cache holds nothing anybody can still finish.
func (e *Engine) releaseCache(cacheDir string) {
	if e.cache == nil || cacheDir == "" {
		return
	}
	dir, err := cacheDirOf(cacheDir)
	if err != nil {
		return
	}
	e.cache.removeSession(dir)
}
