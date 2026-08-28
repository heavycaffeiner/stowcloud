package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// stubTransport answers from a table rather than a socket, so a provider's
// behaviour can be stated exactly. The address guard has its own tests
// against a real socket; these are about what this client does with what
// comes back.
type stubTransport struct {
	mu     sync.Mutex
	calls  map[string]int
	handle func(*http.Request) (*http.Response, error)
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[req.URL.String()]++
	s.mu.Unlock()
	return s.handle(req)
}

func (s *stubTransport) count(url string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[url]
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// stubbed builds a client whose transport is the given handler.
func stubbed(t *testing.T, clk clock.Clock, handle func(*http.Request) (*http.Response, error)) (*Client, *stubTransport) {
	t.Helper()
	tr := &stubTransport{handle: handle}
	c := &Client{
		cfg: Config{
			Issuer:       testIssuer,
			ClientID:     testClientID,
			ClientSecret: secret.New([]byte("a client secret")),
		},
		clk:       clk,
		discovery: newTTLCache[*Discovery](limits.OIDCDiscoveryTTL),
		jwks:      newTTLCache[[]jwk](limits.OIDCJWKSTTL),
		http: &http.Client{
			Transport: tr,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return &ProviderError{Reason: "answered with a redirect to " + req.URL.Redacted()}
			},
		},
	}
	return c, tr
}

