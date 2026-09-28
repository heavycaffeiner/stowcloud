//go:build linux

// Package adminlogs serves administrator log query and timeline routes.
package adminlogs

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/admin/logbook"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/humabridge"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
)

const (
	logsPageDefault = 100
	logsPageCeiling = 500
)

// Deps contains the log services and narrow composition callbacks required by
// the administrator log routes.
type Deps struct {
	Logs *logbook.Sink
	Auth *auth.Service
}

// Register adds typed administrator log operations. Paths are relative to the
// caller's API mount.
func Register(api huma.API, d Deps) {
	h := &handlers{d: d}
	huma.Register(api, huma.Operation{
		OperationID: "admin.logs.list",
		Method:      http.MethodGet,
		Path:        "/admin/logs",
		Summary:     "List administrator logs",
	}, h.listHuma)
	huma.Register(api, huma.Operation{
		OperationID: "admin.logs.timeline",
		Method:      http.MethodGet,
		Path:        "/admin/logs/timeline",
		Summary:     "Summarize administrator logs",
	}, h.timelineHuma)
}

type logInput struct {
	Since     string `query:"since"`
	Until     string `query:"until"`
	Level     string `query:"level"`
	Text      string `query:"text"`
	Subsystem string `query:"subsystem"`
	RequestID string `query:"request_id"`
	Limit     string `query:"limit"`
	Cursor    string `query:"cursor"`
}

type timelineInput struct {
	Since     string `query:"since"`
	Until     string `query:"until"`
	Level     string `query:"level"`
	Text      string `query:"text"`
	Subsystem string `query:"subsystem"`
	RequestID string `query:"request_id"`
	Limit     string `query:"limit"`
	Cursor    string `query:"cursor"`
	BucketNS  string `query:"bucket_ns"`
}

type logPageOutput struct{ Body handler.LogPageView }
type timelineOutput struct{ Body handler.LogsTimelineView }

func (h *handlers) listHuma(ctx context.Context, in *logInput) (*logPageOutput, error) {
	if h.d.Logs == nil {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.SubsystemUnavailable})
	}
	q, ok := logQueryInput(in.Since, in.Until, in.Level, in.Text, in.Subsystem, in.RequestID, in.Limit, in.Cursor)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Malformed})
	}
	page, err := h.d.Logs.Query(ctx, q)
	if err != nil {
		if errors.Is(err, logbook.ErrBadCursor) {
			return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Malformed})
		}
		return nil, humabridge.Failure(ctx, err)
	}
	return &logPageOutput{Body: handler.LogPageOf(page, h.d.Logs.Stats())}, nil
}

func (h *handlers) timelineHuma(ctx context.Context, in *timelineInput) (*timelineOutput, error) {
	if h.d.Logs == nil {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.SubsystemUnavailable})
	}
	q, ok := logQueryInput(in.Since, in.Until, in.Level, in.Text, in.Subsystem, in.RequestID, in.Limit, in.Cursor)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Malformed})
	}
	width, ok := bucketInput(in.BucketNS)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Malformed})
	}
	server, widthNS, truncated, err := h.d.Logs.Counts(ctx, q, width)
	if err != nil {
		if errors.Is(err, logbook.ErrBadCursor) {
			return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Malformed})
		}
		return nil, humabridge.Failure(ctx, err)
	}
	var audit []auth.AuditBucket
	if len(server) > 0 {
		var auditTruncated bool
		audit, auditTruncated, err = h.d.Auth.AuditCounts(ctx,
			auth.AuditFilter{SinceNs: q.Since, UntilNs: q.Until},
			server[0].StartNs, widthNS, len(server))
		if err != nil {
			return nil, humabridge.Failure(ctx, err)
		}
		truncated = truncated || auditTruncated
	}
	return &timelineOutput{Body: handler.LogsTimelineOf(server, audit, widthNS, truncated)}, nil
}

func logQueryInput(since, until, level, text, subsystem, requestID, limit, cursor string) (logbook.Query, bool) {
	q := logbook.Query{Text: text, Subsystem: subsystem, RequestID: requestID, Cursor: cursor, Limit: logsPageDefault}
	if since != "" {
		v, err := strconv.ParseInt(since, 10, 64)
		if err != nil {
			return logbook.Query{}, false
		}
		q.Since = v
	}
	if until != "" {
		v, err := strconv.ParseInt(until, 10, 64)
		if err != nil {
			return logbook.Query{}, false
		}
		q.Until = v
	}
	for _, item := range strings.Split(level, ",") {
		if item = strings.TrimSpace(item); item != "" {
			q.Levels = append(q.Levels, item)
		}
	}
	if limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil || n < 1 || n > logsPageCeiling {
			return logbook.Query{}, false
		}
		q.Limit = n
	}
	return q, true
}

func bucketInput(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	return v, err == nil && v > 0
}

type handlers struct{ d Deps }
