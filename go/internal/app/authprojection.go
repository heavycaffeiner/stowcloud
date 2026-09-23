//go:build linux

package app

import (
	"context"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
)

// authSessionDetails projects deployment-specific capabilities without handling HTTP.
func (e *Engine) authSessionDetails(ctx context.Context, id int64) (handler.SessionDetails, error) {
	row, err := e.Auth.UserByID(ctx, id)
	if err != nil {
		return handler.SessionDetails{}, err
	}
	smb, err := e.Auth.SMBStateOf(ctx, id)
	if err != nil {
		return handler.SessionDetails{}, err
	}
	var oidc handler.SessionOidcView
	switch link, linkErr := e.Auth.OIDCLinkOf(ctx, id); {
	case linkErr == nil:
		oidc = handler.SessionOidcOf(link)
	case !errors.Is(linkErr, auth.ErrNoOIDCLink):
		return handler.SessionDetails{}, linkErr
	}
	details := handler.SessionDetails{
		TOTPEnabled:   row.TOTPEnabled,
		SMBOptOut:     smb.OptOut,
		SMBEnabled:    smb.Enabled,
		SMBCredential: string(smb.Credential),
		Oidc:          oidc,
		Roots:         e.rootViews(core.UserID(id)),
		Limits:        e.limitsView(),
		Features:      e.featuresView(),
	}
	if smb.Credential == auth.SMBCredentialNone {
		details.SMBUnavailableReason = string(smb.Reason)
	}
	return details, nil
}
