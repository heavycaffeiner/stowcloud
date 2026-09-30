// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/protocol/limits"
)

// countingReader reports how many bytes were actually pulled, which is how the
// no-full-buffering claim is checked rather than asserted.
type countingReader struct {
	inner io.Reader
	read  int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	r.read += n
	return n, err
}

// A body past the bound fails at the boundary rather than after buffering, so
// a body twice the ceiling costs the ceiling rather than twice it.
func TestAnOversizedBodyFailsWithoutBufferingItAll(t *testing.T) {
	huge := strings.NewReader(`{"name":"` + strings.Repeat("x", int(limits.RequestBody)*3) + `"}`)
	counter := &countingReader{inner: huge}

	err := DecodeJSON(counter, &payload{})
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("an oversized body returned %v", err)
	}
	if int64(counter.read) > limits.RequestBody+1 {
		t.Errorf("the reader pulled %d bytes for a bound of %d", counter.read, limits.RequestBody)
	}
}

// A body exactly at the bound is accepted. The refusal is for crossing it, not
// for reaching it.
func TestABodyAtTheBoundIsAccepted(t *testing.T) {
	shell := `{"name":""}`
	exact := `{"name":"` + strings.Repeat("x", int(limits.RequestBody)-len(shell)) + `"}`
	if err := DecodeJSON(strings.NewReader(exact), &payload{}); err != nil {
		t.Fatalf("a body at the bound returned %v", err)
	}
}

type payload struct {
	Name string `json:"name"`
}

// One document is decoded; a second one is refused rather than ignored.
func TestJSONDecodeRefusesTrailingData(t *testing.T) {
	var into payload
	if err := DecodeJSON(strings.NewReader(`{"name":"alice"}`), &into); err != nil {
		t.Fatalf("a single document returned %v", err)
	}
	if into.Name != "alice" {
		t.Errorf("decoded %+v", into)
	}

	err := DecodeJSON(strings.NewReader(`{"name":"alice"}{"name":"root"}`), &payload{})
	if !errors.Is(err, ErrBodyMalformed) {
		t.Fatalf("two documents returned %v", err)
	}
}

// An oversized JSON body is too large rather than malformed, so the caller
// answers 413 with a limit rather than 400 blaming the client's syntax.
func TestAnOversizedJSONBodyIsTooLargeNotMalformed(t *testing.T) {
	big := `{"name":"` + strings.Repeat("a", int(limits.RequestBody)) + `"}`
	err := DecodeJSON(strings.NewReader(big), &payload{})
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("an oversized document returned %v", err)
	}
	if errors.Is(err, ErrBodyMalformed) {
		t.Error("an oversized document was also reported as malformed")
	}
}
