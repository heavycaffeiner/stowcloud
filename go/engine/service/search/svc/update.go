//go:build linux

package svc

import (
	"context"
	"log/slog"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
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

// Change is one directory the watcher says moved.
//
// Declared here rather than imported from the watcher: this package sits below
// it, and a sensor's event type is not something the index should have to
// depend on to be testable. The watcher's own event converts to this at the
// wiring, which is one struct literal.
type Change struct {
	Share uint32
	// Dir is share-relative, and empty when All is set.
	Dir string
	// All says events were lost, so what changed is exactly what is unknown.
	All bool
}

// updateQueue bounds the directories waiting to be reconciled.
//
// A full queue drops rather than blocking the watcher: the watcher feeds the
// change channel every connected client reads, and an index update must never
// be what makes a listing go stale in a browser.
//
// The drop is safe because of a chain of three, and every link is load-bearing:
// a dropped update costs a stale index entry and nothing more; the walk tier
// still finds the file, because the index is never the only answer; and stat
// revalidation on the query path hides a deleted file the index still names.
// Remove any one and the drop becomes real data loss.
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

	queue chan Change
	// saidFull keeps the ceiling from being logged per event. It is touched
	// only from Run's own goroutine, which is the one place reconcile runs.
	saidFull bool
	// entryCeiling overrides the compiled-in bound. Zero means that bound,
	// which is what the product runs with; a test sets a small one rather than
	// building five million entries to reach the real one.
	entryCeiling uint64
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
		queue:   make(chan Change, updateQueue),
	}
}

// Offer hands one event to the updater without blocking.
//
// The caller is the watcher's fan-out and it must not be held up: a dropped
// update leaves the index short of the corpus, which is a slower answer, while
// a blocked fan-out leaves every connected client's listing stale, which is a
// wrong one.
func (u *Updater) Offer(ev Change) {
	select {
	case u.queue <- ev:
	default:
		u.log.Warn("the search index update queue is full; an update was dropped",
			"share", ev.Share)
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
func (u *Updater) apply(ctx context.Context, ev Change) {
	ix := u.svc.index()
	if ix == nil {
		// The index was switched off since this event was queued. Nothing to
		// keep current, and the queue drains rather than backing up.
		return
	}
	src, ok := u.sourceOf(ev.Share)
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
			"share", ev.Share)
		return
	}

	if err := u.reconcile(ctx, ix, src, ev.Dir); err != nil {
		u.log.Warn("a directory could not be reconciled into the search index",
			"share", ev.Share, "error", err)
	}
}

// full reports whether the index has reached the size a build stops at.
//
// The build has always been bounded and this was not, so a corpus that grew
// past what a build covers kept growing the index and with it the cost of
// every merge. The two now stop at the same place, which is what makes "the
// index holds less than the corpus" one condition rather than two.
//
// Removals still apply when it is full: refusing those would leave an index
// that can only grow, which is the opposite of a bound.
func (u *Updater) full(ix *index.NameIndex) bool {
	return ix.Stats().Entries >= u.ceiling()
}

// ceiling is the bound this updater stops adding at. It is the same one the
// build stops at and the same one Open reads to decide that what it loaded is
// short of its corpus: three places, one constant.
func (u *Updater) ceiling() uint64 {
	if u.entryCeiling > 0 {
		return u.entryCeiling
	}
	return limits.CorpusScanEntries
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
	if src.Root == nil {
		return nil
	}

	// The same revalidation the query path uses: a path off the watcher is no
	// more trusted than a path off disk.
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

	// Removals first, and unconditionally. An index at its bound still has to
	// forget a deleted file: a bound that stops removals is one that can only
	// grow, and a stale entry is worse than a missing one because the query
	// path spends a stat on it before dropping it.
	if len(removed) > 0 {
		if err := ix.Tombstone(removed); err != nil {
			return err
		}
	}
	if len(added) > 0 {
		if u.full(ix) {
			// The same ceiling a build stops at. The index is marked short of
			// its corpus, which makes every query decline and take the walk:
			// an index that answered from what it has would return a result
			// missing this file with a success status, and nothing would say
			// the result was short.
			ix.SetIncomplete(true)
			u.reportFull()
			return nil
		}
		if err := ix.Append(added); err != nil {
			return err
		}
	}
	return nil
}

// reportFull logs the ceiling once rather than per event.
//
// A tree past the bound produces an event per change, and a line each would
// make the condition invisible inside its own noise.
func (u *Updater) reportFull() {
	if u.saidFull {
		return
	}
	u.saidFull = true
	u.log.Warn("the search index has reached its entry ceiling, so new files are not being indexed; "+
		"searches for them fall back to a walk, which is slower and always current",
		"ceiling", u.ceiling())
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
