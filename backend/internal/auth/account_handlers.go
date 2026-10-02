//go:build linux

package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

type AccountHandlersDeps struct {
	Service *Service
	Clock   clock.Clock
}

// NewAccountHandlers builds the caller-scoped account and credential routes.
func NewAccountHandlers(d AccountHandlersDeps) *AccountHandlers {
	return &AccountHandlers{d: d}
}

type AccountHandlers struct{ d AccountHandlersDeps }

type passwordRequestTransport struct {
	Current string `json:"current"`
	New     string `json:"new"`
}
type reconfirmRequestTransport struct {
	Current string `json:"current"`
}
type enrollRequestTransport struct {
	Current string `json:"current"`
	Secret  string `json:"secret"`
	Code    string `json:"code"`
}
type appPasswordRequestTransport struct {
	Current       string                     `json:"current"`
	Name          string                     `json:"name"`
	Scope         *appPasswordScopeTransport `json:"scope,omitempty"`
	ExpiresInDays int                        `json:"expires_in_days"`
}
type appPasswordScopeTransport struct {
	Perms  []string `json:"perms,omitempty"`
	Shares []string `json:"shares,omitempty"`
}

// noContent is an operation that answers 204 with no body.
type noContent struct{}

type passwordInput struct{ Body passwordRequestTransport }
type reconfirmInput struct{ Body reconfirmRequestTransport }
type enrollInput struct{ Body enrollRequestTransport }
type appPasswordCreateInput struct{ Body appPasswordRequestTransport }

// sessionCookieInput carries the caller's own session cookie, which marks the
// current row in the session list.
type sessionCookieInput struct {
	Session string `cookie:"__Host-sc_sid"`
}

type sessionIDInput struct {
	ID string `path:"id"`
}

type sessionsOutput struct{ Body []SessionView }
type appPasswordsOutput struct{ Body []AppPasswordView }

type appPasswordCreatedView struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}
type appPasswordCreatedOutput struct {
	Body   appPasswordCreatedView
	Status int
}

type tOTPSetupOutput struct{ Body TOTPSetupView }

type recoveryCodesView struct {
	RecoveryCodes []string `json:"recovery_codes"`
}
type recoveryCodesOutput struct{ Body recoveryCodesView }

type recoveryRemainingView struct {
	Remaining int `json:"remaining"`
}
type recoveryRemainingOutput struct{ Body recoveryRemainingView }

func (h *AccountHandlers) Password(ctx context.Context, in *passwordInput) (*noContent, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.reconfirm(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	if err := h.d.Service.SetPassword(ctx, owner, secret.New([]byte(in.Body.New))); err != nil {
		return nil, err
	}
	return &noContent{}, nil
}

func (h *AccountHandlers) Sessions(ctx context.Context, in *sessionCookieInput) (*sessionsOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := h.d.Service.Sessions(ctx, owner)
	if err != nil {
		return nil, err
	}
	return &sessionsOutput{Body: SessionsOf(rows, sessionDigest(in.Session))}, nil
}

func (h *AccountHandlers) SessionDelete(ctx context.Context, in *sessionIDInput) (*noContent, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	if in.ID == "" {
		return nil, errNotFound()
	}
	rows, err := h.d.Service.Sessions(ctx, owner)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if subtle.ConstantTimeCompare([]byte(SessionHandle(row.IDHash)), []byte(in.ID)) != 1 {
			continue
		}
		if err := h.d.Service.RevokeSessionByHash(ctx, owner, row.IDHash); err != nil {
			return nil, err
		}
		return &noContent{}, nil
	}
	return nil, errNotFound()
}

func (h *AccountHandlers) AppPasswords(ctx context.Context, _ *struct{}) (*appPasswordsOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := h.d.Service.AppPasswords(ctx, owner)
	if err != nil {
		return nil, err
	}
	return &appPasswordsOutput{Body: AppPasswordsOf(rows)}, nil
}

func (h *AccountHandlers) AppPasswordDelete(ctx context.Context, in *sessionIDInput) (*noContent, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := positiveID(in.ID)
	if !ok {
		return nil, errNotFound()
	}
	if err := h.d.Service.RevokeAppPassword(ctx, owner, id); err != nil {
		return nil, err
	}
	return &noContent{}, nil
}

func (h *AccountHandlers) AppPasswordWipe(ctx context.Context, in *sessionIDInput) (*noContent, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := positiveID(in.ID)
	if !ok {
		return nil, errNotFound()
	}
	if err := h.d.Service.RequestWipe(ctx, owner, id); err != nil {
		return nil, err
	}
	return &noContent{}, nil
}

