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

	"github.com/gin-gonic/gin"

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
	OIDCEndSessionURL func(*gin.Context) (string, bool)
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

func (h *AuthHandlers) Login(c *gin.Context) {
	var req loginRequest
	if err := decodeAuthBody(c, &req); err != nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if req.Login == "" || req.Password == "" {
		middleware.Fail(c, ErrCredentials)
		return
	}
	sess, err := h.d.Service.Login(c.Request.Context(), LoginRequest{
		Name: req.Login, Password: secret.New([]byte(req.Password)),
		IP: middleware.ClientOf(c).String(), UA: c.Request.UserAgent(), AMR: passwordAMR,
	}, 0)
	if errors.Is(err, ErrSecondFactor) {
		h.askForFactor(c, req.Login)
		return
	}
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	h.grantSession(c, sess)
}

func (h *AuthHandlers) askForFactor(c *gin.Context, login string) {
	uid, err := h.d.Service.UserIDByName(c.Request.Context(), login)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	challenge, err := MintChallenge(h.d.CSRFKey(), uid, h.d.Clock.Now().Unix())
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, ChallengeView{Required: "totp", Challenge: challenge, ExpiresInSeconds: ChallengeTTL})
}

func (h *AuthHandlers) LoginTOTP(c *gin.Context) {
	var req totpRequest
	if err := decodeAuthBody(c, &req); err != nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	uid, err := OpenChallenge(h.d.CSRFKey(), req.Challenge, h.d.Clock.Now().Unix())
	if err != nil || req.Code == "" {
		middleware.Fail(c, ErrCredentials)
		return
	}
	key := middleware.ClientOf(c).String()
	if h.d.TOTPAllow != nil && !h.d.TOTPAllow.Allow(key+"/"+strconv.FormatInt(uid, 10)) {
		middleware.Refuse(c, apierr.Classified{Class: apierr.RateLimited, Key: "auth.rate_limited"})
		return
	}
	accepted, err := h.acceptFactor(c, uid, req.Code)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	if !accepted {
		middleware.Fail(c, ErrCredentials)
		return
	}
	sess, err := h.d.Service.CreateSession(c.Request.Context(), uid, middleware.ClientOf(c).String(), c.Request.UserAgent(), passwordFactorAMR, 0)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	h.grantSession(c, sess)
}

func (h *AuthHandlers) acceptFactor(c *gin.Context, uid int64, code string) (bool, error) {
	ok, err := h.d.Service.VerifyTOTP(c.Request.Context(), uid, code, h.d.Clock.Nanos())
	if err != nil || ok {
		return ok, err
	}
	return h.d.Service.UseRecoveryCode(c.Request.Context(), uid, code)
}

func (h *AuthHandlers) grantSession(c *gin.Context, sess Session) {
	info, err := h.d.Service.AccountInfo(c.Request.Context(), sess.UserID)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	admin, err := h.d.Service.IsAdmin(c.Request.Context(), sess.UserID)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	printable := hex.EncodeToString(sess.Token.Reveal())
	SetSessionCookie(c, printable)
	c.JSON(http.StatusOK, IdentityViewOf(sess.UserID, info.LoginName, info.DisplayName, admin, middleware.CSRFToken(h.d.CSRFKey(), printable)))
}

func (h *AuthHandlers) Session(c *gin.Context) {
	owner, ok := middleware.UserOf(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	info, err := h.d.Service.AccountInfo(c.Request.Context(), owner)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	admin, err := h.d.Service.IsAdmin(c.Request.Context(), owner)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	var csrf string
	if cookie, cookieErr := c.Cookie(middleware.SessionCookieName); cookieErr == nil && cookie != "" {
		csrf = middleware.CSRFToken(h.d.CSRFKey(), cookie)
	}
	details, err := h.d.SessionDetails(c.Request.Context(), owner)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	view := WhoAmIView{IdentityView: IdentityViewOf(owner, info.LoginName, info.DisplayName, admin, csrf), TOTPEnabled: details.TOTPEnabled, SMBOptOut: details.SMBOptOut, SMBEnabled: details.SMBEnabled, SMBCredential: details.SMBCredential, SMBUnavailableReason: details.SMBUnavailableReason, Oidc: details.Oidc, Roots: details.Roots, Limits: details.Limits, Features: details.Features}
	c.JSON(http.StatusOK, view)
}

func (h *AuthHandlers) Logout(c *gin.Context) {
	cookie, err := c.Cookie(middleware.SessionCookieName)
	if err != nil || cookie == "" {
		c.Status(http.StatusNoContent)
		return
	}
	raw, err := hex.DecodeString(cookie)
	if err != nil {
		clearSessionCookieTransport(c)
		c.Status(http.StatusNoContent)
		return
	}
	token := secret.New(raw)
	provider := h.d.Service.SessionAMR(c.Request.Context(), token) == providerAMR
	if err := h.d.Service.RevokeSession(c.Request.Context(), token); err != nil && !errors.Is(err, ErrCredentials) {
		middleware.Fail(c, err)
		return
	}
	clearSessionCookieTransport(c)
	if provider && h.d.OIDCEndSessionURL != nil {
		if end, ok := h.d.OIDCEndSessionURL(c); ok {
			c.JSON(http.StatusOK, LogoutView{EndSessionURL: end})
			return
		}
	}
	c.Status(http.StatusNoContent)
}

func decodeAuthBody(c *gin.Context, into any) error {
	media, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || !strings.EqualFold(media, "application/json") {
		return errors.New("authentication body is not JSON")
	}
	return middleware.DecodeJSON(c.Request.Body, into)
}

func SetSessionCookie(c *gin.Context, value string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, value, int(sessionCookieMaxAge/time.Second), "/", "", true, true)
}
func clearSessionCookieTransport(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, "", -1, "/", "", true, true)
}

// Reconfirm checks the account password for a sensitive settings operation.
func Reconfirm(c *gin.Context, service *Service, owner int64, password string) bool {
	if password == "" {
		middleware.Fail(c, ErrCredentials)
		return false
	}
	ok, err := service.VerifyAccountPassword(c.Request.Context(), owner, secret.New([]byte(password)))
	if err != nil {
		middleware.Fail(c, err)
		return false
	}
	if !ok {
		middleware.Fail(c, ErrCredentials)
		return false
	}
	return true
}

// Admin authorizes a session for an administrator-owned route.
func Admin(c *gin.Context, service *Service) (int64, bool) {
	owner, ok := middleware.UserOf(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	isAdmin, err := service.IsAdmin(c.Request.Context(), owner)
	if err != nil {
		middleware.Fail(c, err)
		return 0, false
	}
	if !isAdmin {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Denied})
		return 0, false
	}
	return owner, true
}
