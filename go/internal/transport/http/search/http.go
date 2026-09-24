//go:build linux

package search

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/search/stowcloud"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/search/svc"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	searchlib "github.com/stowcloud/namesearch"
)

const searchQueryMax = 512
const searchProgressEvery = 400 * time.Millisecond

func (m *Manager) searchStream(c *gin.Context) {
	owner, ok := m.ownerOf(c)
	if !ok {
		m.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	if m.Search == nil {
		m.refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		m.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if len(query) > searchQueryMax {
		m.refuse(c, apierr.Classified{Class: apierr.LimitExceeded})
		return
	}
	kind, kindOK := searchlib.ParseKind(c.Query("kind"))
	exts, extOK := searchlib.ParseExts(c.Query("ext"))
	if !kindOK || !extOK || (kind == searchlib.KindDir && len(exts) > 0) {
		m.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	sources := stowcloud.LabelSources(m.Core, owner, m.Core.UserScanSources(owner))
	opt := svc.QueryOptions{Query: query, Scope: c.Query("path"), WithMetadata: c.Query("metadata") == "1", Filter: searchlib.Filter{Kind: kind, Exts: exts}}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	m.writeSearchStream(ctx, cancel, bufio.NewWriter(c.Writer), sources, opt)
}

func (m *Manager) writeSearchStream(ctx context.Context, cancel context.CancelFunc, w *bufio.Writer, sources []searchlib.Source, opt svc.QueryOptions) {
	writeSSE(w, handler.SSEComment(), m)
	if err := w.Flush(); err != nil {
		cancel()
		return
	}
	count := 0
	broken := false
	opt.Stream = func(hits []searchlib.Hit) {
		if broken {
			return
		}
		for _, hit := range hits {
			writeSSEEvent(w, "hit", handler.SearchHitViewOf(hit), m)
			count++
		}
		if err := w.Flush(); err != nil {
			broken = true
			cancel()
		}
	}
	var lastProgress time.Time
	opt.Progress = func(p searchlib.WalkProgress) {
		if broken {
			return
		}
		now := m.clk().Now()
		if !lastProgress.IsZero() && now.Sub(lastProgress) < searchProgressEvery {
			return
		}
		lastProgress = now
		writeSSEEvent(w, "progress", map[string]any{"dirs": p.DirsVisited, "files": p.EntriesSeen, "found": count}, m)
		if err := w.Flush(); err != nil {
			broken = true
			cancel()
		}
	}
	results, err := m.Search.Query(ctx, sources, opt)
	if broken {
		return
	}
	if err != nil {
		m.log().Warn("a search failed after its stream was committed", "error", err)
		writeSSEEvent(w, "done", map[string]any{"error": searchErrorName(err), "count": count}, m)
		_ = w.Flush()
		return
	}
	writeSSEEvent(w, "done", map[string]any{"count": count, "tier": results.Tier.String(), "elapsed_ms": results.Elapsed.Milliseconds(), "truncated": results.Truncated}, m)
	if err := w.Flush(); err != nil {
		m.log().Warn("flushing a search stream", "error", err)
	}
}

func searchErrorName(err error) string {
	if errors.Is(err, svc.ErrBusy) {
		return "busy"
	}
	return "search_failed"
}

func writeSSEEvent(w *bufio.Writer, name string, payload any, m *Manager) {
	frame, err := handler.SSEFrame(name, payload)
	if err != nil {
		m.log().Warn("a search event could not be framed and is dropped", "event", name, "error", err)
		return
	}
	writeSSE(w, frame, m)
}

func writeSSE(w *bufio.Writer, frame string, m *Manager) {
	if _, err := w.WriteString(frame); err != nil {
		m.log().Warn("writing a search event", "error", err)
		return
	}
	if err := w.Flush(); err != nil {
		m.log().Warn("flushing a search event", "error", err)
	}
}
