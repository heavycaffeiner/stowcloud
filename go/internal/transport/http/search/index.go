//go:build linux

package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/search/svc"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/concurrency"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	fsatomic "github.com/stowcloud/durablefs"
	searchlib "github.com/stowcloud/namesearch"
	"github.com/stowcloud/namesearch/index"
)

const indexDirName = ".scindex"
const legacyIndexDirName = "index"

func (m *Manager) openSearchIndex(ctx context.Context) {
	on, err := m.State.IndexNameEnabled(ctx)
	if err != nil {
		m.log().Warn("the search index setting could not be read; search runs on the walk", "error", err)
		return
	}
	if !on {
		m.Search.SetIndex(nil)
		m.stopSearchUpdater()
		m.indexRecovery.Store(false)
		return
	}
	if err := migrateLegacyIndexDir(m.DataDir); err != nil {
		m.log().Warn("the legacy search index directory could not be migrated; search runs on the walk", "error", err)
		return
	}
	ix, opened := svc.OpenIndex(indexDir(m.DataDir), index.DefaultConfig(), m.log())
	if ix == nil {
		return
	}
	ix.SetIncomplete(true)
	m.indexRecovery.Store(true)
	if opened == svc.OpenAbsent {
		m.log().Info("the search index is enabled and empty; build it to use it", "dir", indexDir(m.DataDir))
	}
	m.Search.SetIndex(ix)
	m.startSearchUpdater(ctx)
}

func (m *Manager) markSearchIndexIncomplete() {
	if m.Search == nil || !m.Search.IndexStateOf().Attached {
		return
	}
	m.Search.SetIndexIncomplete(true)
	m.indexRecovery.Store(true)
}

func (m *Manager) recoverSearchIndex(ctx context.Context) error {
	if !m.indexRecovery.Swap(false) || m.Search == nil || m.Core == nil || !m.hasWatcher() {
		return nil
	}
	state := m.Search.IndexStateOf()
	if !state.Attached || !state.Incomplete {
		return nil
	}
	if !m.indexBuilding.CompareAndSwap(false, true) {
		m.indexRecovery.Store(true)
		return nil
	}
	defer m.indexBuilding.Store(false)
	progress, err := m.Search.Build(ctx, indexSourcesOf(m.Core.ScanSources()), func() bool { return ctx.Err() == nil }, nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if m.Search.IndexStateOf().Attached {
			m.indexRecovery.Store(true)
		}
		return fmt.Errorf("recovering the search index: %w", err)
	}
	if !progress.Partial {
		m.log().Info("the search index recovered current coverage", "files", progress.Files)
	}
	return nil
}

func (m *Manager) startSearchUpdater(ctx context.Context) {
	m.stopSearchUpdater()
	loop, stop := context.WithCancel(context.WithoutCancel(ctx))
	u := svc.NewUpdater(m.Search, func() []searchlib.Source { return indexSourcesOf(m.Core.ScanSources()) }, m.log())
	u.SetIncompleteCallback(func() { m.indexRecovery.Store(true) })
	m.searchUpdaterMu.Lock()
	m.searchUpdater = u
	m.searchUpdaterStop = stop
	m.searchUpdaterMu.Unlock()
	concurrency.Go(loop, "search index updater", func() { u.Run(loop) })
}

func (m *Manager) stopSearchUpdater() {
	m.searchUpdaterMu.Lock()
	stop := m.searchUpdaterStop
	m.searchUpdater = nil
	m.searchUpdaterStop = nil
	m.searchUpdaterMu.Unlock()
	if stop != nil {
		stop()
	}
}

func (m *Manager) offerToSearchUpdater(share uint32, dir string, all bool) {
	m.searchUpdaterMu.Lock()
	u := m.searchUpdater
	m.searchUpdaterMu.Unlock()
	if u != nil {
		u.Offer(svc.Change{Share: share, Dir: dir, All: all})
	}
}

