package mw

import (
	"net/http"
)

// BodyLimit is step 6. It is http.MaxBytesReader at the route-class bound,
// applied before the handler reads a byte: a body is refused by its declared
// length before a handler pays to read it. The bound comes from the D5 table
// and is keyed by the route class the request belongs to.
//
// The upload mount does not compose this step; TUS chunks are arbitrarily
// large by design and enforce their own per-chunk ceiling. The same split
// exists in the reference implementation and is why step 6 is a route split
// there rather than a layer.
func BodyLimit(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
