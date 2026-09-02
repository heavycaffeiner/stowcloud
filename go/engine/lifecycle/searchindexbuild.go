//go:build linux

// Building the name index, and holding one open across a restart.
//
// The index is an escalation rather than the default. Search answers by
// walking, and an index is what an operator adds once measurement shows the
// walk is too slow for their corpus: the estimate route sizes it, this one
// spends the traversal.
//
// Nothing here can fail a query. The index is a cache, so every failure below
// ends at the same place, a nil index and search on the walk, because a broken
// cache costs speed and never answers.
package lifecycle

import (
	"context"
	"math"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// indexDirName is where the index lives under the data directory. It is a
// fixed name so a restart finds what the last build wrote.
const indexDirName = "index"

// openSearchIndex attaches or detaches the name index against what the
// operator has currently asked for, and starts or stops the updater that
// keeps an attached index current from watcher events.
//
// The setting is what decides, not the directory's existence: an index left on
// disk after being switched off must not come back at the next restart, and an
// operator who switched it on before building expects the next build to have
// somewhere to go.
//
// Called at boot and again from loadSettings on every save, so a switch flips
// live: on opens the index and starts its updater, off drops the reference and
// stops the updater. A query already holding the old index finishes against
// it regardless, since Query takes its own local reference.
func (e *Engine) openSearchIndex(ctx context.Context) {
	on, err := e.State.IndexNameEnabled(ctx)
	if err != nil {
		// Unreadable is off. The walk answers either way, and guessing on
		// would open a directory the operator may have meant to leave alone.
		//
		// No test covers this branch and the mutation that opens the index
		// anyway is absorbed: reaching it needs the settings read to fail,
		// which happens when the database does, and by then Open has already
		// refused for a louder reason. It stays because the direction is the
		// point, and a later caller reaching this with a live database and a
		// broken read gets the quiet answer rather than a confident wrong one.
		e.logger.Warn("the search index setting could not be read; search runs on the walk",
			"error", err)
		return
	}
	if !on {
		// Detach rather than leave whatever was attached running. A caller
		// re-invoking this at save time is what makes disabling the setting
		// take hold immediately instead of being a silent no-op that leaves
		// the live index and its updater in place.
		e.Search.SetIndex(nil)
		e.stopSearchUpdater()
		return
	}

	ix, opened := svc.OpenIndex(indexDir(e.dataDir), index.DefaultConfig(), e.logger)
	if ix == nil {
		// OpenIndex has already said which of the three failures this was, and
		// the distinction is the point: a corrupt index wants a rebuild and an
		// unreadable one does not.
		return
	}
	if opened == svc.OpenAbsent {
		e.logger.Info("the search index is enabled and empty; build it to use it",
			"dir", indexDir(e.dataDir))
	}
	e.Search.SetIndex(ix)
	e.startSearchUpdater(ctx)
}

// startSearchUpdater starts the goroutine that keeps the attached index
// current from watcher events, stopping and replacing any updater already
// running: one updater runs against the currently attached index, never two
// and never one left over from before a toggle.
//
// sources is a closure over the engine's shares rather than a snapshot taken
// here, so a share created after the index was attached is covered by the
// next event without restarting the updater.
func (e *Engine) startSearchUpdater(ctx context.Context) {
	e.stopSearchUpdater()

	loop, stop := context.WithCancel(context.WithoutCancel(ctx))
	u := svc.NewUpdater(e.Search, func() []search.Source {
		return indexSourcesOf(e.Core.ScanSources())
	}, e.logger)

	e.searchUpdaterMu.Lock()
	e.searchUpdater = u
	e.searchUpdaterStop = stop
	e.searchUpdaterMu.Unlock()

	task.Go(loop, "search index updater", func() { u.Run(loop) })
}

// stopSearchUpdater halts the running updater, if any. Called before the
// index it feeds is dropped or replaced, and at shutdown, so an updater never
// keeps running against an index the search service no longer holds.
func (e *Engine) stopSearchUpdater() {
	e.searchUpdaterMu.Lock()
	stop := e.searchUpdaterStop
	e.searchUpdater = nil
	e.searchUpdaterStop = nil
	e.searchUpdaterMu.Unlock()
	if stop != nil {
		stop()
	}
}

// offerToSearchUpdater relays one watcher event to the running updater, if
// any. Safe with no index attached: the updater is nil and this is a no-op,
// which is the ordinary state for a deployment that never turned the index on.
func (e *Engine) offerToSearchUpdater(share uint32, dir string, all bool) {
	e.searchUpdaterMu.Lock()
	u := e.searchUpdater
	e.searchUpdaterMu.Unlock()
	if u == nil {
		return
	}
	u.Offer(svc.Change{Share: share, Dir: dir, All: all})
}

// indexDir is the one place the index's location is spelled.
func indexDir(dataDir string) string { return dataDir + "/" + indexDirName }

// adminIndexBuild starts a build and answers with the job that runs it.
//
// The build outlives the request by design. It traverses every share, which is
// minutes on a real corpus, so the client is told a job id and comes back for
// the result rather than holding a connection open across the walk.
func (e *Engine) adminIndexBuild(c *fiber.Ctx) error {
	owner, ok, written := e.admin(c)
	if !ok {
		return written
	}
	if e.Search.HasIndex() {
		// An index is open, so there is somewhere to build into.
		return e.startIndexBuild(c, int64(owner))
	}
	// Refused rather than started: a build with no index writes nothing and
	// would report a job that finished having done nothing at all.
	return refuse(c, apierr.Classified{
		Class: apierr.SubsystemUnavailable, Key: "search.index_disabled",
	})
}

// startIndexBuild records the job and runs the walk behind it.
func (e *Engine) startIndexBuild(c *fiber.Ctx, owner int64) error {
	// No item list. A build walks every share, so the request named no set of
	// paths and an interrupted one has none to hand back.
	id, err := e.State.CreateOp(c.UserContext(), owner, state.OpIndexBuild, 0, e.clock.Nanos(), nil)
	if err != nil {
		return failKnown(c, err)
	}

	// Detached, because the request's context ends when the response is
	// written and the build has barely started by then.
	ctx := context.WithoutCancel(c.UserContext())
	sources := indexSourcesOf(e.Core.ScanSources())

	task.Go(ctx, "index build", func() { e.runIndexBuild(ctx, id, sources) })

	// The job as the jobs surface reports it, so a client polls the same shape
	// it was handed rather than one this route spells only here.
	op, rerr := e.Core.Operation(c.UserContext(), core.UserID(owner), core.OperationID(id))
	if rerr != nil {
		return failKnown(c, rerr)
	}
	return writeJSON(c, fiber.StatusAccepted, handler.OperationOf(op))
}

// runIndexBuild is the walk, and the bookkeeping around it.
func (e *Engine) runIndexBuild(ctx context.Context, id int64, sources []search.Source) {
	// The gate reads the operation row rather than a flag captured here, so a
	// cancel reaches a build that is already running rather than only stopping
	// one that has not started.
	gate := func() bool {
		op, _, err := e.State.GetOp(ctx, id)
		if err != nil {
			// A row that cannot be read is not a reason to keep walking every
			// share. The build stops and what it wrote stays, which a query
			// falls back around.
			return false
		}
		return !op.Cancellation
	}

	started := e.clock.Now()
	progress, err := e.Search.Build(ctx, sources, gate, func(p svc.BuildProgress) {
		if perr := e.State.SetOpProgress(ctx, id, indexedCount(p.Files), ""); perr != nil {
			e.logger.Warn("the index build's progress could not be recorded", "error", perr)
		}
	})

	now := e.clock.Nanos()
	if err != nil {
		// Recorded as failed rather than done. No test reaches it and the
		// mutation reporting it as done is absorbed: a build fails when a
		// segment write fails, which takes a disk that is refusing writes, and
		// the fixture for that costs more than the branch. What it protects is
		// an operator reading "done" on a build that indexed nothing and
		// concluding their search is now fast.
		e.logger.Warn("the index build failed", "error", err, "files", progress.Files)
		if ferr := e.State.FinishOp(ctx, id, state.OpFailed,
			indexedCount(progress.Files), err.Error(), now, nil); ferr != nil {
			e.logger.Warn("the index build's failure could not be recorded", "error", ferr)
		}
		return
	}

	// What this build actually measured replaces the compiled-in guess for
	// every estimate after this one, on this deployment's own disk and
	// corpus. Skipped for too short an interval: a build that finished in
	// under a second produces a rate the clock's own resolution cannot
	// support.
	if elapsed := e.clock.Now().Sub(started); progress.Files > 0 && elapsed >= time.Second {
		rate := uint64(float64(progress.Files) / elapsed.Seconds())
		if rerr := e.State.SetIndexBuildRate(ctx, rate); rerr != nil {
			e.logger.Warn("the measured build rate could not be recorded", "error", rerr)
		}
	}

	// A build that stopped at its bound is finished, and says which. A query
	// for what it did not reach falls back to a walk, so an index short of its
	// corpus is a slower answer rather than a wrong one.
	message := ""
	if progress.Partial {
		message = "the corpus is larger than one build covers; a query beyond it falls back to a walk"
	}
	if ferr := e.State.FinishOp(ctx, id, state.OpDone,
		indexedCount(progress.Files), message, now, nil); ferr != nil {
		e.logger.Warn("the index build's completion could not be recorded", "error", ferr)
	}
}

// indexedCount narrows a file count into what an operation row records.
//
// A build is bounded well below the signed range, so this cannot lose a real
// count. It saturates rather than wrapping because the value is only ever
// reported: a progress figure that came back negative would read as a build
// that went backwards.
func indexedCount(files uint64) int64 {
	if files > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(files)
}
