//go:build linux && compat_nc

package nc

import "net/http"

// Mount is one route this layer answers.
//
// The layer describes its routes rather than registering them, because the
// route table lives in the server and a layer that registered its own would be
// a second place routes come from.
type Mount struct {
	Method  string
	Pattern string
	Handler http.Handler
}

// Principal is who a compat request is from.
//
// It carries the credential id because one endpoint revokes the credential
// that made the request, and a session-authenticated call has none to revoke.
type Principal struct {
	User UserIDValue
	// CredentialID names the app password this request authenticated with,
	// and is zero for a browser session.
	CredentialID int64
}

// UserIDValue is the account id, aliased so this file does not import the seam
// for one name.
type UserIDValue = uint32

// Authenticator resolves a request to a principal.
//
// It is supplied rather than implemented here: authentication is the server's,
// and a compat mount with its own copy is how "who is this request from" stops
// having one answer.
type Authenticator func(r *http.Request) (Principal, bool)
