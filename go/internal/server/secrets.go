// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// SecretOIDCClient is the name the single-sign-on client secret is stored
// under. One constant, because the write and the read are in different
// packages and a typo between them is single sign-on that silently stops
// working.
const SecretOIDCClient = "oidc_client_secret" //nolint:gosec // G101 reads the identifier: a row name, not a credential.

// OpenOIDCSecret reads the stored client secret and unseals it.
//
// An absent row is the empty string with no error: a provider that needs no
// secret is an ordinary public client, and a deployment with no provider at
// all has nothing to read.
//
// A row that will not open is an error the caller logs and continues from. The
// alternative is refusing to start over a credential for one sign-in method,
// which locks out every other one as well.
func OpenOIDCSecret(ctx context.Context, st *state.DB, svc *auth.Service) (string, error) {
	if st == nil || svc == nil {
		return "", nil
	}
	row, ok, err := st.ReadConfigSecret(ctx, SecretOIDCClient)
	if err != nil || !ok {
		return "", err
	}
	plain, oerr := svc.OpenConfigSecret(SecretOIDCClient, row.Value, row.KeyVer)
	if oerr != nil {
		return "", oerr
	}
	return string(plain), nil
}

// StoreOIDCSecret seals a client secret and writes it. An empty one removes
// the row, which is what clearing the field means.
func StoreOIDCSecret(ctx context.Context, st *state.DB, svc *auth.Service, plain string) error {
	if plain == "" {
		return st.DeleteConfigSecret(ctx, SecretOIDCClient)
	}
	sealed, ver, err := svc.SealConfigSecret(SecretOIDCClient, []byte(plain))
	if err != nil {
		return err
	}
	return st.WriteConfigSecret(ctx, SecretOIDCClient, state.ConfigSecret{Value: sealed, KeyVer: ver})
}
