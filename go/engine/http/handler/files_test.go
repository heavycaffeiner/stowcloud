// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Sizes and timestamps cross as strings. A file past 2^53 bytes that comes
// back with a different size is a download that comes back wrong.
func TestEntrySizesAndTimesCrossAsStrings(t *testing.T) {
	const big = uint64(1)<<53 + 1
	const when = int64(1700000000123456789)

	v := EntryOf(core.Entry{Size: big, MTimeNs: when}, "")

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"size":"9007199254740993"`) {
		t.Errorf("the size lost exactness: %s", raw)
	}
	if !strings.Contains(string(raw), `"mtime_ns":"1700000000123456789"`) {
		t.Errorf("the timestamp lost exactness: %s", raw)
	}

	// The round trip a client performs.
	var back EntryView
	if derr := json.Unmarshal(raw, &back); derr != nil {
		t.Fatalf("decoding: %v", derr)
	}
	got, perr := strconv.ParseUint(back.Size, 10, 64)
	if perr != nil || got != big {
		t.Errorf("the size round-tripped to %d (%v)", got, perr)
	}
}

// A filesystem with no birth time reports nothing rather than zero, because
// zero is a real timestamp and would show a file created in 1970.
func TestAMissingBirthTimeIsAbsentNotZero(t *testing.T) {
	absent := EntryOf(core.Entry{}, "")
	if absent.BTimeNs != nil {
		t.Errorf("a missing birth time is %v", absent.BTimeNs)
	}
	raw, err := json.Marshal(absent)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "btime") {
		t.Errorf("a missing birth time was encoded: %s", raw)
	}

	// A real epoch birth time is present and is zero.
	var epoch int64
	got := EntryOf(core.Entry{BTimeNs: &epoch}, "")
	if got.BTimeNs == nil || *got.BTimeNs != "0" {
		t.Errorf("a real zero birth time encoded as %v", got.BTimeNs)
	}
}

// Permissions cross as names. The bits are an internal encoding, and a client
// that learned them would make adding one a wire change.
func TestPermissionsCrossAsNames(t *testing.T) {
	v := EntryOf(core.Entry{Perms: acl.Read | acl.Download}, "")

	if len(v.Perms) != 2 {
		t.Fatalf("the permissions are %v", v.Perms)
	}
	// The model's own order, so the same set is always the same list.
	if v.Perms[0] != "read" || v.Perms[1] != "download" {
		t.Errorf("the permissions are %v", v.Perms)
	}

	// No permissions is an empty list rather than null.
	raw, err := json.Marshal(EntryOf(core.Entry{}, ""))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"perms":[]`) {
		t.Errorf("an entry with no permissions encoded as %s", raw)
	}
}

// A symlink is not a directory whatever it points at, and both the kind and
// the flag say so: a client needs to tell one from a file to draw it, and a
// boolean cannot.
func TestASymlinkIsNotADirectory(t *testing.T) {
	// IsDir is Kind.IsDir() on the service side, so a symlink carries false
	// with a kind of its own.
	v := EntryOf(core.Entry{Name: "link", IsDir: false}, "")
	if v.IsDir {
		t.Error("a symlink was projected as a directory")
	}
	if v.Kind == "" {
		t.Error("the entry carries no kind")
	}
}

// The page counts describe the whole directory rather than the page in hand,
// which is what lets a grid place a scrollbar without loading every row.
func TestPageCountsDescribeTheWholeDirectory(t *testing.T) {
	p := PageOf(core.Page{
		Entries: []core.Entry{{Name: "a"}, {Name: "b"}},
		Dirs:    7,
		Total:   500,
		Next:    core.Cursor("2"),
	}, func(core.Entry) string { return "" })

	if len(p.Entries) != 2 {
		t.Fatalf("the page carries %d entries", len(p.Entries))
	}
	if p.Dirs != 7 || p.Total != 500 {
		t.Errorf("the counts are dirs=%d total=%d", p.Dirs, p.Total)
	}
	if p.Next != "2" {
		t.Errorf("the cursor is %q", p.Next)
	}
}

// The final page has no cursor, so its absence is what a client tests rather
// than comparing counts.
func TestTheFinalPageCarriesNoCursor(t *testing.T) {
	raw, err := json.Marshal(PageOf(core.Page{}, func(core.Entry) string { return "" }))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "next") {
		t.Errorf("the final page carried a cursor: %s", raw)
	}
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Errorf("an empty page encoded as %s", raw)
	}
}
