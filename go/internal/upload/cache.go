//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The cache spool.
//
// A chunk normally lands straight in the part file, in the destination
// directory, and that stays the default. When the network is faster than the
// destination disk the chunks queue behind it, so the cache is somewhere else
// to put them: a directory under the data dir, which an operator can mount a
// tmpfs or an NVMe volume at.
//
// The cache is a window over the file rather than a copy of it. A 200 GB
// upload has to work with a 128 GB cache volume, which means data is merged
// into the destination and deleted from the cache while the upload is still
// running. What the cache holds at any moment is bounded by a share of its
// volume's free space, measured live.

// cacheShareID is the id the spool's rooted handle carries. It is not a share:
// the spool is under the data directory, nothing grants access to it, and no
// path in it is ever resolved from a request. The value exists because a
// rooted handle carries one.
const cacheShareID = vfs.ShareID(0)

// cacheFreeFraction is the share of the cache volume's free space the spool
// may hold, as a percentage. The requirement is 20%: the rest of the free
// space belongs to whatever else runs on that volume.
const cacheFreeFraction = 20

// cacheRetryAfter is what a refused chunk is told to wait. It is short because
// the thing it waits for is a disk write already in progress.
const cacheRetryAfter = 5 * time.Second

// cacheWaitMax bounds how long one chunk waits for the merger. Reaching it
// means the merge is not failing loudly but is not moving either, which is a
// destination problem rather than a full cache; the request is answered rather
// than held open forever.
const cacheWaitMax = 30 * time.Second

// mergeCopyMax bounds one merge step, so a chunk larger than it is drained
// across several and the loop gets to look at its cancellation in between. An
// unbounded step is one copy a shutdown has to wait out.
const mergeCopyMax = 64 << 20

// Cache is the spool directory and what is currently in it.
type Cache struct {
	root *vfs.ShareRoot

	// enabled is the admin switch. It is read when a session is created and
	// never afterwards: a session already in flight keeps the mode it started
	// in, because its bytes are in one place or the other and a switch cannot
	// move them.
	enabled atomic.Bool

	// used is the bytes the spool holds, maintained by the writers and the
	// merger rather than measured. Measuring means walking a directory per
	// chunk, and the number is only ever compared against a budget.
	used atomic.Int64

	// limit replaces the measured budget, and step replaces the per-step copy
	// bound, when they are non-zero. Only the tests set them: one bound is a
	// share of a real volume's free space and the other is measured in tens of
	// megabytes, and proving what happens at either otherwise means moving that
	// much data per test.
	limit atomic.Int64
	step  atomic.Int64
}

// stepMax is how much one merge step may copy.
func (c *Cache) stepMax() uint64 {
	if n := c.step.Load(); n > 0 {
		return uint64(n)
	}
	return mergeCopyMax
}

// openCache prepares the spool directory and opens it as a rooted handle.
//
// The directory is fixed under the data dir rather than configurable. It is
// already inside the sandbox's domain, so the switch applies with no restart,
// and there is no path setting to validate or to get wrong. An operator who
// wants a faster or larger spool mounts a volume there; nothing here mounts or
// manages one.
func openCache(dir string) (*Cache, error) {
	// The path is the operator's own data directory, never request input.
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // G703 reads the variable: the path is the operator's data directory.
		return nil, fmt.Errorf("preparing the upload cache spool: %w", err)
	}
	policy := vfs.SharePolicy{
		Symlink: vfs.SymlinkDeny,
		// Nothing is mounted inside the spool: a volume an operator supplies
		// is mounted at the spool itself, which is this anchor. Refusing to
		// cross a boundary below it means a mount somebody placed in there
		// cannot be written through.
		CrossMount: false,
		// The spool holds other people's file contents in transit and lives
		// under the data directory, which is the server's alone.
		ModeFile: 0o600,
		ModeDir:  0o700,
	}
	root, err := vfs.OpenShareRoot(cacheShareID, dir, policy)
	if err != nil {
		return nil, fmt.Errorf("opening the upload cache spool: %w", err)
	}
	c := &Cache{root: root}
	c.used.Store(c.measure())
	return c, nil
}

