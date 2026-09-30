// Linux only, matching the package under test.
//go:build linux

package search

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	searchlib "github.com/stowcloud/namesearch"
)

// A partial answer says so, whichever way it was cut short. This is the field

// An unmeasured size is absent rather than zero. Reporting zero would show a
// client a 0-byte file that is not one.
func TestAnUnmeasuredSizeIsAbsentNotZero(t *testing.T) {
	unmeasured := SearchHitViewOf(searchlib.Hit{Path: "a/b.txt", Name: "b.txt"})
	if unmeasured.Size != nil || unmeasured.MTimeNs != nil {
		t.Errorf("an unmeasured hit carries %+v", unmeasured)
	}

	raw, err := json.Marshal(unmeasured)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "size") || strings.Contains(string(raw), "mtime") {
		t.Errorf("an unmeasured hit encoded a size or time: %s", raw)
	}

	// A real zero-byte file is a zero, and it is present.
	var zero uint64
	var when int64 = 1700000000000000000
	measured := SearchHitViewOf(searchlib.Hit{Path: "a/empty.txt", Name: "empty.txt", Size: &zero, MTimeNs: &when})
	if measured.Size == nil || *measured.Size != "0" {
		t.Errorf("a measured zero encoded as %v", measured.Size)
	}
	if measured.MTimeNs == nil || *measured.MTimeNs != strconv.FormatInt(when, 10) {
		t.Errorf("a measured time encoded as %v", measured.MTimeNs)
	}
}

// Sizes and times cross as strings, since both exceed a JavaScript number's
// exact range.
func TestSearchSizesCrossAsStrings(t *testing.T) {
	var big uint64 = 1<<53 + 1
	raw, err := json.Marshal(SearchHitViewOf(searchlib.Hit{Size: &big}))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"size":"9007199254740993"`) {
		t.Errorf("the size is not an exact string: %s", raw)
	}
}
