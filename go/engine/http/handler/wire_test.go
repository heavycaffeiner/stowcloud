// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

type patch struct {
	Name  Value[string] `json:"name"`
	Quota Value[int64]  `json:"quota"`
}

// Absent, null and set are three states. A pointer alone cannot express them,
// because an omitted field and an explicit null both decode to nil, and they
// mean opposite things in a PATCH: leave it alone, and clear it.
func TestThePatchValueSeparatesAbsentFromNull(t *testing.T) {
	var p patch
	if err := json.Unmarshal([]byte(`{"name":"alice"}`), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !p.Name.Set || p.Name.Null || p.Name.Value != "alice" {
		t.Errorf("a set field decoded as %+v", p.Name)
	}
	if p.Quota.Set {
		t.Errorf("an absent field decoded as set: %+v", p.Quota)
	}

	var q patch
	if err := json.Unmarshal([]byte(`{"quota":null}`), &q); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !q.Quota.Set || !q.Quota.Null {
		t.Errorf("an explicit null decoded as %+v", q.Quota)
	}
	// The distinction that matters: absent and null are not the same value.
	if p.Quota.Set == q.Quota.Set {
		t.Error("an absent field and an explicit null are indistinguishable")
	}
}

// An untrusted number is bounded before it is used, and the failure says why.
func TestIntParsingRefusesOutOfRangeAndGarbage(t *testing.T) {
	if got, err := Int("42", 1, 100); err != nil || got != 42 {
		t.Fatalf("Int(42) = %d, %v", got, err)
	}
	for _, c := range []struct{ what, raw string }{
		{"empty", ""},
		{"not a number", "abc"},
		{"a float", "1.5"},
		{"past the ceiling", "101"},
		{"below the floor", "0"},
		{"a negative", "-1"},
		// The one an Atoi-then-cast would let through: it overflows int64 and
		// must be caught before any narrowing rather than after.
		{"past int64", "99999999999999999999999"},
	} {
		if _, err := Int(c.raw, 1, 100); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s (%q) returned %v", c.what, c.raw, err)
		}
	}
}

// An identifier starts at one. Zero is the zero value of a missing row rather
// than a row.
func TestIDRefusesZero(t *testing.T) {
	if _, err := ID("0"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ID(0) returned %v", err)
	}
	if got, err := ID("7"); err != nil || got != 7 {
		t.Errorf("ID(7) = %d, %v", got, err)
	}
}

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
		got := ContentDisposition(name)

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
	got := ContentDisposition("été.pdf")
	// é is C3 A9 in UTF-8.
	if !strings.Contains(got, "%C3%A9t%C3%A9.pdf") {
		t.Errorf("the encoded form is wrong: %q", got)
	}
}

// Every item runs and every result carries the index it came from, so one
// item's failure cannot shift another's result.
func TestABatchRunsEveryItemInOrder(t *testing.T) {
	items := []string{"a", "bad", "c", "bad", "e"}

	got := RunBatch(context.Background(), items, apierr.VisibilityKnown,
		func(_ context.Context, s string) (string, error) {
			if s == "bad" {
				return "", core.ErrNotFound
			}
			return strings.ToUpper(s), nil
		})

	if len(got) != len(items) {
		t.Fatalf("%d results for %d items", len(got), len(items))
	}
	for i, r := range got {
		if r.Index != i {
			t.Errorf("result %d carries index %d", i, r.Index)
		}
		wantOK := items[i] != "bad"
		if r.OK != wantOK {
			t.Errorf("item %d (%q) reported ok=%v", i, items[i], r.OK)
		}
		if wantOK && r.Value != strings.ToUpper(items[i]) {
			t.Errorf("item %d produced %v", i, r.Value)
		}
		if !wantOK && r.Error == nil {
			t.Errorf("item %d failed with no error", i)
		}
	}
}

// A failed item carries the same wire shape a single request would, so a batch
// does not become the surface that reveals what a single request hid.
func TestABatchItemCarriesTheSameShapeAsASingleRequest(t *testing.T) {
	got := RunBatch(context.Background(), []string{"x"}, apierr.VisibilityHidden,
		func(context.Context, string) (string, error) { return "", core.ErrDenied })

	if len(got) != 1 || got[0].Error == nil {
		t.Fatalf("the batch produced %+v", got)
	}
	single := apierr.WireOf(core.ErrDenied, apierr.VisibilityHidden)
	a, aerr := json.Marshal(*got[0].Error)
	b, berr := json.Marshal(single)
	if aerr != nil || berr != nil {
		t.Fatalf("encoding: %v %v", aerr, berr)
	}
	if string(a) != string(b) {
		t.Errorf("the batch item differs from the single response:\n  %s\n  %s", a, b)
	}
}

// A cancelled batch reports the remaining items rather than omitting them. A
// caller comparing lengths must not read a short list as "the rest succeeded".
func TestACancelledBatchReportsTheRemainingItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	got := RunBatch(ctx, []string{"a", "b", "c", "d"}, apierr.VisibilityKnown,
		func(_ context.Context, s string) (string, error) {
			if s == "b" {
				cancel()
			}
			return s, nil
		})

	if len(got) != 4 {
		t.Fatalf("a cancelled batch produced %d results for 4 items", len(got))
	}
	for i, r := range got {
		if r.Index != i {
			t.Errorf("result %d carries index %d", i, r.Index)
		}
	}
	// The first two ran; the rest report the cancellation.
	if !got[0].OK || !got[1].OK {
		t.Errorf("the items before the cancellation reported %+v %+v", got[0], got[1])
	}
	if got[2].OK || got[3].OK {
		t.Errorf("items after the cancellation reported success: %+v %+v", got[2], got[3])
	}
	if got[2].Error == nil || got[3].Error == nil {
		t.Error("a cancelled item carries no error")
	}
}

// An empty batch is an empty result, not a nil one: a client decoding the
// response gets a list either way.
func TestAnEmptyBatchProducesAnEmptyList(t *testing.T) {
	got := RunBatch(context.Background(), []string{}, apierr.VisibilityKnown,
		func(_ context.Context, s string) (string, error) { return s, nil })
	if got == nil {
		t.Fatal("an empty batch produced a nil list")
	}
	if len(got) != 0 {
		t.Fatalf("an empty batch produced %d results", len(got))
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("an empty batch encoded as %s", raw)
	}
}
