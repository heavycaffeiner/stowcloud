//go:build linux

// What a sign-in answers.
package handler

import "strconv"

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
