//go:build linux

package core

import (
	"context"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"lukechampine.com/blake3"
)

// Aggregate is a directory's cached ETag with the recursive size and count
// that were computed alongside it.
type Aggregate = cache.Aggregate

// aggGuard is the single-flight state for one directory's rollup.
type aggGuard struct {
	mu   sync.Mutex
	done bool
}

// Aggregate returns the recursive rollup for a directory: its ETag, and the
// size and count of everything beneath it. The result is cached in the
// rebuildable half and invalidated by generation or dirty-marking.
//
// The ETag is a hash over the children's identities and tokens, so a change
// of any kind under the directory changes it. The directory id is allocated
// lazily: computing a rollup is the one thing that needs a directory's stable
// id to exist, so that is where it is minted.
func (c *Core) Aggregate(ctx context.Context, share ShareID, p vfs.SafePath) (cache.Aggregate, error) {
	root, ok := c.ShareRoot(share)
	if !ok {
		return cache.Aggregate{}, errf(ErrNotFound, "aggregate an unregistered share")
	}
	gen, err := c.cache.ShareGen(ctx, share)
	if err != nil {
		return cache.Aggregate{}, err
	}
	targetID, err := c.ensureFileIDChain(ctx, root, share, p)
	if err != nil {
		return cache.Aggregate{}, err
	}
	return c.computeAggregate(ctx, root, share, p, targetID, gen, nil, &map[cache.FileID]*aggGuard{})
}

// ensureFileIDChain walks from the share root to p, allocating (or reusing) a
// file id for every directory component along the way. Files never need one
// for a rollup.
func (c *Core) ensureFileIDChain(
	ctx context.Context, root *vfs.ShareRoot, share ShareID, p vfs.SafePath,
) (cache.FileID, error) {
	if p.IsRoot() {
		// The share root has no parent to be named under, so nothing is ever
		// inserted for it; its rollup is still cached, keyed by the sentinel.
		return cache.RootID, nil
	}
	var curID cache.FileID = cache.RootID
	curPath := vfs.RootPath()
	for _, comp := range p.Components() {
		var jerr error
		curPath, jerr = curPath.JoinExisting(comp)
		if jerr != nil {
			return 0, jerr
		}
		st, serr := root.Stat(curPath)
		if serr != nil {
			return 0, mapVFSErr(serr)
		}
		id, uerr := c.upsertDir(ctx, share, curID, comp, st)
		if uerr != nil {
			return 0, uerr
		}
		curID = id
	}
	return curID, nil
}

// upsertDir allocates a stable id for a directory, best-effort: a rollup that
// cannot mint an id is a rollup that cannot be cached, which is a slower
// listing, not a failure.
func (c *Core) upsertDir(ctx context.Context, share ShareID, parent cache.FileID, name string, st vfs.Stat) (cache.FileID, error) {
	var id cache.FileID
	err := c.cache.Write(ctx, func(tx *sql.Tx) error {
		var ierr error
		id, ierr = c.cache.Upsert(ctx, tx, share, parent, name, st)
		return ierr
	})
	return id, err
}

