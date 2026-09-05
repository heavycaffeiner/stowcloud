package uniname_test

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/uniname"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// encodeTo spells a name in a legacy code page, which is what a macOS-free
// legacy client or a Windows zip tool would have put on the wire.
func encodeTo(t *testing.T, enc encoding.Encoding, s string) string {
	t.Helper()
	out, _, err := transform.String(enc.NewEncoder(), s)
	if err != nil {
		t.Fatalf("encoding %q: %v", s, err)
	}
	return out
}

func TestADecomposedNameComposes(t *testing.T) {
	// The NFD spelling macOS writes, given as escapes so the source file's own
	// normalization cannot silently change what is under test.
	nfd := "cafe\u0301.txt"
	const nfc = "caf\u00e9.txt"
	if got := uniname.Normalize(nfd); got != nfc {
		t.Errorf("Normalize(%q) = %q, want %q", nfd, got, nfc)
	}
	if uniname.IsNormalized(nfd) {
		t.Error("the decomposed spelling reports as already normalized")
	}
	if !uniname.IsNormalized(nfc) {
		t.Error("the composed spelling reports as needing normalization")
	}
}

func TestDecomposedHangulComposes(t *testing.T) {
	// Conjoining jamo, which is what a macOS client writes for a Korean name.
	jamo := "\u1100\u1161\u11b7.txt"
	const syllable = "\uac10.txt"
	if got := uniname.Normalize(jamo); got != syllable {
		t.Errorf("Normalize(jamo) = %q, want %q", got, syllable)
	}
}

func TestAnAlreadyNormalPathIsNotCopied(t *testing.T) {
	in := []string{"documents", "report.txt"}
	out := uniname.Components(in)
	if &out[0] != &in[0] {
		t.Error("Components allocated a new slice for an all-ASCII path")
	}
}

func TestALegacyPathDecodesFromItsWholeSample(t *testing.T) {
	cases := []struct {
		name string
		enc  encoding.Encoding
		path string
	}{
		// Each path is long enough for the detector to reach its confident
		// band. That is the point of detecting over the whole path rather
		// than one component: the same names alone score far below the bar.
		{"CP949", korean.EUCKR, "\ud68c\uc0ac\uc790\ub8cc/2026\ub144/\ud55c\uad6d\uc5b4 \ubaa9\ub85d \ucd5c\uc885\ud310.txt"},
		{"ShiftJIS", japanese.ShiftJIS, "\u66f8\u985e/\u5831\u544a\u66f8/\u65e5\u672c\u8a9e\u306e\u30d5\u30a1\u30a4\u30eb.txt"},
		{"GBK", simplifiedchinese.GBK, "\u6587\u6863/\u62a5\u544a/\u4e2d\u6587\u6587\u4ef6\u540d\u79f0\u6d4b\u8bd5.txt"},
		{"Big5", traditionalchinese.Big5, "\u6587\u4ef6/\u5831\u544a/\u4e2d\u6587\u6a94\u6848\u540d\u7a31\u6e2c\u8a66.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := encodeTo(t, c.enc, c.path)
			got := strings.Join(uniname.Components(strings.Split(raw, "/")), "/")
			if got != c.path {
				t.Errorf("decoded %q, want %q", got, c.path)
			}
		})
	}
}

func TestAnUndetectableNameStaysRecoverable(t *testing.T) {
	// One short Korean name is below the detector's bar, so it falls to the
	// Latin-1 fallback. The point of that fallback is that it loses nothing:
	// re-encoding recovers the exact bytes the client sent, which U+FFFD
	// would have destroyed.
	raw := encodeTo(t, korean.EUCKR, "\ubaa9.txt")
	got := uniname.Normalize(raw)
	if !utf8.ValidString(got) {
		t.Fatalf("the fallback produced ill-formed UTF-8: %q", got)
	}
	back, _, err := transform.String(charmap.ISO8859_1.NewEncoder(), got)
	if err != nil {
		t.Fatalf("re-encoding %q: %v", got, err)
	}
	if back != raw {
		t.Errorf("re-encoded to %x, want the original %x", back, raw)
	}
}

