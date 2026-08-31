package search

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// Two Hangul syllables and the same name with an extension, spelled as escapes
// so this file holds no non-Latin source text. A UTF-8 Hangul syllable is three
// bytes, which is the case the byte-trigram index exists to handle.
const (
	hangul    = "\ubb38\uc11c"
	hangulDoc = hangul + ".txt"
)

// Fold's contract in one table: ASCII case folds, a name that is not UTF-8
// survives byte for byte, and the Unicode mapping is the full one rather than
// the simple one.
func TestFoldGoldenVectors(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"ascii lowercases", []byte("README.md"), []byte("readme.md")},
		{"already folded is unchanged", []byte("readme.md"), []byte("readme.md")},
		{"digits and punctuation survive", []byte("A-1_2.TXT"), []byte("a-1_2.txt")},
		{"empty stays empty", []byte(""), []byte("")},
		// Not valid UTF-8: the ASCII case still folds and the invalid byte is
		// kept rather than replaced with an error rune, or the name would be
		// findable by no spelling at all.
		{"invalid utf8 keeps its bytes", []byte{'A', 0xff, 'B'}, []byte{'a', 0xff, 'b'}},
		{"hangul is unchanged by case folding", []byte(hangulDoc), []byte(hangulDoc)},
		{"latin capitals with accents fold", []byte("ÉCOLE"), []byte("école")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Fold(c.in); !bytes.Equal(got, c.want) {
				t.Errorf("Fold(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The Turkish dotted capital I is the case the simple lowercase mapping gets
// wrong: its lowercase form is longer than one rune, and a name spelled either
// way has to fold to the same bytes or it is findable by only one spelling.
func TestFoldUsesTheFullUnicodeMapping(t *testing.T) {
	folded := Fold([]byte("\u0130")) // LATIN CAPITAL LETTER I WITH DOT ABOVE
	if len(folded) <= 2 {
		t.Errorf("the full mapping should expand this rune, got %q (%d bytes)", folded, len(folded))
	}
	if !bytes.HasPrefix(folded, []byte("i")) {
		t.Errorf("expected the folded form to start with an ASCII i, got %q", folded)
	}
}

// Folding twice must equal folding once, or an index built from folded names
// and a query folded again would meet in different spaces.
func TestFoldIsIdempotent(t *testing.T) {
	for _, s := range []string{
		"README.md", "ÉCOLE", hangulDoc, "Ünïcödé NAME", "\u0130stanbul", "", "a/B/c.TXT",
	} {
		once := Fold([]byte(s))
		twice := Fold(once)
		if !bytes.Equal(once, twice) {
			t.Errorf("Fold(%q) is not idempotent: %q then %q", s, once, twice)
		}
	}
}

func FuzzFoldNeverPanics(f *testing.F) {
	for _, s := range []string{"", "A", "README.md", hangul, "\xff\xfe", "\u0130"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		out := Fold(in)
		// Idempotence has to hold on arbitrary bytes too, since a filename is
		// an arbitrary byte string.
		if !bytes.Equal(out, Fold(out)) {
			t.Errorf("Fold is not idempotent on %q", in)
		}
		if utf8.Valid(in) && !utf8.Valid(out) {
			t.Errorf("Fold turned valid UTF-8 %q into invalid %q", in, out)
		}
	})
}

// IsFoldedASCII is the hot loop's permission to skip the allocation, so it has
// to agree with Fold exactly on the inputs it claims.
func TestIsFoldedASCIIAgreesWithFold(t *testing.T) {
	for _, s := range []string{"readme.md", "README.md", hangulDoc, "", "a1-_.", "A"} {
		b := []byte(s)
		claimed := IsFoldedASCII(b)
		unchanged := bytes.Equal(Fold(b), b)
		if claimed && !unchanged {
			t.Errorf("IsFoldedASCII(%q) said folding changes nothing, but it does", s)
		}
	}
}

// The allocation-free matcher has to answer exactly what fold-then-contains
// answers, or the common Latin query would take a different path to a
// different result.
func TestContainsASCIIFoldAgreesWithFoldThenContains(t *testing.T) {
	haystacks := []string{"README.md", "readme.md", "Annual Report.PDF", "", "aAaA", "photo.JPG"}
	needles := []string{"readme", "report", "", "aaa", "jpg", "zzz", "a"}
	for _, h := range haystacks {
		for _, n := range needles {
			folded := Fold([]byte(n))
			if !IsFoldedASCII(folded) {
				continue
			}
			got := ContainsASCIIFold([]byte(h), folded)
			want := Contains(Fold([]byte(h)), folded)
			if got != want {
				t.Errorf("ContainsASCIIFold(%q, %q) = %v, fold-then-contains = %v", h, n, got, want)
			}
		}
	}
}

// An empty needle matches everything, which is how a scoped listing with no
// query term is expressed.
func TestContainsASCIIFoldEmptyNeedleMatches(t *testing.T) {
	if !ContainsASCIIFold([]byte("anything"), nil) {
		t.Error("an empty needle should match")
	}
	if ContainsASCIIFold([]byte("ab"), []byte("abc")) {
		t.Error("a needle longer than the haystack cannot match")
	}
}

func FuzzContainsASCIIFoldMatchesFoldThenContains(f *testing.F) {
	f.Add("README.md", "readme")
	f.Add("photo.JPG", "jpg")
	f.Fuzz(func(t *testing.T, hay, needle string) {
		folded := Fold([]byte(needle))
		if !IsFoldedASCII(folded) {
			t.Skip("the fast path only claims folded ASCII needles")
		}
		got := ContainsASCIIFold([]byte(hay), folded)
		want := Contains(Fold([]byte(hay)), folded)
		if got != want {
			t.Errorf("ContainsASCIIFold(%q, %q) = %v, want %v", hay, needle, got, want)
		}
	})
}

func TestHasPrefixOnFoldedBytes(t *testing.T) {
	if !HasPrefix(Fold([]byte("README.md")), Fold([]byte("read"))) {
		t.Error("expected a prefix match on folded bytes")
	}
	if HasPrefix(Fold([]byte("README.md")), Fold([]byte("me"))) {
		t.Error("a substring that is not a prefix must not match")
	}
}

// A long name is the realistic worst case for the matcher; this pins that it
// still agrees with the slow path rather than falling off an index bound.
func TestContainsASCIIFoldOnALongName(t *testing.T) {
	hay := strings.Repeat("ab", 500) + "NEEDLE.txt"
	if !ContainsASCIIFold([]byte(hay), []byte("needle")) {
		t.Error("expected the needle near the end of a long name to be found")
	}
}
