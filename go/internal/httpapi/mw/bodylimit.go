package mw

import (
	"net/http"
	"strings"
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
		if r.Body != nil && !exemptFromBodyLimit(r.URL.Path) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// DefaultBodyLimit is the bound this step applies to every mount but the
// upload one. Exported because the drop-link view reports it: a page that
// announces no ceiling lets somebody wait out a whole upload to be refused at
// the end.
const DefaultBodyLimit int64 = 1 << 20

// uploadPrefix is the one mount this step does not apply to.
const uploadPrefix = "/api/uploads"

// exemptFromBodyLimit reports whether a path is the upload mount.
//
// A chunk is bounded by the session's declared length and the account's
// reservation, which the engine enforces against what it has actually
// received. A ceiling here would refuse exactly the requests this surface
// exists for, and it would refuse them by closing the connection rather than
// by answering, which a resuming client reads as a network fault.
func exemptFromBodyLimit(path string) bool {
	return path == uploadPrefix || strings.HasPrefix(path, uploadPrefix+"/")
}
