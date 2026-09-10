//go:build linux

package search_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
)

// The kind filter decides on what an entry is, and the extension filter on
// what it is called.
func TestTheFilterAdmitsByKindAndExtension(t *testing.T) {
	t.Parallel()

	pdfOnly := search.Filter{Exts: []string{"pdf"}}
	for _, c := range []struct {
		what   string
		filter search.Filter
		name   string
		isDir  bool
		want   bool
	}{
		{"the empty filter takes a file", search.Filter{}, "report.pdf", false, true},
		{"the empty filter takes a folder", search.Filter{}, "documents", true, true},
		{"files only drops a folder", search.Filter{Kind: search.KindFile}, "documents", true, false},
		{"files only takes a file", search.Filter{Kind: search.KindFile}, "report.pdf", false, true},
		{"folders only drops a file", search.Filter{Kind: search.KindDir}, "report.pdf", false, false},
		{"folders only takes a folder", search.Filter{Kind: search.KindDir}, "documents", true, true},
		{"an extension matches", pdfOnly, "report.pdf", false, true},
		{"an extension folds case", pdfOnly, "REPORT.PDF", false, true},
		{"another extension does not", pdfOnly, "report.txt", false, false},
		{"a near miss does not", pdfOnly, "report.pdfx", false, false},
		{"a name with no extension does not", pdfOnly, "report", false, false},
		{"a folder never carries an extension", pdfOnly, "photos.pdf", true, false},
		{"one of several matches", search.Filter{Exts: []string{"txt", "md"}}, "notes.md", false, true},
	} {
		if got := c.filter.Admits(c.name, c.isDir); got != c.want {
			t.Errorf("%s: %q (dir=%v) admitted=%v, want %v", c.what, c.name, c.isDir, got, c.want)
		}
	}
}

// A dotfile's leading dot is its name, not an extension. Filtering for "gz"
// must not hand back every file whose name begins with a dot.
func TestTheExtensionOfANameFollowsTheDotConvention(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ name, want string }{
		{"report.pdf", "pdf"},
		{"archive.tar.gz", "gz"},
		{"REPORT.PDF", "pdf"},
		{"report", ""},
		{".bashrc", ""},
		{"report.", ""},
		// The extension itself is what gets folded, so the case that exercises
		// the Unicode path has to carry the non-ASCII letter after the dot.
		{"scan.TÜR", "tür"},
		{"Ünterlagen.JPEG", "jpeg"},
	} {
		if got := search.ExtensionOf(c.name); got != c.want {
			t.Errorf("the extension of %q is %q, want %q", c.name, got, c.want)
		}
	}
}

// The wire list is normalised once, here, so the per-entry comparison is byte
// equality against an already folded name.
func TestParsingAnExtensionList(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		raw  string
		want []string
		ok   bool
	}{
		{"", nil, true},
		{"pdf", []string{"pdf"}, true},
		{"PDF, .TXT", []string{"pdf", "txt"}, true},
		{"pdf,,txt,", []string{"pdf", "txt"}, true},
		{"pdf,pdf", []string{"pdf"}, true},
		{"pdf/../etc", nil, false},
		{tooManyExts(), nil, false},
	} {
		got, ok := search.ParseExts(c.raw)
		if ok != c.ok {
			t.Errorf("parsing %q accepted=%v, want %v", c.raw, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("parsing %q gave %v, want %v", c.raw, got, c.want)
		}
	}
}

// tooManyExts names one more distinct extension than a query may carry. The
// list is compared against every matched entry and arrives from a client.
func tooManyExts() string {
	parts := make([]string, 0, search.MaxExts+1)
	for i := range search.MaxExts + 1 {
		parts = append(parts, "e"+strconv.Itoa(i))
	}
	return strings.Join(parts, ",")
}

// An unknown kind is refused. Widening it back to everything would answer a
// narrow question with the whole tree and say nothing about having done so.
func TestAnUnknownKindIsRefused(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		raw  string
		want search.Kind
		ok   bool
	}{
		{"", search.KindAny, true},
		{"any", search.KindAny, true},
		{"file", search.KindFile, true},
		{"dir", search.KindDir, true},
		{"folder", search.KindAny, false},
		{"File", search.KindAny, false},
	} {
		got, ok := search.ParseKind(c.raw)
		if ok != c.ok || got != c.want {
			t.Errorf("parsing %q gave (%v, %v), want (%v, %v)", c.raw, got, ok, c.want, c.ok)
		}
		if ok && c.raw != "" && got.String() != c.raw {
			t.Errorf("%q does not round trip, it spells back as %q", c.raw, got.String())
		}
	}
}
