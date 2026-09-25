//go:build linux

// Package controller owns the search index lifecycle and durable build jobs.
// HTTP adapters depend on this package for request-independent search work.
package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/search/stowcloud"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/search/svc"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/concurrency"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
	fsatomic "github.com/stowcloud/durablefs"
	searchlib "github.com/stowcloud/namesearch"
	"github.com/stowcloud/namesearch/index"
)

const indexDirName = ".scindex"
const legacyIndexDirName = "index"

var (
	ErrIndexDisabled = errors.New("search index is disabled")
	ErrIndexBuilding = errors.New("search index is already building")
)

// Options supplies the durable and live dependencies for the search runtime.
type Options struct {
	Core       *core.Core
	State      *state.DB
	Search     *svc.Service
	DataDir    string
	Clock      clock.Clock
	Logger     *slog.Logger
	Jobs       *concurrency.Group
	JobsCtx    context.Context
	JobsStop   context.CancelFunc
	HasWatcher func() bool
}

// Controller owns index attachment, freshness recovery, watcher updates, and
// the durable index-build operation. It has no HTTP dependency.
type Controller struct {
	Core              *core.Core
	State             *state.DB
	Search            *svc.Service
	DataDir           string
	Clock             clock.Clock
	Logger            *slog.Logger
	Jobs              *concurrency.Group
	JobsCtx           context.Context
	JobsStop          context.CancelFunc
	HasWatcher        func() bool
	indexBuilding     atomic.Bool
	indexRecovery     atomic.Bool
	searchUpdaterMu   sync.Mutex
	searchUpdater     *svc.Updater
	searchUpdaterStop context.CancelFunc
}

func New(o Options) *Controller {
	return &Controller{Core: o.Core, State: o.State, Search: o.Search, DataDir: o.DataDir, Clock: o.Clock, Logger: o.Logger, Jobs: o.Jobs, JobsCtx: o.JobsCtx, JobsStop: o.JobsStop, HasWatcher: o.HasWatcher}
}

func (c *Controller) clk() clock.Clock {
	if c.Clock == nil {
		return clock.System()
	}
	return c.Clock
}
func (c *Controller) log() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}
	return c.Logger
}
func (c *Controller) hasWatcher() bool  { return c.HasWatcher != nil && c.HasWatcher() }
func (c *Controller) jobsStopped() bool { return c.JobsCtx != nil && c.JobsCtx.Err() != nil }

// Search exposes the query service to the protocol adapter without exposing
// the controller's lifecycle state.
func (c *Controller) Query(ctx context.Context, sources []searchlib.Source, opt svc.QueryOptions) (svc.Results, error) {
	if c.Search == nil {
		return svc.Results{}, ErrIndexDisabled
	}
	return c.Search.Query(ctx, sources, opt)
}
func (c *Controller) IndexState() svc.IndexState {
	if c.Search == nil {
		return svc.IndexState{}
	}
	return c.Search.IndexStateOf()
}
func (c *Controller) HasIndex() bool { return c.Search != nil && c.Search.HasIndex() }

// Estimate scans the currently configured corpus and reads the measured build
// rate used by the administrator's estimate screen.
func (c *Controller) Estimate(ctx context.Context) (searchlib.ScanResult, searchlib.IndexEstimate, error) {
	result, err := searchlib.ScanCorpus(ctx, indexSourcesOf(c.Core.ScanSources()), searchlib.ScanOptions{})
	if err != nil {
		return searchlib.ScanResult{}, searchlib.IndexEstimate{}, err
	}
	rate, err := c.State.IndexBuildRate(ctx)
	if err != nil {
		rate = 0
	}
	return result, searchlib.EstimateNameIndex(result.Stats, 1024, rate), nil
}

