package vfs

import (
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// FuzzParseVpath drives the one parser every path from the wire goes through.
//
// The property it asserts is not "does not crash". It is that anything the
// parser accepts is already a path that cannot escape: no traversal component
// survived, no NUL survived, no reserved prefix survived, and the accepted
// string is exactly what went in, because a parser that repairs its input is a
// parser whose output nobody can reason about.
func FuzzParseVpath(f *testing.F) {
	seeds := []string{
		"",
		"media",
		"media/movies/a.mkv",
		"..",
		"a/../b",
		"/etc/passwd",
		"a//b",
		"a/",
		"a\x00b",
		".sctrash",
		"a/.scpart-0123456789abcdef",
		"café/café",
		"café/café",
		"\xff\xfe",
		strings.Repeat("a/", 300),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		v, err := ParseVpath(s)
		if err != nil {
			return
		}
		// One leading slash is accepted and dropped, and that is the only
		// difference the parser is allowed to make to its input. The client's
		// path model is rooted and this one is not, so the two spellings name
		// the same virtual path; everything below still has to hold of what
		// comes back.
		s = strings.TrimPrefix(s, "/")
		if v.String() != s {
			t.Fatalf("ParseVpath repaired its input into %q, beyond dropping one leading slash", v)
		}
		if len(s) > limits.PathBytes {
			t.Fatalf("ParseVpath accepted %d bytes, over the %d byte bound", len(s), limits.PathBytes)
		}
		if s == "" {
			return
		}
		comps := strings.Split(s, "/")
		if len(comps) > limits.PathComponents {
			t.Fatalf("ParseVpath accepted %d components, over the %d bound", len(comps), limits.PathComponents)
		}
		for _, c := range comps {
			switch {
			case c == "":
				t.Fatalf("ParseVpath(%q) accepted an empty component", s)
			case c == "." || c == "..":
				t.Fatalf("ParseVpath(%q) accepted a traversal component", s)
			case strings.IndexByte(c, 0) >= 0:
				t.Fatalf("ParseVpath(%q) accepted a NUL", s)
			case len(c) > limits.NameBytes:
				t.Fatalf("ParseVpath(%q) accepted a %d byte component", s, len(c))
			case IsReservedName(c):
				t.Fatalf("ParseVpath(%q) accepted a control-file prefix", s)
			}
		}

		// Whatever parsed has to survive the crossing into the vocabulary the
		// resolver accepts, because that crossing is what every request makes.
		safe, err := v.Rest().Safe()
		if err != nil {
			t.Fatalf("ParseVpath accepted %q but its remainder is not a SafePath: %v", s, err)
		}
		if got := safe.String(); got != v.Rest().String() {
			t.Fatalf("the crossing rewrote %q into %q", v.Rest(), got)
		}
	})
}
