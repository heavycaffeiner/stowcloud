//go:build linux

package search

import (
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/search/controller"
)

type Manager struct {
	Core       *files.Core
	Controller *controller.Controller
	Clock      clock.Clock
	Logger     *slog.Logger
}

func (m *Manager) clk() clock.Clock {
	if m.Clock == nil {
		return clock.System()
	}
	return m.Clock
}