func (c *Controller) OpenIndex(ctx context.Context) {
	on, err := c.State.IndexNameEnabled(ctx)
	if err != nil {
		c.log().Warn("the search index setting could not be read; search runs on the walk", "error", err)
		return
	}
	if !on {
		c.Search.SetIndex(nil)
		c.StopUpdater()
		c.indexRecovery.Store(false)
		return
	}
	if err := migrateLegacyIndexDir(c.DataDir); err != nil {
		c.log().Warn("the legacy search index directory could not be migrated; search runs on the walk", "error", err)
		return
	}
	ix, opened := svc.OpenIndex(indexDir(c.DataDir), index.DefaultConfig(), c.log())
	if ix == nil {
		return
	}
	ix.SetIncomplete(true)
	c.indexRecovery.Store(true)
	if opened == svc.OpenAbsent {
		c.log().Info("the search index is enabled and empty; build it to use it", "dir", indexDir(c.DataDir))
	}
	c.Search.SetIndex(ix)
	c.startSearchUpdater(ctx)
}

func (c *Controller) MarkIncomplete() {
	if c.Search == nil || !c.Search.IndexStateOf().Attached {
		return
	}
	c.Search.SetIndexIncomplete(true)
	c.indexRecovery.Store(true)
}

func (c *Controller) Recover(ctx context.Context) error {
	if !c.indexRecovery.Swap(false) || c.Search == nil || c.Core == nil || !c.hasWatcher() {
		return nil
	}
	state := c.Search.IndexStateOf()
	if !state.Attached || !state.Incomplete {
		return nil
	}
	if !c.indexBuilding.CompareAndSwap(false, true) {
		c.indexRecovery.Store(true)
		return nil
	}
	defer c.indexBuilding.Store(false)
	progress, err := c.Search.Build(ctx, indexSourcesOf(c.Core.ScanSources()), func() bool { return ctx.Err() == nil }, nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.Search.IndexStateOf().Attached {
			c.indexRecovery.Store(true)
		}
		return fmt.Errorf("recovering the search index: %w", err)
	}
	if !progress.Partial {
		c.log().Info("the search index recovered current coverage", "files", progress.Files)
	}
	return nil
}

func (c *Controller) startSearchUpdater(ctx context.Context) {
	c.StopUpdater()
	loop, stop := context.WithCancel(context.WithoutCancel(ctx))
	u := svc.NewUpdater(c.Search, func() []searchlib.Source { return indexSourcesOf(c.Core.ScanSources()) }, c.log())
	u.SetIncompleteCallback(func() { c.indexRecovery.Store(true) })
	c.searchUpdaterMu.Lock()
	c.searchUpdater, c.searchUpdaterStop = u, stop
	c.searchUpdaterMu.Unlock()
	concurrency.Go(loop, "search index updater", func() { u.Run(loop) })
}

func (c *Controller) StopUpdater() {
	c.searchUpdaterMu.Lock()
	stop := c.searchUpdaterStop
	c.searchUpdater, c.searchUpdaterStop = nil, nil
	c.searchUpdaterMu.Unlock()
	if stop != nil {
		stop()
	}
}
func (c *Controller) Offer(share uint32, dir string, all bool) {
	c.searchUpdaterMu.Lock()
	u := c.searchUpdater
	c.searchUpdaterMu.Unlock()
	if u != nil {
		u.Offer(svc.Change{Share: share, Dir: dir, All: all})
	}
}

// StartIndexBuild creates the durable operation and schedules the detached
// build. The returned operation is the same initial row exposed by the API.
func (c *Controller) StartIndexBuild(ctx context.Context, owner int64) (core.Operation, error) {
	if !c.HasIndex() {
		return core.Operation{}, ErrIndexDisabled
	}
	if !c.indexBuilding.CompareAndSwap(false, true) {
		return core.Operation{}, ErrIndexBuilding
	}
	id, err := c.State.CreateOp(ctx, owner, state.OpIndexBuild, 0, c.clk().Nanos(), nil)
	if err != nil {
		c.indexBuilding.Store(false)
		return core.Operation{}, err
	}
	jobCtx := context.WithoutCancel(ctx)
	sources := indexSourcesOf(c.Core.ScanSources())
	if c.Jobs == nil {
		c.Jobs = &concurrency.Group{}
	}
	c.Jobs.Go(jobCtx, "index build", func() { c.runIndexBuild(jobCtx, id, sources) })
	op, err := c.Core.Operation(ctx, core.UserID(owner), core.OperationID(id))
	if err != nil {
		c.indexBuilding.Store(false)
		return core.Operation{}, err
	}
	return op, nil
}