func TestIllFormedBytesBecomeWellFormed(t *testing.T) {
	// A byte sequence no code page maps cleanly still has to come back as
	// something a JSON or XML encoder can carry, or a client can never name
	// the file again.
	got := uniname.Normalize("bad\xff\xfename.txt")
	if !utf8.ValidString(got) {
		t.Errorf("Normalize left ill-formed UTF-8: %q", got)
	}
	if !uniname.IsNormalized(got) {
		t.Errorf("Normalize is not idempotent on %q", got)
	}
}

func TestTheCallerFallbackCarriesTheSingleByteCase(t *testing.T) {
	// A DOS-era zip name: one short Western name in CP437. The detector is
	// not trusted for single-byte code pages at all, so this has to come
	// back through the caller's fallback, which is what the zip format
	// specifies for an entry without the UTF-8 flag.
	raw := encodeTo(t, charmap.CodePage437, "M\u00fcnchen.txt")
	cs := uniname.Charset([]byte(raw), charmap.CodePage437)
	if got := uniname.Decode([]byte(raw), cs); got != "M\u00fcnchen.txt" {
		t.Errorf("decoded %q, want %q", got, "M\u00fcnchen.txt")
	}
}

// One set of bytes must always produce one name. The detector scores
// windows-1252 and windows-1250 identically at 98 on the bytes below and its
// own DetectBest returns whichever its sort left first, which differed
// between calls; a name that decoded two ways would land on disk under two
// spellings depending on which run won.
func TestOneSetOfBytesAlwaysDecodesToOneName(t *testing.T) {
	raw := "00A0\x81/009A0z7000\xcc"
	first := strings.Join(uniname.Components(strings.Split(raw, "/")), "/")
	for i := range 400 {
		got := strings.Join(uniname.Components(strings.Split(raw, "/")), "/")
		if got != first {
			t.Fatalf("call %d decoded %q, the first call decoded %q", i+1, got, first)
		}
	}
}

// A single-byte code page is never the detector's answer to take, because
// the caller's fallback covers that case and is chosen rather than guessed.
// Latin-1 here means the bytes survive re-encoding, which a wrong
// windows-125x guess would not guarantee.
func TestASingleByteGuessNeverBeatsTheFallback(t *testing.T) {
	raw := []byte("00A0\x81009A0z7000\xcc")
	if got := uniname.Charset(raw, nil); got != nil {
		t.Errorf("Charset returned %T for bytes only a single-byte page could explain", got)
	}
	back, _, err := transform.Bytes(charmap.ISO8859_1.NewEncoder(), []byte(uniname.Normalize(string(raw))))
	if err != nil {
		t.Fatalf("re-encoding: %v", err)
	}
	if !bytes.Equal(back, raw) {
		t.Errorf("the fallback lost bytes: %x became %x", raw, back)
	}
}

func TestJointDetectionBeatsPerNameDetection(t *testing.T) {
	// The zip reader's case: many short names in one archive, all written by
	// one tool in one code page. Detected together they are unambiguous.
	names := []string{"\ubb38\uc11c.txt", "\uc0ac\uc9c4.jpg", "\ubcf4\uace0\uc11c \ucd5c\uc885.docx", "\uacc4\uc57d\uc11c.pdf"}
	var sample []byte
	raws := make([][]byte, 0, len(names))
	for _, n := range names {
		b := []byte(encodeTo(t, korean.EUCKR, n))
		raws = append(raws, b)
		sample = append(sample, b...)
	}
	cs := uniname.Charset(sample, charmap.CodePage437)
	for i, b := range raws {
		if got := uniname.Decode(b, cs); got != names[i] {
			t.Errorf("entry %d decoded %q, want %q", i, got, names[i])
		}
	}
}

func TestNormalizeIsIdentityForASCII(t *testing.T) {
	for _, s := range []string{"", "plain.txt", "a/b/c.txt", "CON", "space in name.txt"} {
		if got := uniname.Normalize(s); got != s {
			t.Errorf("Normalize(%q) = %q, want it unchanged", s, got)
		}
	}
}
