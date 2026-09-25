//go:build linux

// Package jobs serves authenticated long-running operation routes.
package jobs

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/uploads"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
)

const jobsPageSize = 100

// Deps contains only the operation services and composition callbacks needed by
// these routes.
type Deps struct {
	Core      *core.Core
	State     *state.DB
	Owner     func(*gin.Context) (core.UserID, bool)
	StartJobs func()
	NowNs     func() int64
}

// NewHandlers returns route-name to handler bindings for operation routes.
func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	h := &handlers{d: d}
	return map[string]gin.HandlerFunc{
		"jobs.list":   h.list,
		"jobs.get":    h.get,
		"jobs.cancel": h.cancel,
		"jobs.retry":  h.retry,
		"jobs.pause":  h.pause,
		"jobs.resume": h.resume,
	}
}

type handlers struct{ d Deps }

func (h *handlers) list(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	ops, err := h.d.Core.ListOperations(c.Request.Context(), owner, jobsPageSize)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, handler.OperationsOf(ops))
}

func (h *handlers) get(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	id, ok := operationID(c)
	if !ok {
		h.notFound(c)
		return
	}
	op, err := h.d.Core.Operation(c.Request.Context(), owner, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, handler.OperationOf(op))
}

func (h *handlers) cancel(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	id, ok := operationID(c)
	if !ok {
		h.notFound(c)
		return
	}
	if err := h.d.Core.CancelOperation(c.Request.Context(), owner, id); err != nil {
		h.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handlers) retry(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	id, ok := operationID(c)
	if !ok {
		h.notFound(c)
		return
	}
	op, err := h.d.Core.Operation(c.Request.Context(), owner, id)
	if err != nil {
		h.fail(c, err)
		return
	}
	if op.State != state.OpFailed && op.State != state.OpInterrupted {
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if err := h.d.State.ResumeOp(c.Request.Context(), int64(id), h.d.NowNs()); err != nil {
		h.fail(c, err)
		return
	}
	h.d.StartJobs()
	c.Status(http.StatusNoContent)
}

func (h *handlers) pause(c *gin.Context)  { h.pauseResume(c, true) }
func (h *handlers) resume(c *gin.Context) { h.pauseResume(c, false) }

func (h *handlers) pauseResume(c *gin.Context, pause bool) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	id, ok := operationID(c)
	if !ok {
		h.notFound(c)
		return
	}
	if _, err := h.d.Core.Operation(c.Request.Context(), owner, id); err != nil {
		h.fail(c, err)
		return
	}
	var err error
	if pause {
		err = h.d.State.PauseOp(c.Request.Context(), int64(id))
	} else {
		err = h.d.State.ResumeOp(c.Request.Context(), int64(id), h.d.NowNs())
	}
	if err != nil {
		h.fail(c, err)
		return
	}
	h.d.StartJobs()
	c.Status(http.StatusNoContent)
}

func (h *handlers) owner(c *gin.Context) (core.UserID, bool) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	return owner, ok
}

func operationID(c *gin.Context) (core.OperationID, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return core.OperationID(n), true
}

func (h *handlers) notFound(c *gin.Context) { h.fail(c, core.ErrNotFound) }

func (h *handlers) fail(c *gin.Context, err error) {
	var full *upload.CacheFullError
	if errors.As(err, &full) && full.RetryAfterSeconds > 0 {
		c.Header("Retry-After", strconv.Itoa(full.RetryAfterSeconds))
	}
	middleware.SetCause(c, err)
	h.refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}

func (h *handlers) refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	c.JSON(status, body)
}

var _ interface {
	ListOperations(context.Context, core.UserID, int) ([]core.Operation, error)
} = (*core.Core)(nil)
