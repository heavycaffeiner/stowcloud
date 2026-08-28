// Linux only, for the same reason as the rest of this package.
//go:build linux

// CSRF: the token derived from a session, and who has to send it.
//
// The token is derived rather than stored. A session that exists has a valid
// token by construction, so there is no second table to keep in step with the
// session table and no window where one outlives the other.
package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// CSRFHeader is where a browser sends the token.
const CSRFHeader = "Sc-Csrf"

// CSRFToken derives the token for a session.
//
// The printable session token is hashed before it reaches the HMAC so the
// derivation never handles the raw secret's bytes directly, and the HMAC key
// is deployment material rather than anything the client can see. Neither half
// alone is enough to mint a token for someone else's session.
//
// The key is durable. Sessions survive a restart in the rebuilt auth service,
// so a process-random key would strand every still-valid session at every
// restart: signed in, but unable to make a single mutation.
func CSRFToken(key []byte, printableSessionToken string) string {
	sum := sha256.Sum256([]byte(printableSessionToken))
	mac := hmac.New(sha256.New, key)
	// hash.Hash documents Write as never returning an error.
	mac.Write(sum[:]) //nolint:errcheck // see the comment above.
	return hex.EncodeToString(mac.Sum(nil))
}

// CSRFRequired reports whether this request has to present a token.
//
// Only ambient authority needs it. An app password travels in a header the
// browser does not attach on its own, so a cross-site page cannot cause one to
// be sent. No principal skips the check because Auth has already refused, or
// the route is a public flow whose own token is the authority.
func CSRFRequired(method string, kind CredentialKind) bool {
	if kind != CredentialSessionCookie {
		return false
	}
	return mutating(method)
}

// CSRFValid compares the presented token with the derived one.
//
// Constant time, and length-checked first so a wrong-length value does not
// take a different amount of time to reject than a wrong value of the right
// length.
func CSRFValid(key []byte, printableSessionToken, presented string) bool {
	want := CSRFToken(key, printableSessionToken)
	got := strings.TrimSpace(presented)
	if len(got) != len(want) {
		return false
	}
	return hmac.Equal([]byte(got), []byte(want))
}
