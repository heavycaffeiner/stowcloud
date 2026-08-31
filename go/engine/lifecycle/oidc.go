//go:build linux

// Single sign-on.
//
// Two flows share one callback: signing in, and attaching a provider identity
// to an account that is already signed in. Which one a callback is completing
// is decided by the flow row it consumes, never by anything the browser
// carries back, because the browser is the one thing an attacker can aim.
package lifecycle

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/oidc"
)

// oidcBindingCookie carries the browser binding for an in-flight sign-on.
//
// The state parameter travels through the address bar, referrers and any
// proxy log on the way. The binding does not: it is what makes a state value
// lifted from one of those insufficient on its own.
const oidcBindingCookie = "__Host-sc_oidc"

// authOIDCConfig says whether single sign-on is offered.
//
// Public, because the sign-in screen reads it to decide whether to draw the
// button, and a caller with no session is exactly who is looking at that
// screen. It reports the display name and nothing else: the issuer, the
// client id and every endpoint are the deployment's configuration.
func (e *Engine) authOIDCConfig(c *fiber.Ctx) error {
	client := e.oidc()
	if client == nil {
		return writeJSON(c, fiber.StatusOK, handler.OIDCConfigView{Enabled: false})
	}
	return writeJSON(c, fiber.StatusOK, handler.OIDCConfigView{
		Enabled:     true,
		DisplayName: e.oidcDisplayName(),
	})
}

// authOIDCStart sends the browser to the provider.
func (e *Engine) authOIDCStart(c *fiber.Ctx) error {
	client := e.oidc()
	if client == nil {
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}

	// Where to land afterwards, validated now rather than at the callback: a
	// value that only fails on the way back has already cost the person a
	// round trip through the provider.
	returnTo, err := handler.SafeReturnTo(c.Query("return_to"))
	if err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	// No account yet. Signing in is the flow that establishes one, so the row
	// carries no user and the callback decides who this is from the identity
	// the provider asserts.
	return e.beginOIDCFlow(c, client, 0, returnTo)
}

// accountOIDCLinkStart attaches a provider identity to the caller's account.
//
// The account comes from the session rather than the request, and it is
// written into the flow row now. A callback that took the account from
// anything the browser carried would let one person attach their provider
// identity to somebody else's account.
func (e *Engine) accountOIDCLinkStart(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	// The proof comes before the provider check, not after: whether a
	// provider is configured is a fact about the deployment, and answering
	// it to a caller who has not proved the password tells an unlocked
	// browser something the account holder never authorised.
	//
	// Attaching an identity mints a permanent second way into the account, so
	// a live session alone must not be enough: whoever is holding an unlocked
	// browser could otherwise leave themselves a way back in. This is the
	// same proof detaching one already demands.
	var req linkStartRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	client := e.oidc()
	if client == nil {
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}

	// From the body, where the client sends it. Read from the query the value
	// was always empty, so a finished link flow landed on the root instead of
	// the screen it started from.
	returnTo, err := handler.SafeReturnTo(req.ReturnTo)
	if err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	return e.beginOIDCFlow(c, client, int64(owner), returnTo)
}

// linkStartRequest is the proof plus where to come back to.
type linkStartRequest struct {
	Current  string `json:"current"`
	ReturnTo string `json:"return_to"`
}

