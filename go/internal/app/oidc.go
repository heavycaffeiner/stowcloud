//go:build linux

// Single sign-on provider construction. HTTP protocol handling lives in
// transport/http/oidc; app keeps only lifecycle configuration here.
package app

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/oidc"
	secret "github.com/heavycaffeiner/stowcloud/go/internal/platform/security/secret"
)

// buildOIDCClient constructs the provider client from the stored settings.
// Nil is off, and incomplete configuration leaves sign-on off rather than
// preventing the deployment from starting.
func (e *Engine) buildOIDCClient(ctx context.Context, cfg *oidcSettings) *oidc.Client {
	if cfg == nil {
		return nil
	}

	plain, ok, err := e.Settings.ConfigSecret(ctx, "oidc_client_secret")
	if err != nil {
		e.log().Error("the single sign-on secret could not be opened; sign-on stays off", "error", err)
		return nil
	}
	if !ok && !cfg.PublicClient {
		e.log().Error("single sign-on is configured with no client secret and is not a public client; it stays off")
		return nil
	}

	client, err := oidc.New(oidc.Config{
		Issuer:                cfg.Issuer,
		ClientID:              cfg.ClientID,
		ClientSecret:          secret.New([]byte(plain)),
		Scopes:                cfg.Scopes,
		AllowPrivateEndpoints: cfg.AllowPrivateEndpoints,
		CACertFile:            cfg.CACertFile,
		PublicClient:          cfg.PublicClient,
	}, e.clock)
	if err != nil {
		e.log().Error("the single sign-on client would not build; it stays off", "error", err)
		return nil
	}
	return client
}

// oidcSettings is the provider configuration this package reads.
type oidcSettings struct {
	Issuer                string
	ClientID              string
	Scopes                []string
	AllowPrivateEndpoints bool
	CACertFile            string
	PublicClient          bool
}
