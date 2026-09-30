// Linux only, for the same reason as the rest of this package.
//go:build linux

// Route scope: whether a credential class may reach a route at all, and
// whether an app password carries the bits the route declares.
//
// It never resolves a path. Path-specific permission is core.Resolve's answer,
// and evaluating it here as well would create two authorities that can drift
// apart, with the more permissive one deciding.
package middleware

import (
	"errors"

	"github.com/heavycaffeiner/stowcloud/backend/internal/http/route"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// ErrCredentialRequired is a route that needs a credential the request did not
// present.
var ErrCredentialRequired = errors.New("this route requires a credential")

// ErrSessionRequired is a route that accepts only a browser session.
var ErrSessionRequired = errors.New("this route requires a session")

// Principal is what Auth resolved, as scope needs it.
type Principal struct {
	// UserID is the account the credential proved. Zero is an anonymous
	// request, which a public route serves and the audit record notes as such.
	UserID int64
	// Kind is which credential proved it.
	Kind CredentialKind
	// Mask is the app password's permission mask. A session has every bit,
	// because a session is the account itself rather than a delegation of it.
	Mask acl.Perms
	// Shares is the app password's allowed root share labels. Empty means every share.
	Shares []string
	// AppPasswordID identifies the verified app password. Sessions leave it
	// zero because no app credential issued the request.
	AppPasswordID int64
}

// Scope reports whether this principal may reach a route with this
// requirement.
//
// The zero Access is not handled permissively: route.Validate refuses a route
// that declares none, and if one reached here it is refused rather than
// defaulting into the class that lets anyone in.
//
// Only the browser session reaches a route that is not public. This runs for
// the native API alone, because that is the only surface whose routes carry a
// requirement: the compatibility mount and the file protocol resolve their
// own callers. A device credential belongs to those surfaces, and the native
// API is the interface's own, not a second public one with the same powers.
func Scope(req route.Requirement, p Principal) error {
	if req.Access == route.AccessPublic {
		return nil
	}
	if p.Kind == CredentialNone {
		return ErrCredentialRequired
	}
	if p.Kind != CredentialSessionCookie {
		return ErrSessionRequired
	}

	if req.Access == route.AccessSession {
		return nil
	}
	return ErrCredentialRequired
}
