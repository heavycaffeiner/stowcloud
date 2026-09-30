//go:build linux

// Package oidc serves the provider sign-on and account-linking routes.
package oidc

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/httpx"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
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
type Deps struct {
	Auth        *auth.Service
	Client      func() *Client
	DisplayName func() string
	AppHosts    func() []string
	Logger      *slog.Logger
}

// Handlers owns the OIDC HTTP handlers and logout URL callback.
type Handlers struct{ d Deps }

// NewHandlers constructs OIDC route handlers from narrow typed dependencies.
func NewHandlers(d Deps) *Handlers { return &Handlers{d: d} }

// EndSessionURL returns the provider logout URL for the request, when available.
func (h *Handlers) EndSessionURL(ctx context.Context) (string, bool) {
	client := h.d.Client()
	if client == nil {
		return "", false
	}
	origin, ok := h.requestOrigin(ctx)
	if !ok {
		return "", false
	}
	target, err := client.EndSessionURL(ctx, "", origin+loginPath)
	if err != nil || target == "" {
		return "", false
	}
	return target, true
}

type configOutput struct{ Body OIDCConfigView }

func (h *Handlers) Config(context.Context, *struct{}) (*configOutput, error) {
	if h.d.Client() == nil {
		return &configOutput{Body: OIDCConfigView{Enabled: false}}, nil
	}
	return &configOutput{Body: OIDCConfigView{Enabled: true, DisplayName: h.d.DisplayName()}}, nil
}

type startInput struct {
	ReturnTo string `query:"return_to"`
}

type linkStartInput struct{ Body linkStartRequest }

// startOutput sets the cookie that binds the provider's answer to this browser.
type startOutput struct {
	SetCookie string `header:"Set-Cookie"`
	Body      OIDCStartView
}

