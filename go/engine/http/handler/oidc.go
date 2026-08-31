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

// OIDCCallbackView is what a completed flow answers.
//
// One shape for both flows, distinguished by which field is set: a sign-in
// carries the identity it established, a link carries only that it linked.
// A client reading the wrong one gets a zero value rather than a wrong
// answer.
type OIDCCallbackView struct {
	// Linked marks the account-linking flow. A sign-in leaves it false and
	// carries an identity instead.
	Linked bool `json:"linked,omitempty"`

	// Identity is the session that was established, absent on a link.
	Identity IdentityView `json:"identity,omitzero"`

	// ReturnTo is where the client navigates next. It came from the request
	// that began the flow and was validated then, so a client following it is
	// following a path this server already accepted.
	ReturnTo string `json:"return_to,omitempty"`
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
