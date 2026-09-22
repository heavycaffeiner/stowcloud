// Linux only, for the same reason as the rest of this package.
//go:build linux

// Request identity and the response headers that do not depend on the outcome.
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// TraceHeader carries the request id back to the client.
const TraceHeader = "Sc-Trace"

// NewTraceID mints a UUID v4 from 16 random bytes.
//
// Returns an error rather than falling back to a weaker source. A process that
// cannot read randomness cannot mint a session either, so continuing with a
// predictable id would hide the real failure behind a working-looking server.
func NewTraceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("minting a request id: %w", err)
	}
	// Version 4 and the RFC 4122 variant, which is what makes this a UUID
	// rather than 16 hex bytes with dashes.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// SecurityHeaders is the set applied to every response, success or error.
//
// Returned as a map rather than written directly so the set is a value the
// tests can read, and so a protocol handler can be checked against it rather
// than trusted to leave it alone.
func SecurityHeaders() map[string]string {
	return map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		// Nothing here needs a device. Naming them denies them for embedded
		// content too, which is where a permission is most easily forgotten.
		"Permissions-Policy":         "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
		"Cross-Origin-Opener-Policy": "same-origin",
		// The content host serves file bytes that a browser must not treat as
		// same-origin script, and this is the header that says so.
		"Cross-Origin-Resource-Policy": "same-origin",
	}
}

// CSP builds the application's content security policy.
//
// scriptHashes are the hashes of the embedded hydration scripts, in
// "sha256-..." form. They exist because the interface inlines its hydration
// payload, and a policy that admitted it with unsafe-inline would admit every
// other inline script with it.
func CSP(scriptHashes []string) string {
	script := []string{"'self'"}
	for _, h := range scriptHashes {
		script = append(script, "'"+h+"'")
	}

	directives := []string{
		"default-src 'self'",
		"script-src " + strings.Join(script, " "),
		"style-src 'self' 'unsafe-inline'",
		// data: for fonts, blob: for workers. Both are named directives rather
		// than a blanket source, so neither reaches script.
		"font-src 'self' data:",
		"worker-src 'self' blob:",
		"img-src 'self' data: blob:",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"object-src 'none'",
	}
	return strings.Join(directives, "; ")
}

// CSPAdmitsUploadedContent reports whether a policy names a host in a directive
// that can execute or frame what it points at.
//
// Uploaded content is served from the content host, and the app's policy must
// not admit that host into script, frame or worker sources: a file a user
// uploaded would otherwise run as the application.
func CSPAdmitsUploadedContent(policy, contentHost string) bool {
	if contentHost == "" {
		return false
	}
	for _, d := range strings.Split(policy, ";") {
		d = strings.TrimSpace(d)
		name, sources, found := strings.Cut(d, " ")
		if !found {
			continue
		}
		switch name {
		case "script-src", "worker-src", "frame-src", "child-src", "default-src":
			if strings.Contains(sources, contentHost) {
				return true
			}
		}
	}
	return false
}
