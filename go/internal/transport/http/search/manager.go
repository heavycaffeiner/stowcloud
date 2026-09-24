//go:build linux

package search

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/search/svc"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/concurrency"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
)

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
	Owner      func(*gin.Context) (core.UserID, bool)
	Admin      func(*gin.Context) (int64, bool)
	Refuse     func(*gin.Context, apierr.Classified)
	FailKnown  func(*gin.Context, error)
	WriteJSON  func(*gin.Context, int, any)
}

type Manager struct {
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
	Owner             func(*gin.Context) (core.UserID, bool)
	Admin             func(*gin.Context) (int64, bool)
	Refuse            func(*gin.Context, apierr.Classified)
	FailKnown         func(*gin.Context, error)
	WriteJSON         func(*gin.Context, int, any)
	indexBuilding     atomic.Bool
	indexRecovery     atomic.Bool
	searchUpdaterMu   sync.Mutex
	searchUpdater     *svc.Updater
	searchUpdaterStop context.CancelFunc
}

func NewManager(o Options) *Manager {
	return &Manager{Core: o.Core, State: o.State, Search: o.Search, DataDir: o.DataDir, Clock: o.Clock, Logger: o.Logger, Jobs: o.Jobs, JobsCtx: o.JobsCtx, JobsStop: o.JobsStop, HasWatcher: o.HasWatcher, Owner: o.Owner, Admin: o.Admin, Refuse: o.Refuse, FailKnown: o.FailKnown, WriteJSON: o.WriteJSON}
}
func (m *Manager) clk() clock.Clock {
	if m.Clock == nil {
		return clock.System()
	}
	return m.Clock
}
func (m *Manager) log() *slog.Logger {
	if m.Logger == nil {
		return slog.Default()
	}
	return m.Logger
}
func (m *Manager) ownerOf(c *gin.Context) (core.UserID, bool) {
	if m.Owner == nil {
		return 0, false
	}
	return m.Owner(c)
}
func (m *Manager) admin(c *gin.Context) (int64, bool) {
	if m.Admin == nil {
		return 0, false
	}
	return m.Admin(c)
}
func (m *Manager) refuse(c *gin.Context, err apierr.Classified) {
	if m.Refuse != nil {
		m.Refuse(c, err)
	}
}
func (m *Manager) failKnown(c *gin.Context, err error) {
	if m.FailKnown != nil {
		m.FailKnown(c, err)
	}
}
func (m *Manager) writeJSON(c *gin.Context, status int, value any) {
	if m.WriteJSON != nil {
		m.WriteJSON(c, status, value)
	}
}
func (m *Manager) SearchStream(c *gin.Context)              { m.searchStream(c) }
func (m *Manager) IndexEstimate(c *gin.Context)             { m.adminIndexEstimate(c) }
func (m *Manager) IndexStatus(c *gin.Context)               { m.adminIndexStatus(c) }
func (m *Manager) IndexBuild(c *gin.Context)                { m.adminIndexBuild(c) }
func (m *Manager) StopUpdater()                             { m.stopSearchUpdater() }
func (m *Manager) MarkIncomplete()                          { m.markSearchIndexIncomplete() }
func (m *Manager) Offer(share uint32, dir string, all bool) { m.offerToSearchUpdater(share, dir, all) }
func (m *Manager) OpenIndex(ctx context.Context)            { m.openSearchIndex(ctx) }
func (m *Manager) Recover(ctx context.Context) error        { return m.recoverSearchIndex(ctx) }
func (m *Manager) DrainJobs(ctx context.Context) error {
	if m.JobsStop != nil {
		m.JobsStop()
	}
	if m.Jobs == nil {
		return nil
	}
	return m.Jobs.Wait(ctx)
}
