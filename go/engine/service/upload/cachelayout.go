//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The cache spool: its layout, its accounting, and what a restart rebuilds.
//
// A chunk normally lands straight in the part file and that stays the default.
// When the network is faster than the destination disk, chunks queue behind
// it, and the cache is somewhere else to put them: a directory under the data
// directory an operator can mount faster storage at.
//
// The cache is a window over the file and never a copy of it. A 200 GB upload
// has to work with a 128 GB spool, so data merges into the destination and
// leaves the cache while the upload is still running.

// cacheFreeFraction is the share of the spool volume's free space the cache
// may hold, as a percentage. The rest belongs to whatever else runs there.
const cacheFreeFraction = 20

// cacheSpool is the spool directory and what is currently in it.
type cacheSpool struct {
	root *vfs.ShareRoot

	// enabled is the administrative switch, read when a session is created
	// and never afterwards: a session in flight keeps the mode it started in,
	// because its bytes are in one place or the other and no switch moves
	// them.
	enabled atomic.Bool

	// used counts the bytes the spool holds, kept current by the writers and
	// the merger rather than being measured. Measuring would walk a directory
	// per chunk, and the figure is only ever weighed against a budget.
	used atomic.Int64

	// limit and step replace the measured budget and the per-step copy bound
	// when they are non-zero. Only a test sets them: one bound is a share of
	// a real volume's free space and the other is tens of megabytes, and
	// proving what happens at either otherwise means moving that much data.
	limit atomic.Int64
	step  atomic.Int64
}

// openCacheSpool prepares the spool directory and opens it as a rooted
// handle.
//
// The directory is fixed under the data directory rather than configurable.
// It is already inside the sandbox's domain, so the switch applies with no
// restart and there is no path setting to validate or to get wrong. An
// operator who wants faster or larger scratch space mounts a volume there.
func openCacheSpool(dir string) (*cacheSpool, error) {
	// The path comes from the operator's data directory and never from a
	// request.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("preparing the upload cache spool: %w", err)
	}
	policy := vfs.SharePolicy{
		Symlink: vfs.SymlinkDeny,
		// Nothing mounts inside the spool. An operator-supplied volume mounts at
		// the spool itself, which is this anchor. Declining to cross a boundary
		// beneath it means a mount someone placed there cannot be written
		// through.
		CrossMount: false,
		// The spool carries other people's file contents in transit and sits
		// under the data directory, which belongs to the server alone.
		ModeFile: 0o600,
		ModeDir:  0o700,
	}
	// Scratch space rather than a share: it borrows no share id, and nothing
	// resolves a request path in it.
	root, err := vfs.OpenScratchRoot(dir, policy)
	if err != nil {
		return nil, fmt.Errorf("opening the upload cache spool: %w", err)
	}
	c := &cacheSpool{root: root}
	c.used.Store(c.measure())
	return c, nil
}

func (c *cacheSpool) close() error {
	if c == nil {
		return nil
	}
	return c.root.Close()
}

func (c *cacheSpool) setEnabled(on bool) { c.enabled.Store(on) }

// measure walks the spool and sums its contents, staging files included, since a
// half-written chunk consumes the volume like any other file. It runs at
// startup, where the alternative would be trusting a counter that a crash
// prevented from being written.
func (c *cacheSpool) measure() int64 {
	var total int64
	for _, dir := range c.sessionDirs() {
		// A directory that cannot be read contributes nothing, and the budget
		// then errs high rather than refusing every write.
		if err := c.root.ReadDirFunc(dir, vfs.IncludeReserved, func(e vfs.DirEntry) bool {
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
		}); err != nil {
			continue
		}
	}
	return total
}

// sessionDirs lists each per-session directory within the spool.
func (c *cacheSpool) sessionDirs() []vfs.SafePath {
	var out []vfs.SafePath
	// An unreadable spool root yields no directories, which every caller
	// treats as an empty spool.
	if err := c.root.ReadDirFunc(vfs.RootPath(), vfs.IncludeReserved, func(e vfs.DirEntry) bool {
		if !e.Kind.IsDir() {
			return true
		}
		if p, jerr := vfs.RootPath().JoinControl(e.Name); jerr == nil {
			out = append(out, p)
		}
		return true
	}); err != nil {
		return nil
	}
	return out
}

// budget gives what the spool may currently hold: a portion of the volume's free
// space plus whatever the spool already uses, since that space belongs to the
// spool and the filesystem no longer counts it as free.
//
// A probe that cannot run returns zero, rejecting new cached writes instead of
// permitting an unmeasurable volume to fill.
func (c *cacheSpool) budget() int64 {
	if n := c.limit.Load(); n > 0 {
		return n
	}
	space, err := c.root.Space(vfs.RootPath())
	if err != nil {
		return 0
	}
	// Available rather than free: the blocks reserved for root are not ours,
	// and counting them buys a window that ends in a full disk.
	const maxInt64 = int64(^uint64(0) >> 1)
	avail, nerr := num.Narrow[int64](space.Available)
	if nerr != nil {
		avail = maxInt64
	}
	used := c.used.Load()
	if used > 0 && avail > maxInt64-used {
		return maxInt64
	}
	return (avail + used) / 100 * cacheFreeFraction
}

