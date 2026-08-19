// Package mw is the twelve-step request chain, one file per step. Each step
// is a plain func(http.Handler) http.Handler so the source order in chain.go
// is the execution order, which is the whole point of the composition: the
// axum version this replaces had to wrap in reverse and the reversal note
// lived in a comment that could drift.
package mw

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// The context key for the request id. A distinct type keeps the value from
// colliding with anything a framework or a handler stashes under a string.
type reqIDKey struct{}

// RequestID is step 1. It mints an id, puts it in the context and writes it
// back as the Sc-Trace response header, so a log line and the response it
// produced carry the same correlation value. It is not duplicated in the JSON
// body, because the envelope has no trace field.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newV4()
		ctx := context.WithValue(r.Context(), reqIDKey{}, id)
		w.Header().Set("Sc-Trace", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TraceFrom reads the request id a RequestID step placed in the context.
// AuditSink and the handlers that log a line of their own use it so the line
// correlates with the Sc-Trace header the client sees.
func TraceFrom(ctx context.Context) string {
	if id, ok := ctx.Value(reqIDKey{}).(string); ok {
		return id
	}
	return ""
}

// newV4 builds a UUID v4 from crypto/rand by hand. The dependency the plan
// would otherwise add exists to format sixteen bytes, and sixteen bytes with
// the version and variant bits set is not a subtle problem to reimplement.
func newV4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// The generator is on the request path and rand.Read only fails when
		// the kernel entropy source is gone, at which point no secure session
		// can be minted either and refusing the request is the honest answer.
		panic("crypto/rand is unavailable: " + err.Error()) //nolint:forbidigo // see above.
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:])
}
