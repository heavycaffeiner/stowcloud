package search

import (
	"slices"
	"testing"
)

// The packing is big-endian into the low 24 bits, which is what makes an
// integer comparison the same order as a comparison on the three raw bytes.
// The dictionary is binary-searched on that assumption.
func TestTrigramPackingRoundTrips(t *testing.T) {
	for _, b := range [][3]byte{
		{'a', 'b', 'c'}, {0, 0, 0}, {0xff, 0xff, 0xff}, {0x00, 0xff, 0x00}, {'/', '.', 'z'},
	} {
		got := TrigramOf(b[0], b[1], b[2]).Bytes()
		if got != b {
			t.Errorf("packing %v round-tripped to %v", b, got)
		}
	}
}

// Numeric order has to equal byte order, or the sorted dictionary a segment
// writes would not be the one a byte-wise reader binary-searches.
func TestTrigramOrderMatchesByteOrder(t *testing.T) {
	pairs := [][2][3]byte{
		{{'a', 'a', 'a'}, {'a', 'a', 'b'}},
		{{'a', 'a', 0xff}, {'a', 'b', 0x00}},
		{{0x00, 0x00, 0x00}, {0x00, 0x00, 0x01}},
		{{0x7f, 0xff, 0xff}, {0x80, 0x00, 0x00}},
	}
	for _, p := range pairs {
		lo := TrigramOf(p[0][0], p[0][1], p[0][2])
		hi := TrigramOf(p[1][0], p[1][1], p[1][2])
		if lo >= hi {
			t.Errorf("%v should pack below %v, got %d and %d", p[0], p[1], lo, hi)
		}
	}
}

func TestTrigramExtractionGoldens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"abcd", []string{"abc", "bcd"}},
		{"abc", []string{"abc"}},
		{"ab", nil},
		{"", nil},
		{"aaaa", []string{"aaa", "aaa"}},
	}
	for _, c := range cases {
		var got []string
		Trigrams([]byte(c.in), func(tr Trigram) {
			b := tr.Bytes()
			got = append(got, string(b[:]))
		})
		if !slices.Equal(got, c.want) {
			t.Errorf("Trigrams(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Hangul spelled as escapes, so this file holds no non-Latin source text.
const (
	oneSyllable  = "\ubb38"
	twoSyllables = "\ubb38\uc11c"
)

// A UTF-8 Hangul syllable is exactly three bytes, so one syllable is one
// trigram. This is the property that makes a byte trigram index work for CJK
// at all, and it is why the window is bytes rather than characters.
func TestOneHangulSyllableIsOneTrigram(t *testing.T) {
	var got []Trigram
	Trigrams([]byte(oneSyllable), func(tr Trigram) { got = append(got, tr) })
	if len(got) != 1 {
		t.Fatalf("one syllable produced %d trigrams, want 1", len(got))
	}
	// Two syllables are six bytes, so four overlapping windows.
	got = got[:0]
	Trigrams([]byte(twoSyllables), func(tr Trigram) { got = append(got, tr) })
	if len(got) != 4 {
		t.Errorf("two syllables produced %d trigrams, want 4", len(got))
	}
}

// DistinctTrigrams is the normal form the index and the query both compare in:
// sorted and deduplicated.
func TestDistinctTrigramsIsSortedAndDeduplicated(t *testing.T) {
	got := DistinctTrigrams([]byte("abcabc"))
	if !slices.IsSorted(got) {
		t.Errorf("not sorted: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Errorf("duplicate at %d: %v", i, got)
		}
	}
	// "abcabc" holds abc, bca, cab, abc: four windows, three distinct.
	if len(got) != 3 {
		t.Errorf("got %d distinct trigrams, want 3: %v", len(got), got)
	}
	if DistinctTrigrams([]byte("ab")) != nil {
		t.Error("a string under three bytes has no trigram")
	}
}

// Sort and dedup have one home here, and the index package uses these rather
// than keeping a copy. This pins the normal form they produce.
func TestSortAndDedupProduceTheNormalForm(t *testing.T) {
	in := []Trigram{9, 3, 9, 1, 3, 3}
	SortTrigrams(in)
	got := DedupTrigrams(in)
	want := []Trigram{1, 3, 9}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if DedupTrigrams(nil) != nil {
		t.Error("dedup of nothing is nothing")
	}
}

// AppendTrigrams does not clear its target: the base builder accumulates a
// whole block's windows into one buffer before sorting once.
func TestAppendTrigramsAccumulates(t *testing.T) {
	out := AppendTrigrams(nil, []byte("abcd"))
	out = AppendTrigrams(out, []byte("wxyz"))
	if len(out) != 4 {
		t.Errorf("expected 2 + 2 windows, got %d", len(out))
	}
}

func TestTrigramOccurrences(t *testing.T) {
	for _, c := range []struct{ n, want int }{{0, 0}, {2, 0}, {3, 1}, {10, 8}} {
		if got := TrigramOccurrences(c.n); got != c.want {
			t.Errorf("TrigramOccurrences(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

func FuzzDistinctTrigramsNeverPanics(f *testing.F) {
	f.Add([]byte("abcd"))
	f.Add([]byte(twoSyllables + ".txt"))
	f.Add([]byte{0xff, 0x00, 0xfe, 0x01})
	f.Fuzz(func(t *testing.T, in []byte) {
		got := DistinctTrigrams(in)
		if !slices.IsSorted(got) {
			t.Errorf("DistinctTrigrams(%q) is not sorted", in)
		}
		for i := 1; i < len(got); i++ {
			if got[i] == got[i-1] {
				t.Errorf("DistinctTrigrams(%q) has a duplicate", in)
			}
		}
	})
}
