// Package uniname puts a name that arrived from outside this program into the
// one spelling the rest of it stores: well-formed UTF-8 in Normalization Form
// C.
//
// Two kinds of misspelling arrive. A macOS client writes "é" decomposed, as
// U+0065 U+0301, while every other client writes the composed U+00E9, so the
// same name reaches the server as two different byte strings and a directory
// ends up appearing to hold two files. And an archive or an older client hands
// over a name in a legacy code page (CP949, Shift_JIS, GBK, Big5, CP437)
// without declaring which one, so the bytes are not UTF-8 at all and every
// encoder downstream turns them into U+FFFD.
//
// The detection is ICU's and the code pages and normalization tables are
// x/text's. Nothing here re-implements either.
package uniname

import (
	"strings"
	"unicode/utf8"

	"github.com/gogs/chardet"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// detectable reports whether a detection carrying this label is one to take.
//
// A switch behind a function rather than a package map, following the same
// reasoning the path layer's reserved-prefix table states: a variable is
// state a caller could reassign, and this decides what a name decodes as.
//
// The set is closed because the detector is only needed for one question:
// which of the legacy multi-byte East Asian code pages a name is written in,
// since no format that carries such a name declares it. Everything else it
// can answer is a single-byte code page, and there the caller already knows
// better than a guess does: a zip entry without the UTF-8 flag is CP437 by
// specification, and a name off the wire falls back to Latin-1, which is
// lossless.
//
// Admitting the single-byte answers was measurably wrong. On the bytes
// "00A0\x81/009A0z7000\xcc" the detector reports windows-1252 and
// windows-1250 at an identical confidence of 98, and which one it calls best
// varied between calls, so the same uploaded name would have landed on disk
// under two different spellings depending on which run won. UTF-16, UTF-32
// and the ISO-2022 escapes are excluded for a second reason: they are not
// ASCII-transparent, so decoding a path with one could invent a separator or
// a NUL.
func detectable(label string) bool {
	switch label {
	case "EUC-KR", "EUC-JP", "Shift_JIS", "GB18030", "Big5":
		return true
	default:
		return false
	}
}

// minConfidence is the score below which a detection is discarded for the
// caller's fallback. ICU reports 0..100, and within the set above the
// distribution is close to bimodal: every correct detection measured here
// scored exactly 100 once the sample reached roughly thirty bytes, while
// every wrong one scored 10.
const minConfidence = 90

// Latin1 is the fallback for bytes that are not UTF-8 and that the detector
// could not place. It is the one code page whose decode loses nothing: all
// 256 byte values map to a distinct rune, so the original bytes are still in
// the name and a later pass can recover them. Mapping the undecodable bytes
// to U+FFFD instead would be equally unreadable and permanently lossy.
func Latin1() encoding.Encoding { return charmap.ISO8859_1 }

// Normalize returns s as well-formed UTF-8 in NFC.
//
// Valid UTF-8 is only composed. Anything else is decoded first, from whichever
// character encoding the detector recognises in it and from Latin-1 when it
// recognises none.
//
// A whole path is not passed here. Callers holding one use Components, which
// splits before it decodes so a decoder cannot mint a separator that no
// validation step saw.
func Normalize(s string) string {
	if utf8.ValidString(s) {
		return norm.NFC.String(s)
	}
	b := []byte(s)
	return Decode(b, Charset(b, Latin1()))
}

// IsNormalized reports whether Normalize would return its input unchanged,
// which lets a hot path skip the call without allocating.
func IsNormalized(s string) bool {
	return utf8.ValidString(s) && norm.NFC.IsNormalString(s)
}

// Components normalizes the components of one path, which were written by one
// program and therefore share one character encoding.
//
// The encoding is detected once, over every component at once, because the
// detector's accuracy scales with the sample and a single component is close
// to the shortest input it can say anything about: the same Korean name scored
// 33 for the wrong answer alone and 100 for the right one as part of its path.
//
// The components arrive already split, which is what makes the decode safe:
// none of the code pages considered here can carry 0x2F as a trailing byte, so
// splitting on the raw bytes first cannot merge two components, and decoding
// afterwards cannot split one.
//
// The argument slice is returned untouched when every component is already
// normal, which is every all-ASCII path.
func Components(comps []string) []string {
	dirty := false
	for _, c := range comps {
		if !IsNormalized(c) {
			dirty = true
			break
		}
	}
	if !dirty {
		return comps
	}
	var enc encoding.Encoding
	if joined := strings.Join(comps, "/"); !utf8.ValidString(joined) {
		enc = Charset([]byte(joined), Latin1())
	}
	out := make([]string, len(comps))
	for i, c := range comps {
		if utf8.ValidString(c) {
			out[i] = norm.NFC.String(c)
			continue
		}
		out[i] = Decode([]byte(c), enc)
	}
	return out
}

// Charset identifies the character encoding of sample, which is expected not
// to be UTF-8. It returns fallback, which may be nil, when the detector has
// no usable opinion within the set it is trusted for.
//
// DetectAll rather than DetectBest, and the winner is chosen here rather than
// taken: the detector's own "best" is whichever of two equally scored
// candidates its sort happened to leave first, which is not stable between
// calls. Choosing the highest score and breaking a tie on the label makes one
// set of bytes always decode to one name.
func Charset(sample []byte, fallback encoding.Encoding) encoding.Encoding {
	// A detector per call rather than one shared: ICU's recognizers are cheap
	// to build, and this runs once per path or once per archive, which is not
	// often enough to trade for reasoning about whether the port is safe to
	// share between goroutines.
	all, err := chardet.NewTextDetector().DetectAll(sample)
	if err != nil {
		return fallback
	}
	best := ""
	score := 0
	for _, r := range all {
		switch {
		case !detectable(r.Charset) || r.Confidence < minConfidence:
		case r.Confidence > score, r.Confidence == score && r.Charset < best:
			best, score = r.Charset, r.Confidence
		}
	}
	if best == "" {
		return fallback
	}
	if enc := encodingFor(best); enc != nil {
		return enc
	}
	return fallback
}

// Decode reads b as enc and returns it as well-formed UTF-8 in NFC. A nil enc
// means the bytes were meant to be UTF-8 already, so only the repair and the
// composition apply.
func Decode(b []byte, enc encoding.Encoding) string {
	t := repair()
	if enc != nil {
		t = transform.Chain(enc.NewDecoder(), t)
	}
	out, _, err := transform.Bytes(t, b)
	if err == nil {
		return string(out)
	}
	// A transform that gave up mid-string leaves a partial result, which would
	// silently truncate the name. The undecoded bytes, mapped to error runes,
	// at least keep every character position.
	if out, _, err = transform.Bytes(repair(), b); err == nil {
		return string(out)
	}
	return string(b)
}

// repair maps ill-formed byte sequences onto U+FFFD and then composes.
//
// Built per call: a transform.Transformer carries state between Transform
// calls, so one shared instance would be a data race between two requests.
func repair() transform.Transformer {
	return transform.Chain(runes.ReplaceIllFormed(), norm.NFC)
}

// encodingFor resolves a character-encoding label from the detector into a
// decoder.
//
// The registry order matters. The IANA index answers with the registered
// encoding, so "ISO-8859-1" decodes as Latin-1 and stays lossless; the WHATWG
// index answers the same label with windows-1252, which leaves five bytes
// undecodable. The WHATWG index is still consulted second, for the labels IANA
// does not register.
func encodingFor(label string) encoding.Encoding {
	if enc, err := ianaindex.IANA.Encoding(label); err == nil && enc != nil {
		return enc
	}
	if enc, err := htmlindex.Get(label); err == nil && enc != nil {
		return enc
	}
	return nil
}