// Close releases the spool's anchor.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.root.Close()
}

// measure walks the spool and totals what is in it, staging files included: a
// half-written chunk occupies the volume like any other file. It runs at
// startup, where the alternative is trusting a counter a crash did not get to
// write.
func (c *Cache) measure() int64 {
	var total int64
	dirs := c.sessionDirs()
	for _, dir := range dirs {
		_ = c.root.ReadDirFunc(dir, vfs.IncludeReserved, func(e vfs.DirEntry) bool { //nolint:errcheck // a directory that cannot be read contributes nothing; the budget errs high.
			child, jerr := dir.JoinControl(e.Name)
			if jerr != nil {
				return true
			}
			if st, serr := c.root.Stat(child); serr == nil {
				if n, nerr := num.Narrow[int64](st.Size); nerr == nil {
					total += n
				}
			}
			return true
		})
	}
	return total
}

// sessionDirs is every per-session directory in the spool.
func (c *Cache) sessionDirs() []vfs.SafePath {
	var out []vfs.SafePath
	_ = c.root.ReadDirFunc(vfs.RootPath(), vfs.IncludeReserved, func(e vfs.DirEntry) bool { //nolint:errcheck // an unreadable spool root yields no directories, which the callers treat as an empty spool.
		if !e.Kind.IsDir() {
			return true
		}
		if p, jerr := vfs.RootPath().JoinControl(e.Name); jerr == nil {
			out = append(out, p)
		}
		return true
	})
	return out
}

// budget is what the spool may hold right now: a share of the volume's free
// space plus what the spool already occupies, since that space is the spool's
// already and statfs has stopped counting it as free.
//
// A probe that cannot run answers zero, which refuses new cached writes rather
// than letting an unmeasurable volume fill up.
func (c *Cache) budget() int64 {
	if n := c.limit.Load(); n > 0 {
		return n
	}
	space, err := c.root.Space(vfs.RootPath())
	if err != nil {
		return 0
	}
	// Available rather than Free: the blocks reserved for root are not ours,
	// and counting them buys a window that ends in ENOSPC.
	avail, nerr := num.Narrow[int64](space.Available)
	if nerr != nil {
		avail = int64(^uint64(0) >> 1)
	}
	used := c.used.Load()
	total := avail
	if used > 0 && total > (int64(^uint64(0)>>1))-used {
		return int64(^uint64(0) >> 1)
	}
	total += used
	return total / 100 * cacheFreeFraction
}

// cacheDirOf is a session's directory inside the spool.
func cacheDirOf(name string) (vfs.SafePath, error) {
	return vfs.RootPath().JoinControl(name)
}

// cacheChunkName is what one cached chunk is called: the reserved prefix and
// the offset it holds, fixed width and hex, so a listing sorts by offset
// without parsing and the name is the whole of the file's placement.
func cacheChunkName(off uint64) string {
	return fmt.Sprintf(".scpart-%016x", off)
}

// parseCacheChunkName reads an offset back out of a chunk file's name. It is a
// trust boundary in the weak sense: the names come off a directory this server
// owns, and one that does not parse names a file this build did not write, so
// it is skipped rather than guessed at.
func parseCacheChunkName(name string) (uint64, bool) {
	const prefix = ".scpart-"
	if len(name) != len(prefix)+16 || name[:len(prefix)] != prefix {
		return 0, false
	}
	var off uint64
	for i := len(prefix); i < len(name); i++ {
		d := name[i]
		switch {
		case d >= '0' && d <= '9':
			off = off<<4 | uint64(d-'0')
		case d >= 'a' && d <= 'f':
			off = off<<4 | uint64(d-'a'+10)
		default:
			return 0, false
		}
	}
	return off, true
}

// cacheChunk is one file in a session's cache directory.
type cacheChunk struct {
	path vfs.SafePath
	off  uint64
	size uint64
}

