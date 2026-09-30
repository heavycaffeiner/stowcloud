// Linux only, for the same reason as the rest of this package.
//go:build linux

// The JSON request body bound, enforced while the body is read.
package middleware

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/protocol/limits"
)

// ErrBodyTooLarge is a body past the JSON bound. It carries the bound so a
// caller can say what the limit was.
var ErrBodyTooLarge = errors.New("the request body is too large")

// ErrBodyMalformed is a body that did not parse.
var ErrBodyMalformed = errors.New("the request body is malformed")

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
	dec := json.NewDecoder(&boundedReader{inner: io.LimitReader(body, limits.RequestBody+1), bound: limits.RequestBody})
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
