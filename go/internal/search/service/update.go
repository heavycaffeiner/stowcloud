//go:build linux

package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/search"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/index"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
	"github.com/heavycaffeiner/stowcloud/go/internal/watch"
)

// Keeping the index current after a build.
//
// Without this the index is filled once and then frozen. That failure is worse
// than it sounds, because it is silent in both directions a person can check.
// A query the index can answer is answered from the index alone, so a file
// created after the build is absent from the result and nothing says the
// result is short: a person searches for a file they made this morning, finds
// nothing, and concludes it is not there.
//
// The watcher cannot say what changed, only where. Its events name a directory
// whose entries moved, and one event says the whole share is stale because
// events were lost. So an update is a re-read of that directory compared
// against what the index holds for it, and that comparison is this file.
//
// Deletion is handled by the query path already: a hit is revalidated with a
// stat before it is returned, so an entry for a file that is gone is dropped.
// Tombstoning is still done here, because an entry nothing removes is one the
// next merge writes into the base segment and one every query keeps scanning.

// updateQueue bounds the directories waiting to be reconciled.
//
// A full queue drops the oldest rather than blocking the watcher: the watcher
// feeds the change channel every connected client reads, and an index update
// must never be what makes a listing go stale in a browser. A dropped update
// costs a stale index entry, which the walk fallback and the stat revalidation
// already cover.
const updateQueue = 4096

// mergeInterval is how often the updater considers collapsing the overlay.
//
// Not per update: a merge rewrites the base segment, and doing that on a busy
// tree would be a rebuild per change. The gate is the ratio the index already
// enforces; this is only how often it is asked.
const mergeInterval = 5 * time.Minute

// Updater keeps the index current from the watcher's events.
type Updater struct {
	svc     *Service
	sources func() []search.Source
	log     *slog.Logger

	queue chan watch.InvalEvent
}

// NewUpdater builds one. sources is read per event rather than captured, so a
// share added or removed after startup is seen.
func NewUpdater(svc *Service, sources func() []search.Source, log *slog.Logger) *Updater {
	if log == nil {
		log = slog.Default()
	}
	return &Updater{
		svc:     svc,
		sources: sources,
		log:     log,
		queue:   make(chan watch.InvalEvent, updateQueue),
	}
}

// Offer hands one event to the updater without blocking.
//
// The caller is the watcher's fan-out and it must not be held up: a dropped
// update leaves the index short of the corpus, which is a slower answer, while
// a blocked fan-out leaves every connected client's listing stale, which is a
// wrong one.
func (u *Updater) Offer(ev watch.InvalEvent) {
	select {
	case u.queue <- ev:
	default:
		u.log.Warn("the search index update queue is full; an update was dropped",
			"share", uint32(ev.Share))
	}
}

// Run consumes the queue until the context ends. It is one goroutine: the
// index serialises writes anyway, so a second one would only queue on the
// lock.
func (u *Updater) Run(ctx context.Context) {
	merge := time.NewTicker(mergeInterval)
	defer merge.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-u.queue:
			u.apply(ctx, ev)
		case <-merge.C:
			u.maybeMerge(ctx)
		}
	}
}

// apply reconciles one event.
func (u *Updater) apply(ctx context.Context, ev watch.InvalEvent) {
	ix := u.svc.index()
	if ix == nil {
		// The index was switched off since this event was queued. Nothing to
		// keep current, and the queue drains rather than backing up.
		return
	}
	src, ok := u.sourceOf(uint32(ev.Share))
	if !ok {
		return
	}

	if ev.All {
		// The watcher lost events, so what changed is exactly what is unknown.
		// There is nothing to replay and no directory to re-read.
		//
		// The index is left as it is rather than dropped: a stale index still
		// answers most queries correctly, and dropping it turns every query
		// into a walk until somebody notices and rebuilds. What this does is
		// say so, because a share that has gone unreconcilable is the one case
		// on this path an operator has to act on.
		u.log.Warn("change events were lost, so the search index for this share is now behind; rebuild it to catch up",
			"share", uint32(ev.Share))
		return
	}

	if err := u.reconcile(ctx, ix, src, ev.Dir); err != nil {
		u.log.Warn("a directory could not be reconciled into the search index",
			"share", uint32(ev.Share), "error", err)
	}
}

// reconcile compares one directory's listing against what the index holds for
// it, and writes only the difference.
//
// Only the difference, rather than re-appending the listing: an append is a
// delta record, and re-appending an unchanged directory on every touch of one
// file inside it grows the overlay until the merge that collapses it is the
// only thing the index does.
func (u *Updater) reconcile(ctx context.Context, ix *index.NameIndex, src search.Source, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	dirPath, perr := pathUnder(src, dir)
	if perr != nil {
		// A path the validator refuses is not one this index should hold
		// entries for either. It is not an error: the watcher reports what the
		// kernel said, and the kernel has no opinion about this server's rules.
		return nil
	}

	held, herr := ix.ChildrenOf(src.Share, dirPath.String())
	if herr != nil {
		return herr
	}

	onDisk := map[string]bool{}
	entries, rerr := src.Root.ReadDir(dirPath, vfs.HideReserved)
	if rerr == nil {
		for _, e := range entries {
			if e.Kind.IsDir() {
				// Directories are not indexed: the build indexes files and
				// descends through directories, so holding one here would put
				// a name in the index that a query returns and a stat then
				// resolves to a directory the walk never offered.
				continue
			}
			child, jerr := dirPath.JoinExisting(e.Name)
			if jerr != nil {
				continue
			}
			onDisk[child.String()] = true
		}
	}
	// A directory that could not be read is treated as empty, which tombstones
	// what the index held for it. That is the right answer for the common
	// cause, which is the directory having been deleted, and a wrong-but-safe
	// one for a permission change: the entries come back on the next event or
	// the next build, and until then those queries fall back to the walk.

	var added, removed []index.Entry
	heldSet := make(map[string]bool, len(held))
	for _, p := range held {
		heldSet[p] = true
		if !onDisk[p] {
			removed = append(removed, index.Entry{Share: src.Share, Path: p})
		}
	}
	for p := range onDisk {
		if !heldSet[p] {
			added = append(added, index.Entry{Share: src.Share, Path: p})
		}
	}

	if len(added) > 0 {
		if err := ix.Append(added); err != nil {
			return err
		}
	}
	if len(removed) > 0 {
		if err := ix.Tombstone(removed); err != nil {
			return err
		}
	}
	return nil
}

// maybeMerge collapses the overlay when it has outgrown its share of the base.
//
// The ratio is the index's own gate; this only decides how often it is asked.
// A merge rewrites the base segment, so it runs on this timer and never on a
// request path.
func (u *Updater) maybeMerge(ctx context.Context) {
	ix := u.svc.index()
	if ix == nil || !ix.NeedsMerge() {
		return
	}
	if err := ix.Merge(ctx, func() bool { return ctx.Err() == nil }); err != nil {
		u.log.Warn("the search index could not be merged", "error", err)
	}
}

// sourceOf finds the share this event belongs to.
func (u *Updater) sourceOf(share uint32) (search.Source, bool) {
	for _, src := range u.sources() {
		if src.Share == share {
			return src, true
		}
	}
	return search.Source{}, false
}
