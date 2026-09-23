//go:build linux

// Search, streamed to the end.
//
// A walk of a large tree takes as long as it takes, so results arrive as they
// are found rather than at the end: a client shows the first match while the
// rest is still being looked for. Nothing here shortens the answer. There is
// no result ceiling and no walk deadline, because a stream has no second page
// to ask for and a partial list nobody can extend is a wrong list.
//
// The stream is committed only after the query has been validated and the
// sources resolved, because after the first byte there is no status left to
// refuse with.
package app

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
	search "github.com/stowcloud/namesearch"
)

// searchStream answers a query as server-sent events.
func (e *Engine) searchStream(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	if e.Search == nil {
		refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if len(query) > searchQueryMax {
		refuse(c, apierr.Classified{Class: apierr.LimitExceeded})
		return
	}

	// A filter the server does not understand is refused rather than dropped.
	// A client that asked for folders and was silently handed the whole tree
	// would present that as the answer to the narrow question it asked.
	kind, kok := search.ParseKind(c.Query("kind"))
	exts, eok := search.ParseExts(c.Query("ext"))
	if !kok || !eok {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if kind == search.KindDir && len(exts) > 0 {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}

	// Permission-scoped, and resolved before commitment. Every source carries
	// its own per-entry check, because a grant can begin partway down a tree
	// and a share-level answer would either conceal a readable subtree or
	// include an unreadable one.
	//
	// Two gates, deliberately. Measured, swapping this for the unscoped
	// ScanSources changes no answer: the label lookup below is empty for a
	// share the account holds no grant over, so the source is dropped anyway.
	// The per-entry check is what covers the case the label cannot, a grant
	sources := stowcloud.LabelSources(e.Core, owner, e.Core.UserScanSources(owner))

	opt := svc.QueryOptions{
		Query:        query,
		Scope:        c.Query("path"),
		WithMetadata: c.Query("metadata") == "1",
		Filter:       search.Filter{Kind: kind, Exts: exts},
	}

	// The headers the protocol needs, all of them before the first byte. The
	// buffering hint is for the proxy rather than the client: without it an
	// intermediary can hold the whole stream and deliver it at the end, which
	// is the one thing streaming exists to avoid.
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	e.writeSearchStream(ctx, cancel, bufio.NewWriter(c.Writer), sources, opt)
}

// searchQueryMax bounds the query text. It is a client-supplied string that
// reaches a matcher run against every entry in a walk.
const searchQueryMax = 512

// searchProgressEvery bounds how often a running search reports its counters.
// Often enough to read as alive, rare enough that the stream is results with
// a pulse rather than a pulse with results.
const searchProgressEvery = 400 * time.Millisecond

// writeSearchStream runs the query into a committed response.
//
// Every hit is written the moment the walk hands it over, and the walk stops
// when the writes stop landing: a client that closed the tab is how an
// unbounded search ends early.
func (e *Engine) writeSearchStream(
	ctx context.Context, cancel context.CancelFunc, w *bufio.Writer, sources []search.Source, opt svc.QueryOptions,
) {
	// An immediate comment, so the client and any proxy see an established
	// stream before the first result exists. If the peer is already gone,
	// cancel the search context immediately so worker slots are released.
	writeSSE(w, handler.SSEComment(), e)
	if err := w.Flush(); err != nil {
		cancel()
		return
	}

	var (
		count  int
		broken bool
	)
	opt.Stream = func(hits []search.Hit) {
		if broken {
			return
		}
		for _, hit := range hits {
			writeSSEEvent(w, "hit", handler.SearchHitViewOf(hit), e)
			count++
		}
		if ferr := w.Flush(); ferr != nil {
			// The reader is gone. Cancelling here is what keeps a walk of a
			// whole tree from running on for a screen nobody is watching.
			broken = true
			cancel()
		}
	}

	// The counters, so a search that has matched nothing still shows movement.
	// A walk of a large tree can run for a long time before its first hit, and
	// a stream that says nothing for that long reads as a stalled one.
	// Throttled by the clock rather than by the walk's own cadence: how often
	// directories pass is a property of the disk, not of what a person can
	// read.
	var lastProgress time.Time
	opt.Progress = func(p search.WalkProgress) {
		if broken {
			return
		}
		now := e.clock.Now()
		if !lastProgress.IsZero() && now.Sub(lastProgress) < searchProgressEvery {
			return
		}
		lastProgress = now
		writeSSEEvent(w, "progress", map[string]any{
			"dirs":  p.DirsVisited,
			"files": p.EntriesSeen,
			"found": count,
		}, e)
		if ferr := w.Flush(); ferr != nil {
			broken = true
			cancel()
		}
	}

	results, err := e.Search.Query(ctx, sources, opt)
	if broken {
		return
	}
	if err != nil {
		// The status is spent, so the failure travels as the terminal event.
		// Attempting a 500 here would write a status onto a response the
		// client has already begun reading.
		e.logger.Warn("a search failed after its stream was committed", "error", err)
		writeSSEEvent(w, "done", map[string]any{"error": searchErrorName(err), "count": count}, e)
		if ferr := w.Flush(); ferr != nil {
			e.logger.Warn("flushing a search stream", "error", ferr)
		}
		return
	}

	// A complete request has no result limit or deadline, but traversal can
	// still encounter an unreadable subtree or depth boundary. Surface that
	// partiality on the terminal event instead of claiming full coverage.
	writeSSEEvent(w, "done", map[string]any{
		"count":      count,
		"tier":       results.Tier.String(),
		"elapsed_ms": results.Elapsed.Milliseconds(),
		"truncated":  results.Truncated,
	}, e)

	if ferr := w.Flush(); ferr != nil {
		e.logger.Warn("flushing a search stream", "error", ferr)
	}
}

// searchErrorName is what a client is told went wrong.
//
// A busy engine is named apart from a failure: one is worth retrying in a
// moment and the other is not, and a client showing "search failed" for a
// queue that was momentarily full sends people looking for a broken server.
func searchErrorName(err error) string {
	if errors.Is(err, svc.ErrBusy) {
		return "busy"
	}
	return "search_failed"
}

// writeSSEEvent writes one named event carrying a JSON payload.
//
// Framed by the shared builder rather than by a second encoder here. Both
// produced the same bytes, since the JSON encoder escapes newlines and a
// split frame was never reachable through a payload; one encoder is simply
// one place for that to stop being true.
func writeSSEEvent(w *bufio.Writer, name string, payload any, e *Engine) {
	frame, err := handler.SSEFrame(name, payload)
	if err != nil {
		e.logger.Warn("a search event could not be framed and is dropped",
			"event", name, "error", err)
		return
	}
	writeSSE(w, frame, e)
}

// writeSSE writes one frame and flushes it.
//
// Flushed per event rather than at the end, which is the whole point: a
// buffered stream delivered on close is a slower version of a single
// response.
func writeSSE(w *bufio.Writer, frame string, e *Engine) {
	if _, err := w.WriteString(frame); err != nil {
		e.logger.Warn("writing a search event", "error", err)
		return
	}
	if err := w.Flush(); err != nil {
		e.logger.Warn("flushing a search event", "error", err)
	}
}
