package search

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

// Case folding together with NFC normalisation, performed at index time so no
// case or normalisation variant is ever stored twice. The identical fold applies
// to the query, so both sides always meet in one space.
//
// Filenames are byte strings rather than strings. A name that is not valid UTF-8
// must remain findable, so it is folded ASCII-only and otherwise preserved
// verbatim instead of being rewritten with error runes.

// lowerFull stays language-neutral. Filenames carry no locale, and a Turkish
// locale would fold ASCII "I" into a dotless form, rendering every Latin name
// containing it unfindable.
//
//nolint:gochecknoglobals // cases.Caser is immutable and documented as concurrency-safe.
var lowerFull = cases.Lower(language.Und)

// Fold normalises a byte string for indexing and matching.
func Fold(b []byte) []byte {
	if !utf8.Valid(b) || isASCII(b) {
		// Not UTF-8, so no Unicode operation is meaningful, or plain ASCII,
		// which is the common case and the one worth keeping off the tables.
		// Either way ASCII case still folds and every other byte survives.
		return asciiLower(b)
	}
	// The full Unicode lowercase mapping, not the simple one: they differ on
	// characters whose lowercase form is longer than one rune, and a name
	// spelled either way has to fold to the same bytes or it is findable by
	// only one of its spellings.
	return []byte(lowerFull.String(norm.NFC.String(string(b))))
}

// FoldString applies Fold to a string.
func FoldString(s string) []byte { return Fold([]byte(s)) }

// IsFoldedASCII reports whether folding would leave the input unchanged, letting
// a hot loop avoid the allocation.
func IsFoldedASCII(b []byte) bool {
	for _, c := range b {
		if c >= utf8.RuneSelf || (c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = lowerByte(c)
	}
	return out
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// ContainsASCIIFold performs a case-insensitive ASCII substring search without
// allocating. needle must arrive already folded and ASCII, covering the
// overwhelmingly common case of a Latin query against a Latin filename.
func ContainsASCIIFold(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	first := needle[0]
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if lowerByte(haystack[i]) != first {
			continue
		}
		match := true
		for j := range needle {
			if lowerByte(haystack[i+j]) != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// Contains performs a byte-exact substring search, expecting both sides to be
// pre-folded.
func Contains(haystack, needle []byte) bool { return bytes.Contains(haystack, needle) }

// HasPrefix tests a prefix over pre-folded bytes.
func HasPrefix(haystack, needle []byte) bool { return bytes.HasPrefix(haystack, needle) }
