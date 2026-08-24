// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/service"
)

// Searching, streamed as the hits are found.
//
// A stream rather than one document, because a walk over a large share takes
// long enough that a screen showing nothing until it finishes looks broken. The
// client renders each hit as it arrives and stops when the end event says the
// search is over.
//
// The permission check is per entry, inside the walk, not per share: a grant
// can begin partway down a tree, so a share-level answer would either hide a
// readable subtree or return names from an unreadable one.

// searchHit is the wire shape. The share is kept separate from the
// share-relative path, and the client puts the two back together, because a
// share label may itself contain a separator and only this side knows where
// the label ends.
type searchHit struct {
	Share string `json:"share"`
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	// Null rather than zero when the stat phase did not run: a size of zero is
	// a fact about a file and a null is the absence of one.
	Size *uint64 `json:"size"`
	// A decimal string, because a nanosecond epoch is past what a JSON number
	// survives in a browser.
	MTimeNs *string `json:"mtime_ns"`
	Score   float32 `json:"score"`
}

// SearchStream answers GET /api/search/stream.
func SearchStream(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		if d.Search == nil {
			return notImplemented("search.unavailable")
		}

		query := r.URL.Query().Get("q")
		if query == "" {
			return apierr.BadRequest("search.no_query", "q")
		}
		opt := service.QueryOptions{
			Query: query,
			Scope: r.URL.Query().Get("scope"),
			// The screen shows a size and a time per row, so the stat phase
			// runs. It is the expensive half, and asking for it is what the
			// client's own rendering needs rather than a default.
			WithMetadata: true,
		}
		if v := r.URL.Query().Get("limit"); v != "" {
			n, perr := strconv.Atoi(v)
			if perr != nil || n <= 0 {
				return apierr.BadRequest("search.bad_limit", "limit")
			}
			opt.Limit = n
		}

		// The stream is opened before the search starts, so a client sees the
		// connection succeed rather than waiting on a walk to decide whether
		// there is a response at all.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		// The proxy contract: a buffering proxy holds a stream until it ends,
		// which is exactly the delay streaming exists to avoid.
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		flusher, canFlush := w.(http.Flusher)
		if canFlush {
			flusher.Flush()
		}

		// Only what this account may read is searched, and the check runs per
		// entry inside the walk.
		results, serr := d.Search.Query(r.Context(), d.Core.UserScanSources(uid), opt)
		if serr != nil {
			// The status is long gone. The stream ends with an event saying
			// so, which the client can show, rather than closing silently and
			// leaving a spinner running.
			d.Log.Warn("a search failed", "error", serr)
			writeEvent(w, "done", map[string]any{"error": "the search did not complete"})
			return nil
		}

		for _, h := range results.Hits {
			hit := searchHit{
				Share: d.Core.ShareLabel(uid, h.Share),
				Path:  h.Path,
				Name:  h.Name,
				IsDir: h.IsDir,
				Size:  h.Size,
				Score: h.Score,
			}
			if h.MTimeNs != nil {
				s := strconv.FormatInt(*h.MTimeNs, 10)
				hit.MTimeNs = &s
			}
			writeEvent(w, "hit", hit)
			if canFlush {
				flusher.Flush()
			}
		}

		// The end event carries whether the answer is complete. A truncated
		// result presented as a whole one is the worst outcome a search has:
		// the person concludes the file is not there.
		writeEvent(w, "done", map[string]any{
			"truncated": results.Truncated,
			"tier":      results.Tier.String(),
		})
		if canFlush {
			flusher.Flush()
		}
		return nil
	})
}

// writeEvent emits one event.
//
// A failure has nowhere to go: the status is sent and the body is a stream, so
// a write that fails means the client is gone and the next one will fail too.
func writeEvent(w http.ResponseWriter, event string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body) //nolint:errcheck // the response is already committed; a failed write means the client left.
}
