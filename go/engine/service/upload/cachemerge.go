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

// cacheWaitMax bounds how long one chunk waits for the merger. Reaching it
// means the merge is not failing loudly but is not moving either, which is a
// destination problem rather than a full cache; the request is answered
// rather than held open forever.
const cacheWaitMax = 30 * time.Second

// mergeCopyMax bounds one merge step, so a chunk larger than it drains across
// several and the loop gets to look at its cancellation in between. An
// unbounded step is one copy a shutdown has to wait out.
const mergeCopyMax = 64 << 20

// merger is one session's drain: the goroutine copying contiguous cached data
// into the part file, and the two channels a writer talks to it through.
type merger struct {
	wake chan struct{}
	stop context.CancelFunc
	done chan struct{}

	// progress is closed and replaced at the end of every round, where a round
	// is one merge step or the moment the merger runs out of work. A writer
	// waiting for room reads this channel before it checks the budget, so a
	// round landing in between wakes it rather than being missed. That
	// ordering is the protocol's correctness.
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

// nudge asks the merger to look again. It never blocks: the channel holds one
// token, and one pending wake is the same request as five.
func (m *merger) nudge() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// endRound reports that a round finished, waking everything waiting for room.
func (m *merger) endRound() {
	m.mu.Lock()
	old := m.progress
	m.progress = make(chan struct{})
	m.mu.Unlock()
	close(old)
}

// watch is the channel to wait on for the next round. It is read before the
// budget is checked, which is what closes the race.
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

// startMerger returns the session's merger, starting it if it is not running.
//
// It takes a row rather than an id because a session that is not cached needs
// no merger at all, and asking the store again to find that out is a read per
// chunk.
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

// stopMerger cancels a session's merger and waits for it to leave.
//
// Waiting is the point: the merger writes the part file, and finalize is
// about to sync it, close it and rename it. A merger still running through
// that is a write to a descriptor another goroutine is closing.
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

// mergeLoop drains a session's cache until it is cancelled.
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
				// A step that failed is retried on the next chunk rather than
				// spun on: the part file is unchanged, the cache file is still
				// there, and the frontier has not moved. A cancelled context
				// is this merger being stopped, which is not a failure.
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
		// The end of the round is announced whether or not it moved anything.
		// Without this a waiter that decided to sleep just as the merger ran
		// out of work would wait for a step that is never coming.
		m.endRound()
	}
}

// mergeStep copies one cached chunk's unmerged tail into the part file and
// reports whether it moved anything.
//
// The order is the whole durability argument: copy, make the part file
// durable, record the frontier, then delete the cache file. A crash between
// the copy and the record re-merges the same range, which is idempotent. A
// crash between the record and the delete leaves a cache file entirely below
// the frontier, which the next step removes without copying. No ordering here
// can lose a byte the client was told had landed.
//
// Nothing above the committed contiguous prefix is ever merged. A cache file
// exists from the moment its chunk is whole, which is before the range is
// recorded and therefore before its checksum has been checked: merging one
// early would put bytes the client is about to resend into the part file, and
// then answer the resend with a frontier already past them.
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
		// The share is not registered in this process, so the part file is not
		// reachable. The cache keeps its files and the frontier stays put.
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

	// Durable in the destination before anything else is believed about it.
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
		// The bytes are in the part file and the frontier has moved, so a file
		// that would not go is an unlistable orphan the sweep collects.
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
		// Either it has something to contribute below the commit point, or it
		// is entirely behind the frontier and is a leftover to remove.
		if committed > frontier || ch.off+ch.size <= frontier {
			return ch, true
		}
	}
	return cacheChunk{}, false
}

