//go:build linux

package search

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/search/controller"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
)

func (m *Manager) adminIndexEstimate(c *gin.Context) {
	if _, ok := m.admin(c); !ok {
		return
	}
	result, estimate, err := m.Controller.Estimate(c.Request.Context())
	if err != nil {
		m.failKnown(c, err)
		return
	}
	m.writeJSON(c, http.StatusOK, handler.IndexEstimateOf(result, estimate))
}
func (m *Manager) adminIndexStatus(c *gin.Context) {
	if _, ok := m.admin(c); !ok {
		return
	}
	m.writeJSON(c, http.StatusOK, handler.IndexStatusOf(m.Controller.IndexState()))
}
func (m *Manager) adminIndexBuild(c *gin.Context) {
	owner, ok := m.admin(c)
	if !ok {
		return
	}
	if m.Controller == nil {
		m.refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	op, err := m.Controller.StartIndexBuild(c.Request.Context(), owner)
	if err == controller.ErrIndexDisabled {
		m.refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable, Key: "search.index_disabled"})
		return
	}
	if err == controller.ErrIndexBuilding {
		m.refuse(c, apierr.Classified{Class: apierr.Conflict, Key: "search.index_building"})
		return
	}
	if err != nil {
		m.failKnown(c, err)
		return
	}
	m.writeJSON(c, http.StatusAccepted, handler.OperationOf(op))
}
