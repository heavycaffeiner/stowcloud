// Linux only, for the same reason as the rest of this package.
//go:build linux

// Request body bounds, decided by the route's declared class.
//
// One place enforces the bound. The old code checked a length before reading
// and again after decoding, which is two answers to one question and the
// second one only arrives after the bytes are already in memory.
package middleware

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// ErrBodyTooLarge is a body past its class's bound. It carries the bound so a
// caller can say what the limit was.
var ErrBodyTooLarge = errors.New("the request body is too large")

// ErrBodyMalformed is a body that did not parse.
var ErrBodyMalformed = errors.New("the request body is malformed")

// BodyBound is the ceiling for a class, in bytes.
//
// BodyNone and BodyStream have no shared ceiling: nothing is read for the
// first, and the owning protocol applies its own rules for the second. A TUS
// PATCH is a stream and must not meet the JSON ceiling on its way past.
func BodyBound(c route.BodyClass) (int64, bool) {
	switch c {
	case route.BodyJSON:
		return limits.RequestBody, true
	case route.BodyDAVXML:
		// Lower than JSON because an XML body is parsed into a tree, so its
		// cost is not linear in its length the way a decode is.
		return limits.RequestBodyXML, true
	case route.BodyNone, route.BodyStream:
		return 0, false
	default:
		return 0, false
	}
}

// LimitBody wraps a body at its class's bound.
//
// The reader refuses at the boundary rather than after buffering, so a body
// twice the ceiling costs the ceiling plus one byte of memory rather than
// twice the ceiling.
func LimitBody(body io.Reader, c route.BodyClass) io.Reader {
	bound, ok := BodyBound(c)
	if !ok {
		if c == route.BodyNone {
			// Nothing is read. A non-empty body on a route that declares none
			// is the protocol's business, not this reader's.
			return eofReader{}
		}
		return body
	}
	return &boundedReader{inner: io.LimitReader(body, bound+1), bound: bound}
}

// eofReader is an empty body.
type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// boundedReader fails the read that crosses the bound.
//
// io.LimitReader alone reports EOF at the ceiling, which a decoder reads as a
// truncated document rather than as an oversized one. The difference matters:
// the first is a 400 blaming the client's syntax and the second is a 413
// naming a limit they can act on.
type boundedReader struct {
	inner io.Reader
	bound int64
	read  int64
}

func (r *boundedReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	r.read += int64(n)
	if r.read > r.bound {
		return n, fmt.Errorf("%w: the bound is %d bytes", ErrBodyTooLarge, r.bound)
	}
	return n, err
}

// DecodeJSON reads exactly one JSON document from a bounded body.
//
// Strict: an unknown field and trailing data are both refusals. Trailing data
// is the one that matters, because a body of two documents is a request whose
// meaning depends on which one the reader happened to take.
func DecodeJSON(body io.Reader, into any) error {
	dec := json.NewDecoder(LimitBody(body, route.BodyJSON))
	dec.DisallowUnknownFields()

	if err := dec.Decode(into); err != nil {
		if errors.Is(err, ErrBodyTooLarge) {
			return err
		}
		return fmt.Errorf("%w: %s", ErrBodyMalformed, err)
	}
	// One document, then end. A second value is refused rather than ignored.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if errors.Is(err, ErrBodyTooLarge) {
			return err
		}
		return fmt.Errorf("%w: the body carries more than one document", ErrBodyMalformed)
	}
	return nil
}
