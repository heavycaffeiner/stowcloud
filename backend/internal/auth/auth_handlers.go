//go:build linux

package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

const (
	passwordAMR         int64 = 1
	passwordFactorAMR   int64 = 2
	providerAMR         int64 = 4
	sessionCookieMaxAge       = 30 * 24 * time.Hour
)

// SessionDetails contains the deployment-specific portions of /auth/session.
// The callback is a projection seam: it carries no Gin or request handling.
type SessionDetails struct {
	TOTPEnabled          bool
	SMBOptOut            bool
	SMBEnabled           bool
	SMBCredential        string
	SMBUnavailableReason string
	Oidc                 SessionOidcView
	Roots                []RootView
	Limits               LimitsView
	Features             FeaturesView
}

type AuthHandlersDeps struct {
	Service           *Service
	Clock             clock.Clock
	CSRFKey           func() []byte
	TOTPAllow         interface{ Allow(string) bool }
	SessionDetails    func(context.Context, int64) (SessionDetails, error)
	OIDCEndSessionURL func(context.Context) (string, bool)
}

// NewAuthHandlers builds the native authentication handlers.
func NewAuthHandlers(d AuthHandlersDeps) *AuthHandlers {
	return &AuthHandlers{d: d}
}

type AuthHandlers struct{ d AuthHandlersDeps }

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type totpRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

// A sign-in body must declare JSON. A cross-site form cannot, so the check
// keeps a page on another origin from signing a browser in.
type loginInput struct {
	ContentType string `header:"Content-Type"`
	UserAgent   string `header:"User-Agent"`
	Body        loginRequest
}

type loginTOTPInput struct {
	ContentType string `header:"Content-Type"`
	UserAgent   string `header:"User-Agent"`
	Body        totpRequest
}

// loginResult is the signed-in identity, or the challenge when a second factor
// is still owed. Exactly one is set.
type loginResult struct {
	*IdentityView
	*ChallengeView
}

type loginOutput struct {
	SetCookie string `header:"Set-Cookie"`
	Body      loginResult
}

type identityOutput struct {
	SetCookie string `header:"Set-Cookie"`
	Body      IdentityView
}

type whoAmIOutput struct{ Body WhoAmIView }

// logoutOutput carries a body only when the provider session has to be ended
// too; otherwise it answers 204.
type logoutOutput struct {
	SetCookie string `header:"Set-Cookie"`
	Status    int
	Body      *LogoutView
}

func (h *AuthHandlers) Login(ctx context.Context, in *loginInput) (*loginOutput, error) {
	if !declaresJSON(in.ContentType) {
		return nil, apierr.AsClassified(apierr.Malformed, "")
	}
	req := in.Body
	if req.Login == "" || req.Password == "" {
		return nil, ErrCredentials
	}
	sess, err := h.d.Service.Login(ctx, LoginRequest{
		Name: req.Login, Password: secret.New([]byte(req.Password)),
		IP: middleware.ClientFrom(ctx).String(), UA: in.UserAgent, AMR: passwordAMR,
	}, 0)
	if errors.Is(err, ErrSecondFactor) {
		challenge, cerr := h.challenge(ctx, req.Login)
		if cerr != nil {
			return nil, cerr
		}
		return &loginOutput{Body: loginResult{ChallengeView: &challenge}}, nil
	}
	if err != nil {
		return nil, err
	}
	identity, cookie, err := h.grantSession(ctx, sess)
	if err != nil {
		return nil, err
	}
	return &loginOutput{SetCookie: cookie, Body: loginResult{IdentityView: &identity}}, nil
}

func (h *AuthHandlers) challenge(ctx context.Context, login string) (ChallengeView, error) {
	uid, err := h.d.Service.UserIDByName(ctx, login)
	if err != nil {
		return ChallengeView{}, err
	}
	challenge, err := MintChallenge(h.d.CSRFKey(), uid, h.d.Clock.Now().Unix())
	if err != nil {
		return ChallengeView{}, err
	}
	return ChallengeView{Required: "totp", Challenge: challenge, ExpiresInSeconds: ChallengeTTL}, nil
}

func (h *AuthHandlers) LoginTOTP(ctx context.Context, in *loginTOTPInput) (*identityOutput, error) {
	if !declaresJSON(in.ContentType) {
		return nil, apierr.AsClassified(apierr.Malformed, "")
	}
	req := in.Body
	uid, err := OpenChallenge(h.d.CSRFKey(), req.Challenge, h.d.Clock.Now().Unix())
	if err != nil || req.Code == "" {
		return nil, ErrCredentials
	}
	client := middleware.ClientFrom(ctx).String()
	if h.d.TOTPAllow != nil && !h.d.TOTPAllow.Allow(client+"/"+strconv.FormatInt(uid, 10)) {
		return nil, apierr.AsClassified(apierr.RateLimited, "auth.rate_limited")
	}
	accepted, err := h.acceptFactor(ctx, uid, req.Code)
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, ErrCredentials
	}
	sess, err := h.d.Service.CreateSession(ctx, uid, client, in.UserAgent, passwordFactorAMR, 0)
	if err != nil {
		return nil, err
	}
	identity, cookie, err := h.grantSession(ctx, sess)
	if err != nil {
		return nil, err
	}
	return &identityOutput{SetCookie: cookie, Body: identity}, nil
}

