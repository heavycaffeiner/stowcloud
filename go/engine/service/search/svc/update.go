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

// Keeping the index current once a build has finished.
//
// Without this the index fills once and then freezes. That failure is worse than
// it sounds, because it stays silent in both directions a person might check.
// Any query the index can serve is answered from the index alone, so a file
// created after the build is missing from the result with nothing indicating the
// result is short. Someone searches for a file they made this morning, finds
// nothing, and concludes it does not exist.
//
// The watcher reports where a change occurred, never what changed. Its events
// name a directory whose entries moved, and one event declares an entire share
// stale because events were lost. An update therefore re-reads that directory
// and compares it against what the index holds, and this file performs that
// comparison.
//
// The query path already handles deletion: every hit is revalidated by a stat
// before being returned, so entries for vanished files are dropped. Tombstoning
// still happens here, because an entry nothing removes is one the next merge
// commits into the base segment and one every query continues to scan.

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

// updateQueue limits how many directories may await reconciliation.
//
// A full queue discards rather than blocking the watcher, because the watcher
// feeds the change channel every connected client reads and an index update must
// never be what leaves a listing stale in a browser.
//
// Discarding is safe thanks to three properties, each of which carries weight: a
// dropped update costs one stale index entry and nothing further; the walk tier
// still locates the file, since the index is never the sole answer; and stat
// revalidation on the query path conceals a deleted file the index still lists.
// Remove any one and discarding becomes genuine data loss.
const updateQueue = 4096

// mergeInterval sets how frequently the updater weighs collapsing the overlay.
//
// Not once per update: a merge rewrites the base segment, and doing so on a busy
// tree would amount to a rebuild for every change. The ratio the index already
// enforces acts as the gate; this only controls how often it is consulted.
const mergeInterval = 5 * time.Minute

// Updater maintains the index using the watcher's events.
type Updater struct {
	svc     *Service
	sources func() []search.Source
	log     *slog.Logger

	queue chan Change
	// saidFull prevents the ceiling being logged for every event. Only Run's own
	// goroutine touches it, and that is the sole place reconcile executes.
	saidFull bool
	// entryCeiling replaces the compiled-in bound. Zero selects that bound, which
	// is what the product uses; a test supplies a small value instead of
	// constructing five million entries to reach the real one.
	entryCeiling uint64
}

// NewUpdater constructs one. sources is consulted per event rather than captured
// up front, so shares added or removed after startup are noticed.
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

// Offer passes one event to the updater without blocking.
//
// The caller is the watcher's fan-out and must never be delayed. A dropped
// update leaves the index covering less than the corpus, producing a slower
// answer, whereas a blocked fan-out leaves every connected client's listing
// stale, producing a wrong one.
func (u *Updater) Offer(ev Change) {
	select {
	case u.queue <- ev:
	default:
		u.log.Warn("the search index update queue is full; an update was dropped",
			"share", ev.Share)
	}
}

// Run drains the queue until the context ends. A single goroutine suffices,
// since the index already serialises writes and a second would merely wait on
// the lock.
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
		// The index was disabled after this event was queued. Nothing remains to
		// keep current, and the queue drains instead of accumulating.
		return
	}
	src, ok := u.sourceOf(ev.Share)
	if !ok {
		return
	}

	if ev.All {
		// The watcher lost events, so precisely what changed is what nobody
		// knows. Nothing can be replayed and no directory can be re-read.
		//
		// The index is retained rather than discarded. A stale index still
		// answers most queries correctly, while discarding it converts every
		// query into a walk until someone notices and rebuilds. What happens
		// instead is a log line, since a share that has become unreconcilable is
		// the one case on this path requiring an operator's attention.
		u.log.Warn("change events were lost, so the search index for this share is now behind; rebuild it to catch up",
			"share", ev.Share)
		return
	}

	if err := u.reconcile(ctx, ix, src, ev.Dir); err != nil {
		u.log.Warn("a directory could not be reconciled into the search index",
			"share", ev.Share, "error", err)
	}
}

