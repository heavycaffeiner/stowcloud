package search

import "slices"

// Trigrams over bytes, meaning three bytes rather than three characters.
//
// plocate makes the same choice, and it is what lets the index serve CJK. A
// UTF-8 Hangul syllable occupies exactly three bytes, so a single syllable forms
// one trigram and a two-syllable query produces four overlapping ones.

// Trigram is one three-byte window, packed big-endian into the low 24 bits.
//
// The packing is what makes numeric order and byte order the same order, so
// the sorted dictionary a segment writes is byte-for-byte what a comparison
// on the raw bytes would have produced. A caller reading a dictionary entry
// compares integers; the format still holds bytes.
type Trigram uint32

// TrigramOf packs three bytes.
func TrigramOf(a, b, c byte) Trigram {
	return Trigram(a)<<16 | Trigram(b)<<8 | Trigram(c)
}

// Bytes unpacks the window, which is what a dictionary entry stores.
//
// Each field is masked to its own byte before it narrows, so every conversion
// is proven in range where it happens.
func (t Trigram) Bytes() [3]byte {
	return [3]byte{
		byte((t >> 16) & 0xff),
		byte((t >> 8) & 0xff),
		byte(t & 0xff),
	}
}

// Trigrams invokes fn on each overlapping window, avoiding a materialised slice
// for a name the caller merely scans.
func Trigrams(b []byte, fn func(Trigram)) {
	for i := 0; i+3 <= len(b); i++ {
		fn(TrigramOf(b[i], b[i+1], b[i+2]))
	}
}

// AppendTrigrams adds every window to out without clearing it first. Sorting and
// deduplication happen once, in the caller, at the end.
func AppendTrigrams(out []Trigram, b []byte) []Trigram {
	Trigrams(b, func(t Trigram) { out = append(out, t) })
	return out
}

// DistinctTrigrams returns b's trigram set, sorted and deduplicated.
func DistinctTrigrams(b []byte) []Trigram {
	if len(b) < 3 {
		return nil
	}
	out := AppendTrigrams(make([]Trigram, 0, len(b)-2), b)
	SortTrigrams(out)
	return DedupTrigrams(out)
}

// SortTrigrams sorts a window set into the format's order. It lives here
// rather than in the index package so there is one implementation of the
// order the dictionary is binary-searched with.
func SortTrigrams(v []Trigram) { slices.Sort(v) }

// DedupTrigrams removes adjacent duplicates from a sorted set.
func DedupTrigrams(v []Trigram) []Trigram { return slices.Compact(v) }

// TrigramOccurrences gives how many windows a byte string of the given length
// contributes, a figure the estimator's posting-list model requires.
func TrigramOccurrences(n int) int {
	if n < 3 {
		return 0
	}
	return n - 2
}
