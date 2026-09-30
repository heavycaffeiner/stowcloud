//go:build linux

package search

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/search/controller"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
)

type Options struct {
	Core       *files.Core
	Controller *controller.Controller
	Clock      clock.Clock
	Logger     *slog.Logger
	Owner      func(*gin.Context) (files.UserID, bool)
	Admin      func(*gin.Context) (int64, bool)
	Refuse     func(*gin.Context, apierr.Classified)
	FailKnown  func(*gin.Context, error)
	WriteJSON  func(*gin.Context, int, any)
}

type Manager struct {
	Core       *files.Core
	Controller *controller.Controller
	Clock      clock.Clock
	Logger     *slog.Logger
	Owner      func(*gin.Context) (files.UserID, bool)
	Admin      func(*gin.Context) (int64, bool)
	Refuse     func(*gin.Context, apierr.Classified)
	FailKnown  func(*gin.Context, error)
	WriteJSON  func(*gin.Context, int, any)
}

func NewManager(o Options) *Manager {
	return &Manager{Core: o.Core, Controller: o.Controller, Clock: o.Clock, Logger: o.Logger, Owner: o.Owner, Admin: o.Admin, Refuse: o.Refuse, FailKnown: o.FailKnown, WriteJSON: o.WriteJSON}
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
func (m *Manager) ownerOf(c *gin.Context) (files.UserID, bool) {
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
func (m *Manager) SearchStream(c *gin.Context)  { m.searchStream(c) }
func (m *Manager) IndexEstimate(c *gin.Context) { m.adminIndexEstimate(c) }
func (m *Manager) IndexStatus(c *gin.Context)   { m.adminIndexStatus(c) }
func (m *Manager) IndexBuild(c *gin.Context)    { m.adminIndexBuild(c) }
