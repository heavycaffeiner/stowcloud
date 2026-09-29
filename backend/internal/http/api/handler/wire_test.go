// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"errors"
	"strings"
	"testing"

	httpheader "github.com/heavycaffeiner/stowcloud/backend/internal/http/headers"
)

// A return path stays on this origin. The protocol-relative form is the case a
// naive "starts with /" check lets through, and a browser resolves it to
// another origin entirely.
func TestSafeReturnToRefusesEverythingThatLeavesTheOrigin(t *testing.T) {
	if got, err := SafeReturnTo("/files/documents"); err != nil || got != "/files/documents" {
		t.Fatalf("a local path returned %q, %v", got, err)
	}
	if got, err := SafeReturnTo(""); err != nil || got != "/" {
		t.Fatalf("an empty path returned %q, %v", got, err)
	}

	for _, c := range []struct{ what, raw string }{
		{"a protocol-relative URL", "//evil.example.test/"},
		{"an absolute URL", "https://evil.example.test/"},
		{"a scheme-only URL", "javascript:alert(1)"},
		{"a relative path", "files/documents"},
		{"a backslash", "/\\evil.example.test"},
		{"a newline", "/files\nX-Injected: 1"},
		{"a control byte", "/files\x00"},
		{"non-ASCII", "/files/\u00e9"},
	} {
		if _, err := SafeReturnTo(c.raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s (%q) was accepted", c.what, c.raw)
		}
	}
}

// Neither form of the filename can end the quoted string or the header.
func TestContentDispositionCannotEscapeEitherForm(t *testing.T) {
	for _, name := range []string{
		`report".pdf`,
		"report\\.pdf",
		"report\r\nX-Injected: 1.pdf",
		"report\x00.pdf",
		"\u00e9t\u00e9.pdf",
		"файл.pdf",
	} {
		got := httpheader.Attachment(name)

		// The header is one line: a CR or LF anywhere in it is a second header
		// the caller did not write.
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("%q produced a header with a line break: %q", name, got)
		}
		// The quoted fallback ends where it should. Counting quotes is enough:
		// the format has exactly two, and a third means the name closed it.
		if strings.Count(got, `"`) != 2 {
			t.Errorf("%q produced %d quotes: %q", name, strings.Count(got, `"`), got)
		}
		if !strings.Contains(got, "filename*=UTF-8''") {
			t.Errorf("%q produced no RFC 5987 form: %q", name, got)
		}
	}
}

// The RFC 5987 form carries the real name, so a client that reads it gets the
// characters the fallback could not represent.
func TestTheEncodedFilenameRoundTrips(t *testing.T) {
	got := httpheader.Attachment("été.pdf")
	// é is C3 A9 in UTF-8.
	if !strings.Contains(got, "%C3%A9t%C3%A9.pdf") {
		t.Errorf("the encoded form is wrong: %q", got)
	}
}
