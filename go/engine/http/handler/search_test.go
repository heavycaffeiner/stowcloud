// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
)

// A partial answer says so, whichever way it was cut short. This is the field
// a caller reads to know whether an empty list means "nothing matches".
func TestAPartialAnswerIsNeverReportedComplete(t *testing.T) {
	for _, c := range []struct {
		what     string
		results  svc.Results
		complete bool
	}{
		{"a whole answer", svc.Results{}, true},
		{"a truncated answer", svc.Results{Truncated: true}, false},
		{"a timed-out walk", svc.Results{Deadline: true}, false},
		{"both at once", svc.Results{Truncated: true, Deadline: true}, false},
	} {
		got := SearchResultsOf(c.results)
		if got.Complete != c.complete {
			t.Errorf("%s reported complete=%v", c.what, got.Complete)
		}
		// The two reasons stay distinguishable, because they call for
		// different actions: narrow the query, or ask again.
		if got.Truncated != c.results.Truncated || got.Deadline != c.results.Deadline {
			t.Errorf("%s lost which of the two applied: %+v", c.what, got)
		}
	}
}

// An unmeasured size is absent rather than zero. Reporting zero would show a
// client a 0-byte file that is not one.
func TestAnUnmeasuredSizeIsAbsentNotZero(t *testing.T) {
	unmeasured := SearchResultsOf(svc.Results{Hits: []search.Hit{{Path: "a/b.txt", Name: "b.txt"}}})
	if len(unmeasured.Hits) != 1 {
		t.Fatalf("the projection produced %d hits", len(unmeasured.Hits))
	}
	if unmeasured.Hits[0].Size != nil || unmeasured.Hits[0].MTimeNs != nil {
		t.Errorf("an unmeasured hit carries %+v", unmeasured.Hits[0])
	}

	raw, err := json.Marshal(unmeasured.Hits[0])
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "size") || strings.Contains(string(raw), "mtime") {
		t.Errorf("an unmeasured hit encoded a size or time: %s", raw)
	}

	// A real zero-byte file is a zero, and it is present.
	var zero uint64
	var when int64 = 1700000000000000000
	measured := SearchResultsOf(svc.Results{Hits: []search.Hit{
		{Path: "a/empty.txt", Name: "empty.txt", Size: &zero, MTimeNs: &when},
	}})
	if measured.Hits[0].Size == nil || *measured.Hits[0].Size != "0" {
		t.Errorf("a measured zero encoded as %v", measured.Hits[0].Size)
	}
	if measured.Hits[0].MTimeNs == nil || *measured.Hits[0].MTimeNs != strconv.FormatInt(when, 10) {
		t.Errorf("a measured time encoded as %v", measured.Hits[0].MTimeNs)
	}
}

// Sizes and times cross as strings, since both exceed a JavaScript number's
// exact range.
func TestSearchSizesCrossAsStrings(t *testing.T) {
	var big uint64 = 1<<53 + 1
	got := SearchResultsOf(svc.Results{Hits: []search.Hit{{Size: &big}}})

	raw, err := json.Marshal(got.Hits[0])
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"size":"9007199254740993"`) {
		t.Errorf("the size is not an exact string: %s", raw)
	}
}

// Every reason has a name, and the answering case has none, so the field is
// absent rather than carrying a placeholder a client compares against.
func TestEveryFallbackReasonIsNamed(t *testing.T) {
	if got := fallbackName(index.FallbackNone); got != "" {
		t.Errorf("the answering case is named %q", got)
	}
	for _, c := range []struct {
		reason index.FallbackReason
		name   string
	}{
		{index.FallbackQueryTooShort, "query_too_short"},
		{index.FallbackAllTrigramsPruned, "all_trigrams_pruned"},
		{index.FallbackIncomplete, "incomplete"},
	} {
		if got := fallbackName(c.reason); got != c.name {
			t.Errorf("the reason %v is named %q, want %q", c.reason, got, c.name)
		}
	}
	if got := fallbackName(index.FallbackReason(99)); got != "unknown" {
		t.Errorf("an unnamed reason is called %q", got)
	}

	// Absent from the body when the index answered.
	raw, err := json.Marshal(SearchResultsOf(svc.Results{}))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "fallback") {
		t.Errorf("an answering index encoded a fallback: %s", raw)
	}
}

// An incomplete index is reported, which is what tells an operator that an
// index exists and did not contribute rather than that none is configured.
func TestAnIncompleteIndexIsReported(t *testing.T) {
	got := SearchResultsOf(svc.Results{Fallback: index.FallbackIncomplete})
	if got.Fallback != "incomplete" {
		t.Errorf("an incomplete index reported %q", got.Fallback)
	}
}

// An empty result is an empty list rather than null, so a client iterating the
// hits does not have to test the field first.
func TestAnEmptySearchResultCarriesAList(t *testing.T) {
	raw, err := json.Marshal(SearchResultsOf(svc.Results{Elapsed: 5 * time.Millisecond}))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"hits":[]`) {
		t.Errorf("an empty result encoded as %s", raw)
	}
	if !strings.Contains(string(raw), `"elapsed_ms":"5"`) {
		t.Errorf("the elapsed time encoded as %s", raw)
	}
}
