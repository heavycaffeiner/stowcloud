//go:build linux

package check

import (
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/admin/settings/runtimecfg"
)

// A repeat within one list is named by its own field, so the screen puts the
// message beside the list it is about.
func TestADuplicateHostIsNamedByItsField(t *testing.T) {
	got := Section(Input{
		Section: "network",
		Body: map[string]any{
			"app_hosts": []any{"files.example.test", "FILES.example.test"},
		},
		SelfHost: "files.example.test",
	})

	f := mustFind(t, got, keyDuplicateHost)
	if !f.Blocking {
		t.Error("a duplicate host was accepted")
	}
	if f.Field != "app_hosts" {
		t.Errorf("the duplicate was reported against %q", f.Field)
	}
	// A repeat within one list is not a role conflict, and saying so would send
	// the administrator looking at the other list.
	mustNotFind(t, got, keyHostRoleConflict)
}

// A conflict across the two lists names both roles, so the message says which
// other list to look at.
func TestARoleConflictNamesBothFields(t *testing.T) {
	got := Section(Input{
		Section: "network",
		Body: map[string]any{
			"app_hosts":     []any{"files.example.test"},
			"content_hosts": []any{"files.example.test"},
		},
		SelfHost: "files.example.test",
	})

	f := mustFind(t, got, keyHostRoleConflict)
	if field, _ := f.Arg("field"); field != "content_hosts" {
		t.Errorf("the conflict was reported against %q", field)
	}
	if other, _ := f.Arg("other_field"); other != "app_hosts" {
		t.Errorf("the conflict named the other role as %q", other)
	}
	mustNotFind(t, got, keyDuplicateHost)
}

// The bounds a refusal reports are the ones the loader clamps to, so the range
// in the message is the range that is actually enforced.
func TestTheReportedRangeIsTheEnforcedRange(t *testing.T) {
	for field, b := range runtimecfg.Bounds() {
		section, key, ok := strings.Cut(field, ".")
		if !ok {
			t.Fatalf("the bound key %q is not section.field", field)
		}

		over := Section(Input{Section: section, Body: map[string]any{key: float64(b.Max + 1)}})
		f, found := find(t, over, keyOutOfRange)
		if !found {
			// Not every bounded field lives in a section this checker probes;
			// what must not happen is a refusal quoting a different range.
			continue
		}
		gotMin, _ := f.Arg("min")
		gotMax, _ := f.Arg("max")
		if gotMin != strconv.FormatInt(b.Min, 10) || gotMax != strconv.FormatInt(b.Max, 10) {
			t.Errorf("%s refuses quoting %s..%s, but the loader clamps to %d..%d",
				field, gotMin, gotMax, b.Min, b.Max)
		}

		under := Section(Input{Section: section, Body: map[string]any{key: float64(b.Min)}})
		mustNotFind(t, under, keyOutOfRange)
	}
}

// A garbled proc file skips the watch check rather than blocking a save: a
// kernel that does not report its limit is not the administrator's mistake.
func TestAGarbledWatchLimitSkipsTheCheck(t *testing.T) {
	// The parser is what decides, so it is what gets the bad input.
	for _, bad := range []string{"", "   ", "not a number", "-1", "0", "8192 8192"} {
		if _, ok := parseWatchLimit(bad); ok {
			t.Errorf("the limit %q parsed as usable", bad)
		}
	}
	for _, good := range []struct {
		in   string
		want int
	}{{"8192", 8192}, {" 65536\n", 65536}} {
		got, ok := parseWatchLimit(good.in)
		if !ok || got != good.want {
			t.Errorf("parseWatchLimit(%q) = %d, %v", good.in, got, ok)
		}
	}
}
