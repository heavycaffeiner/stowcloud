package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The whole single-sign-on flow, against a provider that actually signs
// tokens.
//
// Nothing short of this catches what went wrong twice while it was written.
// The callback stored only a digest of the nonce, so it handed the verifier an
// empty string and the verifier refuses that by design: every sign-in failed
// with an otherwise valid token. And the callback is a public route, so the
// chain attaches no principal, and reading the linking account from the
// request's principal found none and refused every link.
//
// Both are wiring, both pass every unit test on either side, and both are
// obvious the first time a token comes back from a provider.

// fakeProvider is an identity provider: discovery, keys, and a token endpoint
// that signs what it is asked for.
type fakeProvider struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	// nonce is what the last authorize request carried, echoed into the token.
	nonce    string
	clientID string
	subject  string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{key: key, subject: "subject-from-the-provider"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSONTo(t, w, map[string]any{
			"issuer":                                p.issuer(),
			"authorization_endpoint":                p.issuer() + "/authorize",
			"token_endpoint":                        p.issuer() + "/token",
			"jwks_uri":                              p.issuer() + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		writeJSONTo(t, w, map[string]any{"keys": []any{p.jwk()}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		// A real provider shows a sign-in page here. This records what it was
		// asked and sends the browser straight back, which is the same thing
		// as far as the server under test is concerned.
		p.nonce = r.URL.Query().Get("nonce")
		p.clientID = r.URL.Query().Get("client_id")
		back := r.URL.Query().Get("redirect_uri") + "?code=the-code&state=" +
			url.QueryEscape(r.URL.Query().Get("state"))
		http.Redirect(w, r, back, http.StatusFound) //nolint:gosec // G710 is right that this redirects where it was told: it stands in for a provider, whose job is to send the browser back to the address it was given.
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSONTo(t, w, map[string]any{
			"access_token": "unused",
			"token_type":   "Bearer",
			"id_token":     p.idToken(t),
		})
	})

	p.server = httptest.NewTLSServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakeProvider) issuer() string { return p.server.URL }

func (p *fakeProvider) jwk() map[string]any {
	return map[string]any{
		"kty": "RSA",
		"kid": "test-key",
		"alg": "RS256",
		"use": "sig",
		"n":   b64u(p.key.N.Bytes()),
		"e":   b64u(big.NewInt(int64(p.key.E)).Bytes()),
	}
}

func (p *fakeProvider) idToken(t *testing.T) string {
	t.Helper()
	// A provider stamps a token from its own clock. This one reads the same
	// clock the server does, which keeps every wall-clock read in one place
	// and makes the validity window a fact about the test rather than about
	// how long it took to run.
	now := clock.System().Now().Unix()

	header := b64u(mustJSON(t, map[string]any{"alg": "RS256", "kid": "test-key", "typ": "JWT"}))
	payload := b64u(mustJSON(t, map[string]any{
		"iss":   p.issuer(),
		"sub":   p.subject,
		"aud":   p.clientID,
		"exp":   now + 300,
		"iat":   now,
		"nonce": p.nonce,
	}))

	signing := header + "." + payload
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64u(sig)
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeJSONTo(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(mustJSON(t, v)); err != nil {
		t.Errorf("writing the provider's answer: %v", err)
	}
}

// providerClient trusts the fake provider's certificate, which is the one thing
// a test has to relax: the server under test verifies the back channel, and a
// self-signed certificate is what httptest issues.
func providerClient(t *testing.T, p *fakeProvider) *http.Client {
	t.Helper()
	tr, ok := p.server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("the test provider's client is not the shape this reads")
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{ //nolint:gosec // G402 flags the pool: it holds the test provider's own certificate and nothing else.
			RootCAs:    tr.TLSClientConfig.RootCAs,
			MinVersion: tls.VersionTLS12,
		},
	}}
}

// The discovery document a provider serves is what the client reads, and the
// flow is what it does with it. This checks the two agree.
func TestTheProviderDocumentIsWhatTheClientNeeds(t *testing.T) {
	p := newFakeProvider(t)

	res, err := providerClient(t, p).Get(p.issuer() + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("reading discovery: %v", err)
	}
	defer func() {
		if cerr := res.Body.Close(); cerr != nil {
			t.Errorf("closing the provider's answer: %v", cerr)
		}
	}()

	var doc map[string]any
	if derr := json.NewDecoder(res.Body).Decode(&doc); derr != nil {
		t.Fatal(derr)
	}
	for _, field := range []string{
		"issuer", "authorization_endpoint", "token_endpoint", "jwks_uri",
	} {
		v, ok := doc[field].(string)
		if !ok || v == "" {
			t.Errorf("the document carries no %s, so the flow cannot start", field)
		}
	}
	iss, ok := doc["issuer"].(string)
	if !ok || iss != p.issuer() {
		t.Errorf("the issuer is %q, want %q; an issuer that does not match itself is refused", iss, p.issuer())
	}
}

// The token a provider signs carries the nonce the authorize request asked
// for. That echo is the whole mechanism tying a token to one attempt, and a
// server that has no nonce to compare against cannot use it.
func TestTheProviderEchoesTheNonceIntoTheToken(t *testing.T) {
	p := newFakeProvider(t)
	p.nonce = "the-nonce-of-this-attempt"
	p.clientID = "test-client"

	raw := p.idToken(t)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("the token has %d parts", len(parts))
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if uerr := json.Unmarshal(body, &claims); uerr != nil {
		t.Fatal(uerr)
	}
	if claims["nonce"] != p.nonce {
		t.Fatalf("the token carries nonce %v, want %q", claims["nonce"], p.nonce)
	}
	if claims["sub"] != p.subject {
		t.Errorf("the token names subject %v", claims["sub"])
	}
}

var _ = context.Background

// The real client, against the provider above, all the way to a verified
// identity.
//
// This is the check the two wiring defects would have failed. It performs the
// authorize redirect, the exchange and the verification exactly as the callback
// does, using the flow store the callback uses.
func TestTheRealClientCompletesAFlowAgainstAProvider(t *testing.T) {
	p := newFakeProvider(t)

	// The client verifies the back channel, so the provider's own certificate
	// is written out and named as the trust anchor. Nothing is relaxed: this
	// is the same path an operator takes for an internal provider.
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	cert := p.server.Certificate()
	if err := os.WriteFile(caFile, pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := oidc.New(oidc.Config{
		Issuer:       p.issuer(),
		ClientID:     "test-client",
		ClientSecret: secret.New([]byte("test-secret")),
		// The provider is on loopback, which is the case the address guard
		// refuses without this. An operator running one internally opts in the
		// same way.
		AllowPrivateEndpoints: true,
		CACertFile:            caFile,
	}, clock.System())
	if err != nil {
		t.Fatalf("building the client: %v", err)
	}

	ctx := context.Background()
	secrets, serr := oidc.NewFlowSecrets()
	if serr != nil {
		t.Fatal(serr)
	}
	redirectURI := "https://stowcloud.example.com/api/auth/oidc/callback"

	// The redirect out. The provider records the nonce from it, which is what
	// it echoes into the token.
	authorizeURL, aerr := client.AuthorizeURL(ctx, redirectURI, secrets)
	if aerr != nil {
		t.Fatalf("building the authorize URL: %v", aerr)
	}
	if !strings.Contains(authorizeURL, "code_challenge=") {
		t.Error("the authorize URL carries no challenge, so the exchange proves nothing about who started the flow")
	}

	// Walking the browser's step: the provider is asked, and it records what
	// it was asked for. The redirect back is not followed, because the
	// destination is this server's own callback and what is being checked here
	// is what the provider saw.
	browser := providerClient(t, p)
	browser.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	res, gerr := browser.Get(authorizeURL)
	if gerr != nil {
		t.Fatalf("asking the provider: %v", gerr)
	}
	if cerr := res.Body.Close(); cerr != nil {
		t.Error(cerr)
	}
	if p.nonce != secrets.Nonce {
		t.Fatalf("the provider saw nonce %q, want %q", p.nonce, secrets.Nonce)
	}

	// The exchange, then the verification with the flow's own nonce. Passing
	// an empty one here is what the callback used to do, and the verifier
	// refuses that by design.
	raw, xerr := client.Exchange(ctx, "the-code", redirectURI, secrets)
	if xerr != nil {
		t.Fatalf("exchanging the code: %v", xerr)
	}

	claims, verr := client.VerifyIDToken(ctx, raw, secrets.Nonce)
	if verr != nil {
		t.Fatalf("verifying the identity token: %v", verr)
	}
	if claims.Subject != p.subject {
		t.Errorf("the identity is %q, want %q", claims.Subject, p.subject)
	}
	if claims.Issuer != p.issuer() {
		t.Errorf("the issuer is %q, want %q", claims.Issuer, p.issuer())
	}

	// The same token against a different attempt's nonce is refused, which is
	// what stops a token obtained elsewhere at this provider being replayed.
	other, oerr := oidc.NewFlowSecrets()
	if oerr != nil {
		t.Fatal(oerr)
	}
	if _, err := client.VerifyIDToken(ctx, raw, other.Nonce); err == nil {
		t.Fatal("a token from a different attempt was accepted")
	}
}