func discoveryJSON(extra map[string]any) string {
	doc := map[string]any{
		"issuer":                 testIssuer,
		"authorization_endpoint": testIssuer + "/authorize",
		"token_endpoint":         testIssuer + "/token",
		"jwks_uri":               testIssuer + "/keys",
	}
	for k, v := range extra {
		if v == nil {
			delete(doc, k)
			continue
		}
		doc[k] = v
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestDiscoveryReadsAndValidatesTheDocument(t *testing.T) {
	ctx := context.Background()
	c, _ := stubbed(t, fixedClock(), func(*http.Request) (*http.Response, error) {
		return response(200, discoveryJSON(nil)), nil
	})

	doc, err := c.FetchDiscovery(ctx)
	if err != nil {
		t.Fatalf("FetchDiscovery: %v", err)
	}
	if doc.TokenEndpoint != testIssuer+"/token" || doc.JWKSURI != testIssuer+"/keys" {
		t.Fatalf("the document read back as %+v", doc)
	}
}

// The document names the endpoints this server posts a client secret to, so
// every field is validated before any of them is used.
func TestDiscoveryRefusesEveryUnusableDocument(t *testing.T) {
	ctx := context.Background()
	for name, body := range map[string]string{
		"a different issuer":          discoveryJSON(map[string]any{"issuer": "https://other.example.test"}),
		"a missing token endpoint":    discoveryJSON(map[string]any{"token_endpoint": nil}),
		"an endpoint over plain http": discoveryJSON(map[string]any{"token_endpoint": "http://idp.example.test/token"}),
		"an endpoint with no host":    discoveryJSON(map[string]any{"jwks_uri": "https:///keys"}),
		"an endpoint carrying credentials": discoveryJSON(map[string]any{
			"token_endpoint": "https://user:pass@idp.example.test/token"}),
		"an endpoint on a private address": discoveryJSON(map[string]any{
			"jwks_uri": "https://169.254.169.254/keys"}),
		"no algorithm this build verifies": discoveryJSON(map[string]any{
			"id_token_signing_alg_values_supported": []string{"HS256", "none"}}),
		"no client authentication this build implements": discoveryJSON(map[string]any{
			"token_endpoint_auth_methods_supported": []string{"private_key_jwt"}}),
		"not JSON at all": "{",
		// A repeated key means two answers to one question: a reader that
		// stops at the first and one that does not would disagree about which
		// server this is.
		"a repeated key": `{"issuer":"https://idp.example.test","issuer":"https://attacker.example.test",
			"authorization_endpoint":"https://idp.example.test/authorize",
			"token_endpoint":"https://idp.example.test/token",
			"jwks_uri":"https://idp.example.test/keys"}`,
	} {
		c, _ := stubbed(t, fixedClock(), func(*http.Request) (*http.Response, error) {
			return response(200, body), nil
		})
		if _, err := c.FetchDiscovery(ctx); !errors.Is(err, ErrDiscovery) {
			t.Fatalf("%s returned %v, want a discovery refusal", name, err)
		}
	}
}

// A document a provider is allowed to extend is read for what this package
// needs and no more.
func TestDiscoveryIgnoresFieldsItDoesNotRead(t *testing.T) {
	ctx := context.Background()
	body := discoveryJSON(map[string]any{
		"userinfo_endpoint":  testIssuer + "/userinfo",
		"claims_supported":   []string{"sub", "email"},
		"something_new_here": map[string]any{"nested": true},
	})
	c, _ := stubbed(t, fixedClock(), func(*http.Request) (*http.Response, error) {
		return response(200, body), nil
	})
	if _, err := c.FetchDiscovery(ctx); err != nil {
		t.Fatalf("a document with extra fields was refused: %v", err)
	}
}

// The guard validated one URL, and a redirect names another that nobody
// checked.
func TestTheBackChannelFollowsNoRedirect(t *testing.T) {
	ctx := context.Background()
	c, _ := stubbed(t, fixedClock(), func(req *http.Request) (*http.Response, error) {
		res := response(302, "")
		res.Header.Set("Location", "https://attacker.example.test"+req.URL.Path)
		return res, nil
	})
	if _, err := c.FetchDiscovery(ctx); !errors.Is(err, ErrProvider) {
		t.Fatalf("a redirecting provider returned %v", err)
	}
}

// The bound is charged while the body arrives: one checked at the end has
// already allocated what it was meant to refuse.
func TestAnOversizedBodyIsRefusedAndAnExactlySizedOneIsNot(t *testing.T) {
	ctx := context.Background()

	// A document padded to exactly the ceiling with a field this package
	// ignores, so the size rather than the content is what is under test.
	atBound := func(size int) string {
		base := discoveryJSON(map[string]any{"padding": ""})
		pad := size - len(base)
		if pad < 0 {
			t.Fatalf("the base document is already %d bytes", len(base))
		}
		return discoveryJSON(map[string]any{"padding": strings.Repeat("x", pad)})
	}

	c, _ := stubbed(t, fixedClock(), func(*http.Request) (*http.Response, error) {
		return response(200, atBound(limits.OIDCResponseBytes)), nil
	})
	if _, err := c.FetchDiscovery(ctx); err != nil {
		t.Fatalf("a body exactly at the bound was refused: %v", err)
	}

	c, _ = stubbed(t, fixedClock(), func(*http.Request) (*http.Response, error) {
		return response(200, atBound(limits.OIDCResponseBytes+1)), nil
	})
	if _, err := c.FetchDiscovery(ctx); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("a body one byte over the bound returned %v", err)
	}
}

func TestANonSuccessStatusIsReportedWithItsCode(t *testing.T) {
	ctx := context.Background()
	c, _ := stubbed(t, fixedClock(), func(*http.Request) (*http.Response, error) {
		return response(503, ""), nil
	})
	_, ferr := c.FetchDiscovery(ctx)
	var provider *ProviderError
	if !errors.As(ferr, &provider) {
		t.Fatalf("a failing provider returned %v", ferr)
	}
	if !strings.Contains(provider.Reason, "503") {
		t.Fatalf("the refusal does not name the status: %q", provider.Reason)
	}
}

// A provider that was down a second ago may be up now, so caching the failure
// would turn a one-second outage into an hour of them.
func TestAFailedFetchIsNeverCached(t *testing.T) {
	ctx := context.Background()
	var attempts int
	c, _ := stubbed(t, fixedClock(), func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("the provider is down")
		}
		return response(200, discoveryJSON(nil)), nil
	})

	if _, err := c.FetchDiscovery(ctx); err == nil {
		t.Fatal("the first fetch was expected to fail")
	}
	if _, err := c.FetchDiscovery(ctx); err != nil {
		t.Fatalf("the second fetch was refused from cache: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("the provider was called %d times, want 2", attempts)
	}
}

// A cached document is served inside its lifetime and refetched after, so a
// provider's own rotation is eventually noticed.
func TestTheDocumentIsCachedForItsLifetimeAndRefetchedAfter(t *testing.T) {
	ctx := context.Background()
	clk := &steppingClock{at: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	c, tr := stubbed(t, clk, func(*http.Request) (*http.Response, error) {
		return response(200, discoveryJSON(nil)), nil
	})
	url := DiscoveryURL(testIssuer)

	for i := 0; i < 3; i++ {
		if _, err := c.FetchDiscovery(ctx); err != nil {
			t.Fatalf("FetchDiscovery: %v", err)
		}
	}
	if n := tr.count(url); n != 1 {
		t.Fatalf("the provider was called %d times inside the lifetime, want 1", n)
	}

	clk.advance(limits.OIDCDiscoveryTTL + time.Second)
	if _, err := c.FetchDiscovery(ctx); err != nil {
		t.Fatalf("FetchDiscovery: %v", err)
	}
	if n := tr.count(url); n != 2 {
		t.Fatalf("the provider was called %d times after the lifetime, want 2", n)
	}
}

// A key set that has rotated is refetched once rather than being an outage
// lasting as long as the cache does.
func TestAnUnknownKeyRefetchesTheSetOnce(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()
	s := newSigner(t)

	rotated := s.rsaJWK()
	rotated.Kid = "key-2"
	setJSON, err := json.Marshal(jwkSet{Keys: []jwk{rotated}})
	if err != nil {
		t.Fatalf("encoding the key set: %v", err)
	}

	c, tr := stubbed(t, clk, func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/keys") {
			return response(200, string(setJSON)), nil
		}
		return response(200, discoveryJSON(nil)), nil
	})
	// The cache holds the old key, and the token names the new one.
	stale := s.rsaJWK()
	c.jwks.store([]jwk{stale}, clk)

	raw := s.sign(t, map[string]any{"alg": algRS256, "kid": "key-2"}, claimsAt(clk))
	if _, verr := c.VerifyIDToken(ctx, raw, testNonce); verr != nil {
		t.Fatalf("a rotated key was not picked up: %v", verr)
	}
	if n := tr.count(testIssuer + "/keys"); n != 1 {
		t.Fatalf("the key set was fetched %d times, want 1", n)
	}
}

