// Linux only, for the same reason as the rest of this package.
//go:build linux

// The log dashboard's one route.
package lifecycle

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/logbook"
)

// adminLogsList answers a page of the durable log.
func (e *Engine) adminLogsList(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	if e.Logs == nil {
		// A deployment whose data directory refused the log store at boot.
		// Every other route still works; this is the one screen with
		// nothing to show.
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}

	q, ok := logQueryOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	page, err := e.Logs.Query(c.UserContext(), q)
	if err != nil {
		// A cursor this store did not write is the caller's to correct, not a
		// fault: a stale page token is what a reloaded dashboard sends.
		if errors.Is(err, logbook.ErrBadCursor) {
			return refuse(c, apierr.Classified{Class: apierr.Malformed})
		}
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.LogPageOf(page, e.Logs.Stats()))
}

// logQueryOf reads the filter off the request.
//
// since, until and limit are the values a malformed submission refuses
// rather than silently drops: a filter a client asked for and did not get is
// a dashboard reporting the wrong window without saying so. Every other
// parameter is free text or an exact match with no invalid shape to reject.
func logQueryOf(c *fiber.Ctx) (logbook.Query, bool) {
	q := logbook.Query{
		Text:      c.Query("text"),
		Subsystem: c.Query("subsystem"),
		RequestID: c.Query("request_id"),
		Cursor:    c.Query("cursor"),
	}

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
			if level == "" {
				continue
			}
			q.Levels = append(q.Levels, level)
		}
	}

	q.Limit = logsPageDefault
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > logsPageCeiling {
			return logbook.Query{}, false
		}
		q.Limit = n
	}
	return q, true
}

// adminLogsTimeline answers the graph above the log list.
//
// Both logs, counted over one frame. The counts come from their own walks
// rather than from the pages the list loaded: a page is a hundred rows and
// the graph covers a window, so a chart added up from what the reader
// scrolled to would rise and fall with the scrolling.
func (e *Engine) adminLogsTimeline(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	if e.Logs == nil {
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}

	q, ok := logQueryOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	width, ok := bucketNsOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	// The server log decides the frame, because its walk is the one that
	// picks a width the window divides into.
	server, widthNs, truncated, err := e.Logs.Counts(c.UserContext(), q, width)
	if err != nil {
		if errors.Is(err, logbook.ErrBadCursor) {
			return refuse(c, apierr.Classified{Class: apierr.Malformed})
		}
		return failKnown(c, err)
	}

	var audit []auth.AuditBucket
	if len(server) > 0 {
		var auditTruncated bool
		audit, auditTruncated, err = e.Auth.AuditCounts(c.UserContext(),
			auth.AuditFilter{SinceNs: q.Since, UntilNs: q.Until},
			server[0].StartNs, widthNs, len(server))
		if err != nil {
			return failKnown(c, err)
		}
		// Either side stopping early makes the whole graph a prefix.
		truncated = truncated || auditTruncated
	}

	return writeJSON(c, fiber.StatusOK,
		handler.LogsTimelineOf(server, audit, widthNs, truncated))
}

// bucketNsOf reads the interval width a caller asked for.
//
// Refused rather than ignored when it is not a number or not positive, for
// the reason the filter parameters are: a width a client asked for and did
// not get is a graph whose bars mean something other than what the screen
// says they do. Absent is fine and lets the store pick.
func bucketNsOf(c *fiber.Ctx) (int64, bool) {
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

// The page bounds the batch contract states: a default a screen shows without
// asking, and the ceiling a caller may request at most.
const (
	logsPageDefault = 100
	logsPageCeiling = 500
)
