// Linux only, for the same reason as the rest of this package.
//go:build linux

// Credential selection: which of the three the request presented, and which
// one wins when it presented more than one.
//
// The choice is a pure function over the two header values and the cookie, so
// the ordering rule is a table rather than a sequence of early returns buried
// in a handler. What the credential proves is not decided here; that is the
// auth service's answer.
package middleware

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// decodeBase64 reads the standard encoding a Basic header uses.
func decodeBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// CredentialKind names which credential a request presented.
type CredentialKind uint8

const (
	// CredentialNone is a request carrying no credential at all.
	CredentialNone CredentialKind = iota

	// CredentialBasicApp is an app password in an HTTP Basic header. The
	// username half is ignored: the token names its own account, and trusting
	// the username would let a caller point a valid token at another account.
	CredentialBasicApp

	// CredentialBearerApp is an app password in a Bearer header.
	CredentialBearerApp

	// CredentialSessionCookie is the browser session cookie.
	CredentialSessionCookie
)

// String is the kind's name in a diagnostic.
func (k CredentialKind) String() string {
	switch k {
	case CredentialBasicApp:
		return "basic app password"
	case CredentialBearerApp:
		return "bearer app password"
	case CredentialSessionCookie:
		return "session cookie"
	case CredentialNone:
		return "none"
	default:
		return "unknown"
	}
}

// SessionCookieName is the browser session cookie. The __Host- prefix is part
// of the name: it binds the cookie to this exact host with no Domain attribute
// and a Secure flag, enforced by the browser rather than by this server.
const SessionCookieName = "__Host-sc_sid"

// Presented is what a request carried, before anything checks whether any of
// it is valid.
type Presented struct {
	// Authorization is the header verbatim, or empty.
	Authorization string
	// Cookie is the session cookie's value, or empty.
	Cookie string
}

// Credential is the one credential to attempt.
type Credential struct {
	Kind CredentialKind
	// Token is the secret to check: the app password for either header form,
	// or the decoded session token bytes for the cookie.
	Token []byte
}

// Select picks the credential to attempt, in the fixed order.
//
// Basic precedes Bearer because WebDAV and sync clients routinely have both
// headers represented, and the Basic one is what those libraries actually
// populate. The cookie is last so a device credential is never shadowed by a
// stale browser session sharing the connection.
//
// sessionOnly is the public-read case: a signed-in browser sees personalised
// state, and a stale header does not turn a public page into an auth failure.
func Select(p Presented, sessionOnly bool) Credential {
	if !sessionOnly {
		if tok, ok := basicToken(p.Authorization); ok {
			return Credential{Kind: CredentialBasicApp, Token: tok}
		}
		if tok, ok := bearerToken(p.Authorization); ok {
			return Credential{Kind: CredentialBearerApp, Token: tok}
		}
	}
	if tok, ok := sessionToken(p.Cookie); ok {
		return Credential{Kind: CredentialSessionCookie, Token: tok}
	}
	return Credential{Kind: CredentialNone}
}

// basicToken reads the password half of an HTTP Basic header.
func basicToken(header string) ([]byte, bool) {
	rest, ok := afterScheme(header, "basic")
	if !ok {
		return nil, false
	}
	raw, err := decodeBase64(rest)
	if err != nil {
		return nil, false
	}
	// The username is discarded rather than compared. A token names the
	// account it belongs to, so a username here is decoration at best and a
	// way to aim a valid token elsewhere at worst.
	_, pass, found := strings.Cut(string(raw), ":")
	if !found || pass == "" {
		return nil, false
	}
	return []byte(pass), true
}

// bearerToken reads a Bearer header's token.
func bearerToken(header string) ([]byte, bool) {
	rest, ok := afterScheme(header, "bearer")
	if !ok || rest == "" {
		return nil, false
	}
	return []byte(rest), true
}

// sessionToken decodes the cookie's hex value into the raw bytes the auth
// store hashes.
//
// The store hashes the bytes, not their printable form. Hashing the hex text
// would make the cookie's spelling part of the secret, so a client that
// re-encoded it in upper case would be signed out.
func sessionToken(cookie string) ([]byte, bool) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return nil, false
	}
	raw, err := hex.DecodeString(cookie)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// afterScheme matches a case-insensitive auth scheme and returns what follows.
func afterScheme(header, scheme string) (string, bool) {
	header = strings.TrimSpace(header)
	if len(header) <= len(scheme) {
		return "", false
	}
	if !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	// Exactly one space is what a client sends, but a tolerant trim here costs
	// nothing: the value is checked against a store either way.
	rest := header[len(scheme):]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
