//go:build linux

// Account-owned SMB credential and access routes.
package smbaccount

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/humabridge"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
)

// Deps supplies the account service used by SMB routes.
type Deps struct {
	Auth *auth.Service
}

type handlers struct{ d Deps }

type accessRequest struct {
	Current string `json:"current"`
	OptOut  bool   `json:"opt_out"`
	Enabled bool   `json:"enabled"`
}

type passwordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

type reconfirmRequest struct {
	Current string `json:"current"`
}

type accessInput struct{ Body accessRequest }
type passwordInput struct{ Body passwordRequest }
type reconfirmInput struct{ Body reconfirmRequest }
type stateOutput struct{ Body handler.SMBStateView }
type clearedOutput struct{ Body handler.SMBClearedView }

// Register mounts typed account-owned SMB operations below the API prefix.
func Register(api huma.API, d Deps) {
	h := &handlers{d: d}
	huma.Register[accessInput, stateOutput](api, huma.Operation{
		OperationID: "account.smb.create", Method: http.MethodPost, Path: "/account/smb",
	}, h.accessHuma)
	huma.Register[passwordInput, stateOutput](api, huma.Operation{
		OperationID: "account.smb.password.set", Method: http.MethodPost, Path: "/account/smb/password",
	}, h.passwordSetHuma)
	huma.Register[reconfirmInput, clearedOutput](api, huma.Operation{
		OperationID: "account.smb.password.delete", Method: http.MethodDelete, Path: "/account/smb/password",
	}, h.passwordDeleteHuma)
}

func (h *handlers) humaOwner(ctx context.Context) (int64, error) {
	c := humabridge.Gin(ctx)
	v, ok := c.Get(string(middleware.KeyCredential))
	p, okp := v.(middleware.Principal)
	if !ok || !okp || p.UserID == 0 {
		return 0, humabridge.Refusal(apierr.Classified{Class: apierr.AuthRequired})
	}
	return p.UserID, nil
}

func (h *handlers) reconfirmHuma(ctx context.Context, owner int64, password string) error {
	if password == "" {
		return humabridge.Refusal(apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}
	ok, err := h.d.Auth.VerifyAccountPassword(ctx, owner, secret.New([]byte(password)))
	if err != nil {
		return humabridge.Failure(ctx, err)
	}
	if !ok {
		return humabridge.Refusal(apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}
	return nil
}

func (h *handlers) accessHuma(ctx context.Context, in *accessInput) (*stateOutput, error) {
	owner, err := h.humaOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.reconfirmHuma(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	if err := h.d.Auth.SetSMBAccess(ctx, owner, in.Body.OptOut, in.Body.Enabled); err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	state, err := h.d.Auth.SMBStateOf(ctx, owner)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &stateOutput{Body: handler.SMBStateOf(state)}, nil
}

func (h *handlers) passwordSetHuma(ctx context.Context, in *passwordInput) (*stateOutput, error) {
	owner, err := h.humaOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.reconfirmHuma(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	if err := h.d.Auth.SetSMBPassword(ctx, owner, secret.New([]byte(in.Body.New))); err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	state, err := h.d.Auth.SMBStateOf(ctx, owner)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &stateOutput{Body: handler.SMBStateOf(state)}, nil
}

func (h *handlers) passwordDeleteHuma(ctx context.Context, in *reconfirmInput) (*clearedOutput, error) {
	owner, err := h.humaOwner(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.reconfirmHuma(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	revertible, err := h.d.Auth.ClearSMBPassword(ctx, owner)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	state, err := h.d.Auth.SMBStateOf(ctx, owner)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &clearedOutput{Body: handler.SMBClearedOf(state, revertible)}, nil
}
