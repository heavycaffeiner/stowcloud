//go:build linux

// Package oidc serves the provider sign-on and account-linking routes.
package oidc

import (
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	featureoidc "github.com/heavycaffeiner/stowcloud/backend/internal/feature/oidc"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
)

const (
	bindingCookie = "__Host-sc_oidc"
	loginPath     = "/login"
	linkErrorPath = "/settings/security"
	flowWindow    = 15 * time.Minute
	providerAMR   = 4
	sessionTTL    = 12 * time.Hour
)

const (
	errDisabled             = "oidc.disabled"
	errBadRequest           = "oidc.bad_request"
	errBadState             = "oidc.bad_state"
	errNotLinked            = "oidc.not_linked"
	errProviderUnavailable  = "oidc.provider_unavailable"
	errAccessDenied         = "oidc.access_denied"
	errLinkSessionChanged   = "oidc.link_session_changed"
	errSubjectAlreadyLinked = "oidc.subject_already_linked"
	codeBadToken            = "auth.invalid_credentials" //nolint:gosec // G101 flags this client-facing wire error code; it is not credential material.
	errInternal             = "internal"
)

type linkStartRequest struct {
	Current  string `json:"current"`
	ReturnTo string `json:"return_to"`
}

// Deps are the narrow application services and request policies needed by OIDC.
// The transport does not receive an Engine-shaped object.
type Deps struct {
	Auth             *auth.Service
	Client           func() *featureoidc.Client
	DisplayName      func() string
	AppHosts         func() []string
	Logger           *slog.Logger
	Owner            func(*gin.Context) (int64, bool)
	Admin            func(*gin.Context) (int64, bool)
	Reconfirm        func(*gin.Context, int64, string) bool
	Decode           func(*gin.Context, any) error
	Fail             func(*gin.Context, error)
	FailKnown        func(*gin.Context, error)
	Refuse           func(*gin.Context, apierr.Classified)
	WriteJSON        func(*gin.Context, int, any)
	ClientAddr       func(*gin.Context) string
	SetSessionCookie func(*gin.Context, string)
}

// Handlers owns the OIDC HTTP handlers and logout URL callback.
type Handlers struct{ d Deps }

// New constructs OIDC route handlers from narrow typed dependencies.
func New(d Deps) *Handlers { return &Handlers{d: d} }

// Routes returns handlers keyed by the server route names.
func (h *Handlers) Routes() map[string]gin.HandlerFunc {
	return map[string]gin.HandlerFunc{
		"auth.oidc.config":         h.config,
		"auth.oidc.start":          h.start,
		"auth.oidc.callback":       h.callback,
		"account.oidc-link.start":  h.linkStart,
		"account.oidc-link.delete": h.linkDelete,
		"admin.users.oidc.get":     h.adminGet,
		"admin.users.oidc.delete":  h.adminDelete,
		"admin.oidc.endpoints":     h.adminEndpoints,
	}
}

// NewHandlers is retained as a convenience for callers that only need routes.
func NewHandlers(d Deps) map[string]gin.HandlerFunc { return New(d).Routes() }

// EndSessionURL returns the provider logout URL for the request, when available.
func (h *Handlers) EndSessionURL(c *gin.Context) (string, bool) {
	client := h.d.Client()
	if client == nil {
		return "", false
	}
	origin, ok := h.requestOrigin(c)
	if !ok {
		return "", false
	}
	target, err := client.EndSessionURL(c.Request.Context(), "", origin+loginPath)
	if err != nil || target == "" {
		return "", false
	}
	return target, true
}

type handlers = Handlers

func (h *handlers) config(c *gin.Context) {
	if h.d.Client() == nil {
		h.d.WriteJSON(c, http.StatusOK, handler.OIDCConfigView{Enabled: false})
		return
	}
	h.d.WriteJSON(c, http.StatusOK, handler.OIDCConfigView{Enabled: true, DisplayName: h.d.DisplayName()})
}

func (h *handlers) start(c *gin.Context) {
	client := h.d.Client()
	if client == nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	returnTo, err := handler.SafeReturnTo(c.Query("return_to"))
	if err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	h.begin(c, client, 0, returnTo)
}

func (h *handlers) linkStart(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req linkStartRequest
	if err := h.d.Decode(c, &req); err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if !h.d.Reconfirm(c, owner, req.Current) {
		return
	}
	client := h.d.Client()
	if client == nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	returnTo, err := handler.SafeReturnTo(req.ReturnTo)
	if err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	h.begin(c, client, owner, returnTo)
}

