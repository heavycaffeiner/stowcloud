//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// session is a signed-in browser's credential: the cookie the login response
// set, and the CSRF token that has to ride beside it on every mutation.
//
// The native API now admits only this. middleware.Scope refuses every
// non-public route unless the resolved principal is a session cookie, so a
// helper that drives /api/v1 needs the cookie-and-token pair rather than a
// device credential's token string.
type session struct {
	cookie *http.Cookie
	csrf   string
}

// attach adds the session to a request: the cookie always, and the CSRF
// header for every method but GET and HEAD, which is what the chain's own
// CSRFRequired rule demands of a session credential.
//
// The zero session attaches nothing. That is how a test drives a route
// anonymously through the same helper, rather than through a second one that
// would drift from this.
func (s session) attach(req *http.Request) {
	if s.cookie == nil {
		return
	}
	req.AddCookie(s.cookie)
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		req.Header.Set(middleware.CSRFHeader, s.csrf)
	}
}

// signIn signs an account in over HTTP and returns its session, failing the
// test if the sign-in produced no cookie.
func signIn(t *testing.T, base, login, password string) session {
	t.Helper()

	resp := postJSON(t, base+"/api/v1/auth/login", map[string]string{"login": login, "password": password})
	cookie := resp.sessionCookie()
	if cookie == nil {
		t.Fatalf("%s could not sign in: %v", login, resp.body)
	}
	return session{cookie: cookie, csrf: resp.field("csrf")}
}

// bootWithUser serves an engine holding one account, and returns the base
// URL, an app password for it, and a session for the same account.
//
// Both credentials, because the two surfaces take different ones: the DAV and
// compat mounts resolve a device credential directly, and the native API
// admits only the browser session.
func bootWithUser(t *testing.T) (string, string, session) {
	t.Helper()
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing the engine: %v", cerr)
		}
	})

	id, err := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	token, err := e.Auth.CreateAppPassword(ctx, id, "test",
		auth.Scope{Perms: uint16(acl.Read | acl.Write | acl.Download)}, 0)
	if err != nil {
		t.Fatalf("minting an app password: %v", err)
	}

	base := serve(t, e)
	sess := signIn(t, base, "alice", "a-long-enough-password")
	return base, token, sess
}

// authed performs a request carrying a browser session, which is what every
// /api/v1 route now requires.
func authed(t *testing.T, method, url string, sess session) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	sess.attach(req)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, body
}

// appPasswordAuthed performs a request carrying an app password, which
// middleware.Scope now refuses on every /api/v1 route: the native API is the
// browser's own surface, and a device credential belongs to DAV and compat
// instead. Kept for the tests that assert exactly that refusal.
func appPasswordAuthed(t *testing.T, method, url, token string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.SetBasicAuth("ignored", token)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, rerr := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, body
}

// A real credential reaches a real service through a real route. Every layer
// runs: the chain resolves the session, the route metadata admits it, the
// handler calls the core and the projection encodes the answer.
func TestAnAuthenticatedRequestReachesTheService(t *testing.T) {
	base, _, sess := bootWithUser(t)

	status, body := authed(t, http.MethodGet, base+"/api/v1/jobs", sess)
	if status != http.StatusOK {
		t.Fatalf("an authenticated listing answered %d: %s", status, body)
	}

	// An account with no jobs lists as an empty array, not null. A client
	// iterating a null gets a runtime error rather than zero rows.
	var jobs []map[string]any
	if err := json.Unmarshal(body, &jobs); err != nil {
		t.Fatalf("the listing does not parse: %v\n%s", err, body)
	}
	if jobs == nil {
		t.Errorf("an empty listing encoded as null: %s", body)
	}
	if len(jobs) != 0 {
		t.Errorf("a new account already has %d jobs", len(jobs))
	}
}

// The same route with no credential is refused as a path that is not there.
// middleware.scopeHandler answers every refusal with 404 rather than 401 or
// 403, so a stranger probing the surface cannot map which routes exist.
func TestAnUnauthenticatedRequestIsRefused(t *testing.T) {
	base, _, _ := bootWithUser(t)

	status, body := get(t, base+"/api/v1/jobs")
	if status != http.StatusNotFound {
		t.Fatalf("an anonymous listing answered %d: %s", status, body)
	}

	var refusal struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("the refusal does not parse: %v\n%s", err, body)
	}
	if refusal.Error == "" {
		t.Errorf("the refusal carries no code: %s", body)
	}
}

