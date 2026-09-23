// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// A missing version header is refused as firmly as a wrong one. The header is
// how a request says which contract it was written against, and one without it
// is a request whose meaning has to be guessed.
func TestTheVersionHeaderIsRequired(t *testing.T) {
	if err := CheckResumable(TusProtocolVersion); err != nil {
		t.Errorf("the supported version returned %v", err)
	}
	for _, c := range []struct{ what, header string }{
		{"absent", ""},
		{"blank", "   "},
		{"an older version", "0.2.2"},
		{"a newer version", "2.0.0"},
		{"not a version", "yes"},
	} {
		if err := CheckResumable(c.header); !errors.Is(err, ErrTusVersion) {
			t.Errorf("%s (%q) returned %v", c.what, c.header, err)
		}
	}
}

// A negative offset is a different request, not a small number: it would seek
// backwards through the part file.
func TestANegativeOffsetIsRefused(t *testing.T) {
	if got, err := ParseOffset("1024"); err != nil || got != 1024 {
		t.Fatalf("ParseOffset(1024) = %d, %v", got, err)
	}
	if got, err := ParseOffset("0"); err != nil || got != 0 {
		t.Errorf("ParseOffset(0) = %d, %v", got, err)
	}

	for _, c := range []struct{ what, header string }{
		{"negative", "-1"},
		{"absent", ""},
		{"not a number", "abc"},
		{"a float", "10.5"},
		{"hex", "0x10"},
		{"past uint64", "99999999999999999999999"},
	} {
		if _, err := ParseOffset(c.header); !errors.Is(err, ErrTus) {
			t.Errorf("%s (%q) returned %v", c.what, c.header, err)
		}
	}
}

// The two length headers are one decision. Read separately, a request carrying
// both is treated as whichever the code happened to check first.
func TestTheLengthHeadersAreOneDecision(t *testing.T) {
	got, err := ParseLength("2048", "")
	if err != nil || got.Deferred || got.Value != 2048 {
		t.Errorf("a declared length parsed as %+v, %v", got, err)
	}

	got, err = ParseLength("", "1")
	if err != nil || !got.Deferred {
		t.Errorf("a deferred length parsed as %+v, %v", got, err)
	}

	for _, c := range []struct{ what, length, defer_ string }{
		{"neither", "", ""},
		{"both", "2048", "1"},
		{"a deferred flag that is not 1", "", "true"},
		{"a deferred flag of 0", "", "0"},
		{"a negative length", "-1", ""},
		{"a length that is not a number", "big", ""},
	} {
		if _, lerr := ParseLength(c.length, c.defer_); !errors.Is(lerr, ErrTus) {
			t.Errorf("%s returned %v", c.what, lerr)
		}
	}
}

// A deferred length is not zero. Zero is a real declared length, and reading
// the value of a deferred request would announce an empty file.
func TestADeferredLengthIsNotZero(t *testing.T) {
	deferred, err := ParseLength("", "1")
	if err != nil {
		t.Fatalf("ParseLength: %v", err)
	}
	declared, err := ParseLength("0", "")
	if err != nil {
		t.Fatalf("ParseLength: %v", err)
	}

	// Both have Value zero, so the flag is the only thing that tells them
	// apart, which is why it exists.
	if deferred.Value != declared.Value {
		t.Fatal("the two cases differ by value, so this test proves nothing")
	}
	if !deferred.Deferred || declared.Deferred {
		t.Errorf("the deferred flag is %v and %v", deferred.Deferred, declared.Deferred)
	}
}

// Metadata is base64 pairs, and the value is a name the client chose.
func TestMetadataDecodes(t *testing.T) {
	name := base64.StdEncoding.EncodeToString([]byte("holiday photo.jpg"))
	kind := base64.StdEncoding.EncodeToString([]byte("image/jpeg"))

	got, err := ParseMetadata("filename "+name+",filetype "+kind+",is_final", 16)
	if err != nil {
		t.Fatalf("ParseMetadata: %v", err)
	}
	if got["filename"] != "holiday photo.jpg" {
		t.Errorf("the filename decoded to %q", got["filename"])
	}
	if got["filetype"] != "image/jpeg" {
		t.Errorf("the type decoded to %q", got["filetype"])
	}
	// A key with no value is a flag, which the protocol allows.
	if v, ok := got["is_final"]; !ok || v != "" {
		t.Errorf("the flag key decoded to %q (present=%v)", v, ok)
	}

	// An empty header is an empty map rather than an error: metadata is
	// optional.
	empty, eerr := ParseMetadata("", 16)
	if eerr != nil || len(empty) != 0 {
		t.Errorf("an empty header returned %v, %v", empty, eerr)
	}
}

// A repeated key has no defined winner, so picking one silently would leave
// two clients disagreeing about what they sent.
func TestARepeatedMetadataKeyIsRefused(t *testing.T) {
	a := base64.StdEncoding.EncodeToString([]byte("first.jpg"))
	b := base64.StdEncoding.EncodeToString([]byte("second.jpg"))

	if _, err := ParseMetadata("filename "+a+",filename "+b, 16); !errors.Is(err, ErrTus) {
		t.Errorf("a repeated key returned %v", err)
	}
}

// Malformed metadata is refused rather than partially accepted.
func TestMalformedMetadataIsRefused(t *testing.T) {
	for _, c := range []struct{ what, header string }{
		{"a value that is not base64", "filename !!!!"},
		{"an empty key", " dmFsdWU="},
		{"a key carrying an equals", "a=b dmFsdWU="},
		{"a trailing separator", "filename dmFsdWU=,"},
	} {
		if _, err := ParseMetadata(c.header, 16); !errors.Is(err, ErrTus) {
			t.Errorf("%s (%q) returned %v", c.what, c.header, err)
		}
	}

	// The pair count is bounded, since each pair costs a decode.
	many := strings.TrimSuffix(strings.Repeat("k dmFsdWU=,", 40), ",")
	if _, err := ParseMetadata(many, 16); !errors.Is(err, ErrTus) {
		t.Errorf("a header past the pair bound returned %v", err)
	}
}

// The checksum mismatch status is the protocol's own, not one the shared
// mapper would produce.
func TestTheChecksumStatusIsTheProtocolsOwn(t *testing.T) {
	if StatusChecksumMismatch != 460 {
		t.Errorf("the checksum status is %d", StatusChecksumMismatch)
	}
	// Outside the standard 4xx range that a generic mapper hands out, which is
	// why it cannot come from apierr.
	if StatusChecksumMismatch < 460 || StatusChecksumMismatch > 499 {
		t.Errorf("the checksum status %d is not in the protocol's range", StatusChecksumMismatch)
	}
}