func indexDir(dataDir string) string { return filepath.Join(dataDir, indexDirName) }

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

func (m *Manager) adminIndexBuild(c *gin.Context) {
	owner, ok := m.admin(c)
	if !ok {
		return
	}
	if !m.Search.HasIndex() {
		m.refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable, Key: "search.index_disabled"})
		return
	}
	if !m.indexBuilding.CompareAndSwap(false, true) {
		m.refuse(c, apierr.Classified{Class: apierr.Conflict, Key: "search.index_building"})
		return
	}
	m.startIndexBuild(c, int64(owner))
}

func (m *Manager) startIndexBuild(c *gin.Context, owner int64) {
	id, err := m.State.CreateOp(c.Request.Context(), owner, state.OpIndexBuild, 0, m.clk().Nanos(), nil)
	if err != nil {
		m.indexBuilding.Store(false)
		m.failKnown(c, err)
		return
	}
	ctx := context.WithoutCancel(c.Request.Context())
	sources := indexSourcesOf(m.Core.ScanSources())
	if m.Jobs == nil {
		m.Jobs = &concurrency.Group{}
	}
	m.Jobs.Go(ctx, "index build", func() { m.runIndexBuild(ctx, id, sources) })
	op, rerr := m.Core.Operation(c.Request.Context(), core.UserID(owner), core.OperationID(id))
	if rerr != nil {
		m.indexBuilding.Store(false)
		m.failKnown(c, rerr)
		return
	}
	m.writeJSON(c, http.StatusAccepted, handler.OperationOf(op))
}

func (m *Manager) runIndexBuild(ctx context.Context, id int64, sources []searchlib.Source) {
	defer m.indexBuilding.Store(false)
	gate := func() bool {
		if m.jobsStopped() {
			return false
		}
		op, _, err := m.State.GetOp(ctx, id)
		return err == nil && !op.Cancellation
	}
	started := m.clk().Now()
	progress, err := m.Search.Build(ctx, sources, gate, func(p svc.BuildProgress) {
		if perr := m.State.SetOpProgress(ctx, id, indexedCount(p.Files), ""); perr != nil {
			m.log().Warn("the index build's progress could not be recorded", "error", perr)
		}
	})
	now := m.clk().Nanos()
	if err != nil {
		m.log().Warn("the index build failed", "error", err, "files", progress.Files)
		_ = m.State.FinishOp(ctx, id, state.OpFailed, indexedCount(progress.Files), err.Error(), now, nil)
		return
	}
	if !m.hasWatcher() {
		m.Search.SetIndexIncomplete(true)
		progress.Partial = true
	}
	if elapsed := m.clk().Now().Sub(started); progress.Files > 0 && elapsed >= time.Second {
		rate := uint64(float64(progress.Files) / elapsed.Seconds())
		_ = m.State.SetIndexBuildRate(ctx, rate)
	}
	indexed := indexedCount(progress.Files)
	switch {
	case m.jobsStopped():
		_ = m.State.InterruptOp(ctx, id, now)
	case m.buildCancelled(ctx, id):
		_ = m.State.FinishOp(ctx, id, state.OpCancelled, indexed, "cancelled; the index holds what the walk reached", now, nil)
	default:
		message := ""
		if progress.Partial {
			message = "the corpus is larger than one build covers; a query beyond it falls back to a walk"
		}
		_ = m.State.FinishOp(ctx, id, state.OpDone, indexed, message, now, nil)
	}
}

func (m *Manager) jobsStopped() bool { return m.JobsCtx != nil && m.JobsCtx.Err() != nil }
func (m *Manager) buildCancelled(ctx context.Context, id int64) bool {
	op, _, err := m.State.GetOp(ctx, id)
	return err == nil && op.Cancellation
}
func indexedCount(files uint64) int64 {
	if files > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(files)
}
func (m *Manager) hasWatcher() bool { return m.HasWatcher != nil && m.HasWatcher() }
