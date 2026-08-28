// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
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

// Each class gets its own ceiling, and the two that have none say so.
func TestEachClassHasItsBound(t *testing.T) {
	for _, c := range []struct {
		class route.BodyClass
		bound int64
		has   bool
	}{
		{route.BodyJSON, limits.RequestBody, true},
		{route.BodyDAVXML, limits.RequestBodyXML, true},
		{route.BodyStream, 0, false},
		{route.BodyNone, 0, false},
	} {
		got, has := BodyBound(c.class)
		if has != c.has || got != c.bound {
			t.Errorf("%v answered (%d, %v), want (%d, %v)", c.class, got, has, c.bound, c.has)
		}
	}
	// The XML bound is the lower one, since an XML body becomes a tree.
	if limits.RequestBodyXML >= limits.RequestBody {
		t.Error("the XML bound is not lower than the JSON bound")
	}
}

// A body past its bound fails at the boundary rather than after buffering, so
// a body twice the ceiling costs the ceiling rather than twice it.
func TestAnOversizedBodyFailsWithoutBufferingItAll(t *testing.T) {
	huge := strings.NewReader(strings.Repeat("x", int(limits.RequestBody)*3))
	counter := &countingReader{inner: huge}

	_, err := io.ReadAll(LimitBody(counter, route.BodyJSON))
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
	exact := strings.NewReader(strings.Repeat("x", int(limits.RequestBody)))
	got, err := io.ReadAll(LimitBody(exact, route.BodyJSON))
	if err != nil {
		t.Fatalf("a body at the bound returned %v", err)
	}
	if int64(len(got)) != limits.RequestBody {
		t.Errorf("read %d bytes, want %d", len(got), limits.RequestBody)
	}
}

// A stream is not bounded here. TUS sends multi-gigabyte chunks and must not
// meet the JSON ceiling on its way past.
func TestAStreamIsNotBoundedByTheSharedReader(t *testing.T) {
	big := strings.Repeat("x", int(limits.RequestBody)+4096)
	got, err := io.ReadAll(LimitBody(strings.NewReader(big), route.BodyStream))
	if err != nil {
		t.Fatalf("a stream past the JSON bound returned %v", err)
	}
	if len(got) != len(big) {
		t.Errorf("the stream delivered %d of %d bytes", len(got), len(big))
	}
}

// A route declaring no body reads none, whatever the client sent.
func TestARouteWithNoBodyReadsNothing(t *testing.T) {
	counter := &countingReader{inner: strings.NewReader("unexpected payload")}
	got, err := io.ReadAll(LimitBody(counter, route.BodyNone))
	if err != nil {
		t.Fatalf("a no-body route returned %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a no-body route delivered %q", got)
	}
	if counter.read != 0 {
		t.Errorf("a no-body route pulled %d bytes", counter.read)
	}
}

// The DAV bound is lower, and a body between the two bounds is refused as XML
// while it would have passed as JSON.
func TestTheDAVBoundIsEnforcedSeparately(t *testing.T) {
	between := strings.Repeat("x", int(limits.RequestBodyXML)+1024)

	_, err := io.ReadAll(LimitBody(strings.NewReader(between), route.BodyDAVXML))
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("a body past the XML bound returned %v", err)
	}
	if _, jerr := io.ReadAll(LimitBody(strings.NewReader(between), route.BodyJSON)); jerr != nil {
		t.Fatalf("the same body as JSON returned %v", jerr)
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
	if !strings.Contains(err.Error(), "more than one document") {
		t.Errorf("the refusal says %q", err)
	}
}

// An unknown field is a refusal, so a client cannot send a field the server
// silently drops and believe it took effect.
func TestJSONDecodeRefusesAnUnknownField(t *testing.T) {
	err := DecodeJSON(strings.NewReader(`{"name":"alice","admin":true}`), &payload{})
	if !errors.Is(err, ErrBodyMalformed) {
		t.Fatalf("an unknown field returned %v", err)
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

// Ordinary bad syntax is malformed rather than too large.
func TestBadSyntaxIsMalformed(t *testing.T) {
	for _, body := range []string{``, `{`, `not json`, `{"name":}`} {
		err := DecodeJSON(strings.NewReader(body), &payload{})
		if !errors.Is(err, ErrBodyMalformed) {
			t.Errorf("the body %q returned %v", body, err)
		}
	}
}