// stepMax caps how much a single merge step copies.
func (c *cacheSpool) stepMax() uint64 {
	if n := c.step.Load(); n > 0 {
		if v, err := num.Narrow[uint64](n); err == nil {
			return v
		}
	}
	return mergeCopyMax
}

// cacheDirOf gives a session's directory within the spool.
func cacheDirOf(name string) (vfs.SafePath, error) {
	return vfs.RootPath().JoinControl(name)
}

// cacheChunkName builds a cached chunk's filename from the reserved prefix and
// its offset in fixed-width hex, so a listing sorts by offset without parsing
// and the name fully determines the file's placement.
func cacheChunkName(off uint64) string { return fmt.Sprintf(".scpart-%016x", off) }

// parseCacheChunkName reads an offset back out of a chunk file's name.
//
// A name that does not parse is one this build did not write, so it is
// skipped rather than guessed at: a stray file in the spool must never be
// merged as data.
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

// cacheStagingName gives the name a chunk holds while being written. The control
// prefix keeps it unlistable, and it intentionally fails to parse as a chunk
// name, which is what conceals it from the merger.
func cacheStagingName() (string, error) {
	id, err := NewSessionID()
	if err != nil {
		return "", err
	}
	return ".scpart-w" + id.String(), nil
}

// cacheChunk describes a single file in a session's cache directory.
type cacheChunk struct {
	path vfs.SafePath
	off  uint64
	size uint64
}

// chunksOf enumerates a session's cached chunks ordered by ascending offset.
func (c *cacheSpool) chunksOf(dir vfs.SafePath) ([]cacheChunk, error) {
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

// sortChunks orders by offset. The slice holds one entry per chunk in flight,
// so an insertion sort beats reaching for a comparator closure.
func sortChunks(s []cacheChunk) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].off < s[j-1].off; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// removeSession clears a session's cache directory and deletes it, returning the
// bytes it occupied so the caller can restore them to the budget.
//
// The entire directory goes, not merely files whose names parse as chunks: a
// staging file left by an interrupted write consumes exactly as much of the
// volume as a completed chunk.
func (c *cacheSpool) removeSession(dir vfs.SafePath) int64 {
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
	if rerr := c.root.Rmdir(dir); rerr != nil && !errors.Is(rerr, vfs.ErrNotFound) {
		// A directory that will not go is unlistable debt the next sweep takes.
		return freed
	}
	c.used.Add(-freed)
	return freed
}

// RecoverCache reconstructs what cached sessions genuinely still hold.
//
// It runs at startup and exists because the recommended spool is a memory
// filesystem. A reboot empties one, and a session whose recorded intervals still
// claimed those bytes would hand a resuming client an offset whose data has
// vanished. What survives is everything below the merge frontier, already in the
// part file, together with the cache files actually present on disk; the
// recorded set is trimmed to that.
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
			e.log.Warn("an upload's cached state could not be rebuilt; "+
				"it stays resumable from what is durable",
				"dest", sess.Dest, "error", rerr)
		}
	}
	// A directory whose session row has disappeared is debt invisible to any
	// walk of the shares: the spool is not a share, and the row is the only
	// thing that names a directory inside it.
	for _, dir := range e.cache.sessionDirs() {
		if _, held := known[dir.Name()]; held {
			continue
		}
		e.cache.removeSession(dir)
	}
	// Measured instead of accumulated, because the writers' counter is exactly
	// what a crash prevented from being written, and every subsequent budget
	// decision consults it.
	e.cache.used.Store(e.cache.measure())
	return nil
}

// recoverSession trims a session's interval set to what remains on disk.
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

	// The recorded set states the claim and this supplies the evidence, so the
	// result is their intersection. A range recorded but no longer present
	// anywhere must cease being reported, and a cache file lacking a recorded
	// range belongs to a chunk whose write never committed.
	trimmed := intersectSets(r.set, survived)
	if sameRuns(trimmed, r.set) {
		return nil
	}
	e.log.Warn("an upload resumes from less than it had: "+
		"part of its cache did not survive the restart",
		"dest", r.sess.Dest, "offset", trimmed.ContiguousPrefix())
	r.set = trimmed
	return e.commitRange(ctx, r)
}

// intersectSets yields the ranges present in both sets.
func intersectSets(a, b *IntervalSet) *IntervalSet {
	out := NewIntervalSet()
	for _, x := range a.Runs() {
		for _, y := range b.Runs() {
			lo := max64(x.Lo, y.Lo)
			hi := min64(x.Hi, y.Hi)
			if lo >= hi {
				continue
			}
			// Both inputs are normalized and the products are disjoint and
			// ascending, so this cannot exceed a bound either input already
			// satisfies. An error here would still be worth reporting rather
			// than dropping a range silently.
			if ierr := out.Insert(lo, hi); ierr != nil {
				return a
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

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
