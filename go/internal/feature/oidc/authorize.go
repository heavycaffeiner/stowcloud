package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// A single sign-in attempt draws four random values. They are simple to mix up,
// so each one's purpose:
//
//   - State denies an attacker control over the value the callback carries.
//   - The binding cookie denies an attacker the ability to hand a valid state to
//     a different person's browser, the sign-in forgery state alone leaves open.
//   - The nonce binds the identity token to this particular authorization
//     request.
//   - The verifier binds redemption of the code to whoever began the attempt.

// flowSecretBytes holds 256 bits, matching what the code exchange requires of a
// verifier.
const flowSecretBytes = 32

// FlowSecrets are the secrets for one attempt in flight.
//
// Three of them are stored as digests. The verifier is kept whole, because
// the exchange requires presenting the original.
type FlowSecrets struct {
	State        string
	Nonce        string
	Binding      string
	CodeVerifier string
}

// String redacts its output: these are precisely the values an accidental log
// field would expose.
func (FlowSecrets) String() string { return "FlowSecrets(redacted)" }

// GoString redacts for the same reason: a struct printed with the verbose
// verb bypasses String.
func (FlowSecrets) GoString() string { return "FlowSecrets(redacted)" }

// MarshalJSON redacts as well, so a value that reaches a response body or a
// structured log carries nothing.
func (FlowSecrets) MarshalJSON() ([]byte, error) {
	return []byte(`"FlowSecrets(redacted)"`), nil
}

// NewFlowSecrets generates all four values.
func NewFlowSecrets() (FlowSecrets, error) {
	var out FlowSecrets
	for _, dst := range []*string{&out.State, &out.Nonce, &out.Binding, &out.CodeVerifier} {
		v, err := randomToken()
		if err != nil {
			return FlowSecrets{}, err
		}
		*dst = v
	}
	return out, nil
}

