package vfs

import (
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// FuzzParseVpath asserts more than "does not panic": for any input the
// parser accepts, the result differs from the input by at most stripping one
// leading slash, every bound still holds, no traversal component survived,
// and the accepted value crosses cleanly into SafePath.
func FuzzParseVpath(f *testing.F) {
	seeds := []string{
		"",
		"/",
		"media",
		"media/movies/a.mkv",
		"..",
		".",
		"a/../b",
		"a/..",
		"/etc/passwd",
		"a//b",
		"a/",
		"a\x00b",
		".sctrash",
		".scpart-",
		"a/.scpart-0123456789abcdef",
		"café/café",
		"cafe\u0301/cafe\u0301",
		"\xff\xfe",
		strings.Repeat("a/", 300),
		strings.Repeat("a", 5000),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		v, err := ParseVpath(s)
		if err != nil {
			return
		}

		trimmed := strings.TrimPrefix(s, "/")
		if v.String() != trimmed {
			t.Fatalf("ParseVpath(%q) changed more than a leading slash: got %q", s, v.String())
		}
		if len(trimmed) > limits.PathBytes {
			t.Fatalf("ParseVpath(%q) accepted %d bytes, over the %d bound", s, len(trimmed), limits.PathBytes)
		}
		if trimmed == "" {
			return
		}

		comps := strings.Split(trimmed, "/")
		if len(comps) > limits.PathComponents {
			t.Fatalf("ParseVpath(%q) accepted %d components, over the %d bound", s, len(comps), limits.PathComponents)
		}
		for _, c := range comps {
			switch {
			case c == "":
				t.Fatalf("ParseVpath(%q) accepted an empty component", s)
			case c == "." || c == "..":
				t.Fatalf("ParseVpath(%q) accepted a traversal component %q", s, c)
			case strings.IndexByte(c, 0) >= 0:
				t.Fatalf("ParseVpath(%q) accepted a NUL byte", s)
			case len(c) > limits.NameBytes:
				t.Fatalf("ParseVpath(%q) accepted a %d byte component", s, len(c))
			case IsReservedName(c):
				t.Fatalf("ParseVpath(%q) accepted a reserved prefix in %q", s, c)
			}
		}

		safe, err := v.Rest().Safe()
		if err != nil {
			t.Fatalf("ParseVpath(%q) accepted but its remainder %q does not cross into SafePath: %v",
				s, v.Rest(), err)
		}
		if got := safe.String(); got != v.Rest().String() {
			t.Fatalf("crossing into SafePath rewrote %q into %q", v.Rest(), got)
		}
	})
}

// FuzzParseSafePath drives the filesystem-facing parser directly: anything
// it accepts must round-trip through String and Components without loss,
// and a leading slash must never be accepted here the way ParseVpath accepts
// one.
func FuzzParseSafePath(f *testing.F) {
	seeds := []string{
		"",
		"a",
		"a/b/c",
		"/a",
		"//a",
		"..",
		"a/../b",
		"a\x00b",
		".sctrash",
		"café",
		"cafe\u0301",
		strings.Repeat("a/", 300),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParseSafePath(s)
		if err != nil {
			return
		}
		if strings.HasPrefix(s, "/") {
			t.Fatalf("ParseSafePath(%q) accepted an absolute path", s)
		}
		if p.String() != s {
			t.Fatalf("ParseSafePath(%q).String() = %q, want the input unchanged", s, p.String())
		}
		comps := p.Components()
		if s == "" && len(comps) != 0 {
			t.Fatalf("the empty path should have zero components, got %v", comps)
		}
		if s != "" && strings.Join(comps, "/") != s {
			t.Fatalf("Components() = %v does not rejoin to %q", comps, s)
		}
	})
}
