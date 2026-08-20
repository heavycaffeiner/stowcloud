package oidc

import (
	"context"
	"errors"
)

// Link-only. An identity from the provider attaches to an account that already
// exists here. It cannot create one and it cannot raise one's privileges.
//
// This is a position rather than a scope decision. A provider that can create
// accounts is a provider that can grant access to this server, and that is not
// the trust an operator thinks they are extending when they configure single
// sign-on. Keeping account creation local is what makes revocation here total.

// ErrNotLinked is an identity that authenticated at the provider and has no
// account here.
var ErrNotLinked = errors.New("oidc: the identity is not linked to any account")

// LinkStore is the persistence this needs. It is an interface because the row
// belongs to the account layer, which owns every decision about which local
// account a verified subject may become.
type LinkStore interface {
	// UserForIdentity returns the account an identity is linked to. The
	// issuer is part of the key: a subject is only unique within the issuer
	// that minted it, so two providers can both mint the same one.
	UserForIdentity(ctx context.Context, issuer, subject string) (int64, bool, error)
}

// ResolveIdentity turns verified claims into the local account they belong to.
//
// An identity with no account is a refusal that tells the person to link from
// inside their account, not an account quietly created for them.
func ResolveIdentity(ctx context.Context, store LinkStore, claims *Claims) (int64, error) {
	if store == nil {
		return 0, ErrNotLinked
	}
	if claims.Issuer == "" || claims.Subject == "" {
		return 0, ErrNotLinked
	}
	userID, ok, err := store.UserForIdentity(ctx, claims.Issuer, claims.Subject)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNotLinked
	}
	return userID, nil
}
