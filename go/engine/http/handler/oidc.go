//go:build linux

// Single sign-on, as a client sees it.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// OIDCConfigView is what the sign-in screen reads to decide whether to offer
// the provider.
//
// The display name and nothing else. The issuer, the client id and every
// endpoint are the deployment's configuration, and this is the one response
// on the API that an unauthenticated caller can read.
type OIDCConfigView struct {
	Enabled bool `json:"enabled"`

	// DisplayName is what goes on the button. Absent when sign-on is off,
	// since there is no button.
	DisplayName string `json:"display_name,omitempty"`
}

// OIDCStartView carries the provider URL the browser is sent to.
type OIDCStartView struct {
	AuthorizeURL string `json:"authorize_url"`
}

// SessionOidcView is the caller's own provider link, as `GET /auth/session`
// reports it to the account holder.
//
// Smaller than OIDCLinkView on purpose: an administrator's dedicated view
// carries the full subject because diagnosing a sign-in needs the exact
// string to compare against what the provider shows, but the account's own
// screen only ever asks "is something connected", so a hint is enough and a
// full identifier is one less thing this response discloses to whoever is
// reading it in a browser's network tab.
type SessionOidcView struct {
	Linked bool `json:"linked"`

	// SubjectHint is enough to recognise which identity is attached and
	// never the whole subject.
	SubjectHint string `json:"subject_hint,omitempty"`
	LinkedNs    string `json:"linked_ns,omitempty"`
}

// SessionOidcOf projects a link into what the account holder may see about
// their own identity.
func SessionOidcOf(l auth.OIDCLink) SessionOidcView {
	return SessionOidcView{
		Linked:      true,
		SubjectHint: oidcSubjectHint(l.Subject),
		LinkedNs:    strconv.FormatInt(l.LinkedNs, 10),
	}
}

// oidcSubjectHint keeps four characters from each end of a subject and
// replaces the rest, so the screen can say which identity is attached
// without ever printing the whole of it. Runes, not bytes: a subject a
// provider chose is not guaranteed to be ASCII, and slicing bytes could cut
// a multi-byte character in half.
func oidcSubjectHint(subject string) string {
	r := []rune(subject)
	const edge = 4
	if len(r) <= edge*2 {
		return subject
	}
	return string(r[:edge]) + "..." + string(r[len(r)-edge:])
}

// OIDCLinkView is one account's provider link, as an administrator sees it.
//
// No token and no claim beyond the two that identify the link. What an
// operator needs is whether an account signs in through the provider and
// which identity it answers to.
type OIDCLinkView struct {
	Linked bool `json:"linked"`

	Issuer  string `json:"issuer,omitempty"`
	Subject string `json:"subject,omitempty"`

	// LinkedNs is when it was attached. LastLoginNs is absent for a link that
	// exists and has never been used, which is different from one used long
	// ago and is what an operator looks for when auditing.
	LinkedNs    string `json:"linked_ns,omitempty"`
	LastLoginNs string `json:"last_login_ns,omitempty"`
}

// OIDCLinkOf projects a link.
func OIDCLinkOf(l auth.OIDCLink) OIDCLinkView {
	v := OIDCLinkView{
		Linked:   true,
		Issuer:   l.Issuer,
		Subject:  l.Subject,
		LinkedNs: strconv.FormatInt(l.LinkedNs, 10),
	}
	if l.LastLoginNs != nil {
		v.LastLoginNs = strconv.FormatInt(*l.LastLoginNs, 10)
	}
	return v
}