// chunksOf lists a session's cached chunks in ascending offset order.
func (c *Cache) chunksOf(dir vfs.SafePath) ([]cacheChunk, error) {
	var out []cacheChunk
	err := c.root.ReadDirFunc(dir, vfs.IncludeReserved, func(e vfs.DirEntry) bool {
		off, ok := parseCacheChunkName(e.Name)
		if !ok {
			return true
		}
		child, jerr := dir.JoinControl(e.Name)
		if jerr != nil {
			return true
		}
		st, serr := c.root.Stat(child)
		if serr != nil {
			return true
		}
		out = append(out, cacheChunk{path: child, off: off, size: st.Size})
		return true
	})
	if err != nil {
		return nil, mapVFSErr(err)
	}
	sortChunks(out)
	return out, nil
}

// sortChunks orders by offset. The slice is small (one entry per chunk in
// flight) so an insertion sort beats reaching for a comparator closure.
func sortChunks(s []cacheChunk) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].off < s[j-1].off; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// removeSession empties a session's cache directory and removes it, returning
// the bytes it held so the caller can give them back to the budget.
//
// Everything in the directory goes, not only the files whose names parse as
// chunks: a staging file an interrupted write left behind occupies the volume
// exactly as much as a finished chunk does.
func (c *Cache) removeSession(dir vfs.SafePath) int64 {
	var freed int64
	err := c.root.ReadDirFunc(dir, vfs.IncludeReserved, func(e vfs.DirEntry) bool {
		child, jerr := dir.JoinControl(e.Name)
		if jerr != nil {
			return true
		}
		st, serr := c.root.Stat(child)
		if serr != nil {
			return true
		}
		if uerr := c.root.Unlink(child); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			return true
		}
		if n, nerr := num.Narrow[int64](st.Size); nerr == nil {
			freed += n
		}
		return true
	})
	if err != nil {
		return 0
	}
	_ = c.root.Rmdir(dir) //nolint:errcheck // a directory that will not go is unlistable debt the next sweep takes.
	c.used.Add(-freed)
	return freed
}

