package search

import "slices"

// Byte trigrams: three bytes, not three characters.
//
// This is the choice plocate makes and it is what makes the index work for
// CJK. A UTF-8 Hangul syllable is exactly three bytes, so one syllable is one
// trigram and a two-syllable query yields four overlapping trigrams.

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

// Trigrams calls fn for every overlapping window, which avoids materialising a
// slice for a name the caller is only scanning.
func Trigrams(b []byte, fn func(Trigram)) {
	for i := 0; i+3 <= len(b); i++ {
		fn(TrigramOf(b[i], b[i+1], b[i+2]))
	}
}

// AppendTrigrams appends every window to out without clearing it. The caller
// sorts and dedups once at the end.
func AppendTrigrams(out []Trigram, b []byte) []Trigram {
	Trigrams(b, func(t Trigram) { out = append(out, t) })
	return out
}

// DistinctTrigrams is the sorted, deduplicated trigram set of b.
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

// TrigramOccurrences is how many windows a byte string of this length
// contributes, which the estimator's posting-list model needs.
func TrigramOccurrences(n int) int {
	if n < 3 {
		return 0
	}
	return n - 2
}
