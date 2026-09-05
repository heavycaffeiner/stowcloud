package vfs

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/text/encoding/korean"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/uniname"
)

// TestParseFunctionsNormalizeAnNFDPathToNFC covers the trust boundary every
// protocol crosses through this package: a name a macOS client wrote
// decomposed reaches every one of the three parsers already recomposed, so
// the same visible name never appears as two different byte strings to
// whatever reads the result back.
func TestParseFunctionsNormalizeAnNFDPathToNFC(t *testing.T) {
	in := nfdSpelling + "/" + nfdSpelling
	want := nfcSpelling + "/" + nfcSpelling

	v, err := ParseVpath(in)
	if err != nil {
		t.Fatalf("ParseVpath(%q): %v", in, err)
	}
	if got := v.String(); got != want {
		t.Fatalf("ParseVpath(%q) = %q, want %q", in, got, want)
	}

	sp, err := ParseSharePath(in)
	if err != nil {
		t.Fatalf("ParseSharePath(%q): %v", in, err)
	}
	if got := sp.String(); got != want {
		t.Fatalf("ParseSharePath(%q) = %q, want %q", in, got, want)
	}

	safe, err := ParseSafePath(in)
	if err != nil {
		t.Fatalf("ParseSafePath(%q): %v", in, err)
	}
	if got := safe.String(); got != want {
		t.Fatalf("ParseSafePath(%q) = %q, want %q", in, got, want)
	}
}

// TestJoinNormalizesButJoinExistingPreserves covers the split Join's own
// doc comment draws: a name this package is about to mint gets the same
// normalization ParseSafePath applies, but a name read back out of a real
// directory listing is walked exactly as given, since those are the exact
// bytes the next syscall needs to find it again.
func TestJoinNormalizesButJoinExistingPreserves(t *testing.T) {
	root := RootPath()

	minted, err := root.Join(nfdSpelling)
	if err != nil {
		t.Fatalf("Join(%q): %v", nfdSpelling, err)
	}
	if got := minted.Name(); got != nfcSpelling {
		t.Fatalf("Join(%q).Name() = %q, want the NFC spelling %q", nfdSpelling, got, nfcSpelling)
	}

	walked, err := root.JoinExisting(nfdSpelling)
	if err != nil {
		t.Fatalf("JoinExisting(%q): %v", nfdSpelling, err)
	}
	if got := walked.Name(); got != nfdSpelling {
		t.Fatalf("JoinExisting(%q).Name() = %q, want the bytes unchanged", nfdSpelling, got)
	}
}

// TestADecodedNULIsRefusedNotTreatedAsABoundary constructs a component from
// real bytes that are not valid UTF-8, so it takes uniname's legacy-decode
// path rather than the plain valid-UTF-8 passthrough, and that decode
// happens to preserve an embedded NUL byte rather than losing or moving it.
// The refusal must name that one component, with the NUL still where it
// was: a decode that turned some byte into a separator or a NUL must never
// look like a bypass or a truncation nothing checked.
//
// 0xC0 is always an invalid UTF-8 lead byte (it can only start an overlong
// two-byte encoding, which the standard forbids), so "\xC0\x00" is not
// valid UTF-8 and is also too short a sample for the charset detector to
// place with any confidence, which is exactly the case Latin-1 exists to
// carry: it maps every byte to itself, so the 0x00 survives decode as a NUL
// character at the same position, not as some other rune and not merged
// into its neighbor.
func TestADecodedNULIsRefusedNotTreatedAsABoundary(t *testing.T) {
	dirty := "\xC0\x00"
	raw := "a/" + dirty
	if strings.Count(raw, "/") != 1 {
		t.Fatalf("test fixture must split into exactly two components, got %q", raw)
	}

	_, comps, err := splitValidated(raw)
	if err == nil {
		t.Fatalf("splitValidated(%q) = %v, %v, want a refusal", raw, comps, err)
	}
	if !errors.Is(err, ErrInvalidName) {
		t.Fatalf("splitValidated(%q) error = %v, want ErrInvalidName", raw, err)
	}
	var pathErr *PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("splitValidated(%q) error = %v, want a *PathError", raw, err)
	}
	const decodedSecondComponent = "\u00C0\x00"
	if pathErr.Component != decodedSecondComponent {
		t.Fatalf("refusal named %q, want the decoded second component %q (not a third component the embedded byte might look like)",
			pathErr.Component, decodedSecondComponent)
	}
	if !strings.Contains(pathErr.Detail, "NUL") {
		t.Fatalf("splitValidated(%q) refused for %q, want the NUL refusal", raw, pathErr.Detail)
	}
}