func (h *Handlers) Start(ctx context.Context, in *startInput) (*startOutput, error) {
	client := h.d.Client()
	if client == nil {
		return nil, apierr.AsClassified(apierr.SubsystemUnavailable, "")
	}
	returnTo, err := httpx.SafeReturnTo(in.ReturnTo)
	if err != nil {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	return h.begin(ctx, client, 0, returnTo)
}

func (h *Handlers) LinkStart(ctx context.Context, in *linkStartInput) (*startOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err = auth.Reconfirm(ctx, h.d.Auth, owner, in.Body.Current); err != nil {
		return nil, err
	}
	client := h.d.Client()
	if client == nil {
		return nil, apierr.AsClassified(apierr.SubsystemUnavailable, "")
	}
	returnTo, err := httpx.SafeReturnTo(in.Body.ReturnTo)
	if err != nil {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	return h.begin(ctx, client, owner, returnTo)
}

func (h *Handlers) begin(ctx context.Context, client *Client, user int64, returnTo string) (*startOutput, error) {
	flow, err := NewFlowSecrets()
	if err != nil {
		return nil, err
	}
	origin, ok := h.requestOrigin(ctx)
	if !ok {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	redirectURI := origin + "/api/v1/auth/oidc/callback"
	target, err := client.AuthorizeURL(ctx, redirectURI, flow)
	if err != nil {
		return nil, err
	}
	if err := h.d.Auth.StartOIDCFlow(ctx, user, flow.State, flow.Nonce, flow.Binding, flow.CodeVerifier, redirectURI, returnTo); err != nil {
		return nil, err
	}
	return &startOutput{SetCookie: bindingCookieOf(flow.Binding, int(flowWindow/time.Second)).String(), Body: OIDCStartView{AuthorizeURL: target}}, nil
}

func (h *Handlers) Callback(c *gin.Context) {
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
	raw, err := client.Exchange(c.Request.Context(), authCode, flow.RedirectURI, FlowSecrets{Nonce: flow.Nonce, CodeVerifier: flow.CodeVerifier})
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

func (h *Handlers) completeLink(c *gin.Context, flow auth.OIDCFlow, claims *Claims) error {
	owner, ok := middleware.UserOf(c)
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

func (h *Handlers) completeSignIn(c *gin.Context, flow auth.OIDCFlow, claims *Claims) error {
	user, err := h.d.Auth.UserForOIDCIdentity(c.Request.Context(), claims.Issuer, claims.Subject)
	if err != nil {
		h.logInfo("a provider identity is not linked to any account", "issuer", claims.Issuer)
		return h.redirectError(c, loginPath, errNotLinked)
	}
	sess, err := h.d.Auth.CreateSession(c.Request.Context(), user, middleware.ClientOf(c).String(), c.Request.UserAgent(), providerAMR, sessionTTL)
	if err != nil {
		h.logError("establishing a single-sign-on session failed", "error", err)
		return h.redirectError(c, loginPath, errInternal)
	}
	if err := h.d.Auth.TouchOIDCLink(c.Request.Context(), claims.Issuer, claims.Subject); err != nil {
		h.logWarn("stamping a single-sign-on link's last use failed", "error", err)
	}
	http.SetCookie(c.Writer, auth.SessionCookie(printableToken(sess.Token)))
	return redirect(c, flow.ReturnTo)
}

type linkDeleteInput struct {
	Body struct {
		Current string `json:"current"`
	}
}

type noContent struct{}

func (h *Handlers) LinkDelete(ctx context.Context, in *linkDeleteInput) (*noContent, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	if in.Body.Current == "" {
		return nil, apierr.AsClassified(apierr.Unprocessable, "auth.invalid_credentials")
	}
	if err := auth.Reconfirm(ctx, h.d.Auth, owner, in.Body.Current); err != nil {
		return nil, err
	}
	if err := h.d.Auth.RemoveOIDCLink(ctx, owner); err != nil {
		return nil, err
	}
	return &noContent{}, nil
}

type userIDInput struct {
	ID string `path:"id"`
}

type linkOutput struct{ Body OIDCLinkView }

func (h *Handlers) AdminGet(ctx context.Context, in *userIDInput) (*linkOutput, error) {
	id, ok := positiveID(in.ID)
	if !ok {
		return nil, apierr.AsClassified(apierr.NotFound, "")
	}
	link, err := h.d.Auth.OIDCLinkOf(ctx, id)
	if errors.Is(err, auth.ErrNoOIDCLink) {
		return &linkOutput{Body: OIDCLinkView{Linked: false}}, nil
	}
	if err != nil {
		return nil, err
	}
	return &linkOutput{Body: OIDCLinkOf(link)}, nil
}

func (h *Handlers) AdminDelete(ctx context.Context, in *userIDInput) (*noContent, error) {
	id, ok := positiveID(in.ID)
	if !ok {
		return nil, apierr.AsClassified(apierr.NotFound, "")
	}
	if err := h.d.Auth.RemoveOIDCLink(ctx, id); err != nil {
		return nil, err
	}
	return &noContent{}, nil
}

type endpointsOutput struct{ Body OIDCEndpointsView }

func (h *Handlers) AdminEndpoints(context.Context, *struct{}) (*endpointsOutput, error) {
	hosts := h.d.AppHosts()
	redirects := make([]string, 0, len(hosts))
	postLogouts := make([]string, 0, len(hosts))
	for _, host := range hosts {
		redirects = append(redirects, "https://"+host+"/api/v1/auth/oidc/callback")
		postLogouts = append(postLogouts, "https://"+host+loginPath)
	}
	return &endpointsOutput{Body: OIDCEndpointsView{RedirectURIs: redirects, PostLogoutRedirectURIs: postLogouts}}, nil
}

// requestOrigin is the origin the request arrived on, when it is one this
// deployment declares.
func (h *Handlers) requestOrigin(ctx context.Context) (string, bool) {
	a, ok := middleware.AuthorityFrom(ctx)
	if !ok {
		return "", false
	}
	declared := h.d.AppHosts()
	if len(declared) > 0 && !hostDeclared(declared, a.Host) {
		return "", false
	}
	origin := a.Scheme + "://" + a.Host
	if !middleware.OriginMatchesRequest(origin, a.Scheme, a.Host) {
		return "", false
	}
	return origin, true
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
func (h *Handlers) ambiguousPath(c *gin.Context) string {
	if _, ok := middleware.UserOf(c); ok {
		return linkErrorPath
	}
	return loginPath
}
func redirect(c *gin.Context, target string) error { c.Redirect(http.StatusFound, target); return nil }
func (h *Handlers) redirectError(c *gin.Context, target, code string) error {
	return redirect(c, target+"?oidc_error="+url.QueryEscape(code))
}
func printableToken(t interface{ Reveal() []byte }) string { return hex.EncodeToString(t.Reveal()) }
func bindingCookieOf(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: bindingCookie, Value: value, Path: "/",
		MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}
func (h *Handlers) clearBinding(c *gin.Context) {
	http.SetCookie(c.Writer, bindingCookieOf("", -1))
}
func positiveID(raw string) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n > 0
}
func (h *Handlers) logInfo(msg string, args ...any) {
	if h.d.Logger != nil {
		h.d.Logger.Info(msg, args...)
	}
}
func (h *Handlers) logWarn(msg string, args ...any) {
	if h.d.Logger != nil {
		h.d.Logger.Warn(msg, args...)
	}
}
func (h *Handlers) logError(msg string, args ...any) {
	if h.d.Logger != nil {
		h.d.Logger.Error(msg, args...)
	}
}
