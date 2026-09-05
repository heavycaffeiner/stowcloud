package vfs

import (
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/uniname"
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

		// The accepted spelling is normalized, so the property is not byte
		// equality with the input. It is that the result is a fixed point:
		// well-formed, composed, and unchanged by parsing it again. Asserting
		// that instead of recomputing the expected normalization is
		// deliberate: a test that re-runs the code under test against itself
		// proves nothing, and it caught the detector's own instability as a
		// test failure rather than as the product bug it was.
		got := v.String()
		if !uniname.IsNormalized(got) {
			t.Fatalf("ParseVpath(%q) returned %q, which is not well-formed NFC UTF-8", s, got)
		}
		again, err := ParseVpath(got)
		if err != nil {
			t.Fatalf("ParseVpath(%q) returned %q, which does not parse: %v", s, got, err)
		}
		if again.String() != got {
			t.Fatalf("parsing is not a fixed point: %q became %q then %q", s, got, again.String())
		}
		trimmed := strings.TrimPrefix(s, "/")
		if uniname.IsNormalized(trimmed) && got != trimmed {
			t.Fatalf("ParseVpath(%q) rewrote an already normal path into %q", s, got)
		}

		want := got
		if len(want) > limits.PathBytes {
			t.Fatalf("ParseVpath(%q) accepted %d bytes, over the %d bound", s, len(want), limits.PathBytes)
		}
		if want == "" {
			return
		}

		comps := strings.Split(want, "/")
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
// it accepts must round-trip through String and Components up to
// normalization, and a leading slash must never be accepted here the way
// ParseVpath accepts one.
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
		// The same fixed-point property as ParseVpath, for the same reason:
		// recomputing the expected normalization would only compare the code
		// under test against itself.
		got := p.String()
		if !uniname.IsNormalized(got) {
			t.Fatalf("ParseSafePath(%q) returned %q, which is not well-formed NFC UTF-8", s, got)
		}
		again, err := ParseSafePath(got)
		if err != nil {
			t.Fatalf("ParseSafePath(%q) returned %q, which does not parse: %v", s, got, err)
		}
		if again.String() != got {
			t.Fatalf("parsing is not a fixed point: %q became %q then %q", s, got, again.String())
		}
		if uniname.IsNormalized(s) && got != s {
			t.Fatalf("ParseSafePath(%q) rewrote an already normal path into %q", s, got)
		}
		comps := p.Components()
		if s == "" && len(comps) != 0 {
			t.Fatalf("the empty path should have zero components, got %v", comps)
		}
		if s != "" && strings.Join(comps, "/") != got {
			t.Fatalf("Components() = %v does not rejoin to %q", comps, got)
		}
	})
}
