//go:build linux

package check

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
)

// The misplacement this package was rebuilt to fix: the old checker baked
// http.StatusUnprocessableEntity into its refusal, so a service package chose
// an HTTP status. The presentation layer maps ErrRefused to a status exactly
// once, in its own table, and nothing here knows what a status is.
//
// Asserted by reading the imports rather than by convention, because the
// regression is one import line away and it compiles.
func TestThePackageImportsNothingPresentation(t *testing.T) {
	banned := []string{
		"net/http",
		"apierr",
		"github.com/gofiber/fiber",
	}

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) == 0 {
		t.Fatal("no sources found, so this test would pass without checking anything")
	}

	fset := token.NewFileSet()
	checked := 0
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		checked++

		f, err := parser.ParseFile(fset, source, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: %v", source, err)
			}
			for _, bad := range banned {
				if path == bad || strings.Contains(path, bad) {
					t.Errorf("%s imports %q, which puts a transport decision in a service package",
						source, path)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("every source was skipped, so this test proved nothing")
	}
}

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

// The save-time checker and the boot-time loader run the same rule, so a list
// this package accepts is one the loader keeps whole.
//
// Without this they drift: the loader silently drops what the checker let
// through, and a host an administrator saved stops answering with nothing in
// the interface to say why.
func TestTheCheckerAndTheLoaderAgreeOnHostRoles(t *testing.T) {
	cases := []struct {
		name                 string
		appHosts, contentHos []string
	}{
		{"distinct", []string{"app.example.test"}, []string{"content.example.test"}},
		{"overlapping", []string{"both.example.test"}, []string{"both.example.test"}},
		{"case-folded overlap", []string{"Both.example.test"}, []string{"bOTH.example.test"}},
		{"repeat in one list", []string{"a.example.test", "a.example.test"}, nil},
		{"malformed", []string{"https://bad.example.test/x"}, nil},
		{"empty content list", []string{"app.example.test"}, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := map[string]any{"app_hosts": anyList(c.appHosts)}
			if c.contentHos != nil {
				body["content_hosts"] = anyList(c.contentHos)
			}
			// The self host is one of the app hosts, so the lockout probe stays
			// out of what this test is measuring.
			self := ""
			if len(c.appHosts) > 0 {
				self = strings.ToLower(c.appHosts[0])
			}

			findings := Section(Input{Section: "network", Body: body, SelfHost: self})
			checkerRefuses := Blocked(findings)
			sharedRefuses := runtimecfg.CheckHostRoles(c.appHosts, c.contentHos) != nil

			if checkerRefuses != sharedRefuses {
				t.Errorf("the checker refuses=%v but the shared rule refuses=%v for %v / %v: %v",
					checkerRefuses, sharedRefuses, c.appHosts, c.contentHos, keysOf(findings))
			}
		})
	}
}

func anyList(items []string) []any {
	out := make([]any, 0, len(items))
	for _, s := range items {
		out = append(out, s)
	}
	return out
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
