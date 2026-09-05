//go:build linux

// Search, streamed.
//
// A walk of a large tree takes as long as it takes, so results arrive as they
// are found rather than at the end: a client shows the first match while the
// rest is still being looked for. The stream is committed only after the
// query has been validated and the sources resolved, because after the first
// byte there is no status left to refuse with.
package lifecycle

import (
	"bufio"
	"context"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
)

// searchStream answers a query as server-sent events.
func (e *Engine) searchStream(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	if e.Search == nil {
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}

	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		// Refused rather than answered with everything: an empty query that
		// walked every share is a way to make one request cost a full scan.
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	if len(query) > searchQueryMax {
		return refuse(c, apierr.Classified{Class: apierr.LimitExceeded})
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
	// that starts partway down a share the account can otherwise see.
	sources := searchSourcesOf(e.Core.UserScanSources(owner), owner, e.Core)

	opt := svc.QueryOptions{
		Query:        query,
		Limit:        searchLimit(c.Query("limit")),
		Scope:        c.Query("path"),
		WithMetadata: c.Query("metadata") == "1",
	}

	// The headers the protocol needs, all of them before the first byte. The
	// buffering hint is for the proxy rather than the client: without it an
	// intermediary can hold the whole stream and deliver it at the end, which
	// is the one thing streaming exists to avoid.
	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set("X-Accel-Buffering", "no")
	c.Status(fiber.StatusOK)

	// Detached, because the stream writer runs after this handler returns and
	// the request is recycled by then. Reaching through the request inside
	// the closure is a nil dereference, which is how the archive route
	// panicked on its first run.
	ctx, cancel := context.WithCancel(context.Background())

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer cancel()
		e.writeSearchStream(ctx, cancel, w, sources, opt)
	})
	return nil
}

// searchQueryMax bounds the query text. It is a client-supplied string that
// reaches a matcher run against every entry in a walk.
const searchQueryMax = 512

// writeSearchStream runs the query into a committed response.
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

	results, err := e.Search.Query(ctx, sources, opt)
	if err != nil {
		// The status is spent, so the failure travels as the terminal event.
		// Attempting a 500 here would write a status onto a response the
		// client has already begun reading.
		e.logger.Warn("a search failed after its stream was committed", "error", err)
		writeSSEEvent(w, "done", map[string]string{"error": "search_failed"}, e)
		return
	}

	view := handler.SearchResultsOf(results)
	for _, hit := range view.Hits {
		writeSSEEvent(w, "hit", hit, e)
		if ferr := w.Flush(); ferr != nil {
			cancel()
			return
		}
	}
	writeSSEEvent(w, "done", map[string]any{
		"truncated": view.Truncated,
		"tier":      view.Tier,
		"deadline":  view.Deadline,
	}, e)

	if ferr := w.Flush(); ferr != nil {
		e.logger.Warn("flushing a search stream", "error", ferr)
	}
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

// searchSourcesOf adapts the core's scan sources for the search service.
//
// The prefix is the label this account navigates the share under, so a hit
// names the path the caller asked about rather than a share-relative
// fragment they would have to reassemble.
func searchSourcesOf(scan []core.ScanSource, owner core.UserID, c *core.Core) []search.Source {
	out := make([]search.Source, 0, len(scan))
	for _, s := range scan {
		label := c.ShareLabel(owner, s.Share)
		if label == "" {
			// A share this account cannot see at all. Measured, removing this
			// changes no result: the per-entry check rejects every entry
			// under such a share anyway. It stays because walking a whole
			// share to reject all of it is work nobody asked for, and because
			// a hit with no label would carry a path starting "/" that names
			// nothing the caller can navigate to.
			continue
		}
		out = append(out, search.Source{
			Share: uint32(s.Share),
			Root:  s.Root,
			Base:  s.Base,
			// Trailing slash included: the walker concatenates this with a
			// share-relative path and inserts no separator, so "/docs" and
			// "report.txt" became "/docsreport.txt". Found by reading what a
			// live server actually streamed; the test that covered this only
			// checked the prefix and passed on the broken value.
			Prefix: "/" + label + "/",
			Allow:  s.Allow,
		})
	}
	return out
}

// searchLimit bounds how many hits one query returns.
//
// An unbounded limit is a walk that reports everything it finds, which for a
// one-character query is the whole tree.
func searchLimit(raw string) int {
	const fallback, ceiling = 100, 1000

	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return min(n, ceiling)
}