// A wrong credential is refused exactly as a missing one is. The difference
// between them is information about which tokens exist.
//
// A cookie that does not resolve leaves the request unauthenticated rather
// than distinguishing itself from an absent one, so this drives a session
// carrying a cookie value nothing issued.
func TestAWrongCredentialIsRefusedTheSameWay(t *testing.T) {
	base, _, _ := bootWithUser(t)
	wrongSession := session{cookie: &http.Cookie{
		Name: middleware.SessionCookieName, Value: "not-a-real-token",
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}}

	missing, missingBody := get(t, base+"/api/v1/jobs")
	wrong, wrongBody := authed(t, http.MethodGet, base+"/api/v1/jobs", wrongSession)

	if missing != wrong {
		t.Errorf("missing answered %d and wrong answered %d", missing, wrong)
	}
	if string(missingBody) != string(wrongBody) {
		t.Errorf("the two refusals differ:\n%s\n%s", missingBody, wrongBody)
	}
}

// A job that does not exist and a job belonging to someone else answer the
// same way. Telling them apart says whether another account has that id.
func TestAnAbsentJobIsIndistinguishableFromAForeignOne(t *testing.T) {
	base, _, sess := bootWithUser(t)

	absent, absentBody := authed(t, http.MethodGet, base+"/api/v1/jobs/999999", sess)
	if absent != http.StatusNotFound {
		t.Errorf("an absent job answered %d: %s", absent, absentBody)
	}

	// An unusable id is the same answer rather than a parse error, since a
	// different answer for a malformed id is still a distinguishable one.
	bad, badBody := authed(t, http.MethodGet, base+"/api/v1/jobs/not-a-number", sess)
	if bad != absent {
		t.Errorf("a malformed id answered %d and an absent one %d", bad, absent)
	}
	if string(badBody) != string(absentBody) {
		t.Errorf("the two answers differ:\n%s\n%s", badBody, absentBody)
	}
}

// Cancelling a job nobody owns is refused rather than reported as done. A
// client told a cancel succeeded stops watching.
func TestCancellingAnAbsentJobIsRefused(t *testing.T) {
	base, _, sess := bootWithUser(t)

	status, body := authed(t, http.MethodPost, base+"/api/v1/jobs/999999/cancel", sess)
	if status == http.StatusNoContent || status == http.StatusOK {
		t.Fatalf("cancelling a job that does not exist reported success: %d %s", status, body)
	}
	if status != http.StatusNotFound {
		t.Errorf("answered %d, want a not-found: %s", status, body)
	}
}

// A listing of credentials carries no credential. The tokens are not stored,
// so no listing can return one, and a test that reads the bytes is what keeps
// a future field from carrying one by accident.
func TestNoCredentialListingCarriesACredential(t *testing.T) {
	base, token, sess := bootWithUser(t)

	for _, path := range []string{"/api/v1/account/sessions", "/api/v1/account/app-passwords"} {
		t.Run(path, func(t *testing.T) {
			// Read with the session: the account family is the browser's own
			// surface, and an app password no longer reaches api/v1 at all.
			status, body := withCookie(t, http.MethodGet, base+path, sess.cookie)
			if status != http.StatusOK {
				t.Fatalf("answered %d: %s", status, body)
			}

			// The app password used to make this very request must not be in
			// its own listing.
			if strings.Contains(string(body), token) {
				t.Errorf("the listing carries the caller's own token: %s", body)
			}
			for _, word := range []string{"password", "token", "secret", "hash", "digest"} {
				if strings.Contains(strings.ToLower(string(body)), `"`+word+`"`) {
					t.Errorf("the listing has a %q field: %s", word, body)
				}
			}
		})
	}
}

// An app password lists itself, so the listing is real rather than empty for a
// reason that would also hide a leak.
func TestAnAppPasswordAppearsInItsOwnListing(t *testing.T) {
	base, _, sess := bootWithUser(t)

	status, body := withCookie(t, http.MethodGet, base+"/api/v1/account/app-passwords", sess.cookie)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}

	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("the listing does not parse: %v\n%s", err, body)
	}
	if len(rows) != 1 {
		t.Fatalf("the account has %d app passwords, want the one it was given", len(rows))
	}
	if rows[0]["name"] != "test" {
		t.Errorf("the row names %v", rows[0]["name"])
	}
}