// computeAggregate is the recursive rollup. visited guards against a cycle
// that a hard-linked directory could present, though the walk never follows
// symlinks and the path bound is enforced on every join.
func (c *Core) computeAggregate(
	ctx context.Context,
	root *vfs.ShareRoot,
	share ShareID,
	dirPath vfs.SafePath,
	dirID cache.FileID,
	gen uint64,
	held []cache.FileID,
	guards *map[cache.FileID]*aggGuard,
) (cache.Aggregate, error) {
	if agg, ok, err := c.cache.DirEtag(ctx, share, dirID); err != nil {
		return cache.Aggregate{}, err
	} else if ok {
		return agg, nil
	}

	// Single-flight: only one caller computes a given directory's rollup at a
	// time. The guard is keyed by the file id, and re-entrant calls (a native
	// directory and a hard-linked alias can share one) skip re-acquiring.
	guard, ok := (*guards)[dirID]
	if !ok {
		guard = &aggGuard{}
		(*guards)[dirID] = guard
	}
	acquired := false
	if !containsFileID(held, dirID) {
		held = append(held, dirID)
		guard.mu.Lock()
		acquired = true
		defer func() {
			if acquired {
				guard.mu.Unlock()
			}
		}()
	}
	if agg, ok, err := c.cache.DirEtag(ctx, share, dirID); err != nil {
		return cache.Aggregate{}, err
	} else if ok {
		return agg, nil
	}

	dirStat, err := root.Stat(dirPath)
	if err != nil {
		return cache.Aggregate{}, mapVFSErr(err)
	}
	if !dirStat.Kind.IsDir() {
		return cache.Aggregate{}, errf(ErrNotFound, "aggregate a path that is not a directory")
	}

	entries, err := root.ReadDir(dirPath, vfs.HideReserved)
	if err != nil {
		return cache.Aggregate{}, mapVFSErr(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	insertionSort(names)

	hasher := blake3.New(32, nil)
	var rsize, rcount uint64
	for _, name := range names {
		childPath, jerr := dirPath.JoinExisting(name)
		if jerr != nil {
			continue
		}
		st, serr := root.Stat(childPath)
		if serr != nil {
			continue
		}
		var childEtag string
		var size, count uint64
		if st.Kind.IsDir() {
			childID, aerr := c.upsertDir(ctx, share, dirID, name, st)
			if aerr != nil {
				return cache.Aggregate{}, aerr
			}
			agg, aerr := c.computeAggregate(ctx, root, share, childPath, childID, gen, held, guards)
			if aerr != nil {
				return cache.Aggregate{}, aerr
			}
			childEtag, size, count = agg.Etag, agg.RSize, agg.RCount
		} else {
			childEtag, _ = FileETag(st)
			size, count = st.Size, 1
		}
		hasher.Write([]byte(name))
		hasher.Write([]byte(childEtag))
		rsize += size
		rcount += count
	}

	agg := cache.Aggregate{
		Etag:   hex.EncodeToString(hasher.Sum(nil)[:16]),
		RSize:  rsize,
		RCount: rcount,
	}
	if err := c.cache.Write(ctx, func(tx *sql.Tx) error {
		return c.cache.PutDirEtag(ctx, tx, share, dirID, agg, gen)
	}); err != nil {
		// A rollup that cannot be stored is a slower next listing, not a
		// failure: the walk already committed nothing, so the caller keeps
		// the value it computed.
		c.logger.Warn("a directory aggregate could not be cached", slog.Any("error", err))
	}
	return agg, nil
}

func containsFileID(held []cache.FileID, id cache.FileID) bool {
	for _, h := range held {
		if h == id {
			return true
		}
	}
	return false
}

func insertionSort(names []string) {
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
}

// InvalidateShare is the O(1) whole-share invalidation: bump the generation
// counter so every cached aggregate reads as stale on its next lookup, without
// walking or naming a single path. The watcher calls this when it loses a
// batch of events.
func (c *Core) InvalidateShare(ctx context.Context, share ShareID) error {
	return c.cache.Write(ctx, func(tx *sql.Tx) error {
		_, err := c.cache.BumpShareGen(ctx, tx, share)
		return err
	})
}

// markDirty marks the ancestor chain of path (up to the share root) dirty, so
// their cached aggregates are recomputed. Best-effort: the write already
// committed, so a failure here only costs a stale aggregate that a later
// generation bump or read corrects.
func (c *Core) markDirty(ctx context.Context, share ShareID, p vfs.SafePath) {
	root, ok := c.ShareRoot(share)
	if !ok {
		return
	}
	// The share root's aggregate is cached under the sentinel id that is never
	// a real row, so it is pushed unconditionally.
	chain := []cache.FileID{cache.RootID}
	cur := vfs.RootPath()
	for _, comp := range p.Parent().Components() {
		var jerr error
		cur, jerr = cur.JoinExisting(comp)
		if jerr != nil {
			break
		}
		st, serr := root.Stat(cur)
		if serr != nil {
			continue
		}
		if id, found, lerr := c.cache.Lookup(ctx, share, st); lerr == nil && found {
			chain = append(chain, id)
		}
	}
	if err := c.cache.Write(ctx, func(tx *sql.Tx) error {
		return c.cache.MarkDirty(ctx, tx, share, chain)
	}); err != nil {
		c.logger.Warn("dirty-marking failed; invalidating the whole share instead",
			slog.Uint64("share", uint64(share)), slog.Any("error", err))
		if ierr := c.InvalidateShare(ctx, share); ierr != nil {
			c.logger.Error("whole-share invalidation also failed; cached directory ETags may be stale",
				slog.Uint64("share", uint64(share)), slog.Any("error", ierr))
		}
	}
}
