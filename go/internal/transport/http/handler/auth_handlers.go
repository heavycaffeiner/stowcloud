//go:build linux

package handler

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

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
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
	Service           *auth.Service
	Clock             clock.Clock
	CSRFKey           func() []byte
	TOTPAllow         interface{ Allow(string) bool }
	SessionDetails    func(context.Context, int64) (SessionDetails, error)
	OIDCEndSessionURL func(*gin.Context) (string, bool)
}

// NewAuthHandlers builds the native authentication handlers. The returned map
// is keyed by route names from server.Table and can be merged into the route
// binding without importing app.
func NewAuthHandlers(d AuthHandlersDeps) map[string]gin.HandlerFunc {
	h := &authHandlers{d: d}
	return map[string]gin.HandlerFunc{
		"auth.login":      h.login,
		"auth.login.totp": h.loginTOTP,
		"auth.session":    h.session,
		"auth.logout":     h.logout,
	}
}

type authHandlers struct{ d AuthHandlersDeps }

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type totpRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
}

func (h *authHandlers) login(c *gin.Context) {
	var req loginRequest
	if err := decodeAuthBody(c, &req); err != nil {
		refuseTransport(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if req.Login == "" || req.Password == "" {
		refuseTransport(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return
	}
	sess, err := h.d.Service.Login(c.Request.Context(), auth.LoginRequest{
		Name: req.Login, Password: secret.New([]byte(req.Password)),
		IP: middleware.ClientOf(c).String(), UA: c.Request.UserAgent(), AMR: passwordAMR,
	}, 0)
	if errors.Is(err, auth.ErrSecondFactor) {
		h.askForFactor(c, req.Login)
		return
	}
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	h.grantSession(c, sess)
}

func (h *authHandlers) askForFactor(c *gin.Context, login string) {
	uid, err := h.d.Service.UserIDByName(c.Request.Context(), login)
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	challenge, err := MintChallenge(h.d.CSRFKey(), uid, h.d.Clock.Now().Unix())
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	writeTransportJSON(c, http.StatusOK, ChallengeView{Required: "totp", Challenge: challenge, ExpiresInSeconds: ChallengeTTL})
}

func (h *authHandlers) loginTOTP(c *gin.Context) {
	var req totpRequest
	if err := decodeAuthBody(c, &req); err != nil {
		refuseTransport(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	uid, err := OpenChallenge(h.d.CSRFKey(), req.Challenge, h.d.Clock.Now().Unix())
	if err != nil || req.Code == "" {
		refuseTransport(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return
	}
	key := middleware.ClientOf(c).String()
	if h.d.TOTPAllow != nil && !h.d.TOTPAllow.Allow(key+"/"+strconv.FormatInt(uid, 10)) {
		refuseTransport(c, apierr.Classified{Class: apierr.RateLimited, Key: "auth.rate_limited"})
		return
	}
	accepted, err := h.acceptFactor(c, uid, req.Code)
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	if !accepted {
		refuseTransport(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return
	}
	sess, err := h.d.Service.CreateSession(c.Request.Context(), uid, middleware.ClientOf(c).String(), c.Request.UserAgent(), passwordFactorAMR, 0)
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	h.grantSession(c, sess)
}

func (h *authHandlers) acceptFactor(c *gin.Context, uid int64, code string) (bool, error) {
	ok, err := h.d.Service.VerifyTOTP(c.Request.Context(), uid, code, h.d.Clock.Nanos())
	if err != nil || ok {
		return ok, err
	}
	return h.d.Service.UseRecoveryCode(c.Request.Context(), uid, code)
}

func (h *authHandlers) grantSession(c *gin.Context, sess auth.Session) {
	info, err := h.d.Service.AccountInfo(c.Request.Context(), sess.UserID)
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	admin, err := h.d.Service.IsAdmin(c.Request.Context(), sess.UserID)
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	printable := hex.EncodeToString(sess.Token.Reveal())
	setSessionCookieTransport(c, printable)
	writeTransportJSON(c, http.StatusOK, IdentityViewOf(sess.UserID, info.LoginName, info.DisplayName, admin, middleware.CSRFToken(h.d.CSRFKey(), printable)))
}

func (h *authHandlers) session(c *gin.Context) {
	owner, ok := ownerTransport(c)
	if !ok {
		refuseTransport(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	info, err := h.d.Service.AccountInfo(c.Request.Context(), owner)
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	admin, err := h.d.Service.IsAdmin(c.Request.Context(), owner)
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	var csrf string
	if cookie, cookieErr := c.Cookie(middleware.SessionCookieName); cookieErr == nil && cookie != "" {
		csrf = middleware.CSRFToken(h.d.CSRFKey(), cookie)
	}
	details, err := h.d.SessionDetails(c.Request.Context(), owner)
	if err != nil {
		failKnownTransport(c, err)
		return
	}
	view := WhoAmIView{IdentityView: IdentityViewOf(owner, info.LoginName, info.DisplayName, admin, csrf), TOTPEnabled: details.TOTPEnabled, SMBOptOut: details.SMBOptOut, SMBEnabled: details.SMBEnabled, SMBCredential: details.SMBCredential, SMBUnavailableReason: details.SMBUnavailableReason, Oidc: details.Oidc, Roots: details.Roots, Limits: details.Limits, Features: details.Features}
	writeTransportJSON(c, http.StatusOK, view)
}

func (h *authHandlers) logout(c *gin.Context) {
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
	if err := h.d.Service.RevokeSession(c.Request.Context(), token); err != nil && !errors.Is(err, auth.ErrCredentials) {
		failKnownTransport(c, err)
		return
	}
	clearSessionCookieTransport(c)
	if provider && h.d.OIDCEndSessionURL != nil {
		if end, ok := h.d.OIDCEndSessionURL(c); ok {
			writeTransportJSON(c, http.StatusOK, LogoutView{EndSessionURL: end})
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
	return middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), into)
}

func ownerTransport(c *gin.Context) (int64, bool) {
	v, ok := c.Get(string(middleware.KeyCredential))
	if !ok {
		return 0, false
	}
	p, ok := v.(middleware.Principal)
	if !ok || p.UserID == 0 {
		return 0, false
	}
	return p.UserID, true
}
func writeTransportJSON(c *gin.Context, status int, value any) { c.JSON(status, value) }
func failKnownTransport(c *gin.Context, err error) {
	refuseTransport(c, apierr.Classify(err, apierr.VisibilityKnown))
}
func refuseTransport(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	writeTransportJSON(c, status, body)
}
func setSessionCookieTransport(c *gin.Context, value string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, value, int(sessionCookieMaxAge/time.Second), "/", "", true, true)
}
func clearSessionCookieTransport(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, "", -1, "/", "", true, true)
}
