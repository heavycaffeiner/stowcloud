package search

import "sort"

// Byte trigrams: three bytes, not three characters.
//
// This is the choice plocate makes and it is what makes the index work for
// CJK. A UTF-8 Hangul syllable is exactly three bytes, so one syllable is one
// trigram and a two-syllable query yields four overlapping trigrams.

// Trigram is one three-byte window.
type Trigram [3]byte

// Trigrams calls fn for every overlapping window, which avoids materialising a
// slice for a name the caller is only scanning.
func Trigrams(b []byte, fn func(Trigram)) {
	for i := 0; i+3 <= len(b); i++ {
		fn(Trigram{b[i], b[i+1], b[i+2]})
	}
}

// DistinctTrigrams is the sorted, deduplicated trigram set of b.
func DistinctTrigrams(b []byte) []Trigram {
	if len(b) < 3 {
		return nil
	}
	out := make([]Trigram, 0, len(b)-2)
	Trigrams(b, func(t Trigram) { out = append(out, t) })
	sortTrigrams(out)
	return dedupTrigrams(out)
}

// AppendTrigrams appends every window to out without clearing it. The caller
// sorts and dedups once at the end.
func AppendTrigrams(out []Trigram, b []byte) []Trigram {
	Trigrams(b, func(t Trigram) { out = append(out, t) })
	return out
}

// TrigramOccurrences is how many windows a byte string of this length
// contributes, which the estimator's posting-list model needs.
func TrigramOccurrences(n int) int {
	if n < 3 {
		return 0
	}
	return n - 2
}

func sortTrigrams(v []Trigram) {
	sort.Slice(v, func(i, j int) bool { return lessTrigram(v[i], v[j]) })
}

func lessTrigram(a, b Trigram) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func dedupTrigrams(v []Trigram) []Trigram {
	if len(v) < 2 {
		return v
	}
	n := 1
	for i := 1; i < len(v); i++ {
		if v[i] != v[n-1] {
			v[n] = v[i]
			n++
		}
	}
	return v[:n]
}