// TestLegacyCP949PathNormalizesToKoreanText covers an archive or an older
// client handing over a name in a legacy code page instead of UTF-8. The
// fixture is 40 bytes of CP949/EUC-KR, comfortably past the roughly 30 byte
// floor the detector needs to place an East Asian encoding with
// confidence, and decodes back to the exact Korean text it started as.
func TestLegacyCP949PathNormalizesToKoreanText(t *testing.T) {
	// The literal is held as \u escapes because Go source carries no Korean; it decodes to the same text.
	const want = "\uc548\ub155\ud558\uc138\uc694\uc138\uacc4\ud3c9\ud654\ub97c\uc704\ud574\ub178\ub825\ud558\uc790\ubaa8\ub450\ud568\uaed8"
	raw, err := korean.EUCKR.NewEncoder().Bytes([]byte(want))
	if err != nil {
		t.Fatalf("encoding the fixture as CP949/EUC-KR: %v", err)
	}
	if len(raw) < 30 {
		t.Fatalf("fixture is only %d bytes, want at least 30 for a confident detection", len(raw))
	}

	p, err := ParseSafePath(string(raw))
	if err != nil {
		t.Fatalf("ParseSafePath(%d CP949 bytes): %v", len(raw), err)
	}
	if got := p.String(); got != want {
		t.Fatalf("ParseSafePath decoded %q, want %q", got, want)
	}
}

// TestAllASCIIPathTakesTheFastPath proves the fast path splitValidated
// documents: an all-ASCII path is already normal, so uniname.Components
// must hand back the exact slice it was given rather than an equal-looking
// copy, and splitValidated must then return the input string unchanged
// rather than a joined reconstruction of it.
//
// The slice identity is the load-bearing assertion. It is what lets
// splitValidated recognise "nothing changed" and skip the join, so pinning
// it pins the reason the join is skipped, without reaching for unsafe to
// inspect the returned string's own backing array.
func TestAllASCIIPathTakesTheFastPath(t *testing.T) {
	s := "documents/report.txt"

	comps := strings.Split(s, "/")
	if normalized := uniname.Components(comps); &normalized[0] != &comps[0] {
		t.Fatalf("uniname.Components(%v) returned a new slice for an all-ASCII input", comps)
	}

	got, gotComps, err := splitValidated(s)
	if err != nil {
		t.Fatalf("splitValidated(%q): %v", s, err)
	}
	if got != s {
		t.Fatalf("splitValidated(%q) returned %q, want the input unchanged", s, got)
	}
	if len(gotComps) != 2 || gotComps[0] != "documents" || gotComps[1] != "report.txt" {
		t.Fatalf("splitValidated(%q) components = %v", s, gotComps)
	}
}

// TestNormalizedOverLengthNameIsRefused covers a name whose raw bytes fit
// limits.NameBytes but whose normalized form does not: CP949 spends 2 bytes
// per Hangul syllable where UTF-8 spends 3, so a component built to sit
// under the raw-byte bound crosses it once decoded, and the refusal must be
// enforced against the bytes that would actually reach the kernel.
func TestNormalizedOverLengthNameIsRefused(t *testing.T) {
	// Held as a \u escape because Go source carries no Korean; it decodes to the same syllable.
	nfc := strings.Repeat("\uac00", 120)
	raw, err := korean.EUCKR.NewEncoder().Bytes([]byte(nfc))
	if err != nil {
		t.Fatalf("encoding the fixture as CP949/EUC-KR: %v", err)
	}
	if len(raw) > limits.NameBytes {
		t.Fatalf("fixture must fit the raw-byte bound so only normalization crosses it, got %d raw bytes", len(raw))
	}
	if len(nfc) <= limits.NameBytes {
		t.Fatalf("fixture must cross the bound once decoded, got %d decoded bytes", len(nfc))
	}

	_, _, err = splitValidated(string(raw))
	if err == nil {
		t.Fatalf("splitValidated accepted %d raw bytes whose normalized form is %d bytes, over the %d bound",
			len(raw), len(nfc), limits.NameBytes)
	}
	var exceeded *limits.Exceeded
	if !errors.As(err, &exceeded) || exceeded.Limit != "name bytes" {
		t.Fatalf("splitValidated error = %v, want a \"name bytes\" limits.Exceeded", err)
	}
}