// copyIntoPart copies a chunk's unmerged tail into the part file at the
// frontier, never below it: everything below is already durable there and is
// the chunk writer's region, not the merger's.
func (e *Engine) copyIntoPart(
	root *vfs.ShareRoot, id SessionID, part vfs.SafePath, ch cacheChunk, frontier, end uint64,
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

// syncPart makes the part file's contents durable.
func (e *Engine) syncPart(root *vfs.ShareRoot, id SessionID, part vfs.SafePath) error {
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
	ctx context.Context, root *vfs.ShareRoot, r *row, id SessionID,
	part vfs.SafePath, off uint64, body io.Reader, sum *Checksum,
) (uint64, []byte, error) {
	m := e.mergerFor(id)
	// The part file is opened here rather than inside the merger, so a failure
	// to open it refuses the chunk instead of becoming a warning in a
	// background loop the client never sees.
	if _, herr := e.handleFor(root, id, part); herr != nil {
		return 0, nil, herr
	}
	if rerr := e.waitForRoom(ctx, m, id, off); rerr != nil {
		return 0, nil, rerr
	}
	n, digest, werr := e.writeCached(r, off, func(f *vfs.File) (uint64, []byte, error) {
		// Zero in the cache file, off in the finished file: the declared length
		// and the chunk floor are rules about the file being assembled, not
		// about the staging file the bytes happen to sit in.
		return e.writeBodyAt(f, 0, off, body, r, sum)
	})
	m.nudge()
	return n, digest, werr
}

// chunkWriter is the write half of a chunk, so the caller decides how the
// body is read and this file decides only where it lands.
type chunkWriter func(*vfs.File) (uint64, []byte, error)

// writeCached puts one chunk in the cache instead of the part file.
//
// The chunk is written under a staging name and renamed into place once it is
// whole and durable. That is not tidiness: the merger runs concurrently and
// decides what to copy from the directory listing, so a file appearing at its
// final name before it is complete is a file the merger can copy half of,
// advance its frontier past, and delete out from under the writer. The
// staging name does not parse as a chunk, so the merger cannot see it, and a
// rename within one directory is atomic.
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
		// A chunk that failed part way leaves nothing behind: it never reached
		// its final name, so nothing will ever look for it.
		if uerr := e.cache.root.Unlink(staging); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("an abandoned cached upload chunk could not be removed", "error", uerr)
		}
	}()

	n, digest, werr := body(f)
	if werr != nil {
		return n, digest, werr
	}
	// Durable in the cache before the range is recorded, which is the same
	// ordering the direct path keeps: a crash between the two under-reports
	// what arrived and the client resends it.
	if syncErr := f.SyncData(); syncErr != nil {
		return n, digest, mapVFSErr(syncErr)
	}
	// Replacing rather than refusing: a repeated offset is a client retry
	// after a lost response, carrying the same bytes.
	if rerr := e.cache.root.Rename(staging, file, false); rerr != nil {
		return n, digest, mapVFSErr(rerr)
	}
	done = true
	if size, nerr := num.Narrow[int64](n); nerr == nil {
		e.cache.used.Add(size)
	}
	return n, digest, nil
}

// waitForRoom blocks until the cache has room for another chunk, or reports
// that it will not get any.
//
// The budget is checked before the write rather than during it, so a chunk
// can carry the spool one chunk past the bound. That overshoot is one chunk
// per client in flight; enforcing it mid-body would mean tearing up a chunk
// that is already half written and asking for it again.
//
// Three outcomes, and they are different facts:
//
//   - There is room. Nothing to decide.
//   - The merger has contiguous data to drain, so the room is coming. Waiting
//     is right, and it is what bounds a sequential client to the disk's speed
//     instead of letting it fill the spool.
//   - It has not, which means the bytes it is missing are the ones this client
//     has not sent. Nothing this server does frees space. The one exception is
//     the chunk that would extend the contiguous region: refusing that would
//     refuse exactly the chunk that unblocks the merge, so it is taken and the
//     overshoot accepted. Anything else is refused with a retry, so the client
//     backs off rather than the server buffering an unbounded hole.
func (e *Engine) waitForRoom(ctx context.Context, m *merger, id SessionID, off uint64) error {
	for {
		// Read before the budget is checked: a round landing between the two
		// closes this channel, so the wait below returns at once rather than
		// sleeping through the very progress it was waiting for.
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
			// The merger had work and has done none of it for this long, so
			// something on the destination side is wrong: a share that went
			// away, a disk that stopped answering. Holding the request open
			// past that point is worse than telling the client to come back.
			return &CacheFullError{RetryAfterSeconds: int(cacheRetryAfter.Seconds())}
		}
	}
}

// drainCache merges everything a cached session still holds and stops its
// merger, so finalize meets a part file nothing else is writing to.
//
// It is not an error for a session to have holes here: finalize is what
// refuses an incomplete set, and it names the missing ranges. This stops when
// the merger has nothing contiguous left rather than deciding that itself.
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
