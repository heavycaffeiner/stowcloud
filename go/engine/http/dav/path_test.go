//go:build linux

package dav

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// The rule the package rests on: a boundary cannot appear after the check that
// looked at it. Decoding first would turn each of these into two segments, and
// the second one is a traversal that no check ever saw.
func TestAnEncodedSeparatorNeverCreatesABoundary(t *testing.T) {
	cases := []string{
		"/a%2fb",
		"/a%2Fb",
		"/%2f",
		"/a/%2f..%2fetc",
		"/share/%2e%2e%2fsecret",
	}

	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			segs, err := SplitPath(raw)
			if err == nil {
				t.Fatalf("%q split into %q instead of being refused", raw, segs)
			}
			if !errors.Is(err, ErrEncodedSeparator) && !errors.Is(err, ErrDotSegment) {
				t.Errorf("%q: want a separator or dot refusal, got %v", raw, err)
			}
		})
	}
}

// Decoding the whole path first is what the split guards against. This shows
// the difference is real: the standard whole-path decoder turns the same input
// into a traversal, and this decoder does not.
func TestDecodingTheWholePathFirstWouldDiffer(t *testing.T) {
	const raw = "/share/%2e%2e%2fetc"

	whole, err := url.PathUnescape(raw)
	if err != nil {
		t.Fatalf("the standard decoder refused %q: %v", raw, err)
	}
	if !strings.Contains(whole, "../") {
		t.Fatalf("the premise is wrong: %q did not decode to a traversal, got %q", raw, whole)
	}

	if _, err := SplitPath(raw); err == nil {
		t.Fatal("the split decoder accepted what the whole-path decoder turns into a traversal")
	}
}

// The refusals, each distinguished so a caller can tell them apart.
func TestThePathRefusals(t *testing.T) {
	cases := []struct {
		raw  string
		want error
	}{
		{"/a%", ErrBadEscape},
		{"/a%2", ErrBadEscape},
		{"/a%zz", ErrBadEscape},
		{"/a%2g", ErrBadEscape},
		{"/a%00b", ErrNUL},
		{"/..", ErrDotSegment},
		{"/.", ErrDotSegment},
		{"/a/../b", ErrDotSegment},
		{"/%2e%2e", ErrDotSegment},
		{"/a%2Fb", ErrEncodedSeparator},
	}

	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			_, err := SplitPath(c.raw)
			if !errors.Is(err, c.want) {
				t.Errorf("%q: want %v, got %v", c.raw, c.want, err)
			}
		})
	}
}

// What a valid path decodes to.
func TestWhatAValidPathDecodesTo(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"/", []string{}},
		{"", []string{}},
		{"/a", []string{"a"}},
		{"/a/b", []string{"a", "b"}},
		{"/a//b", []string{"a", "b"}},
		{"/a/b/", []string{"a", "b"}},
		{"/a%20b", []string{"a b"}},
		{"/a+b", []string{"a+b"}},
		{"/%E2%9C%93", []string{"\u2713"}},
		{"/a%25b", []string{"a%b"}},
	}

	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			got, err := SplitPath(c.raw)
			if err != nil {
				t.Fatalf("%q: %v", c.raw, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("%q: got %q, want %q", c.raw, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("%q segment %d: got %q, want %q", c.raw, i, got[i], c.want[i])
				}
			}
		})
	}
}

// A plus is a literal in a path. Treating it as a space is a query-string
// rule, and applying it here renames every file with a plus in its name.
func TestAPlusIsALiteral(t *testing.T) {
	got, err := SplitPath("/a+b%2Bc")
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if len(got) != 1 || got[0] != "a+b+c" {
		t.Errorf("got %q, want [a+b+c]", got)
	}
}

// The href encoder escapes what XML would otherwise read as markup. The
// standard path escaper leaves these alone, which is why there is a second
// encoder rather than a call to it.
func TestHrefEscapesWhatXmlWouldReadAsMarkup(t *testing.T) {
	for _, seg := range []string{"a&b", "a<b", "a>b", `a"b`, "a'b"} {
		t.Run(seg, func(t *testing.T) {
			href := EncodeHref([]string{seg}, false)
			for _, bad := range []string{"&", "<", ">", `"`, "'"} {
				if strings.Contains(href, bad) {
					t.Errorf("%q encoded to %q, which still carries %q", seg, href, bad)
				}
			}

			// And confirm the premise: the standard escaper does leave it.
			if std := (&url.URL{Path: "/" + seg}).EscapedPath(); !strings.ContainsAny(std, `&<>"'`) {
				t.Logf("the standard escaper handled %q as %q", seg, std)
			}
		})
	}
}

