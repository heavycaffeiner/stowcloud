//go:build linux

// Package adminlogs serves administrator log query and timeline routes.
package adminlogs

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/admin/logbook"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
)

const (
	logsPageDefault = 100
	logsPageCeiling = 500
)

// Deps contains the log services and narrow composition callbacks required by
// the administrator log routes.
type Deps struct {
	Logs   *logbook.Sink
	Auth   *auth.Service
	Admin  func(*gin.Context) (int64, bool)
	Fail   func(*gin.Context, error)
	Refuse func(*gin.Context, apierr.Classified)
}

// NewHandlers returns route-name to handler bindings for administrator logs.
func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	h := &handlers{d: d}
	return map[string]gin.HandlerFunc{
		"admin.logs.list":     h.list,
		"admin.logs.timeline": h.timeline,
	}
}

type handlers struct{ d Deps }

func (h *handlers) list(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	if h.d.Logs == nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	q, ok := logQueryOf(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	page, err := h.d.Logs.Query(c.Request.Context(), q)
	if err != nil {
		if errors.Is(err, logbook.ErrBadCursor) {
			h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
			return
		}
		h.d.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, handler.LogPageOf(page, h.d.Logs.Stats()))
}

func (h *handlers) timeline(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	if h.d.Logs == nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	q, ok := logQueryOf(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	width, ok := bucketNsOf(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	server, widthNs, truncated, err := h.d.Logs.Counts(c.Request.Context(), q, width)
	if err != nil {
		if errors.Is(err, logbook.ErrBadCursor) {
			h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
			return
		}
		h.d.Fail(c, err)
		return
	}
	var audit []auth.AuditBucket
	if len(server) > 0 {
		var auditTruncated bool
		audit, auditTruncated, err = h.d.Auth.AuditCounts(c.Request.Context(), auth.AuditFilter{SinceNs: q.Since, UntilNs: q.Until}, server[0].StartNs, widthNs, len(server))
		if err != nil {
			h.d.Fail(c, err)
			return
		}
		truncated = truncated || auditTruncated
	}
	c.JSON(http.StatusOK, handler.LogsTimelineOf(server, audit, widthNs, truncated))
}

func logQueryOf(c *gin.Context) (logbook.Query, bool) {
	q := logbook.Query{Text: c.Query("text"), Subsystem: c.Query("subsystem"), RequestID: c.Query("request_id"), Cursor: c.Query("cursor"), Limit: logsPageDefault}
	if raw := c.Query("since"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return logbook.Query{}, false
		}
		q.Since = v
	}
	if raw := c.Query("until"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return logbook.Query{}, false
		}
		q.Until = v
	}
	if raw := c.Query("level"); raw != "" {
		for _, level := range strings.Split(raw, ",") {
			level = strings.TrimSpace(level)
			if level != "" {
				q.Levels = append(q.Levels, level)
			}
		}
	}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > logsPageCeiling {
			return logbook.Query{}, false
		}
		q.Limit = n
	}
	return q, true
}

func bucketNsOf(c *gin.Context) (int64, bool) {
	raw := c.Query("bucket_ns")
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}
