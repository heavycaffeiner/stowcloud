//go:build linux

package core

import (
	"context"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"slices"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
	"lukechampine.com/blake3"
)

// Aggregate is a directory's recursive rollup: its own ETag plus the size
// and count of everything beneath it.
type Aggregate = cache.Aggregate

// aggEtagBytes is how much of the blake3 sum becomes the ETag. Sixteen bytes
// of a 32-byte digest, hex-rendered, is the token clients compare.
const aggEtagBytes = 16

// Aggregate computes the rollup for a directory, reading from the cache when
// a fresh row is there and recomputing when it is not.
func (c *Core) Aggregate(ctx context.Context, share ShareID, p vfs.SafePath) (Aggregate, error) {
	root, ok := c.ShareRoot(share)
	if !ok {
		return Aggregate{}, errf(ErrNotFound, "aggregate an unregistered share")
	}
	// Read once, up front: every row this call writes is stamped with this
	// generation, so a bump landing mid-walk makes the whole batch stale
	// together rather than half of it.
	gen, err := c.cache.ShareGen(ctx, share)
	if err != nil {
		return Aggregate{}, err
	}
	target, err := c.ensureFileIDChain(ctx, root, share, p)
	if err != nil {
		return Aggregate{}, err
	}
	return c.computeAggregate(ctx, aggWalk{
		root:   root,
		share:  share,
		gen:    gen,
		guards: map[ident.FileID]*sync.Mutex{},
	}, p, target, nil)
}

// aggWalk is what every level of one rollup shares. It is a value so a
// recursion does not carry six arguments that never change between levels.
//
// guards is per top-level call: it serializes the walk against itself, which
// matters because a directory and a hard-linked alias of it can share one
// file id and appear twice in a single walk. Two concurrent Aggregate calls
// may duplicate work, which is accepted: the computation is idempotent and a
// process-wide lock table would be shared mutable state serving a rare race.
type aggWalk struct {
	root   vfs.Root
	share  ShareID
	gen    uint64
	guards map[ident.FileID]*sync.Mutex
}

func (w aggWalk) guard(id ident.FileID) *sync.Mutex {
	g, ok := w.guards[id]
	if !ok {
		g = &sync.Mutex{}
		w.guards[id] = g
	}
	return g
}

// ensureFileIDChain allocates or reuses a stable id for every directory
// component from the share root down to p.
//
// Ids are minted here rather than on every listing because a rollup is the
// one thing that needs a directory's stable id to exist; minting them
// anywhere else would grow the cache with rows nothing reads. The share root
// is the sentinel, which has no row of its own.
func (c *Core) ensureFileIDChain(
	ctx context.Context, root vfs.Root, share ShareID, p vfs.SafePath,
) (ident.FileID, error) {
	id := ident.RootID
	cur := vfs.RootPath()
	for _, comp := range p.Components() {
		next, jerr := cur.JoinExisting(comp)
		if jerr != nil {
			return 0, mapVFSErr(jerr)
		}
		cur = next
		st, serr := root.Stat(cur)
		if serr != nil {
			return 0, mapVFSErr(serr)
		}
		id, serr = c.upsertDir(ctx, share, id, comp, st)
		if serr != nil {
			return 0, serr
		}
	}
	return id, nil
}

// upsertDir allocates a stable id for one directory, or returns the existing
// one. An error propagates: the rollup cannot key a cached row without it.
func (c *Core) upsertDir(
	ctx context.Context, share ShareID, parent ident.FileID, name string, st vfs.Stat,
) (ident.FileID, error) {
	var id ident.FileID
	err := c.cache.Write(ctx, func(tx *sql.Tx) error {
		var uerr error
		id, uerr = c.cache.Upsert(ctx, tx, share, parent, name, st)
		return uerr
	})
	return id, err
}