func randomToken() (string, error) {
	raw := make([]byte, flowSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("no randomness available: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Hash produces the stored form of the three persisted values.
func Hash(v string) [32]byte { return sha256.Sum256([]byte(v)) }

// CodeChallenge derives the challenge from the verifier.
//
// The specification's alternative method sets the challenge equal to the
// verifier, letting anyone able to read the authorization request redeem the
// code. That method is not offered here.
func (f FlowSecrets) CodeChallenge() string {
	sum := sha256.Sum256([]byte(f.CodeVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL constructs the URL the browser is directed to.
//
// The redirect URI arrives as an argument instead of being configured here. A
// deployment reachable under multiple names registers several of them, and the
// applicable one is selected per request by the layer owning the host list. The
// identical string must reach the exchange unchanged.
func (c *Client) AuthorizeURL(ctx context.Context, redirectURI string, f FlowSecrets) (string, error) {
	doc, err := c.FetchDiscovery(ctx)
	if err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(c.scopes(), " "))
	q.Set("state", f.State)
	q.Set("nonce", f.Nonce)
	q.Set("code_challenge", f.CodeChallenge())
	q.Set("code_challenge_method", "S256")

	sep := "?"
	if strings.Contains(doc.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return doc.AuthorizationEndpoint + sep + q.Encode(), nil
}

// EndSessionURL constructs the provider's RP-initiated logout URL, or
// answers empty when the document names no end_session_endpoint: a provider
// with no logout support leaves the caller with nothing to redirect to, and
// today's local-only logout is exactly the right fallback for that case.
//
// idTokenHint is optional per the specification and is included whenever the
// caller holds one, since it is what lets the provider end the specific
// session this browser holds rather than every session the identity has.
func (c *Client) EndSessionURL(ctx context.Context, idTokenHint, postLogoutRedirectURI string) (string, error) {
	doc, err := c.FetchDiscovery(ctx)
	if err != nil {
		return "", err
	}
	if doc.EndSessionEndpoint == "" {
		return "", nil
	}

	q := url.Values{}
	q.Set("client_id", c.cfg.ClientID)
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}
	if postLogoutRedirectURI != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirectURI)
	}

	sep := "?"
	if strings.Contains(doc.EndSessionEndpoint, "?") {
		sep = "&"
	}
	return doc.EndSessionEndpoint + sep + q.Encode(), nil
}

// scopes always includes the one that causes an identity token to be issued.
// Omitting it leaves the provider authenticating and returning nothing this
// server can verify, producing a flow incapable of succeeding.
func (c *Client) scopes() []string {
	out := slices.Clone(c.cfg.Scopes)
	if !slices.Contains(out, "openid") {
		out = append([]string{"openid"}, out...)
	}
	return out
}

// clientAuth selects the authentication method used at the token endpoint.
type clientAuth int

const (
	// authBasic is both the specification's preferred method and the default
	// here.
	authBasic clientAuth = iota
	// authPost places the secret in the request body, for providers that do not
	// support the preferred method.
	authPost
	// authNone sends no credential at all. Only a client declared public may
	// use it, and only when the document advertises it: PKCE closes the code
	// interception a public client is otherwise exposed to, but that does not
	// make "none" a safe default for a client this deployment never marked
	// public.
	authNone
)

// clientAuthMethod chooses among the methods the document advertises.
//
// A missing or empty list selects the preferred method, which the specification
// defines as the default rather than being a guess made here. public is
// tested first and only wins the choice when the document also advertises
// "none": a client not declared public keeps presenting a secret exactly as
// before, and a public client pointed at a document that never lists "none"
// falls through to the same basic/post ladder every other client uses.
func clientAuthMethod(doc *Discovery, public bool) (clientAuth, error) {
	advertised := doc.TokenEndpointAuthMethodsSupported
	if public && slices.Contains(advertised, "none") {
		return authNone, nil
	}
	switch {
	case len(advertised) == 0:
		return authBasic, nil
	case slices.Contains(advertised, "client_secret_basic"):
		return authBasic, nil
	case slices.Contains(advertised, "client_secret_post"):
		return authPost, nil
	}
	// The other methods require key material this product has no place to
	// store, so this is not a recoverable runtime error. The deployment cannot
	// reach this provider until the client is registered differently.
	return 0, &DiscoveryError{
		Field:  "token_endpoint_auth_methods_supported",
		Reason: "names no client authentication method this build implements",
	}
}

// tokenResponse is what comes back. Only the identity token is read: an
// access token this server does not use is a credential it would be holding.
type tokenResponse struct {
	IDToken string `json:"id_token"`
	// The error fields are parsed so a rejection can repeat what the provider
	// reported, separating a correctable configuration from an unexplained
	// failure.
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Exchange carries out the back-channel code exchange.
//
// Each connection clears the address guard at dial time, so a hostname that
// resolved acceptably cannot resolve to something different between the check
// and the socket.
func (c *Client) Exchange(
	ctx context.Context, code, redirectURI string, f FlowSecrets,
) (string, error) {
	doc, err := c.FetchDiscovery(ctx)
	if err != nil {
		return "", err
	}
	method, err := clientAuthMethod(doc, c.cfg.PublicClient)
	if err != nil {
		return "", err
	}

	form := url.Values{}
	// Both of the first two are required by a conforming provider and both are
	// easy to leave out, because the exchange appears to work against the
	// providers that tolerate their absence.
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	form.Set("code", code)
	form.Set("code_verifier", f.CodeVerifier)

	var basic *basicAuth
	switch method {
	case authBasic:
		basic = &basicAuth{User: c.cfg.ClientID, Pass: string(c.cfg.ClientSecret.Reveal())}
	case authPost:
		form.Set("client_id", c.cfg.ClientID)
		form.Set("client_secret", string(c.cfg.ClientSecret.Reveal()))
	case authNone:
		// RFC 6749 §3.2.1: a client that does not authenticate at the token
		// endpoint still identifies itself, and this is the one place that
		// identification happens. No Authorization header, no client_secret:
		// PKCE's code_verifier above is what proves this exchange belongs to
		// the party that started the authorization request.
		form.Set("client_id", c.cfg.ClientID)
	}

	body, err := c.postForm(ctx, doc.TokenEndpoint, form, basic)
	if err != nil {
		return "", err
	}

	var res tokenResponse
	if uerr := json.Unmarshal(body, &res); uerr != nil {
		return "", &ProviderError{Reason: "the token response is not JSON"}
	}
	if res.Error != "" {
		return "", &ProviderError{Reason: "refused the exchange: " + res.Error}
	}
	if res.IDToken == "" {
		return "", &ProviderError{Reason: "the token response carries no identity token"}
	}
	return res.IDToken, nil
}