func TestTheKeySetIsBoundedAndNonEmpty(t *testing.T) {
	ctx := context.Background()
	clk := fixedClock()

	tooMany := jwkSet{}
	for i := 0; i < limits.OIDCJWKSKeys+1; i++ {
		tooMany.Keys = append(tooMany.Keys, jwk{Kty: "RSA", Kid: fmt.Sprintf("k%d", i)})
	}
	big, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatalf("encoding the key set: %v", err)
	}

	for name, body := range map[string]string{
		"an empty set":  `{"keys":[]}`,
		"not JSON":      `{`,
		"too many keys": string(big),
	} {
		c, _ := stubbed(t, clk, func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/keys") {
				return response(200, body), nil
			}
			return response(200, discoveryJSON(nil)), nil
		})
		if _, err := c.fetchJWKS(ctx); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestTheAuthorizeURLCarriesTheChallengeAndTheOpenIDScope(t *testing.T) {
	ctx := context.Background()
	c, _ := stubbed(t, fixedClock(), func(*http.Request) (*http.Response, error) {
		return response(200, discoveryJSON(nil)), nil
	})
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}

	raw, err := c.AuthorizeURL(ctx, "https://cloud.example.test/callback", f)
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing the URL: %v", err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("the challenge method is %q", q.Get("code_challenge_method"))
	}
	if q.Get("code_challenge") != f.CodeChallenge() || q.Get("code_challenge") == f.CodeVerifier {
		t.Fatal("the challenge is not the derived one")
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("the scopes are %q, and without openid no identity token is issued", q.Get("scope"))
	}
	if q.Get("state") != f.State || q.Get("nonce") != f.Nonce {
		t.Fatal("the state and nonce are not this attempt's")
	}
}

// A provider whose authorize endpoint already carries a query keeps it.
func TestTheAuthorizeURLPreservesAnExistingQuery(t *testing.T) {
	ctx := context.Background()
	c, _ := stubbed(t, fixedClock(), func(*http.Request) (*http.Response, error) {
		return response(200, discoveryJSON(map[string]any{
			"authorization_endpoint": testIssuer + "/authorize?tenant=acme"})), nil
	})
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}
	raw, err := c.AuthorizeURL(ctx, "https://cloud.example.test/callback", f)
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing the URL: %v", err)
	}
	if u.Query().Get("tenant") != "acme" {
		t.Fatalf("the provider's own query was dropped: %s", raw)
	}
}

// The exchange authenticates the way the document advertises, and carries the
// two fields a conforming provider requires and a tolerant one does not.
func TestTheExchangeAuthenticatesAsAdvertised(t *testing.T) {
	ctx := context.Background()
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}

	for name, tc := range map[string]struct {
		advertised []string
		wantBasic  bool
	}{
		"the default when none is advertised": {nil, true},
		"basic when it is advertised":         {[]string{"client_secret_basic"}, true},
		"the body when only that is offered":  {[]string{"client_secret_post"}, false},
	} {
		var sawBasic bool
		var form url.Values
		c, _ := stubbed(t, fixedClock(), func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/token") {
				_, _, sawBasic = req.BasicAuth()
				body, rerr := io.ReadAll(req.Body)
				if rerr != nil {
					return nil, rerr
				}
				var perr error
				if form, perr = url.ParseQuery(string(body)); perr != nil {
					return nil, perr
				}
				return response(200, `{"id_token":"a.b.c"}`), nil
			}
			extra := map[string]any{}
			if tc.advertised != nil {
				extra["token_endpoint_auth_methods_supported"] = tc.advertised
			}
			return response(200, discoveryJSON(extra)), nil
		})

		token, xerr := c.Exchange(ctx, "the-code", "https://cloud.example.test/callback", f)
		if xerr != nil {
			t.Fatalf("%s: Exchange: %v", name, xerr)
		}
		if token != "a.b.c" {
			t.Fatalf("%s: the token is %q", name, token)
		}
		if sawBasic != tc.wantBasic {
			t.Fatalf("%s: basic authentication was %v", name, sawBasic)
		}
		if form.Get("grant_type") != "authorization_code" || form.Get("redirect_uri") == "" {
			t.Fatalf("%s: the form is %v", name, form)
		}
		if form.Get("code_verifier") != f.CodeVerifier {
			t.Fatalf("%s: the verifier was not presented", name)
		}
		if !tc.wantBasic && form.Get("client_secret") == "" {
			t.Fatalf("%s: the secret was not in the body", name)
		}
	}
}

