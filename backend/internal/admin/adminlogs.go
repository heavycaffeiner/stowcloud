//go:build linux

// Package adminlogs serves administrator log query and timeline routes.
package admin

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/admin/logbook"
	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
)

const (
	logsPageDefault = 100
	logsPageCeiling = 500
)

// Deps contains the log services and narrow composition callbacks required by
// the administrator log routes.
type LogsHandler struct {
	Logs *logbook.Sink
	Auth *auth.Service
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

type logPageOutput struct{ Body LogPageView }
type timelineOutput struct{ Body LogsTimelineView }

// List answers one page of the log.
func (h *LogsHandler) List(ctx context.Context, in *logInput) (*logPageOutput, error) {
	if h.Logs == nil {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.SubsystemUnavailable}}
	}
	q, ok := logQueryInput(in.Since, in.Until, in.Level, in.Text, in.Subsystem, in.RequestID, in.Limit, in.Cursor)
	if !ok {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Malformed}}
	}
	page, err := h.Logs.Query(ctx, q)
	if err != nil {
		if errors.Is(err, logbook.ErrBadCursor) {
			return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Malformed}}
		}
		return nil, err
	}
	return &logPageOutput{Body: LogPageOf(page, h.Logs.Stats())}, nil
}

// Timeline answers the log bucketed over time.
func (h *LogsHandler) Timeline(ctx context.Context, in *timelineInput) (*timelineOutput, error) {
	if h.Logs == nil {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.SubsystemUnavailable}}
	}
	q, ok := logQueryInput(in.Since, in.Until, in.Level, in.Text, in.Subsystem, in.RequestID, in.Limit, in.Cursor)
	if !ok {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Malformed}}
	}
	width, ok := bucketInput(in.BucketNS)
	if !ok {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Malformed}}
	}
	server, widthNS, truncated, err := h.Logs.Counts(ctx, q, width)
	if err != nil {
		if errors.Is(err, logbook.ErrBadCursor) {
			return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Malformed}}
		}
		return nil, err
	}
	var audit []auth.AuditBucket
	if len(server) > 0 {
		var auditTruncated bool
		audit, auditTruncated, err = h.Auth.AuditCounts(ctx,
			auth.AuditFilter{SinceNs: q.Since, UntilNs: q.Until},
			server[0].StartNs, widthNS, len(server))
		if err != nil {
			return nil, err
		}
		truncated = truncated || auditTruncated
	}
	return &timelineOutput{Body: LogsTimelineOf(server, audit, widthNS, truncated)}, nil
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