func (h *handlers) begin(c *gin.Context, client *featureoidc.Client, user int64, returnTo string) {
	flow, err := featureoidc.NewFlowSecrets()
	if err != nil {
		h.d.FailKnown(c, err)
		return
	}
	redirectURI, ok := h.redirectURI(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	target, err := client.AuthorizeURL(c.Request.Context(), redirectURI, flow)
	if err != nil {
		h.d.FailKnown(c, err)
		return
	}
	if err := h.d.Auth.StartOIDCFlow(c.Request.Context(), user, flow.State, flow.Nonce, flow.Binding, flow.CodeVerifier, redirectURI, returnTo); err != nil {
		h.d.FailKnown(c, err)
		return
	}
	h.setBinding(c, flow.Binding)
	h.d.WriteJSON(c, http.StatusOK, handler.OIDCStartView{AuthorizeURL: target})
}

func (h *handlers) callback(c *gin.Context) {
	client := h.d.Client()
	if client == nil {
		if err := h.redirectError(c, h.ambiguousPath(c), errDisabled); err != nil {
			h.logError("redirecting the OIDC disabled error failed", "error", err)
		}
		return
	}
	if desc := c.Query("error"); desc != "" {
		h.logInfo("the provider refused a sign-on", "error", desc)
		if err := h.redirectError(c, h.ambiguousPath(c), errAccessDenied); err != nil {
			h.logError("redirecting the OIDC access-denied error failed", "error", err)
		}
		return
	}
	authCode, state := c.Query("code"), c.Query("state")
	if authCode == "" || state == "" {
		if err := h.redirectError(c, h.ambiguousPath(c), errBadRequest); err != nil {
			h.logError("redirecting the OIDC bad-request error failed", "error", err)
		}
		return
	}
	binding, err := c.Cookie(bindingCookie)
	if err != nil {
		binding = ""
	}
	h.clearBinding(c)
	flow, err := h.d.Auth.TakeOIDCFlow(c.Request.Context(), state, binding)
	if err != nil {
		if redirectErr := h.redirectError(c, h.ambiguousPath(c), errBadState); redirectErr != nil {
			h.logError("redirecting the OIDC bad-state error failed", "error", redirectErr)
		}
		return
	}
	landing := loginPath
	if flow.User != 0 {
		landing = linkErrorPath
	}
	raw, err := client.Exchange(c.Request.Context(), authCode, flow.RedirectURI, featureoidc.FlowSecrets{Nonce: flow.Nonce, CodeVerifier: flow.CodeVerifier})
	if err != nil {
		h.logWarn("the token exchange failed", "error", err)
		if redirectErr := h.redirectError(c, landing, errProviderUnavailable); redirectErr != nil {
			h.logError("redirecting the OIDC provider error failed", "error", redirectErr)
		}
		return
	}
	claims, err := client.VerifyIDToken(c.Request.Context(), raw, flow.Nonce)
	if err != nil {
		h.logWarn("an identity token did not verify", "error", err)
		if err := h.redirectError(c, landing, codeBadToken); err != nil {
			h.logError("redirecting the OIDC invalid-token error failed", "error", err)
		}
		return
	}
	if flow.User != 0 {
		if err := h.completeLink(c, flow, claims); err != nil {
			h.logWarn("completing single-sign-on link failed", "error", err)
		}
		return
	}
	if err := h.completeSignIn(c, flow, claims); err != nil {
		h.logWarn("completing single-sign-on sign-in failed", "error", err)
	}
}

func (h *handlers) completeLink(c *gin.Context, flow auth.OIDCFlow, claims *featureoidc.Claims) error {
	owner, ok := h.d.Owner(c)
	if !ok || owner != flow.User {
		return h.redirectError(c, linkErrorPath, errLinkSessionChanged)
	}
	if err := h.d.Auth.CreateOIDCLink(c.Request.Context(), flow.User, claims.Issuer, claims.Subject); err != nil {
		if errors.Is(err, auth.ErrOIDCLinkTaken) {
			return h.redirectError(c, linkErrorPath, errSubjectAlreadyLinked)
		}
		h.logError("attaching a single-sign-on identity failed", "error", err)
		return h.redirectError(c, linkErrorPath, errInternal)
	}
	return redirect(c, flow.ReturnTo)
}

func (h *handlers) completeSignIn(c *gin.Context, flow auth.OIDCFlow, claims *featureoidc.Claims) error {
	user, err := h.d.Auth.UserForOIDCIdentity(c.Request.Context(), claims.Issuer, claims.Subject)
	if err != nil {
		h.logInfo("a provider identity is not linked to any account", "issuer", claims.Issuer)
		return h.redirectError(c, loginPath, errNotLinked)
	}
	sess, err := h.d.Auth.CreateSession(c.Request.Context(), user, h.d.ClientAddr(c), c.Request.UserAgent(), providerAMR, sessionTTL)
	if err != nil {
		h.logError("establishing a single-sign-on session failed", "error", err)
		return h.redirectError(c, loginPath, errInternal)
	}
	if err := h.d.Auth.TouchOIDCLink(c.Request.Context(), claims.Issuer, claims.Subject); err != nil {
		h.logWarn("stamping a single-sign-on link's last use failed", "error", err)
	}
	h.d.SetSessionCookie(c, printableToken(sess.Token))
	return redirect(c, flow.ReturnTo)
}

func (h *handlers) linkDelete(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req struct {
		Current string `json:"current"`
	}
	if err := h.d.Decode(c, &req); err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if req.Current == "" {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "auth.invalid_credentials"})
		return
	}
	if !h.d.Reconfirm(c, owner, req.Current) {
		return
	}
	if err := h.d.Auth.RemoveOIDCLink(c.Request.Context(), owner); err != nil {
		h.d.FailKnown(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handlers) adminGet(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	link, err := h.d.Auth.OIDCLinkOf(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, auth.ErrNoOIDCLink) {
			h.d.WriteJSON(c, http.StatusOK, handler.OIDCLinkView{Linked: false})
			return
		}
		h.d.FailKnown(c, err)
		return
	}
	h.d.WriteJSON(c, http.StatusOK, handler.OIDCLinkOf(link))
}