// computeAggregate rolls one directory up, recursing into its subdirectories.
//
// held is the file ids this call chain already locked. Re-entering an id
// skips the lock rather than deadlocking on it, which also bounds a
// hard-link cycle: the second visit reads the cache or recomputes without
// descending behind its own guard forever.
func (c *Core) computeAggregate(
	ctx context.Context, w aggWalk, dir vfs.SafePath, id ident.FileID, held []ident.FileID,
) (Aggregate, error) {
	if agg, ok, err := c.cache.DirEtag(ctx, w.share, id); err != nil {
		return Aggregate{}, err
	} else if ok {
		return agg, nil
	}

	if !slices.Contains(held, id) {
		g := w.guard(id)
		g.Lock()
		defer g.Unlock()
		held = append(held, id)

		// The branch that held the guard first may have stored the answer
		// while this one waited.
		if agg, ok, err := c.cache.DirEtag(ctx, w.share, id); err != nil {
			return Aggregate{}, err
		} else if ok {
			return agg, nil
		}
	}

	st, err := w.root.Stat(dir)
	if err != nil {
		return Aggregate{}, mapVFSErr(err)
	}
	if !st.Kind.IsDir() {
		return Aggregate{}, errf(ErrNotFound, "aggregate a path that is not a directory")
	}
	entries, err := w.root.ReadDir(dir, vfs.HideReserved)
	if err != nil {
		return Aggregate{}, mapVFSErr(err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	// The hash is over an ordered sequence, so the order is part of the
	// ETag's definition and cannot be whatever readdir returned.
	slices.Sort(names)

	hasher := blake3.New(32, nil)
	var rsize, rcount uint64
	for _, name := range names {
		child, jerr := dir.JoinExisting(name)
		if jerr != nil {
			continue
		}
		cst, serr := w.root.Stat(child)
		if serr != nil {
			// It vanished between the listing and the stat. The listing
			// races the world by design, so the child is skipped.
			continue
		}

		etag, size, count := "", cst.Size, uint64(1)
		if cst.Kind.IsDir() {
			childID, uerr := c.upsertDir(ctx, w.share, id, name, cst)
			if uerr != nil {
				return Aggregate{}, uerr
			}
			agg, aerr := c.computeAggregate(ctx, w, child, childID, held)
			if aerr != nil {
				return Aggregate{}, aerr
			}
			etag, size, count = agg.Etag, agg.RSize, agg.RCount
		} else {
			etag, _ = FileETag(cst)
		}
		// A hash writer never fails, and blake3's own Write says so; the
		// errors are read anyway so the check is not a convention with an
		// exception in it.
		if _, werr := hasher.Write([]byte(name)); werr != nil {
			return Aggregate{}, werr
		}
		if _, werr := hasher.Write([]byte(etag)); werr != nil {
			return Aggregate{}, werr
		}
		rsize += size
		rcount += count
	}

	agg := Aggregate{
		Etag:   hex.EncodeToString(hasher.Sum(nil)[:aggEtagBytes]),
		RSize:  rsize,
		RCount: rcount,
	}
	if err := c.cache.Write(ctx, func(tx *sql.Tx) error {
		return c.cache.PutDirEtag(ctx, tx, w.share, id, agg, w.gen)
	}); err != nil {
		// A rollup that cannot be cached is a slower next listing, not a
		// failure. Nothing here committed anything that needs undoing.
		c.warn("a directory aggregate could not be cached", slog.Any("error", err))
	}
	return agg, nil
}

// InvalidateShare performs constant-time invalidation of an entire share by
// advancing the generation, which makes every cached row read as stale without
// walking or naming any path. The filesystem watcher invokes it after dropping a
// batch of events, when it can no longer identify which paths changed.
func (c *Core) InvalidateShare(ctx context.Context, share ShareID) error {
	return c.cache.Write(ctx, func(tx *sql.Tx) error {
		_, err := c.cache.BumpShareGen(ctx, tx, share)
		return err
	})
}

// InvalidateDir marks a directory and its ancestors stale after a change this
// process did not make: a write over the file-sharing protocol, a sync client,
// or an operator editing the share on disk. The watcher reports which
// directory moved and nothing else here knows the bytes did, so without this
// a cached rollup stays stale until an API write happens to touch the same
// chain. Listings are unaffected either way: they are read from disk.
func (c *Core) InvalidateDir(ctx context.Context, share ShareID, dir vfs.SafePath) {
	c.markChainDirty(ctx, share, dir)
}

// markDirty invalidates the ancestor chain of a path every mutation touched.
//
// The path is the entry that changed, so its parent is the shallowest
// directory whose contents moved.
func (c *Core) markDirty(ctx context.Context, share ShareID, p vfs.SafePath) {
	c.markChainDirty(ctx, share, p.Parent())
}

// markChainDirty marks dir and every directory above it.
//
// Best-effort with a two-level fallback, because the filesystem write has
// already committed: a failed marking falls back to invalidating the whole
// share, which is correct and merely coarse, and a failure of that is logged
// against the chance a later bump or recompute corrects it.
func (c *Core) markChainDirty(ctx context.Context, share ShareID, dir vfs.SafePath) {
	root, ok := c.ShareRoot(share)
	if !ok {
		// The share went away under a racing admin action. There is
		// nothing left to invalidate.
		return
	}

	// The root sentinel unconditionally: it has no row to look up, and its
	// aggregate covers a change anywhere in the share.
	chain := []ident.FileID{ident.RootID}
	cur := vfs.RootPath()
	for _, comp := range dir.Components() {
		next, jerr := cur.JoinExisting(comp)
		if jerr != nil {
			break
		}
		cur = next
		st, serr := root.Stat(cur)
		if serr != nil {
			continue
		}
		// Lookup, not Upsert: a directory with no cached row has no stale
		// aggregate to mark, and marking is not a reason to mint an id.
		if id, found, lerr := c.cache.Lookup(ctx, share, st); lerr == nil && found {
			chain = append(chain, id)
		}
	}

	if err := c.cache.Write(ctx, func(tx *sql.Tx) error {
		return c.cache.MarkDirty(ctx, tx, share, chain)
	}); err != nil {
		c.warn("dirty-marking failed; invalidating the whole share instead",
			slog.Uint64("share", uint64(share)), slog.Any("error", err))
		if ierr := c.InvalidateShare(ctx, share); ierr != nil {
			c.logger.Error("whole-share invalidation also failed; cached directory ETags may be stale",
				slog.Uint64("share", uint64(share)), slog.Any("error", ierr))
		}
	}
}
