//go:build linux

package lifecycle_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
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
	"sync"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// A fake OpenID Connect provider, real enough to drive the whole flow: real
// discovery, a real JWKS, and a real RS256-signed identity token. It never
// checks the authorization code or the PKCE verifier, because
// service/oidc's own tests already prove the client sends both correctly;
// this exists to get a verifying token into lifecycle's hands so what
// lifecycle does with one can be tested.
type fakeProvider struct {
	t      *testing.T
	key    *rsa.PrivateKey
	srv    *httptest.Server
	URL    string
	CACert string

	mu      sync.Mutex
	nonce   string
	subject string
}

const fakeProviderKid = "test-key"

// newFakeProvider starts the server and writes its certificate to a file the
// oidc client can be pointed at with ca_cert_file.
func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the fake provider's key: %v", err)
	}
	fp := &fakeProvider{t: t, key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", fp.discovery)
	mux.HandleFunc("/jwks", fp.jwks)
	mux.HandleFunc("/token", fp.token)

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	fp.srv = srv
	fp.URL = srv.URL

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	path := filepath.Join(t.TempDir(), "provider-ca.pem")
	if werr := os.WriteFile(path, certPEM, 0o600); werr != nil {
		t.Fatalf("writing the fake provider's certificate: %v", werr)
	}
	fp.CACert = path
	return fp
}

// setToken names the nonce and subject the next minted identity token
// carries. The nonce comes from the authorize_url a flow just produced; the
// subject is whichever identity this attempt should present.
func (fp *fakeProvider) setToken(nonce, subject string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.nonce, fp.subject = nonce, subject
}

func (fp *fakeProvider) discovery(w http.ResponseWriter, _ *http.Request) {
	doc := map[string]any{
		"issuer":                 fp.URL,
		"authorization_endpoint": fp.URL + "/authorize",
		"token_endpoint":         fp.URL + "/token",
		"jwks_uri":               fp.URL + "/jwks",
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		fp.t.Errorf("fake provider: writing discovery: %v", err)
	}
}

