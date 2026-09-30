// Linux only, for the same reason as the rest of this package.
//go:build linux

// The search family's projection.
package search

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/backend/internal/search/svc"
	searchlib "github.com/stowcloud/namesearch"
)

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

// SearchHitViewOf projects one hit, which is what a streamed answer sends one
// frame at a time.
func SearchHitViewOf(h searchlib.Hit) SearchHitView {
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
	return v
}

// IndexEstimateView is what building a name index would cost.
type IndexEstimateView struct {
	// IndexBytes is the estimate, as a decimal string: a large corpus runs
	// past 2^53 and the figure an operator is deciding on would round.
	IndexBytes string `json:"index_bytes"`

	// Confidence says how much to trust the number. An estimate presented
	// without it invites an operator to plan against a figure the estimator
	// itself is unsure of.
	Confidence string `json:"confidence"`

	// BuildSecs is processor time. A build runs only while the server is
	// otherwise idle, so it finishes later than this. A number rather than a
	// decimal string: it is seconds, not a byte count, and nothing here runs
	// past what a JavaScript number holds exactly.
	BuildSecs float64 `json:"build_secs"`

	// RateMeasured says BuildSecs came from what the last completed build on
	// this deployment actually measured rather than the compiled-in guess,
	// so a client can tell an operator which kind of number they are seeing.
	RateMeasured bool `json:"build_rate_measured"`

	// Formula records the derivation term by term, so a wrong estimate shows
	// which term was wrong to somebody checking the arithmetic.
	Formula string `json:"formula"`

	// Files and NameBytes are what was measured.
	Files     string `json:"files"`
	NameBytes string `json:"name_bytes"`

	// Partial marks a scan that hit its bound, so the figures describe a
	// sample. Presenting a fraction as the whole is how an index is sized at
	// a tenth of what it needs.
	Partial bool `json:"partial"`
}

// IndexEstimateOf projects a scan and its estimate.
func IndexEstimateOf(r searchlib.ScanResult, e searchlib.IndexEstimate) IndexEstimateView {
	return IndexEstimateView{
		IndexBytes:   strconv.FormatUint(e.IndexBytes, 10),
		Confidence:   e.Confidence.String(),
		BuildSecs:    e.BuildSeconds,
		RateMeasured: e.RateMeasured,
		Formula:      e.Formula,
		Files:        strconv.FormatUint(r.Stats.Files, 10),
		NameBytes:    strconv.FormatUint(r.Stats.NameBytesTotal, 10),
		Partial:      r.Partial,
	}
}

// IndexStatusView is what the attached index holds right now.
type IndexStatusView struct {
	// Enabled is whether an index is open at all. False means every search
	// walks, which is the ordinary state of a deployment that never turned
	// the index on.
	Enabled bool `json:"enabled"`

	// Entries is the number of names held, as a decimal string for the same
	// reason the estimate's byte count is one. Zero with Enabled set means
	// the switch is on and no build has finished.
	Entries string `json:"entries"`

	// Incomplete says the index knows it covers less than the corpus, so
	// every query declines it and walks. A build that stopped at its ceiling
	// or was interrupted leaves this set.
	Incomplete bool `json:"incomplete"`
}

// IndexStatusOf projects the search service's account of its index.
func IndexStatusOf(s svc.IndexState) IndexStatusView {
	return IndexStatusView{
		Enabled:    s.Attached,
		Entries:    strconv.FormatUint(s.Entries, 10),
		Incomplete: s.Incomplete,
	}
}