// A refusal that repeats what the provider said is the difference between a
// fixable configuration and a mystery.
func TestARefusedExchangeSaysWhatTheProviderSaid(t *testing.T) {
	ctx := context.Background()
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}
	c, _ := stubbed(t, fixedClock(), func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/token") {
			return response(200, `{"error":"invalid_client"}`), nil
		}
		return response(200, discoveryJSON(nil)), nil
	})

	_, xerr := c.Exchange(ctx, "the-code", "https://cloud.example.test/callback", f)
	var provider *ProviderError
	if !errors.As(xerr, &provider) {
		t.Fatalf("the refusal is %v", xerr)
	}
	if !strings.Contains(provider.Reason, "invalid_client") {
		t.Fatalf("the refusal does not repeat the provider's reason: %q", provider.Reason)
	}
}

func TestATokenResponseWithoutAnIdentityTokenIsRefused(t *testing.T) {
	ctx := context.Background()
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}
	c, _ := stubbed(t, fixedClock(), func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/token") {
			// An access token this server does not use is a credential it
			// would then be holding, and it is not what the flow is for.
			return response(200, `{"access_token":"at","token_type":"Bearer"}`), nil
		}
		return response(200, discoveryJSON(nil)), nil
	})
	if _, xerr := c.Exchange(ctx, "code", "https://cloud.example.test/callback", f); !errors.Is(xerr, ErrProvider) {
		t.Fatalf("a response with no identity token returned %v", xerr)
	}
}

// A client secret carrying a plus or a space authenticates only if both ends
// agree on the encoding, so the halves are encoded before they are combined.
func TestTheSecretIsFormEncodedBeforeItIsCombined(t *testing.T) {
	if got := formEncode("a b+c"); got != "a+b%2Bc" {
		t.Fatalf("formEncode gave %q", got)
	}
	if got := formEncode("plain-value_1.2~3"); got != "plain-value_1.2~3" {
		t.Fatalf("an unreserved value was escaped: %q", got)
	}
}

// These are exactly the values a stray log field leaks.
func TestFlowSecretsRedactEverywhere(t *testing.T) {
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	for name, rendered := range map[string]string{
		"the plain verb":   fmt.Sprintf("%v", f),
		"the verbose verb": fmt.Sprintf("%#v", f),
		"a string":         fmt.Sprint(f),
		"JSON":             string(encoded),
	} {
		for _, value := range []string{f.State, f.Nonce, f.Binding, f.CodeVerifier} {
			if strings.Contains(rendered, value) {
				t.Fatalf("%s leaked a secret: %s", name, rendered)
			}
		}
	}
}

// The four are distinct: reusing one for another would make the defence it
// provides depend on the one it was copied from.
func TestTheFourFlowSecretsAreDistinct(t *testing.T) {
	f, err := NewFlowSecrets()
	if err != nil {
		t.Fatalf("NewFlowSecrets: %v", err)
	}
	seen := map[string]bool{}
	for _, v := range []string{f.State, f.Nonce, f.Binding, f.CodeVerifier} {
		if v == "" || seen[v] {
			t.Fatalf("the secrets are %v", []string{f.State, f.Nonce, f.Binding, f.CodeVerifier})
		}
		seen[v] = true
	}
	if Hash(f.State) == Hash(f.Nonce) {
		t.Fatal("two secrets hash alike")
	}
}

// steppingClock is a clock a test moves by hand, so a lifetime can be crossed
// without the test waiting for it.
type steppingClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *steppingClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

func (c *steppingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *steppingClock) Now() time.Time                  { return c.now() }
func (c *steppingClock) Since(t time.Time) time.Duration { return c.now().Sub(t) }
func (c *steppingClock) Nanos() int64                    { return c.now().UnixNano() }
