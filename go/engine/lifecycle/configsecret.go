//go:build linux

// Configuration values that are credentials.
//
// A settings document is read by anybody who may read the settings, so a
// value that authenticates this server to somebody else does not belong in
// it. These are stripped from the document on the way in, sealed under the
// master key and stored in their own row; what the settings screen is ever
// told is whether one exists.
package lifecycle

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// secretOIDCClient names the single sign-on client secret's row.
//
//nolint:gosec // G101 reads the identifier; this is a row name, not a credential.
const secretOIDCClient = "oidc_client_secret"

// StoreConfigSecret seals a credential and stores it under name.
//
// An empty value deletes rather than storing an empty string: clearing a
// secret and setting it to nothing are the same intent, and a stored empty
// string would be a credential the client presents and the provider rejects.
func (e *Engine) StoreConfigSecret(ctx context.Context, name, plain string) error {
	if plain == "" {
		return e.State.DeleteConfigSecret(ctx, name)
	}
	sealed, ver, err := e.Auth.SealConfigSecret(name, []byte(plain))
	if err != nil {
		return err
	}
	return e.State.WriteConfigSecret(ctx, name, state.ConfigSecret{Value: sealed, KeyVer: ver})
}

// ConfigSecret opens a stored credential, or reports that none is stored.
//
// The plaintext exists only inside the caller's frame. It is returned rather
// than logged, cached or placed on a struct, because the point of sealing it
// is that a memory it never reaches cannot leak it.
func (e *Engine) ConfigSecret(ctx context.Context, name string) (string, bool, error) {
	row, ok, err := e.State.ReadConfigSecret(ctx, name)
	if err != nil || !ok {
		return "", false, err
	}
	plain, err := e.Auth.OpenConfigSecret(name, row.Value, row.KeyVer)
	if err != nil {
		return "", false, err
	}
	return string(plain), true, nil
}

// HasConfigSecret reports whether one is stored, without opening it.
//
// This is what a settings screen asks: whether the field is set, never what
// it holds. Opening it to answer would decrypt a credential to render a
// checkbox.
func (e *Engine) HasConfigSecret(ctx context.Context, name string) bool {
	_, ok, err := e.State.ReadConfigSecret(ctx, name)
	if err != nil {
		e.logger.Warn("could not read a configuration secret", "name", name, "error", err)
		return false
	}
	return ok
}