func (h *AccountHandlers) AppPasswordCreate(ctx context.Context, in *appPasswordCreateInput) (*appPasswordCreatedOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	req := in.Body
	if req.Name == "" || req.ExpiresInDays < 0 || req.ExpiresInDays > 36500 {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	scope, ok := accountScope(req.Scope)
	if !ok {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	if err = h.reconfirm(ctx, owner, req.Current); err != nil {
		return nil, err
	}
	token, err := h.d.Service.CreateAppPassword(ctx, owner, req.Name, scope, time.Duration(req.ExpiresInDays)*24*time.Hour)
	if err != nil {
		return nil, err
	}
	return &appPasswordCreatedOutput{Body: appPasswordCreatedView{Name: req.Name, Token: token}, Status: http.StatusCreated}, nil
}

func (h *AccountHandlers) TOTPSetup(ctx context.Context, in *reconfirmInput) (*tOTPSetupOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.reconfirm(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	secretB32, err := h.d.Service.GenerateTOTPSecret()
	if err != nil {
		return nil, err
	}
	info, err := h.d.Service.AccountInfo(ctx, owner)
	if err != nil {
		return nil, err
	}
	return &tOTPSetupOutput{Body: TOTPSetupOf(secretB32, info.LoginName)}, nil
}

func (h *AccountHandlers) TOTPEnroll(ctx context.Context, in *enrollInput) (*recoveryCodesOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	req := in.Body
	if req.Secret == "" || req.Code == "" {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	if err = h.reconfirm(ctx, owner, req.Current); err != nil {
		return nil, err
	}
	accepted, err := h.d.Service.VerifyCandidateTOTP(req.Secret, req.Code, h.d.Clock.Nanos())
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, ErrCredentials
	}
	if err = h.d.Service.EnrollTOTP(ctx, owner, req.Secret); err != nil {
		return nil, err
	}
	if _, err = h.d.Service.VerifyTOTP(ctx, owner, req.Code, h.d.Clock.Nanos()); err != nil {
		return nil, err
	}
	codes, err := h.d.Service.GenerateRecoveryCodes(ctx, owner, 10)
	if err != nil {
		return nil, err
	}
	return &recoveryCodesOutput{Body: recoveryCodesView{RecoveryCodes: codes}}, nil
}

func (h *AccountHandlers) TOTPDisable(ctx context.Context, in *reconfirmInput) (*noContent, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.reconfirm(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	if err := h.d.Service.DisableTOTPWithPassword(ctx, owner, secret.New([]byte(in.Body.Current))); err != nil {
		return nil, err
	}
	return &noContent{}, nil
}

func (h *AccountHandlers) RecoveryList(ctx context.Context, _ *struct{}) (*recoveryRemainingOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	remaining, err := h.d.Service.RecoveryCodesRemaining(ctx, owner)
	if err != nil {
		return nil, err
	}
	return &recoveryRemainingOutput{Body: recoveryRemainingView{Remaining: remaining}}, nil
}

func (h *AccountHandlers) RecoveryCreate(ctx context.Context, in *reconfirmInput) (*recoveryCodesOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err = h.reconfirm(ctx, owner, in.Body.Current); err != nil {
		return nil, err
	}
	codes, err := h.d.Service.GenerateRecoveryCodes(ctx, owner, 10)
	if err != nil {
		return nil, err
	}
	return &recoveryCodesOutput{Body: recoveryCodesView{RecoveryCodes: codes}}, nil
}

func (h *AccountHandlers) reconfirm(ctx context.Context, owner int64, password string) error {
	return Reconfirm(ctx, h.d.Service, owner, password)
}

func accountScope(req *appPasswordScopeTransport) (Scope, bool) {
	if req == nil {
		return Scope{Perms: SyncScopePerms}, true
	}
	scope := Scope{Perms: SyncScopePerms, Shares: append([]string(nil), req.Shares...)}
	if len(req.Perms) > 0 {
		var bits acl.Perms
		for _, name := range req.Perms {
			bit, ok := acl.PermByName(name)
			if !ok {
				return Scope{}, false
			}
			bits |= bit
		}
		if bits == 0 {
			return Scope{}, false
		}
		scope.Perms = uint16(bits)
	}
	return scope, true
}

// sessionDigest is the stored hash of a session cookie value, or nil when the
// value is absent or not hex.
func sessionDigest(cookie string) []byte {
	if cookie == "" {
		return nil
	}
	raw, err := hex.DecodeString(cookie)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(raw)
	return sum[:]
}

func positiveID(raw string) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n > 0
}

func errNotFound() error { return apierr.AsClassified(apierr.NotFound, "") }
