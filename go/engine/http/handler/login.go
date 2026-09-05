//go:build linux

// What a sign-in answers.
package handler

import (
	"net/url"
	"strconv"
)

// IdentityView is the identity a client records after signing in, and what
// `GET /auth/session` reports about the one it is holding.
//
// No token appears here. The session travels in a cookie the browser attaches
// on its own, and a body carrying the same secret would put it somewhere a
// page's own scripts can read.
type IdentityView struct {
	// ID is decimal, like every other account id on this API. A JavaScript
	// number loses exactness past 2^53.
	ID    string `json:"id"`
	Login string `json:"login"`

	// Display is an operator-assigned label, absent when none is set. It never
	// replaces Login: a client showing one where it means the other lets two
	// accounts look alike.
	Display string `json:"display,omitempty"`

	Admin bool `json:"admin"`

	// CSRF is the token this session has to send with every mutation. Derived
	// from the session, so it is valid for exactly as long as the session is
	// and there is no second thing to expire.
	CSRF string `json:"csrf"`
}

// IdentityViewOf projects a signed-in identity.
func IdentityViewOf(id int64, login, display string, admin bool, csrf string) IdentityView {
	return IdentityView{
		ID:      strconv.FormatInt(id, 10),
		Login:   login,
		Display: display,
		Admin:   admin,
		CSRF:    csrf,
	}
}

// RootView is one folder the caller can reach, as the interface lists it.
//
// A folder whose disk did not come back stays in the list with a reason rather
// than disappearing: one that vanishes reads as somebody having deleted it,
// when what happened is hardware that needs looking at.
type RootView struct {
	Label string   `json:"label"`
	Perms []string `json:"perms"`

	SharedExternally bool `json:"shared_externally"`
	TrashEnabled     bool `json:"trash_enabled"`

	BrokenReason string `json:"broken_reason,omitempty"`
}

// LimitsView is what a client needs in order to plan an upload before it
// starts one: the chunk size to use, and the floor it may not shrink below
// when the server refuses a chunk as too large.
type LimitsView struct {
	ChunkSize int64 `json:"chunk_size"`
	ChunkMin  int64 `json:"chunk_min"`

	// MaxFileSize is null where the deployment sets no ceiling. Zero is a real
	// limit that would refuse everything, so it cannot stand for "no limit".
	MaxFileSize *int64 `json:"max_file_size"`

	Parallel int `json:"parallel"`
}

// FeaturesView says which surfaces this deployment actually serves, so the
// interface draws the screens that lead somewhere.
type FeaturesView struct {
	WebDAV  bool `json:"webdav"`
	SMB     bool `json:"smb"`
	Preview bool `json:"preview"`
	Trash   bool `json:"trash"`
	Shares  bool `json:"shares"`

	// Search names the tier in use: "walk" reads directories on demand, the
	// others answer from an index.
	Search string `json:"search"`
}

// WhoAmIView is what `GET /auth/session` answers.
//
// It embeds the identity rather than nesting it, so the fields a login answers
// keep their spelling here. The rest is what the interface cannot work out for
// itself: which folders exist, what an upload may do, and which screens lead
// somewhere on this deployment.
type WhoAmIView struct {
	IdentityView

	// TOTPEnabled and the SMB fields describe the account rather than the
	// session. The settings screens read them to decide what to offer, and
	// fetching them separately would mean a screen that can disagree with the
	// session it was drawn from.
	TOTPEnabled bool `json:"totp_enabled"`

	SMBOptOut  bool `json:"smb_opt_out"`
	SMBEnabled bool `json:"smb_enabled"`

	// SMBCredential is what actually works over the protocol right now, with
	// the deployment's TOTP policy already folded in: the credential row and
	// the policy can disagree, and a screen saying "SMB uses a separate
	// password" would then be something a person can only disprove by failing
	// to connect.
	SMBCredential string `json:"smb_credential,omitempty"`

	// SMBUnavailableReason is carried only where the credential is none.
	SMBUnavailableReason string `json:"smb_unavailable_reason,omitempty"`

	// Roots is never null: an account with no grants gets an empty list, which
	// is the state the interface reports as "no folders yet".
	Roots []RootView `json:"roots"`

	// Oidc is the caller's own provider link, never absent: an account with
	// none reports {"linked": false} rather than omitting the key, so a
	// screen reading it does not have to treat "no field" and "not linked" as
	// two different states.
	Oidc SessionOidcView `json:"oidc"`

	Limits   LimitsView   `json:"limits"`
	Features FeaturesView `json:"features"`
}

// ChallengeView is what the password step answers for an account that has a
// second factor: the password was right, and a code is still needed.
//
// It carries no identity. The account is named inside the signed challenge and
// nowhere a client can read, so a caller who guessed a password learns nothing
// further about the account until the code verifies too.
type ChallengeView struct {
	// Required is always "totp", naming which second factor to ask for.
	Required  string `json:"required"`
	Challenge string `json:"challenge"`

	// ExpiresInSeconds lets a screen show a countdown rather than failing
	// silently when the person takes too long.
	ExpiresInSeconds int `json:"expires_in_seconds"`
}

// TOTPSetupView is what an enrolment screen needs: the secret to store and the
// URI an authenticator scans.
type TOTPSetupView struct {
	Secret string `json:"secret"`

	// URI is the otpauth form. Built here rather than by the client, because a
	// client that assembles it wrong produces codes the server will not accept
	// and nothing says which side is wrong.
	URI string `json:"uri"`
}

// TOTPSetupOf builds the enrolment payload.
//
// The issuer appears twice, as a label prefix and as a parameter, which is
// what authenticator applications expect: the prefix is what they display, and
// the parameter is what they group by.
//
// The label is validated rather than escaped. Account names are drawn from a
// charset with nothing in it a URI reserves, so an escaper here would be a
// second answer to a question the tree already answers in one place. A name
// outside that charset, which the stored rows may hold because the rule gates
// creation only, drops to the issuer alone: an authenticator that shows a
// generic label still produces correct codes, while a URI assembled around an
// unescaped delimiter does not.
func TOTPSetupOf(secretB32, login string) TOTPSetupView {
	label := totpIssuer
	if uriSafeLabel(login) {
		label += ":" + login
	}
	q := url.Values{
		"secret": {secretB32},
		"issuer": {totpIssuer},
	}
	return TOTPSetupView{
		Secret: secretB32,
		URI:    "otpauth://totp/" + label + "?" + q.Encode(),
	}
}

// uriSafeLabel reports whether a name can sit in a path segment untouched.
//
// Deliberately narrower than the set a URI permits: it is the account name
// charset, so anything unexpected is refused rather than reasoned about.
func uriSafeLabel(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.':
		default:
			return false
		}
	}
	return true
}

// totpIssuer is what an authenticator shows next to the code.
const totpIssuer = "Stowcloud"