func (h *handlers) adminDelete(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	if err := h.d.Auth.RemoveOIDCLink(c.Request.Context(), id); err != nil {
		h.d.FailKnown(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handlers) adminEndpoints(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	hosts := h.d.AppHosts()
	redirects := make([]string, 0, len(hosts))
	postLogouts := make([]string, 0, len(hosts))
	for _, host := range hosts {
		redirects = append(redirects, "https://"+host+"/api/v1/auth/oidc/callback")
		postLogouts = append(postLogouts, "https://"+host+loginPath)
	}
	h.d.WriteJSON(c, http.StatusOK, handler.OIDCEndpointsView{RedirectURIs: redirects, PostLogoutRedirectURIs: postLogouts})
}

func (h *handlers) redirectURI(c *gin.Context) (string, bool) {
	origin, ok := h.requestOrigin(c)
	if !ok {
		return "", false
	}
	return origin + "/api/v1/auth/oidc/callback", true
}
func (h *handlers) requestOrigin(c *gin.Context) (string, bool) {
	host := c.Request.Host
	if host == "" {
		return "", false
	}
	declared := h.d.AppHosts()
	if len(declared) > 0 && !hostDeclared(declared, host) {
		return "", false
	}
	scheme := "https"
	if c.Request.URL.Scheme == "http" || c.Request.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + host, true
}
func hostDeclared(declared []string, host string) bool {
	name := host
	if i := strings.LastIndex(name, ":"); i > 0 && !strings.Contains(name[i:], "]") {
		name = name[:i]
	}
	for _, d := range declared {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}
func (h *handlers) ambiguousPath(c *gin.Context) string {
	if _, ok := h.d.Owner(c); ok {
		return linkErrorPath
	}
	return loginPath
}
func redirect(c *gin.Context, target string) error { c.Redirect(http.StatusFound, target); return nil }
func (h *handlers) redirectError(c *gin.Context, target, code string) error {
	return redirect(c, target+"?oidc_error="+url.QueryEscape(code))
}
func printableToken(t interface{ Reveal() []byte }) string { return hex.EncodeToString(t.Reveal()) }
func (h *handlers) setBinding(c *gin.Context, binding string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(bindingCookie, binding, int(flowWindow/time.Second), "/", "", true, true)
}
func (h *handlers) clearBinding(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(bindingCookie, "", -1, "/", "", true, true)
}
func pathID(c *gin.Context) (int64, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	return n, err == nil && n > 0
}
func (h *handlers) logInfo(msg string, args ...any) {
	if h.d.Logger != nil {
		h.d.Logger.Info(msg, args...)
	}
}
func (h *handlers) logWarn(msg string, args ...any) {
	if h.d.Logger != nil {
		h.d.Logger.Warn(msg, args...)
	}
}
func (h *handlers) logError(msg string, args ...any) {
	if h.d.Logger != nil {
		h.d.Logger.Error(msg, args...)
	}
}