// full reports whether the index has reached the size at which a build stops.
//
// Builds have always been bounded while this path was not, so a corpus growing
// past what a build covers kept enlarging the index along with the cost of every
// merge. Both now stop at the same point, which reduces the index covering less
// than the corpus to a single condition rather than two.
//
// Removals still apply once full. Rejecting them would produce an index capable
// only of growing, the opposite of a bound.
func (u *Updater) full(ix *index.NameIndex) bool {
	return ix.Stats().Entries >= u.ceiling()
}

// ceiling gives the bound at which this updater stops adding. It matches the
// bound the build stops at and the one Open consults to decide that what it
// loaded covers less than its corpus: three places sharing one constant.
func (u *Updater) ceiling() uint64 {
	if u.entryCeiling > 0 {
		return u.entryCeiling
	}
	return limits.CorpusScanEntries
}

// reconcile compares a directory's listing against what the index holds for it
// and writes only what differs.
//
// Only the difference, rather than re-appending the listing. Each append becomes
// a delta record, and re-appending an unchanged directory whenever one file
// inside it is touched grows the overlay until collapsing it is all the index
// ever does.
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
		// A path the validator rejects is equally one this index should not hold
		// entries for. It is not an error: the watcher relays what the kernel
		// said, and the kernel holds no view on this server's rules.
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
				// Directories go unindexed. The build indexes files while
				// descending through directories, so keeping one here would
				// place a name in the index that a query returns and a stat
				// then resolves to a directory the walk never offered.
				continue
			}
			child, jerr := dirPath.JoinExisting(e.Name)
			if jerr != nil {
				continue
			}
			onDisk[child.String()] = true
		}
	}
	// An unreadable directory is treated as empty, tombstoning whatever the
	// index held for it. That is correct for the usual cause, the directory
	// having been deleted, and wrong but safe for a permission change: the
	// entries return on the next event or the next build, and until then those
	// queries fall back to walking.

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

	// Removals come first and apply unconditionally. An index at its bound must
	// still forget a deleted file, since a bound that blocks removals produces
	// something capable only of growing, and a stale entry costs more than a
	// missing one because the query path spends a stat before discarding it.
	if len(removed) > 0 {
		if err := ix.Tombstone(removed); err != nil {
			return err
		}
	}
	if len(added) > 0 {
		if u.full(ix) {
			// The same ceiling at which a build stops. The index is flagged as
			// covering less than its corpus, making every query decline in
			// favour of the walk. An index answering from what it holds would
			// return a result missing this file while reporting success, with
			// nothing to indicate the result was incomplete.
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

// reportFull logs the ceiling a single time rather than for every event.
//
// A tree beyond the bound generates an event per change, and one line each would
// bury the condition inside its own noise.
func (u *Updater) reportFull() {
	if u.saidFull {
		return
	}
	u.saidFull = true
	u.log.Warn("the search index has reached its entry ceiling, so new files are not being indexed; "+
		"searches for them fall back to a walk, which is slower and always current",
		"ceiling", u.ceiling())
}

// maybeMerge collapses the overlay once it exceeds its allowance against the
// base.
//
// The ratio serves as the index's own gate, and this only governs how often it is
// consulted. A merge rewrites the base segment, so it runs on this timer and
// never along a request path.
func (u *Updater) maybeMerge(ctx context.Context) {
	ix := u.svc.index()
	if ix == nil || !ix.NeedsMerge() {
		return
	}
	if err := ix.Merge(ctx, func() bool { return ctx.Err() == nil }); err != nil {
		u.log.Warn("the search index could not be merged", "error", err)
	}
}

// sourceOf locates the share an event belongs to.
func (u *Updater) sourceOf(share uint32) (search.Source, bool) {
	for _, src := range u.sources() {
		if src.Share == share {
			return src, true
		}
	}
	return search.Source{}, false
}
