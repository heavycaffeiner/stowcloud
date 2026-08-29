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

	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// ErrCredentialRequired is a route that needs a credential the request did not
// present.
var ErrCredentialRequired = errors.New("this route requires a credential")

// ErrSessionRequired is a route that accepts only a browser session.
var ErrSessionRequired = errors.New("this route requires a session")

// ErrInsufficientPermission is an app password missing a bit the route needs.
var ErrInsufficientPermission = errors.New("this credential does not carry the required permission")

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
}

// Scope reports whether this principal may reach a route with this
// requirement.
//
// The zero Access is not handled permissively: route.Validate refuses a route
// that declares none, and if one reached here it is refused rather than
// defaulting into the class that lets anyone in.
func Scope(req route.Requirement, p Principal) error {
	switch req.Access {
	case route.AccessPublic:
		return nil

	case route.AccessSession:
		// Admin and self-service. An app password must not satisfy these: a
		// credential handed to a device would otherwise be able to change the
		// password that revokes it.
		if p.Kind != CredentialSessionCookie {
			if p.Kind == CredentialNone {
				return ErrCredentialRequired
			}
			return ErrSessionRequired
		}
		return nil

	case route.AccessAnyCredential:
		if p.Kind == CredentialNone {
			return ErrCredentialRequired
		}
		return nil

	case route.AccessPerms:
		if p.Kind == CredentialNone {
			return ErrCredentialRequired
		}
		// Every declared bit, not any of them. A route naming read and write
		// needs both, since it does both.
		if !p.Mask.Has(req.Perms) {
			return ErrInsufficientPermission
		}
		return nil

	case route.AccessUnset:
		return ErrCredentialRequired

	default:
		return ErrCredentialRequired
	}
}

// SessionMask is the permission mask a session carries.
//
// Every bit. A session is the account acting directly, so narrowing it here
// would be this layer inventing a restriction the account model does not have.
// What the account may actually reach at a given path is still core.Resolve's
// answer; this only says the credential class itself withholds nothing.
//
// Built from the named list rather than written as a literal, so a bit added
// to the model is carried here without an edit that could be forgotten.
func SessionMask() acl.Perms {
	var all acl.Perms
	for _, np := range acl.NamedPerms() {
		all |= np.Perm
	}
	return all
}
