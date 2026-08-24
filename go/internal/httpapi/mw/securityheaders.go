package mw

import (
	"net/http"
	"strings"
)

// SecurityHeaders is step 4. The set is fixed here and nowhere else, so a
// header added to protect the API surface is present on every mount that
// composes this chain.
//
// The content origin serves stored bytes that a browser must never execute in
// a context that has a session, so its policy is the harshest one the browser
// understands: no scripts, no connections, and a sandbox on top.

// AppPolicy builds the application origin's Content-Security-Policy.
//
// inlineScripts is the source list for the inline scripts the frontend bundle
// ships, or empty for a build carrying no frontend. The framework's hydration
// bootstrap is inline by construction, so a policy of 'self' alone blocks it
// and the browser renders nothing: no request fails, no status is wrong, the
// document arrives whole, and the page is blank with only a console line to
// say why. That was shipped, and it is why the hashes are derived from the
// bundle rather than written down beside it.
//
// A hash rather than 'unsafe-inline', which would admit every inline script on
// every page, including one a stored file managed to get reflected into.
//
// font-src takes data: because the interface inlines its own face. Without it
// the fallback to default-src refuses it and the page renders in a substitute:
// a visible defect rather than a blank one, but a defect.
//
// worker-src is stated rather than left to fall back. The uploader runs in a
// Worker, and a worker script is checked against worker-src, then script-src
// when that is absent: the hash beside 'self' there does not admit it, so the
// browser refused to start the worker and every upload stopped after creating
// its session, with the refusal only in the console.
func AppPolicy(inlineScripts string) string {
	script := "'self'"
	if inlineScripts != "" {
		script += " " + inlineScripts
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + script,
		// Same origin only, and no hash: a worker is a whole script file this
		// build ships, never an inline fragment.
		"worker-src 'self' blob:",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"object-src 'none'",
	}, "; ")
}

// SecurityHeaders writes the fixed set on every response.
//
// appPolicy is the application origin's policy, built by the caller because it
// depends on the frontend bundle and this package sits below the one that
// embeds it. Empty means a build with no frontend, which gets the same policy
// without any inline-script source: absent never means "admit everything".
func SecurityHeaders(appPolicy string) func(http.Handler) http.Handler {
	if appPolicy == "" {
		appPolicy = AppPolicy("")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Written before the handler so every response carries them, error
			// or not; the handler may only add to them.
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cross-Origin-Resource-Policy", "same-site")
			h.Set("X-Robots-Tag", "noindex, nofollow")
			if OriginFrom(r.Context()) == OriginContent {
				h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
			} else {
				h.Set("Content-Security-Policy", appPolicy)
				h.Set("Cross-Origin-Opener-Policy", "same-origin")
				h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=(), interest-cohort=()")
			}
			next.ServeHTTP(w, r)
		})
	}
}