// merger is one session's drain: the goroutine copying contiguous cached data
// into the part file, and the two channels a writer uses to talk to it.
type merger struct {
	wake chan struct{}
	stop context.CancelFunc
	done chan struct{}

	// progress is closed and replaced at the end of every round, where a round
	// is one merge step or the moment the merger runs out of work. A writer
	// waiting for room reads the channel before it checks the budget, so a
	// round that lands in between wakes it rather than being missed.
	//
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

// cacheEnabled reports whether new sessions spool to the cache.
func (e *Engine) cacheEnabled() bool {
	return e.cache != nil && e.cache.enabled.Load()
}

// CacheEnabled is the admin-facing read of the same switch.
func (e *Engine) CacheEnabled() bool { return e.cacheEnabled() }

// CacheAvailable reports whether this build has a spool at all. A deployment
// with no data directory (a test harness) has none, and the screen has to say
// so rather than offering a switch that does nothing.
func (e *Engine) CacheAvailable() bool { return e.cache != nil }

// SetCacheEnabled persists the switch and makes it live.
//
// The probe runs before the write: a spool that cannot take a file is refused
// here, where an administrator is watching, rather than at the first upload
// after the next restart.
func (e *Engine) SetCacheEnabled(ctx context.Context, on bool) error {
	if e.cache == nil {
		return fmt.Errorf("%w: this deployment has no upload cache spool", ErrBadRequest)
	}
	if on {
		if err := e.cache.probe(); err != nil {
			return err
		}
	}
	if err := e.state.WriteUploadCacheEnabled(ctx, on); err != nil {
		return err
	}
	e.cache.enabled.Store(on)
	return nil
}

// probe writes and removes one file, which is the only way to learn whether
// the spool is writable. A stat says what the metadata claims; a read-only
// mount and a directory owned by somebody else both pass it.
func (c *Cache) probe() error {
	name, err := NewSessionID()
	if err != nil {
		return err
	}
	p, jerr := vfs.RootPath().JoinControl(".scpart-probe-" + name.String())
	if jerr != nil {
		return jerr
	}
	f, cerr := c.root.CreatePart(p)
	if cerr != nil {
		return fmt.Errorf("%w: the upload cache spool is not writable: %w", ErrBadRequest, cerr)
	}
	closeErr := f.Close()
	if uerr := c.root.Unlink(p); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
		return fmt.Errorf("%w: the upload cache spool is not writable: %w", ErrBadRequest, uerr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: the upload cache spool is not writable: %w", ErrBadRequest, closeErr)
	}
	return nil
}

// startMerger returns the session's merger, starting it if it is not running.
func (e *Engine) startMerger(id SessionID) *merger {
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
// Waiting is the point: the merger writes the part file, and finalize is about
// to sync it, close it and rename it. A merger still running through that is a
// write to a descriptor another goroutine is closing.
func (e *Engine) stopMerger(id SessionID) {
	e.mergersMu.Lock()
	m, ok := e.mergers[id]
	delete(e.mergers, id)
	e.mergersMu.Unlock()
	if !ok {
		return
	}
	m.stop()
	<-m.done
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
				// A step that failed is retried on the next chunk rather than
				// spun on: the part file is unchanged, the cache file is
				// still there, and the frontier has not moved. A cancelled
				// context is this merger being stopped, which is not a
				// failure and is not worth a line.
				if !errors.Is(err, context.Canceled) {
					e.log.Warn("an upload cache merge step failed; it is retried on the next chunk",
						slog.String("session", id.String()), slog.Any("error", err))
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
// The order is the whole durability argument, and it is: copy, make the part
// file durable, record the frontier, then delete the cache file. A crash
// between the copy and the record re-merges the same range, which is
// idempotent. A crash between the record and the delete leaves a cache file
// entirely below the frontier, which the next step removes without copying.
// No ordering here can lose a byte the client was told had landed.
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
	if r.sess.CacheDir == "" {
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
		return false, fmt.Errorf("a cached session names a share id that does not fit")
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
		copied, err := e.copyIntoPart(root, id, part, next, frontier, end)
		if err != nil {
			return false, err
		}
		end = frontier + copied
	}

	// Durable in the destination before anything else is believed about it.
	if err := e.syncPart(root, id, part); err != nil {
		return false, err
	}
	if end > frontier {
		moved, nerr := num.Narrow[int64](end)
		if nerr != nil {
			return false, nerr
		}
		if err := e.state.AdvanceUploadCacheMerged(ctx, id.Bytes(), moved); err != nil {
			return false, err
		}
	}
	// Only once every byte of it is in the part file. A step is bounded, so a
	// chunk larger than that bound is drained across several of them, and
	// deleting it after the first would throw away the tail the frontier has
	// not reached yet.
	// Only once every byte of it is in the part file. A step is bounded, so a
	// chunk larger than that bound is drained across several of them, and
	// deleting it after the first would throw away the tail the frontier has
	// not reached yet.
	if end < next.off+next.size {
		// A step that copied nothing and cannot delete anything is not
		// progress, and reporting it as such spins the merge loop.
		return end > frontier, nil
	}
	if uerr := e.cache.root.Unlink(next.path); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
		// The bytes are in the part file and the frontier has moved, so a file
		// that would not go is an unlistable orphan the sweep collects.
		e.log.Warn("a merged upload cache chunk could not be removed; the sweep will collect it",
			slog.String("session", id.String()), slog.Any("error", uerr))
	} else if n, nerr := num.Narrow[int64](next.size); nerr == nil {
		e.cache.used.Add(-n)
	}
	return true, nil
}

// nextMergeable is the chunk to merge at frontier: the one with the lowest
// offset that starts at or below it, so no hole is ever skipped over, and
// whose bytes are inside the committed prefix.
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
			e.log.Warn("closing a cached upload chunk failed", slog.Any("error", cerr))
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

// patchCached is the cached path's half of PatchAt: make room, write the chunk
// to the spool, then ask the merger to drain it.
//
// The merger is started here rather than at session creation, because a
// session that never receives a chunk should not cost a goroutine, and the
// engine has to be able to start one after a restart for a session it did not
// create.
func (e *Engine) patchCached(
	ctx context.Context, root *vfs.ShareRoot, r *row, id SessionID,
	part vfs.SafePath, off uint64, body io.Reader, sum *Checksum,
) (uint64, []byte, error) {
	m := e.startMerger(id)
	// The part file is opened here rather than inside the merger, so a failure
	// to open it refuses the chunk instead of being a warning in a background
	// loop the client never sees.
	if _, herr := e.handleFor(root, id, part); herr != nil {
		return 0, nil, herr
	}
	if rerr := e.waitForRoom(ctx, m, id, off); rerr != nil {
		return 0, nil, rerr
	}
	n, digest, werr := e.writeCached(r, off, func(f *vfs.File) (uint64, []byte, error) {
		// Zero in the cache file, off in the finished file: the declared length
		// and the chunk floor are rules about the file being assembled, not
		// about the staging file the bytes happen to be sitting in.
		return e.writeBodyAt(f, 0, off, body, r, sum)
	})
	m.nudge()
	return n, digest, werr
}

// writeCached puts one chunk in the cache instead of the part file and returns
// what it wrote and its digest.
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
			e.log.Warn("closing a cached upload chunk failed", slog.Any("error", closeErr))
		}
		if done {
			return
		}
		// A chunk that failed part way leaves nothing behind: it never reached
		// its final name, so nothing will ever look for it.
		if uerr := e.cache.root.Unlink(staging); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
			e.log.Warn("an abandoned cached upload chunk could not be removed", slog.Any("error", uerr))
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

// cacheStagingName is the name a chunk wears while it is being written. It
// carries the control prefix so it is unlistable, and deliberately does not
// parse as a chunk name, which is what hides it from the merger.
func cacheStagingName() (string, error) {
	id, err := NewSessionID()
	if err != nil {
		return "", err
	}
	return ".scpart-w" + id.String(), nil
}

// chunkWriter is the write half of a chunk, so the caller decides how the body
// is read and this file decides only where it lands.
type chunkWriter func(*vfs.File) (uint64, []byte, error)

// waitForRoom blocks until the cache has room for another chunk, or reports
// that it will not get any.
//
// The budget is checked before the write rather than during it, so a chunk can
// carry the spool one chunk past the bound. That overshoot is one chunk per
// client in flight; enforcing it mid-body would mean tearing up a chunk that
// is already half written and asking for it again.
//
// Three outcomes and they are different facts:
//
//   - There is room. Nothing to decide.
//   - The merger has contiguous data to drain, so the room is coming. Waiting
//     is right, and it is what bounds a sequential client to the disk's speed
//     instead of letting it fill the spool.
//   - It has not, which means the bytes it is missing are the ones this client
//     has not sent. Nothing this server does frees space. The one exception is
//     the chunk that would extend the contiguous region: refusing that is
//     refusing exactly the chunk that unblocks the merge, so it is taken and
//     the overshoot accepted. Anything else is refused with a retry, so the
//     client backs off rather than the server buffering an unbounded hole.
func (e *Engine) waitForRoom(ctx context.Context, m *merger, id SessionID, off uint64) error {
	for {
		// Read before the budget is checked: a round that lands between the two
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
			return &CacheFullError{RetryAfter: cacheRetryAfter}
		}
		m.nudge()
		select {
		case <-watch:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cacheWaitMax):
			// The merger had work and has not done any of it for this long, so
			// something on the destination side is wrong: a share that went
			// away, a disk that stopped answering. Holding the request open
			// past that point is worse than telling the client to come back.
			return &CacheFullError{RetryAfter: cacheRetryAfter}
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

// releaseCache drops a session's cache directory and its merger. It is what an
// abort and the sweep call: the part file's fate is theirs to decide and the
// cache holds nothing anybody can still finish.
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

// RecoverCache rebuilds what the cached sessions actually still hold.
//
// It runs at startup and it exists because the recommended spool is a tmpfs. A
// reboot empties one, and a session whose recorded intervals still claimed
// those bytes would answer a resuming client with an offset whose data is
// gone. What survives is everything below the merge frontier, which is in the
// part file, plus the cache files that are really on disk; the recorded set is
// cut down to that.
func (e *Engine) RecoverCache(ctx context.Context) error {
	if e.cache == nil {
		return nil
	}
	live, err := e.state.ListUploadSessions(ctx)
	if err != nil {
		return err
	}
	known := map[string]struct{}{}
	for _, sess := range live {
		if sess.CacheDir == "" {
			continue
		}
		known[sess.CacheDir] = struct{}{}
		if rerr := e.recoverSession(ctx, sess.ID, sess.CacheDir, sess.CacheMerged); rerr != nil {
			e.log.Warn("an upload's cached state could not be rebuilt; it stays resumable from what is durable",
				slog.String("dest", sess.Dest), slog.Any("error", rerr))
		}
	}
	// A directory whose session row is gone is debt no walk of the shares can
	// see: the spool is not a share and the row is the only thing that names a
	// directory in it.
	for _, dir := range e.cache.sessionDirs() {
		if _, held := known[dir.Name()]; held {
			continue
		}
		e.cache.removeSession(dir)
	}
	// Measured rather than accumulated: the counter the writers keep is what a
	// crash did not get to write, and every later budget decision reads it.
	e.cache.used.Store(e.cache.measure())
	return nil
}

// recoverSession cuts one session's interval set down to what is still on
// disk.
func (e *Engine) recoverSession(ctx context.Context, id []byte, cacheDir string, merged int64) error {
	sid, err := sessionIDFromBytes(id)
	if err != nil {
		return err
	}
	unlock := e.lockRow(sid)
	defer unlock()

	r, err := e.load(ctx, sid)
	if err != nil {
		return err
	}
	dir, derr := cacheDirOf(cacheDir)
	if derr != nil {
		return derr
	}
	frontier, ferr := num.Narrow[uint64](merged)
	if ferr != nil {
		return ferr
	}

	survived := NewIntervalSet()
	if frontier > 0 {
		if ierr := survived.Insert(0, frontier); ierr != nil {
			return ierr
		}
	}
	chunks, cerr := e.cache.chunksOf(dir)
	if cerr != nil && !errors.Is(cerr, ErrNotFound) {
		return cerr
	}
	for _, ch := range chunks {
		if ch.size == 0 {
			continue
		}
		if ierr := survived.Insert(ch.off, ch.off+ch.size); ierr != nil {
			return ierr
		}
	}

	// The recorded set is the claim and this is the evidence, so the answer is
	// the intersection: a range that was recorded and is no longer anywhere
	// must stop being reported, and a cache file with no recorded range is a
	// chunk whose write never committed.
	trimmed := intersectSets(r.set, survived)
	if sameRuns(trimmed, r.set) {
		return nil
	}
	e.log.Warn("an upload resumes from less than it had: part of its cache did not survive the restart",
		slog.String("dest", r.sess.Dest),
		slog.Uint64("offset", trimmed.ContiguousPrefix()))
	r.set = trimmed
	return e.commitRange(ctx, r)
}

// intersectSets is the ranges both sets hold.
func intersectSets(a, b *IntervalSet) *IntervalSet {
	out := NewIntervalSet()
	for _, x := range a.Runs() {
		for _, y := range b.Runs() {
			lo := max64(x.Lo, y.Lo)
			hi := min64(x.Hi, y.Hi)
			if lo < hi {
				// Both inputs are normalised and the products are disjoint and
				// ascending, so this cannot exceed the run bound that either
				// input already satisfies.
				_ = out.Insert(lo, hi) //nolint:errcheck // see the comment above.
			}
		}
	}
	return out
}

func sameRuns(a, b *IntervalSet) bool {
	x, y := a.Runs(), b.Runs()
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// CacheFullError is a chunk refused because the spool is at its budget and the
// merger has nothing it can drain. It carries the wait, because a client told
// only "unavailable" retries immediately and makes it worse.
type CacheFullError struct {
	RetryAfter time.Duration
}

func (e *CacheFullError) Error() string {
	return fmt.Sprintf("the upload cache is full and cannot drain until the missing chunks arrive; retry in %s",
		e.RetryAfter)
}

func (e *CacheFullError) Is(target error) bool { return target == ErrCacheFull }

// ErrCacheFull is the sentinel for the refusal above.
var ErrCacheFull = errors.New("upload cache full")
