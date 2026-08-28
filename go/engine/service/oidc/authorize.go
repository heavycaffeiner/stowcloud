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

// One sign-in attempt needs four random values, and they are easy to confuse,
// so what each defends:
//
//   - The state stops an attacker choosing the value the callback carries.
//   - The binding cookie stops an attacker delivering a legitimate state to
//     somebody else's browser, which is the sign-in forgery the state alone
//     does not prevent.
//   - The nonce ties the identity token to this authorization request.
//   - The verifier ties the code redemption to whoever started the attempt.

// flowSecretBytes is 256 bits, which is also exactly what the code exchange
// wants of a verifier.
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

// String redacts, because these are exactly the values a stray log field
// leaks.
func (FlowSecrets) String() string { return "FlowSecrets(redacted)" }

// GoString redacts for the same reason: a struct printed with the verbose
// verb bypasses String.
func (FlowSecrets) GoString() string { return "FlowSecrets(redacted)" }

// MarshalJSON redacts as well, so a value that reaches a response body or a
// structured log carries nothing.
func (FlowSecrets) MarshalJSON() ([]byte, error) {
	return []byte(`"FlowSecrets(redacted)"`), nil
}

// NewFlowSecrets mints the four.
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

// Hash is what the three stored values are stored as.
func Hash(v string) [32]byte { return sha256.Sum256([]byte(v)) }

// CodeChallenge is the challenge derived from the verifier.
//
// The other method the specification allows makes the challenge equal to the
// verifier, so anyone who can read the authorization request can redeem the
// code. It is not offered here.
func (f FlowSecrets) CodeChallenge() string {
	sum := sha256.Sum256([]byte(f.CodeVerifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL builds the URL the browser is sent to.
//
// The redirect URI is passed in rather than configured here, because a
// deployment reached under several names registers several of them, and which
// one applies is decided per request by the layer that owns the host list.
// The same string has to reach the exchange byte for byte.
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

// scopes always carries the one that makes an identity token be issued at
// all. Without it the provider authenticates and returns nothing this server
// can verify, so leaving it out is a flow that cannot succeed.
func (c *Client) scopes() []string {
	out := slices.Clone(c.cfg.Scopes)
	if !slices.Contains(out, "openid") {
		out = append([]string{"openid"}, out...)
	}
	return out
}

// clientAuth is how this authenticates to the token endpoint.
type clientAuth int

const (
	// authBasic is the method the specification prefers and the default.
	authBasic clientAuth = iota
	// authPost carries the secret in the body, for providers not offering the
	// first.
	authPost
)

// clientAuthMethod picks from what the document advertises.
//
// An absent or empty list means the first, which is what the specification
// defines the default to be rather than a guess made here.
func clientAuthMethod(doc *Discovery) (clientAuth, error) {
	advertised := doc.TokenEndpointAuthMethodsSupported
	switch {
	case len(advertised) == 0:
		return authBasic, nil
	case slices.Contains(advertised, "client_secret_basic"):
		return authBasic, nil
	case slices.Contains(advertised, "client_secret_post"):
		return authPost, nil
	}
	// The remaining methods need key material this product has nowhere to put,
	// so this is not a runtime failure to recover from: the deployment cannot
	// talk to this provider until the client is registered differently.
	return 0, &DiscoveryError{
		Field:  "token_endpoint_auth_methods_supported",
		Reason: "names no client authentication method this build implements",
	}
}

// tokenResponse is what comes back. Only the identity token is read: an
// access token this server does not use is a credential it would be holding.
type tokenResponse struct {
	IDToken string `json:"id_token"`
	// The error fields are read so a refusal can say what the provider said,
	// which is the difference between a fixable configuration and a mystery.
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Exchange performs the back-channel code exchange.
//
// Every connection it makes passes the address guard at dial time, so a
// hostname that resolved acceptably cannot be re-resolved to something else
// between the check and the socket.
func (c *Client) Exchange(
	ctx context.Context, code, redirectURI string, f FlowSecrets,
) (string, error) {
	doc, err := c.FetchDiscovery(ctx)
	if err != nil {
		return "", err
	}
	method, err := clientAuthMethod(doc)
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
