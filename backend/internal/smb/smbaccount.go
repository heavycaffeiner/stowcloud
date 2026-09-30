//go:build linux

// Account-owned SMB credential and access routes.
package smb

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
)

// Deps supplies the account service used by SMB routes.
type AccountHandler struct {
	Auth *auth.Service
}

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
type stateOutput struct{ Body SMBStateView }
type clearedOutput struct{ Body SMBClearedView }

func accountOf(ctx context.Context) (int64, error) {
	owner, err := files.OwnerFrom(ctx)
	return int64(owner), err
}

func (h *AccountHandler) reconfirm(ctx context.Context, owner int64, password string) error {
	if password == "" {
		return auth.ErrCredentials
	}
	ok, err := h.Auth.VerifyAccountPassword(ctx, owner, secret.New([]byte(password)))
	if err != nil {
		return err
	}
	if !ok {
		return auth.ErrCredentials
	}
	return nil
}

// Access turns the caller's SMB access on or off.
func (h *AccountHandler) Access(ctx context.Context, in *accessInput) (*stateOutput, error) {
	owner, err := accountOf(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.reconfirm(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	if err = h.Auth.SetSMBAccess(ctx, owner, in.Body.OptOut, in.Body.Enabled); err != nil {
		return nil, err
	}
	state, err := h.Auth.SMBStateOf(ctx, owner)
	if err != nil {
		return nil, err
	}
	return &stateOutput{Body: SMBStateOf(state)}, nil
}

// SetPassword sets the caller's SMB password.
func (h *AccountHandler) SetPassword(ctx context.Context, in *passwordInput) (*stateOutput, error) {
	owner, err := accountOf(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.reconfirm(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	if err = h.Auth.SetSMBPassword(ctx, owner, secret.New([]byte(in.Body.New))); err != nil {
		return nil, err
	}
	state, err := h.Auth.SMBStateOf(ctx, owner)
	if err != nil {
		return nil, err
	}
	return &stateOutput{Body: SMBStateOf(state)}, nil
}

// DeletePassword clears the caller's SMB password.
func (h *AccountHandler) DeletePassword(ctx context.Context, in *reconfirmInput) (*clearedOutput, error) {
	owner, err := accountOf(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.reconfirm(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	revertible, err := h.Auth.ClearSMBPassword(ctx, owner)
	if err != nil {
		return nil, err
	}
	state, err := h.Auth.SMBStateOf(ctx, owner)
	if err != nil {
		return nil, err
	}
	return &clearedOutput{Body: SMBClearedOf(state, revertible)}, nil
}