func (c *Controller) runIndexBuild(ctx context.Context, id int64, sources []searchlib.Source) {
	defer c.indexBuilding.Store(false)
	gate := func() bool {
		if c.jobsStopped() {
			return false
		}
		op, _, err := c.State.GetOp(ctx, id)
		return err == nil && !op.Cancellation
	}
	started := c.clk().Now()
	progress, err := c.Search.Build(ctx, sources, gate, func(p svc.BuildProgress) {
		if perr := c.State.SetOpProgress(ctx, id, indexedCount(p.Files), ""); perr != nil {
			c.log().Warn("the index build's progress could not be recorded", "error", perr)
		}
	})
	now := c.clk().Nanos()
	if err != nil {
		c.log().Warn("the index build failed", "error", err, "files", progress.Files)
		if ferr := c.State.FinishOp(ctx, id, state.OpFailed, indexedCount(progress.Files), err.Error(), now, nil); ferr != nil {
			c.log().Warn("recording a failed index build failed", "error", ferr)
		}
		return
	}
	if !c.hasWatcher() {
		c.Search.SetIndexIncomplete(true)
		progress.Partial = true
	}
	if elapsed := c.clk().Now().Sub(started); progress.Files > 0 && elapsed >= time.Second {
		rate := uint64(float64(progress.Files) / elapsed.Seconds())
		if rerr := c.State.SetIndexBuildRate(ctx, rate); rerr != nil {
			c.log().Warn("recording the index build rate failed", "error", rerr)
		}
	}
	indexed := indexedCount(progress.Files)
	switch {
	case c.jobsStopped():
		if ierr := c.State.InterruptOp(ctx, id, now); ierr != nil {
			c.log().Warn("interrupting an index build failed", "error", ierr)
		}
	case c.buildCancelled(ctx, id):
		if ferr := c.State.FinishOp(ctx, id, state.OpCancelled, indexed, "cancelled; the index holds what the walk reached", now, nil); ferr != nil {
			c.log().Warn("recording a cancelled index build failed", "error", ferr)
		}
	default:
		message := ""
		if progress.Partial {
			message = "the corpus is larger than one build covers; a query beyond it falls back to a walk"
		}
		if ferr := c.State.FinishOp(ctx, id, state.OpDone, indexed, message, now, nil); ferr != nil {
			c.log().Warn("recording a completed index build failed", "error", ferr)
		}
	}
}
func (c *Controller) buildCancelled(ctx context.Context, id int64) bool {
	op, _, err := c.State.GetOp(ctx, id)
	return err == nil && op.Cancellation
}
func (c *Controller) DrainJobs(ctx context.Context) error {
	if c.JobsStop != nil {
		c.JobsStop()
	}
	if c.Jobs == nil {
		return nil
	}
	return c.Jobs.Wait(ctx)
}
func indexedCount(files uint64) int64 {
	if files > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(files)
}
func indexSourcesOf(scan []core.ScanSource) []searchlib.Source { return stowcloud.SourcesOf(scan) }
func indexDir(dataDir string) string                           { return filepath.Join(dataDir, indexDirName) }

type legacyIndexMigrationError struct {
	outcome fsatomic.Outcome
	err     error
}

func (e *legacyIndexMigrationError) Error() string { return e.err.Error() }
func (e *legacyIndexMigrationError) Unwrap() error { return e.err }
func migrateLegacyIndexDir(dataDir string) error {
	target := indexDir(dataDir)
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", target, err)
	}
	legacy := filepath.Join(dataDir, legacyIndexDirName)
	if _, err := os.Stat(legacy); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("checking %s: %w", legacy, err)
	}
	res, err := fsatomic.RenameDurable(legacy, target)
	if err == nil {
		return nil
	}
	if res.Outcome == fsatomic.PublicationUncertain {
		if _, statErr := os.Stat(target); statErr == nil {
			return nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("checking reconciled destination %s: %w", target, statErr))
		}
	}
	return &legacyIndexMigrationError{outcome: res.Outcome, err: fmt.Errorf("moving %s to %s: %w", legacy, target, err)}
}