// A round trip through both halves. Whatever the encoder writes, the decoder
// reads back as the same segments.
func TestHrefRoundTripsThroughTheDecoder(t *testing.T) {
	cases := [][]string{
		{"plain"},
		{"a b"},
		{"a&b", "c<d"},
		{"\u2713", "\uD55C\uAE00"},
		{"a%b"},
		{"..dots"},
		{"a+b"},
	}

	for _, want := range cases {
		t.Run(strings.Join(want, "/"), func(t *testing.T) {
			got, err := SplitPath(EncodeHref(want, false))
			if err != nil {
				t.Fatalf("%q did not survive the round trip: %v", want, err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %q, want %q", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("segment %d: got %q, want %q", i, got[i], want[i])
				}
			}
		})
	}
}

// A collection href ends in a slash, and the root is a slash on its own.
func TestACollectionHrefEndsInASlash(t *testing.T) {
	if got := EncodeHref([]string{"a", "b"}, true); got != "/a/b/" {
		t.Errorf("got %q, want /a/b/", got)
	}
	if got := EncodeHref([]string{"a", "b"}, false); got != "/a/b" {
		t.Errorf("got %q, want /a/b", got)
	}
	if got := EncodeHref(nil, true); got != "/" {
		t.Errorf("the root collection got %q, want /", got)
	}
}

// The same visible name in two forms, exactly as the vfs package's own
// fixture: precomposed and decomposed. Written with the literal bytes so an
// editor that normalizes on save cannot collapse the difference these tests
// depend on.
const (
	nfcSpelling = "caf\u00e9"
	nfdSpelling = "cafe\u0301"
)

// TestSplitPathOfPercentEncodedNFDYieldsNFCSegments covers the same
// trust-boundary claim as the vfs package's parsers, at this package's own
// entry point: a macOS client's percent-encoded, decomposed request path
// decodes to the precomposed spelling the vfs layer would have minted the
// file under.
func TestSplitPathOfPercentEncodedNFDYieldsNFCSegments(t *testing.T) {
	raw := "/" + url.PathEscape(nfdSpelling)
	got, err := SplitPath(raw)
	if err != nil {
		t.Fatalf("SplitPath(%q): %v", raw, err)
	}
	if len(got) != 1 || got[0] != nfcSpelling {
		t.Fatalf("SplitPath(%q) = %q, want [%q]", raw, got, nfcSpelling)
	}
}

// TestEncodeHrefOfNormalizedSegmentsRoundTrips proves the reason SplitPath
// normalizes at all: EncodeHref renders the same segments SplitPath just
// produced, so a client's NFD request and the href this server answers with
// have to agree, not merely each be individually valid.
func TestEncodeHrefOfNormalizedSegmentsRoundTrips(t *testing.T) {
	raw := "/" + url.PathEscape(nfdSpelling)
	segs, err := SplitPath(raw)
	if err != nil {
		t.Fatalf("SplitPath(%q): %v", raw, err)
	}
	got, err := SplitPath(EncodeHref(segs, false))
	if err != nil {
		t.Fatalf("round trip of %q: %v", segs, err)
	}
	if len(got) != 1 || got[0] != nfcSpelling {
		t.Fatalf("round trip = %q, want [%q]", got, nfcSpelling)
	}
}

// The decoder must not panic, allocate without bound, or return a segment
// carrying a separator, a NUL or a dot spelling.
func FuzzSplitPath(f *testing.F) {
	for _, seed := range []string{
		"/", "/a", "/a/b", "/a%2fb", "/%", "/%zz", "/a%00", "/..", "/%2e%2e",
		"//////", "/a%20b", strings.Repeat("/a", 200), "%",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		segs, err := SplitPath(raw)
		if err != nil {
			if segs != nil {
				t.Errorf("%q was refused but returned %q", raw, segs)
			}
			return
		}
		for _, s := range segs {
			switch {
			case s == "":
				t.Errorf("%q produced an empty segment", raw)
			case strings.Contains(s, "/"):
				t.Errorf("%q produced %q, which carries a separator", raw, s)
			case strings.IndexByte(s, 0) >= 0:
				t.Errorf("%q produced a segment with a NUL", raw)
			case s == "." || s == "..":
				t.Errorf("%q produced a %q segment", raw, s)
			}
		}
		// Whatever was accepted has to survive its own encoder.
		back, err := SplitPath(EncodeHref(segs, false))
		if err != nil {
			t.Errorf("%q accepted as %q, which its own encoder cannot round trip: %v", raw, segs, err)
		}
		if len(back) != len(segs) {
			t.Errorf("%q: round trip gave %q, want %q", raw, back, segs)
		}
	})
}
