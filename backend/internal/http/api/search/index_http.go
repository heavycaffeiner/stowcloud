//go:build linux

package search

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/search"
	"github.com/heavycaffeiner/stowcloud/backend/internal/search/controller"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

func (m *Manager) IndexEstimate(c *gin.Context) {
	result, estimate, err := m.Controller.Estimate(c.Request.Context())
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, search.IndexEstimateOf(result, estimate))
}
func (m *Manager) IndexStatus(c *gin.Context) {
	c.JSON(http.StatusOK, search.IndexStatusOf(m.Controller.IndexState()))
}
func (m *Manager) IndexBuild(c *gin.Context) {
	owner, _ := middleware.UserOf(c)
	if m.Controller == nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	op, err := m.Controller.StartIndexBuild(c.Request.Context(), owner)
	if err == controller.ErrIndexDisabled {
		middleware.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable, Key: "search.index_disabled"})
		return
	}
	if err == controller.ErrIndexBuilding {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Conflict, Key: "search.index_building"})
		return
	}
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, files.OperationOf(op))
}