func (h *AuthHandlers) acceptFactor(ctx context.Context, uid int64, code string) (bool, error) {
	ok, err := h.d.Service.VerifyTOTP(ctx, uid, code, h.d.Clock.Nanos())
	if err != nil || ok {
		return ok, err
	}
	return h.d.Service.UseRecoveryCode(ctx, uid, code)
}

// grantSession projects a new session and the cookie that carries it.
func (h *AuthHandlers) grantSession(ctx context.Context, sess Session) (IdentityView, string, error) {
	info, err := h.d.Service.AccountInfo(ctx, sess.UserID)
	if err != nil {
		return IdentityView{}, "", err
	}
	admin, err := h.d.Service.IsAdmin(ctx, sess.UserID)
	if err != nil {
		return IdentityView{}, "", err
	}
	printable := hex.EncodeToString(sess.Token.Reveal())
	view := IdentityViewOf(sess.UserID, info.LoginName, info.DisplayName, admin, middleware.CSRFToken(h.d.CSRFKey(), printable))
	return view, SessionCookie(printable).String(), nil
}

func (h *AuthHandlers) Session(ctx context.Context, in *sessionCookieInput) (*whoAmIOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	info, err := h.d.Service.AccountInfo(ctx, owner)
	if err != nil {
		return nil, err
	}
	admin, err := h.d.Service.IsAdmin(ctx, owner)
	if err != nil {
		return nil, err
	}
	var csrf string
	if in.Session != "" {
		csrf = middleware.CSRFToken(h.d.CSRFKey(), in.Session)
	}
	details, err := h.d.SessionDetails(ctx, owner)
	if err != nil {
		return nil, err
	}
	view := WhoAmIView{IdentityView: IdentityViewOf(owner, info.LoginName, info.DisplayName, admin, csrf), TOTPEnabled: details.TOTPEnabled, SMBOptOut: details.SMBOptOut, SMBEnabled: details.SMBEnabled, SMBCredential: details.SMBCredential, SMBUnavailableReason: details.SMBUnavailableReason, Oidc: details.Oidc, Roots: details.Roots, Limits: details.Limits, Features: details.Features}
	return &whoAmIOutput{Body: view}, nil
}

func (h *AuthHandlers) Logout(ctx context.Context, in *sessionCookieInput) (*logoutOutput, error) {
	if in.Session == "" {
		return &logoutOutput{Status: http.StatusNoContent}, nil
	}
	cleared := ExpiredSessionCookie().String()
	raw, err := hex.DecodeString(in.Session)
	if err != nil {
		return &logoutOutput{SetCookie: cleared, Status: http.StatusNoContent}, nil
	}
	token := secret.New(raw)
	provider := h.d.Service.SessionAMR(ctx, token) == providerAMR
	if err := h.d.Service.RevokeSession(ctx, token); err != nil && !errors.Is(err, ErrCredentials) {
		return nil, err
	}
	if provider && h.d.OIDCEndSessionURL != nil {
		if end, ok := h.d.OIDCEndSessionURL(ctx); ok {
			return &logoutOutput{SetCookie: cleared, Status: http.StatusOK, Body: &LogoutView{EndSessionURL: end}}, nil
		}
	}
	return &logoutOutput{SetCookie: cleared, Status: http.StatusNoContent}, nil
}

func declaresJSON(contentType string) bool {
	media, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(media, "application/json")
}

// SessionCookie carries a printable session token.
func SessionCookie(value string) *http.Cookie {
	return &http.Cookie{
		Name: middleware.SessionCookieName, Value: value, Path: "/",
		MaxAge: int(sessionCookieMaxAge / time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}

// ExpiredSessionCookie tells the browser to drop the session cookie.
func ExpiredSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name: middleware.SessionCookieName, Path: "/",
		MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}

// Reconfirm checks the account password for a sensitive settings operation.
func Reconfirm(ctx context.Context, service *Service, owner int64, password string) error {
	if password == "" {
		return ErrCredentials
	}
	ok, err := service.VerifyAccountPassword(ctx, owner, secret.New([]byte(password)))
	if err != nil {
		return err
	}
	if !ok {
		return ErrCredentials
	}
	return nil
}