// beginOIDCFlow mints the flow, stores it and answers with the provider URL.
func (e *Engine) beginOIDCFlow(
	c *fiber.Ctx, client *oidc.Client, user int64, returnTo string,
) error {
	flow, err := oidc.NewFlowSecrets()
	if err != nil {
		return failKnown(c, err)
	}

	redirectURI, ok := e.oidcRedirectURI(c)
	if !ok {
		// The redirect has to be a URL the provider was configured with, and
		// building one from an unchecked Host header is how a request becomes
		// a redirect to somewhere else entirely.
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	target, err := client.AuthorizeURL(c.UserContext(), redirectURI, flow)
	if err != nil {
		return failKnown(c, err)
	}

	if serr := e.Auth.StartOIDCFlow(c.UserContext(), user,
		flow.State, flow.Nonce, flow.Binding, flow.CodeVerifier,
		redirectURI, returnTo); serr != nil {
		return failKnown(c, serr)
	}

	e.setOIDCBinding(c, flow.Binding)
	return writeJSON(c, fiber.StatusOK, handler.OIDCStartView{AuthorizeURL: target})
}

// authOIDCCallback completes whichever flow the state names.
//
// The provider sends the browser here with no credential of ours, so every
// decision comes from the stored flow: who it belongs to, where it returns
// to, and which redirect URI the exchange has to repeat.
func (e *Engine) authOIDCCallback(c *fiber.Ctx) error {
	client := e.oidc()
	if client == nil {
		return refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
	}

	if desc := c.Query("error"); desc != "" {
		// The provider refused. Reported as a refusal rather than a fault:
		// nothing here failed, and the person needs to know the other end
		// declined.
		e.logger.Info("the provider refused a sign-on", "error", desc)
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}

	code, oidcState := c.Query("code"), c.Query("state")
	if code == "" || oidcState == "" {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	// The binding is consumed whatever happens next: a flow whose callback
	// arrived is not one to leave redeemable for a second attempt.
	binding := c.Cookies(oidcBindingCookie)
	e.clearOIDCBinding(c)

	flow, err := e.Auth.TakeOIDCFlow(c.UserContext(), oidcState, binding)
	if err != nil {
		// An unknown state, an expired one and a binding that did not match
		// are one answer. Telling them apart says which half of a stolen pair
		// is still worth having.
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}

	rawToken, err := client.Exchange(c.UserContext(), code, flow.RedirectURI, oidc.FlowSecrets{
		Nonce:        flow.Nonce,
		CodeVerifier: flow.CodeVerifier,
	})
	if err != nil {
		e.logger.Warn("the token exchange failed", "error", err)
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}

	claims, err := client.VerifyIDToken(c.UserContext(), rawToken, flow.Nonce)
	if err != nil {
		e.logger.Warn("an identity token did not verify", "error", err)
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}

	if flow.User != 0 {
		return e.completeOIDCLink(c, flow, claims)
	}
	return e.completeOIDCSignIn(c, flow, claims)
}

// completeOIDCLink attaches the identity to the account that started the flow.
func (e *Engine) completeOIDCLink(c *fiber.Ctx, flow auth.OIDCFlow, claims *oidc.Claims) error {
	if err := e.Auth.CreateOIDCLink(c.UserContext(), flow.User, claims.Issuer, claims.Subject); err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.OIDCCallbackView{
		Linked:   true,
		ReturnTo: flow.ReturnTo,
	})
}

// completeOIDCSignIn issues a session for the account holding this identity.
//
// An identity nobody has linked is refused rather than creating an account.
// A provider that will issue a token to anyone would otherwise be a way to
// create accounts here, and who may have one is the operator's decision.
func (e *Engine) completeOIDCSignIn(c *fiber.Ctx, flow auth.OIDCFlow, claims *oidc.Claims) error {
	user, err := e.Auth.UserForOIDCIdentity(c.UserContext(), claims.Issuer, claims.Subject)
	if err != nil {
		e.logger.Info("a provider identity is not linked to any account",
			"issuer", claims.Issuer)
		return refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
	}

	sess, err := e.Auth.CreateSession(c.UserContext(), user,
		clientAddr(c), string(c.Request().Header.UserAgent()), amrProvider, oidcSessionTTL)
	if err != nil {
		return failKnown(c, err)
	}

	printable := printableToken(sess.Token)
	e.setSessionCookie(c, printable)

	info, err := e.Auth.AccountInfo(c.UserContext(), user)
	if err != nil {
		return failKnown(c, err)
	}
	admin, err := e.Auth.IsAdmin(c.UserContext(), user)
	if err != nil {
		return failKnown(c, err)
	}

	return writeJSON(c, fiber.StatusOK, handler.OIDCCallbackView{
		ReturnTo: flow.ReturnTo,
		Identity: handler.IdentityViewOf(user, info.LoginName, info.DisplayName, admin,
			middleware.CSRFToken(e.csrfKey(), printable)),
	})
}

// amrProvider records that a session was established by the provider rather
// than by a password here. A policy asking what was presented reads this.
const amrProvider = 4

// oidcSessionTTL bounds a session the provider established. Zero would take
// the service's default; this is stated so a provider-backed session does not
// silently outlive what the operator expects of one.
const oidcSessionTTL = 12 * time.Hour

// accountOIDCLinkDelete detaches the caller's own provider identity.
func (e *Engine) accountOIDCLinkDelete(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req reconfirmRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if proved, rerr := e.reconfirm(c, int64(owner), req.Current); !proved {
		return rerr
	}

	if err := e.Auth.RemoveOIDCLink(c.UserContext(), int64(owner)); err != nil {
		return failKnown(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// adminUserOIDCGet reports one account's provider link.
func (e *Engine) adminUserOIDCGet(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	link, err := e.Auth.OIDCLinkOf(c.UserContext(), id)
	if err != nil {
		if errors.Is(err, auth.ErrNoOIDCLink) {
			return writeJSON(c, fiber.StatusOK, handler.OIDCLinkView{Linked: false})
		}
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.OIDCLinkOf(link))
}

// adminUserOIDCDelete detaches an account's provider identity.
//
// The service restores local password login as it removes the link, because
// removing it alone would leave an account with no way in at all.
func (e *Engine) adminUserOIDCDelete(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Auth.RemoveOIDCLink(c.UserContext(), id); err != nil {
		return failKnown(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// oidc reports the configured client, or nil when sign-on is off.
func (e *Engine) oidc() *oidc.Client {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.oidcClient
}

// oidcDisplayName is what the sign-in screen puts on the button.
func (e *Engine) oidcDisplayName() string {
	e.settingsMu.RLock()
	defer e.settingsMu.RUnlock()
	return e.oidcName
}

// oidcRedirectURI builds the callback URL for this request.
//
// From the host the boundary has already admitted, never from an unchecked
// header: a redirect assembled out of whatever a client sent is a redirect to
// wherever the client chose. The identical string has to reach the exchange,
// which is why the flow row stores it rather than rebuilding it later.
func (e *Engine) oidcRedirectURI(c *fiber.Ctx) (string, bool) {
	host := string(c.Request().Host())
	if host == "" {
		return "", false
	}

	e.settingsMu.RLock()
	declared := e.appHosts.App
	e.settingsMu.RUnlock()

	// A deployment that has declared its hosts admits only those. One that has
	// not is in first boot, where the boundary has already limited the caller
	// to a private address, so the host it reached is the one to use.
	if len(declared) > 0 && !hostDeclared(declared, host) {
		return "", false
	}

	scheme := "https"
	if c.Protocol() == "http" {
		scheme = "http"
	}
	return scheme + "://" + host + server.Base + "/auth/oidc/callback", true
}

// hostDeclared reports whether a host is one this deployment serves.
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

// setOIDCBinding stores the browser half of an in-flight sign-on.
//
// Short-lived and scoped like the session cookie, because it is the same kind
// of value: something only this browser should be able to present.
func (e *Engine) setOIDCBinding(c *fiber.Ctx, binding string) {
	c.Cookie(&fiber.Cookie{
		Name:     oidcBindingCookie,
		Value:    binding,
		Path:     "/",
		Secure:   true,
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
		Expires:  e.clock.Now().Add(oidcFlowWindow),
	})
}

// oidcFlowWindow is how long a sign-on may take. Long enough to type a
// password and a second factor at the provider, short enough that an
// abandoned flow's cookie is not left in a browser for a day.
const oidcFlowWindow = 15 * time.Minute

// clearOIDCBinding expires the cookie, matching every attribute the browser
// keys a deletion on.
func (e *Engine) clearOIDCBinding(c *fiber.Ctx) {
	c.Cookie(&fiber.Cookie{
		Name:     oidcBindingCookie,
		Value:    "",
		Path:     "/",
		Secure:   true,
		HTTPOnly: true,
		SameSite: fiber.CookieSameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

// buildOIDCClient constructs the provider client from the stored settings.
//
// Nil is off, and off is the ordinary state: a deployment without single
// sign-on is one where people use passwords. An incomplete configuration
// leaves it off with a line rather than refusing to boot, because a server
// that will not start is a deployment where nobody signs in at all.
func (e *Engine) buildOIDCClient(ctx context.Context, cfg *oidcSettings) *oidc.Client {
	if cfg == nil {
		return nil
	}

	plain, ok, err := e.ConfigSecret(ctx, secretOIDCClient)
	if err != nil {
		e.logger.Error("the single sign-on secret could not be opened; sign-on stays off", "error", err)
		return nil
	}
	if !ok {
		e.logger.Error("single sign-on is configured with no client secret; it stays off")
		return nil
	}

	client, err := oidc.New(oidc.Config{
		Issuer:                cfg.Issuer,
		ClientID:              cfg.ClientID,
		ClientSecret:          secret.New([]byte(plain)),
		Scopes:                cfg.Scopes,
		AllowPrivateEndpoints: cfg.AllowPrivateEndpoints,
		CACertFile:            cfg.CACertFile,
	}, e.clock)
	if err != nil {
		e.logger.Error("the single sign-on client would not build; it stays off", "error", err)
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
}