func (fp *fakeProvider) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := fp.key.PublicKey
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	set := map[string]any{
		"keys": []map[string]any{
			{"kty": "RSA", "kid": fakeProviderKid, "use": "sig", "n": n, "e": e},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(set); err != nil {
		fp.t.Errorf("fake provider: writing the key set: %v", err)
	}
}

func (fp *fakeProvider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		fp.t.Errorf("fake provider: parsing the token request: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	clientID := r.PostFormValue("client_id")
	if clientID == "" {
		if user, _, ok := r.BasicAuth(); ok {
			clientID = user
		}
	}

	fp.mu.Lock()
	nonce, subject := fp.nonce, fp.subject
	fp.mu.Unlock()

	now := clock.System().Now()
	idToken, err := fp.sign(map[string]any{
		"iss": fp.URL, "sub": subject, "aud": clientID, "nonce": nonce,
		"exp": now.Add(10 * time.Minute).Unix(), "iat": now.Unix(),
	})
	if err != nil {
		fp.t.Errorf("fake provider: signing the identity token: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"id_token": idToken}); err != nil {
		fp.t.Errorf("fake provider: writing the token response: %v", err)
	}
}

// sign produces a compact RS256 JWS the way jws.go verifies one: the digest
// is SHA-256 of the two encoded segments joined by a dot, and the signature
// is PKCS#1 v1.5 over that digest.
func (fp *fakeProvider) sign(claims map[string]any) (string, error) {
	header := map[string]any{"alg": "RS256", "kid": fakeProviderKid, "typ": "JWT"}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	pb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, fp.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// oidcFixture is a served engine with an administrator and one ordinary
// account, ready for the admin to point at a fake provider.
type oidcFixture struct {
	base        string
	engine      *lifecycle.Engine
	adminCookie *http.Cookie
	adminCSRF   string
	userCookie  *http.Cookie
	userCSRF    string
	userID      int64
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	if _, cerr := e.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatalf("creating the administrator: %v", cerr)
	}
	userID, cerr := e.Auth.CreateUser(ctx, loginName, "Alice", pwOf(loginPassword))
	if cerr != nil {
		t.Fatalf("creating the account: %v", cerr)
	}

	base := serve(t, e)
	admin := postJSON(t, base+"/api/v1/auth/login", map[string]string{"login": "root", "password": loginPassword})
	if admin.sessionCookie() == nil {
		t.Fatalf("the administrator did not sign in: %d %v", admin.status, admin.body)
	}
	user := postJSON(t, base+"/api/v1/auth/login", map[string]string{"login": loginName, "password": loginPassword})
	if user.sessionCookie() == nil {
		t.Fatalf("the account did not sign in: %d %v", user.status, user.body)
	}

	return &oidcFixture{
		base: base, engine: e,
		adminCookie: admin.sessionCookie(), adminCSRF: admin.field("csrf"),
		userCookie: user.sessionCookie(), userCSRF: user.field("csrf"),
		userID: userID,
	}
}

// configureProvider points the deployment at fp as its single sign-on
// provider, over the admin API exactly the way an operator would.
func (f *oidcFixture) configureProvider(t *testing.T, fp *fakeProvider, clientID string) {
	t.Helper()
	status, body := mutate(t, http.MethodPatch, f.base+"/api/v1/admin/settings/oidc",
		f.adminCookie, f.adminCSRF, map[string]any{
			"enabled":                 true,
			"issuer":                  fp.URL,
			"client_id":               clientID,
			"client_secret":           "a-provider-issued-secret",
			"allow_private_endpoints": true,
			"ca_cert_file":            fp.CACert,
		})
	if status != http.StatusOK {
		t.Fatalf("configuring the provider answered %d: %v", status, body)
	}
}

// noRedirectClient never follows a redirect, so a test can read the status
// and the Location header the handler actually produced.
func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout:       5 * time.Second,
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// oidcBindingCookieName mirrors the unexported constant lifecycle keeps for
// the same value: the browser-binding cookie a flow's start response sets
// and its callback consumes.
const oidcBindingCookieName = "__Host-sc_oidc"

// findCookie locates one cookie a response set.
func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// startFlow drives a start endpoint (sign-in or account-linking) and returns
// the state, nonce and binding cookie a browser would carry to the
// callback.
func startFlow(t *testing.T, req *http.Request) (state, nonce string, binding *http.Cookie) {
	t.Helper()

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("starting the flow: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("starting the flow answered %d: %s", resp.StatusCode, readAll(t, resp))
	}

	var view map[string]any
	if derr := json.NewDecoder(resp.Body).Decode(&view); derr != nil {
		t.Fatalf("decoding the start response: %v", derr)
	}
	authorizeURL, ok := view["authorize_url"].(string)
	if !ok {
		t.Fatalf("the start response carries no authorize_url string: %v", view)
	}
	u, perr := url.Parse(authorizeURL)
	if perr != nil {
		t.Fatalf("parsing authorize_url %q: %v", authorizeURL, perr)
	}
	q := u.Query()
	state, nonce = q.Get("state"), q.Get("nonce")
	if state == "" || nonce == "" {
		t.Fatalf("authorize_url carries no state or nonce: %s", authorizeURL)
	}
	binding = findCookie(resp.Cookies(), oidcBindingCookieName)
	if binding == nil {
		t.Fatalf("starting the flow set no binding cookie")
	}
	return state, nonce, binding
}

// callback builds the browser's landing request at the callback route,
// carrying the binding cookie startFlow captured and, optionally, a signed-in
// session.
func callbackRequest(base, state string, binding *http.Cookie, session *http.Cookie) *http.Request {
	req, err := http.NewRequest(http.MethodGet,
		base+"/api/v1/auth/oidc/callback?code=a-fake-authorization-code&state="+url.QueryEscape(state), nil)
	if err != nil {
		panic(err)
	}
	req.AddCookie(binding)
	if session != nil {
		req.AddCookie(session)
	}
	return req
}

// A callback failure never answers JSON: it redirects, because it was
// reached by a full navigation the provider issued and a raw document left
// on screen is a sign-on that silently went nowhere. This is the regression
// test for that defect: before the fix, every one of these answered 200 or
// 4xx with a JSON body and no Location header at all.
func TestCallbackFailuresRedirectRatherThanAnsweringJSON(t *testing.T) {
	f := newOIDCFixture(t)
	fp := newFakeProvider(t)
	f.configureProvider(t, fp, "stowcloud")

	nc := noRedirectClient()

	// No session on the request: an unknown state during what looks like a
	// sign-in attempt lands back on the login screen.
	req, err := http.NewRequest(http.MethodGet,
		f.base+"/api/v1/auth/oidc/callback?code=anything&state=never-issued", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := nc.Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("an invented state answered %d, not a redirect: %s", resp.StatusCode, readAll(t, resp))
	}
	loc := resp.Header.Get("Location")
	if loc != "/login?oidc_error=oidc.bad_state" {
		t.Errorf("the redirect target is %q", loc)
	}

	// The same failure, but with a session already on the request: the
	// browser is treated as mid account-linking attempt instead, since only
	// that flow ever begins with one.
	req2, err := http.NewRequest(http.MethodGet,
		f.base+"/api/v1/auth/oidc/callback?code=anything&state=never-issued", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req2.AddCookie(f.userCookie)
	resp2, err := nc.Do(req2)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp2.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp2.StatusCode != http.StatusFound {
		t.Fatalf("an invented state with a session answered %d: %s", resp2.StatusCode, readAll(t, resp2))
	}
	if loc2 := resp2.Header.Get("Location"); loc2 != "/settings/security?oidc_error=oidc.bad_state" {
		t.Errorf("the redirect target is %q", loc2)
	}
}

// A successful sign-in redirects the browser with the session cookie set,
// and stamps the link as just used. Before the fix, this answered a JSON
// body with no Location header, so the browser never returned to the app,
// and TouchOIDCLink was never called at all so an account signing in every
// day still reported "never" for its last use.
func TestSuccessfulSignOnRedirectsAndEstablishesASession(t *testing.T) {
	f := newOIDCFixture(t)
	fp := newFakeProvider(t)
	f.configureProvider(t, fp, "stowcloud")

	ctx := context.Background()
	if lerr := f.engine.Auth.CreateOIDCLink(ctx, f.userID, fp.URL, "alice-subject"); lerr != nil {
		t.Fatalf("pre-linking the identity: %v", lerr)
	}

	req, err := http.NewRequest(http.MethodGet, f.base+"/api/v1/auth/oidc/start?return_to=%2Fb%2FDocs", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	state, nonce, binding := startFlow(t, req)
	fp.setToken(nonce, "alice-subject")

	nc := noRedirectClient()
	resp, err := nc.Do(callbackRequest(f.base, state, binding, nil))
	if err != nil {
		t.Fatalf("completing the callback: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("a successful sign-in answered %d, not a redirect: %s", resp.StatusCode, readAll(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/b/Docs" {
		t.Errorf("the redirect target is %q, want the return_to the flow began with", loc)
	}
	if sc := findCookie(resp.Cookies(), "__Host-sc_sid"); sc == nil || sc.Value == "" {
		t.Error("a successful sign-in set no session cookie")
	}

	link, lerr := f.engine.Auth.OIDCLinkOf(ctx, f.userID)
	if lerr != nil {
		t.Fatalf("reading the link back: %v", lerr)
	}
	if link.LastLoginNs == nil {
		t.Error("a sign-in through the provider left last_login_ns unset")
	}
}

// An identity the provider vouches for but nobody has linked signs nobody
// in: the provider authenticates, and only the local database decides who
// may have an account.
func TestUnlinkedIdentityIsRefusedAtSignOn(t *testing.T) {
	f := newOIDCFixture(t)
	fp := newFakeProvider(t)
	f.configureProvider(t, fp, "stowcloud")

	req, err := http.NewRequest(http.MethodGet, f.base+"/api/v1/auth/oidc/start", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	state, nonce, binding := startFlow(t, req)
	fp.setToken(nonce, "an-orphan-subject")

	resp, err := noRedirectClient().Do(callbackRequest(f.base, state, binding, nil))
	if err != nil {
		t.Fatalf("completing the callback: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("an unlinked identity answered %d: %s", resp.StatusCode, readAll(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/login?oidc_error=oidc.not_linked" {
		t.Errorf("the redirect target is %q", loc)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-sc_sid" && c.Value != "" {
			t.Error("an unlinked identity produced a session cookie")
		}
	}
}

// A successful link attaches the identity and lands the browser back where
// it started.
func TestSuccessfulLinkRedirectsToReturnTo(t *testing.T) {
	f := newOIDCFixture(t)
	fp := newFakeProvider(t)
	f.configureProvider(t, fp, "stowcloud")

	req, err := http.NewRequest(http.MethodPost, f.base+"/api/v1/account/oidc-link/start",
		jsonBody(t, map[string]string{"current": loginPassword, "return_to": "/settings"}))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.userCookie)
	req.Header.Set("Sc-Csrf", f.userCSRF)
	state, nonce, binding := startFlow(t, req)
	fp.setToken(nonce, "alices-new-identity")

	resp, err := noRedirectClient().Do(callbackRequest(f.base, state, binding, f.userCookie))
	if err != nil {
		t.Fatalf("completing the callback: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("a successful link answered %d: %s", resp.StatusCode, readAll(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/settings" {
		t.Errorf("the redirect target is %q", loc)
	}

	link, lerr := f.engine.Auth.OIDCLinkOf(context.Background(), f.userID)
	if lerr != nil {
		t.Fatalf("reading the link back: %v", lerr)
	}
	if link.Subject != "alices-new-identity" {
		t.Errorf("the linked subject is %q", link.Subject)
	}
}

// The account that finishes a link must be the one that started it. Between
// the redirect out and the redirect back the session can change underneath
// the flow (signed out, or somebody else signed in on the same browser), and
// the flow row alone cannot tell: it names who started it, not who is here
// now. Before this check, completing the callback with no session at all, or
// a different one, still attached the identity to whoever started the flow.
func TestALinkWhoseSessionChangedIsRefused(t *testing.T) {
	f := newOIDCFixture(t)
	fp := newFakeProvider(t)
	f.configureProvider(t, fp, "stowcloud")

	req, err := http.NewRequest(http.MethodPost, f.base+"/api/v1/account/oidc-link/start",
		jsonBody(t, map[string]string{"current": loginPassword}))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.userCookie)
	req.Header.Set("Sc-Csrf", f.userCSRF)
	state, nonce, binding := startFlow(t, req)
	fp.setToken(nonce, "somebody-elses-identity")

	// No session at all on the callback: signed out while the provider
	// redirect was in flight.
	resp, err := noRedirectClient().Do(callbackRequest(f.base, state, binding, nil))
	if err != nil {
		t.Fatalf("completing the callback: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("a session-changed link answered %d: %s", resp.StatusCode, readAll(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/settings/security?oidc_error=oidc.link_session_changed" {
		t.Errorf("the redirect target is %q", loc)
	}

	if _, lerr := f.engine.Auth.OIDCLinkOf(context.Background(), f.userID); lerr == nil {
		t.Error("a session-changed callback still attached the identity")
	}
}

// An identity already linked to a different account is refused, and the
// account attempting the link stays unlinked.
func TestALinkToAnAlreadyLinkedIdentityIsRefused(t *testing.T) {
	f := newOIDCFixture(t)
	fp := newFakeProvider(t)
	f.configureProvider(t, fp, "stowcloud")

	ctx := context.Background()
	otherID, cerr := f.engine.Auth.CreateUser(ctx, "carol", "Carol", pwOf(loginPassword))
	if cerr != nil {
		t.Fatalf("creating the other account: %v", cerr)
	}
	if lerr := f.engine.Auth.CreateOIDCLink(ctx, otherID, fp.URL, "shared-subject"); lerr != nil {
		t.Fatalf("pre-linking the other account: %v", lerr)
	}

	req, err := http.NewRequest(http.MethodPost, f.base+"/api/v1/account/oidc-link/start",
		jsonBody(t, map[string]string{"current": loginPassword}))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.userCookie)
	req.Header.Set("Sc-Csrf", f.userCSRF)
	state, nonce, binding := startFlow(t, req)
	fp.setToken(nonce, "shared-subject")

	resp, err := noRedirectClient().Do(callbackRequest(f.base, state, binding, f.userCookie))
	if err != nil {
		t.Fatalf("completing the callback: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("an already-linked identity answered %d: %s", resp.StatusCode, readAll(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/settings/security?oidc_error=oidc.subject_already_linked" {
		t.Errorf("the redirect target is %q", loc)
	}
	if _, lerr := f.engine.Auth.OIDCLinkOf(ctx, f.userID); lerr == nil {
		t.Error("a refused link still attached an identity to the requesting account")
	}
}

// The session's own view of the caller's identity link. Before this, GET
// /auth/session carried no oidc field at all, so the account's own settings
// screen always read `linked` as false regardless of the account's real
// state, and could never show the disconnect flow to somebody who needed it.
func TestSessionReportsTheAccountsOwnSignOnLink(t *testing.T) {
	f := newOIDCFixture(t)
	ctx := context.Background()

	status, body := withCookie(t, http.MethodGet, f.base+"/api/v1/auth/session", f.userCookie)
	if status != http.StatusOK {
		t.Fatalf("reading the session answered %d: %s", status, body)
	}
	var before map[string]any
	if derr := json.Unmarshal(body, &before); derr != nil {
		t.Fatal(derr)
	}
	oidcBefore, ok := before["oidc"].(map[string]any)
	if !ok {
		t.Fatalf("the session carries no oidc object at all: %s", body)
	}
	if boolField(oidcBefore, "linked") {
		t.Errorf("an unlinked account reports linked: %v", oidcBefore)
	}

	if lerr := f.engine.Auth.CreateOIDCLink(ctx, f.userID, "https://idp.example", "abcdefghijkl"); lerr != nil {
		t.Fatalf("linking: %v", lerr)
	}

	status, body = withCookie(t, http.MethodGet, f.base+"/api/v1/auth/session", f.userCookie)
	if status != http.StatusOK {
		t.Fatalf("reading the session answered %d: %s", status, body)
	}
	var after map[string]any
	if derr := json.Unmarshal(body, &after); derr != nil {
		t.Fatal(derr)
	}
	oidcAfter, ok := after["oidc"].(map[string]any)
	if !ok || !boolField(oidcAfter, "linked") {
		t.Fatalf("a linked account does not report it: %s", body)
	}
	hint, ok := oidcAfter["subject_hint"].(string)
	if !ok || hint == "" || strings.Contains(hint, "efgh") {
		t.Errorf("the subject hint is %q, want the ends of the subject and not its middle", hint)
	}
	// The full subject never reaches this response: it is the admin's
	// dedicated view that may carry it, not the account's own.
	if strings.Contains(string(body), "abcdefghijkl") {
		t.Errorf("the session response carries the whole subject: %s", body)
	}
}

// jsonBody encodes a request body for a plain *http.Request built by hand
// rather than through the mutate/postJSON helpers, which the flow-start
// calls above need for direct access to the response's cookies and body.
func jsonBody(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return bytes.NewReader(encoded)
}
