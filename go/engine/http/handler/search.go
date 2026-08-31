// Linux only, for the same reason as the rest of this package.
//go:build linux

// The search family's projection.
//
// The interesting part is not the hits. It is that a result which is partial
// says so: a walk that ran out of time, a limit that cut the list, and an
// index that declined to answer are three different reasons a client is
// looking at less than everything, and none of them is an error.
package handler

import (
	"context"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
)

// SearchAPI is what this family needs from the service layer.
type SearchAPI interface {
	Query(ctx context.Context, sources []search.Source, opt svc.QueryOptions) (svc.Results, error)
	HasIndex() bool
}

// SearchHitView is one match.
//
// Size and the modification time are pointers on the service side and stay
// absent here when the query never stated. Reporting a zero for "not measured"
// would show a client a 0-byte file that is not one.
type SearchHitView struct {
	Path    string  `json:"path"`
	Name    string  `json:"name"`
	IsDir   bool    `json:"is_dir"`
	Share   string  `json:"share"`
	Size    *string `json:"size,omitempty"`
	MTimeNs *string `json:"mtime_ns,omitempty"`
	Score   float32 `json:"score"`
}

// SearchResultsView is one query's answer.
type SearchResultsView struct {
	Hits []SearchHitView `json:"hits"`

	// Complete is false whenever the client is looking at less than everything
	// the query would have matched. It is the one field a caller has to read
	// to know whether "no results" means "nothing matches".
	Complete bool `json:"complete"`

	// Truncated says the limit cut the list; Deadline says the walk ran out of
	// time. Separate because they call for different actions: narrow the query,
	// or ask again.
	Truncated bool `json:"truncated"`
	Deadline  bool `json:"deadline"`

	// Tier names what answered, and Fallback why the index did not, where it
	// did. An operator reading these can tell an index that is absent from one
	// that exists and declined.
	Tier     string `json:"tier"`
	Fallback string `json:"fallback,omitempty"`

	ElapsedMs string `json:"elapsed_ms"`
}

// SearchResultsOf projects one query's answer.
func SearchResultsOf(r svc.Results) SearchResultsView {
	out := SearchResultsView{
		Hits:      make([]SearchHitView, 0, len(r.Hits)),
		Truncated: r.Truncated,
		Deadline:  r.Deadline,
		Tier:      r.Tier.String(),
		Fallback:  fallbackName(r.Fallback),
		ElapsedMs: strconv.FormatInt(r.Elapsed.Milliseconds(), 10),
	}
	// Complete is derived rather than reported, so a caller cannot forget one
	// of the two ways a result is partial. Both are already flags on the
	// service's own value; this is the single answer built from them.
	out.Complete = !r.Truncated && !r.Deadline

	for _, h := range r.Hits {
		v := SearchHitView{
			Path:  h.Path,
			Name:  h.Name,
			IsDir: h.IsDir,
			Share: strconv.FormatUint(uint64(h.Share), 10),
			Score: h.Score,
		}
		if h.Size != nil {
			s := strconv.FormatUint(*h.Size, 10)
			v.Size = &s
		}
		if h.MTimeNs != nil {
			m := strconv.FormatInt(*h.MTimeNs, 10)
			v.MTimeNs = &m
		}
		out.Hits = append(out.Hits, v)
	}
	return out
}

// fallbackName is the wire name of an index's reason for declining.
//
// Empty when the index answered, so the field is absent rather than carrying a
// placeholder a client would have to compare against.
func fallbackName(f index.FallbackReason) string {
	switch f {
	case index.FallbackNone:
		return ""
	case index.FallbackQueryTooShort:
		return "query_too_short"
	case index.FallbackAllTrigramsPruned:
		return "all_trigrams_pruned"
	case index.FallbackIncomplete:
		return "incomplete"
	default:
		return "unknown"
	}
}