// One account never sees another's credentials. This is the property the whole
// family rests on.
func TestOneAccountNeverSeesAnothersCredentials(t *testing.T) {
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

	alice, err := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := e.Auth.CreateUser(ctx, "bob", "Bob", secret.New([]byte("another-long-password")))
	if err != nil {
		t.Fatal(err)
	}

	aliceToken, err := e.Auth.CreateAppPassword(ctx, alice, "alice-key",
		auth.Scope{Perms: uint16(acl.Read)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Auth.CreateAppPassword(ctx, bob, "bob-key",
		auth.Scope{Perms: uint16(acl.Read)}, 0); err != nil {
		t.Fatal(err)
	}

	base := serve(t, e)
	_ = aliceToken

	// Read with alice's session: the account family is the browser's own
	// surface, and an app password is refused there by design.
	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "alice", "password": "a-long-enough-password"})
	aliceCookie := resp.sessionCookie()
	if aliceCookie == nil {
		t.Fatalf("alice could not sign in: %v", resp.body)
	}

	status, body := withCookie(t, http.MethodGet, base+"/api/v1/account/app-passwords", aliceCookie)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}
	if strings.Contains(string(body), "bob-key") {
		t.Errorf("alice's listing shows bob's credential: %s", body)
	}
	if !strings.Contains(string(body), "alice-key") {
		t.Errorf("alice's listing does not show her own: %s", body)
	}
}

// Revoking someone else's app password is refused, and the credential still
// works afterwards. A revoke that reported success while doing nothing is how
// a person believes they have locked someone out.
func TestRevokingAnothersCredentialIsRefused(t *testing.T) {
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

	alice, err := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := e.Auth.CreateUser(ctx, "bob", "Bob", secret.New([]byte("another-long-password")))
	if err != nil {
		t.Fatal(err)
	}

	aliceToken, err := e.Auth.CreateAppPassword(ctx, alice, "alice-key",
		auth.Scope{Perms: uint16(acl.Read)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	bobToken, err := e.Auth.CreateAppPassword(ctx, bob, "bob-key",
		auth.Scope{Perms: uint16(acl.Read)}, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Find the id of bob's credential, which alice will try to revoke.
	rows, err := e.Auth.AppPasswords(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("bob has %d credentials", len(rows))
	}
	bobID := rows[0].ID

	base := serve(t, e)

	_ = aliceToken
	resp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "alice", "password": "a-long-enough-password"})
	aliceCookie := resp.sessionCookie()
	if aliceCookie == nil {
		t.Fatalf("alice could not sign in: %v", resp.body)
	}
	aliceCSRF := resp.field("csrf")

	status, body := mutate(t, http.MethodDelete,
		base+"/api/v1/account/app-passwords/"+strconv.FormatInt(bobID, 10), aliceCookie, aliceCSRF, nil)
	if status == http.StatusNoContent || status == http.StatusOK {
		t.Fatalf("alice revoked bob's credential: %d %s", status, body)
	}

	// And bob's credential still works, which is what makes the refusal real
	// rather than a status code over a completed deletion. Proven on the DAV
	// mount rather than api/v1: an app password is a device credential, and
	// the native API now admits only a browser session. PROPFIND rather than
	// OPTIONS, since OPTIONS on the root answers before any credential is
	// checked and would prove nothing about the token.
	davReq, derr := http.NewRequest("PROPFIND", base+"/dav/", nil)
	if derr != nil {
		t.Fatalf("building the dav request: %v", derr)
	}
	davReq.SetBasicAuth("ignored", bobToken)
	after, aerr := testClient().Do(davReq)
	if aerr != nil {
		t.Fatalf("requesting: %v", aerr)
	}
	if cerr := after.Body.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if after.StatusCode != http.StatusMultiStatus {
		t.Errorf("bob's credential stopped working: %d", after.StatusCode)
	}
}

// Every route needing a credential refuses without one. Checked over the whole
// bound set rather than one route, because the check is per handler and a
// handler added later would be the one that forgot.
func TestEveryCredentialledRouteRefusesAnonymously(t *testing.T) {
	base, _, _ := bootWithUser(t)

	paths := []string{
		"/api/v1/jobs",
		"/api/v1/account/sessions",
		"/api/v1/account/app-passwords",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			status, body := get(t, base+path)
			if status != http.StatusNotFound {
				t.Errorf("%s answered %d anonymously: %s", path, status, body)
			}
		})
	}

	// And a delete, which takes a path parameter and so has its own guard.
	status, body := get(t, base+"/api/v1/account/app-passwords/1")
	if status == http.StatusOK || status == http.StatusNoContent {
		t.Errorf("an anonymous delete answered %d: %s", status, body)
	}
}
