package mw

import (
	"net/http"
)

// SecurityHeaders is step 4. The set is fixed here and nowhere else, so a
// header added to protect the API surface is present on every mount that
// composes this chain.
//
// The content origin serves stored bytes that a browser must never execute in
// a context that has a session, so its policy is the harshest one the browser
// understands: no scripts, no connections, and a sandbox on top.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Written before the handler so every response carries them, error or
		// not; the handler may only add to them.
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-site")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		if OriginFrom(r.Context()) == OriginContent {
			h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		} else {
			h.Set("Content-Security-Policy",
				"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'; "+
					"base-uri 'none'; form-action 'self'; object-src 'none'")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=(), interest-cohort=()")
		}
		next.ServeHTTP(w, r)
	})
}
