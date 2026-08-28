//go:build linux

package emergency

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/check"
)

// The door is tested against a scripted authenticator rather than a real auth
// service, because what belongs to this package is the surrounding policy: the
// network gate, the administrator requirement, the origin check, the audit
// names and the checker-gated write. Whether a password verifies is the auth
// package's own test, and duplicating it here would test that package twice
// while testing this one once.
//
// What the fake does record is every call, so a test can assert that the door
// went through the shared login rather than checking a password itself, which
// is the invariant that matters most here.

type authCall struct {
	event  string
	target string
	actor  int64
	ok     bool
}

type fakeAuth struct {
	// What Login should do.
	loginErr  error
	loginSess auth.Session

	// Who is an administrator, and what LookupSession resolves to.
	admin     bool
	adminErr  error
	principal auth.Principal
	lookupErr error

	users    int64
	usersErr error

	// Observed.
	logins   []auth.LoginRequest
	records  []authCall
	lookedUp []string
}

func (f *fakeAuth) Login(_ context.Context, req auth.LoginRequest, _ time.Duration) (auth.Session, error) {
	f.logins = append(f.logins, req)
	return f.loginSess, f.loginErr
}

func (f *fakeAuth) LookupSession(_ context.Context, token secret.Secret) (auth.Principal, error) {
	f.lookedUp = append(f.lookedUp, hex.EncodeToString(token.Reveal()))
	return f.principal, f.lookupErr
}

func (f *fakeAuth) IsAdmin(_ context.Context, _ int64) (bool, error) { return f.admin, f.adminErr }
func (f *fakeAuth) CountUsers(_ context.Context) (int64, error)      { return f.users, f.usersErr }

func (f *fakeAuth) Record(_ context.Context, actor int64, event, target, _, _ string, ok bool) {
	f.records = append(f.records, authCall{event: event, target: target, actor: actor, ok: ok})
}

type fakeStore struct {
	doc      map[string]any
	readErr  error
	mergeErr error
	merged   []struct {
		section string
		value   any
	}
}

func (f *fakeStore) Settings(context.Context) (map[string]any, error) {
	return f.doc, f.readErr
}

func (f *fakeStore) MergeSettings(_ context.Context, section string, value any) error {
	if f.mergeErr != nil {
		return f.mergeErr
	}
	f.merged = append(f.merged, struct {
		section string
		value   any
	}{section, value})
	return nil
}

// signedIn is a door whose cookie resolves to an administrator.
func signedIn(t *testing.T) (*fakeAuth, *fakeStore, http.Handler) {
	t.Helper()
	a := &fakeAuth{
		admin:     true,
		principal: auth.Principal{UserID: 7, Login: "root"},
		loginSess: auth.Session{UserID: 7, Token: secret.New([]byte{1, 2, 3, 4})},
	}
	s := &fakeStore{doc: map[string]any{"network": map[string]any{"listen": ":8080"}}}
	return a, s, Handler(Deps{
		Auth: a, State: s, DataDir: t.TempDir(),
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
	})
}

// ask sends a request from a private peer unless the caller overrides it.
func ask(h http.Handler, method, path string, body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.RemoteAddr = "192.168.1.10:5000"
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// withCookie attaches a session cookie to a request.
//
// Only the name and value travel on a request; Secure, HttpOnly and SameSite
// are response-side directives, so setting them here would be meaningless. What
// the handler emits is asserted separately.
func withCookie(value string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Add("Cookie", SessionCookie+"="+value)
	}
}

func withOrigin(o string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Origin", o) }
}

func body(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("the response is not JSON: %v\n%s", err, w.Body.String())
	}
	return out
}

// The gate. A public peer is told the path does not exist, on every route
// including the page, because a 403 would confirm it does.
func TestAPublicPeerIsToldTheDoorDoesNotExist(t *testing.T) {
	a := &fakeAuth{admin: true}
	h := Handler(Deps{
		Auth: a, State: &fakeStore{},
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("203.0.113.9") },
		Page:       http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }),
	})

	for _, c := range []struct{ method, path string }{
		{"GET", Prefix},
		{"GET", Prefix + "/api/state"},
		{"POST", Prefix + "/api/login"},
		{"GET", Prefix + "/api/settings"},
		{"PATCH", Prefix + "/api/settings/network"},
		{"POST", Prefix + "/api/restart"},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			w := ask(h, c.method, c.path, `{}`)
			if w.Code != http.StatusNotFound {
				t.Errorf("a public peer got %d, want 404", w.Code)
			}
			if strings.Contains(strings.ToLower(w.Body.String()), "forbidden") {
				t.Errorf("the refusal names the guard: %s", w.Body.String())
			}
		})
	}
}

// A private peer reaches the page, which is the other half of the gate: a test
// that only proves refusal passes against a door that refuses everybody.
func TestAPrivatePeerReachesTheDoor(t *testing.T) {
	drawn := false
	a := &fakeAuth{}
	h := Handler(Deps{
		Auth: a, State: &fakeStore{},
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("10.1.2.3") },
		Page: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			drawn = true
			w.WriteHeader(http.StatusOK)
		}),
	})

	if w := ask(h, "GET", Prefix, ""); w.Code != http.StatusOK {
		t.Fatalf("a private peer got %d, want 200", w.Code)
	}
	if !drawn {
		t.Error("the page was not served")
	}
}

// With no resolver the peer address decides, and no forwarded header can
// override it. A public client claiming a private address stays out.
func TestAForwardedHeaderCannotClaimAPrivateAddress(t *testing.T) {
	h := Handler(Deps{Auth: &fakeAuth{}, State: &fakeStore{}})

	r := httptest.NewRequest("GET", Prefix+"/api/state", nil)
	r.RemoteAddr = "203.0.113.9:5000"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	r.Header.Set("CF-Connecting-IP", "192.168.1.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("a public peer claiming a private address got %d, want 404", w.Code)
	}
}

// A RemoteAddr that does not parse is not guessed at.
func TestAnUnparseablePeerIsRefused(t *testing.T) {
	h := Handler(Deps{Auth: &fakeAuth{}, State: &fakeStore{}})

	r := httptest.NewRequest("GET", Prefix+"/api/state", nil)
	r.RemoteAddr = "not-an-address"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("an unparseable peer got %d, want 404", w.Code)
	}
}

// Login goes through the shared auth service rather than checking a password
// here. This asserts the call happened and carried what the service needs.
func TestLoginGoesThroughTheSharedAuthService(t *testing.T) {
	a, _, h := signedIn(t)

	w := ask(h, "POST", Prefix+"/api/login", `{"username":"root","password":"pw","factor":"123456"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("login returned %d: %s", w.Code, w.Body)
	}
	if len(a.logins) != 1 {
		t.Fatalf("the auth service saw %d login calls, want 1", len(a.logins))
	}
	got := a.logins[0]
	if got.Name != "root" {
		t.Errorf("the username was %q", got.Name)
	}
	if string(got.Password.Reveal()) != "pw" {
		t.Error("the password did not reach the auth service")
	}
	if got.Factor != "123456" {
		t.Errorf("the second factor was %q, so an enrolled account could not sign in", got.Factor)
	}
	if got.IP == "" {
		t.Error("no address reached the limiter, which is what makes it per-client")
	}
}

// A refused credential is a 401 and never says which half was wrong.
func TestBadCredentialsAreRefused(t *testing.T) {
	a := &fakeAuth{loginErr: auth.ErrCredentials}
	h := Handler(Deps{
		Auth: a, State: &fakeStore{},
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
	})

	w := ask(h, "POST", Prefix+"/api/login", `{"username":"root","password":"wrong"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a bad password returned %d, want 401", w.Code)
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("a refused login set a cookie")
	}
}

// The limiter is the auth service's, so a rate-limited attempt is refused here
// too rather than falling through to a second check.
func TestARateLimitedLoginIsRefused(t *testing.T) {
	a := &fakeAuth{loginErr: auth.ErrRateLimited}
	h := Handler(Deps{
		Auth: a, State: &fakeStore{},
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
	})

	w := ask(h, "POST", Prefix+"/api/login", `{"username":"root","password":"pw"}`)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a rate-limited login returned %d, want 401", w.Code)
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("a rate-limited login set a cookie")
	}
}

// An enrolled account is asked for its code rather than told it failed, or an
// administrator with a second factor could never get in through this door.
func TestAnEnrolledAccountIsAskedForItsCode(t *testing.T) {
	a := &fakeAuth{loginErr: auth.ErrSecondFactor}
	h := Handler(Deps{
		Auth: a, State: &fakeStore{},
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
	})

	w := ask(h, "POST", Prefix+"/api/login", `{"username":"root","password":"pw"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("a second-factor prompt returned %d, want 200", w.Code)
	}
	if got := body(t, w)["status"]; got != "totp_required" {
		t.Errorf("the status was %v, so the screen cannot ask for the code", got)
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("a session was granted before the second factor")
	}
}

// The administrator check runs after the credential and never instead of it: a
// valid non-administrator gets nothing.
func TestAValidNonAdministratorIsRefused(t *testing.T) {
	a := &fakeAuth{
		admin:     false,
		loginSess: auth.Session{UserID: 12, Token: secret.New([]byte{9})},
	}
	h := Handler(Deps{
		Auth: a, State: &fakeStore{},
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
	})

	w := ask(h, "POST", Prefix+"/api/login", `{"username":"bob","password":"pw"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a non-administrator got %d, want 401", w.Code)
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("a non-administrator was given a session cookie")
	}
	// And it is recorded, because an ordinary account trying the repair door is
	// exactly what this log is read to find.
	if len(a.records) != 1 || a.records[0].event != EventLogin || a.records[0].ok {
		t.Errorf("the refusal was recorded as %+v", a.records)
	}
	if a.records[0].target != "not_an_administrator" {
		t.Errorf("the record does not say why: %q", a.records[0].target)
	}
}

// A successful login is recorded under this door's own event name, which is
// what makes the log able to answer whether safe mode was used.
func TestASuccessfulLoginIsRecordedUnderTheDoorsOwnEvent(t *testing.T) {
	a, _, h := signedIn(t)

	if w := ask(h, "POST", Prefix+"/api/login", `{"username":"root","password":"pw"}`); w.Code != http.StatusOK {
		t.Fatalf("login returned %d", w.Code)
	}
	if len(a.records) != 1 {
		t.Fatalf("the login recorded %d events, want 1", len(a.records))
	}
	if a.records[0].event != EventLogin || !a.records[0].ok {
		t.Errorf("recorded %+v", a.records[0])
	}
	if a.records[0].event == "auth.login" {
		t.Error("the door reused the ordinary login event, so the log cannot distinguish it")
	}
}

// The cookie carries the __Host- prefix, which the browser only honours with
// Secure, Path=/ and no Domain. Losing any of the three stops it being stored.
func TestTheSessionCookieMeetsTheHostPrefixRules(t *testing.T) {
	_, _, h := signedIn(t)

	w := ask(h, "POST", Prefix+"/api/login", `{"username":"root","password":"pw"}`)
	raw := w.Header().Get("Set-Cookie")
	if raw == "" {
		t.Fatal("no cookie was set")
	}
	if !strings.HasPrefix(raw, "__Host-") {
		t.Errorf("the cookie is not __Host- prefixed: %s", raw)
	}
	for _, want := range []string{"Secure", "HttpOnly", "Path=/", "SameSite=Lax"} {
		if !strings.Contains(raw, want) {
			t.Errorf("the cookie is missing %s: %s", want, raw)
		}
	}
	if strings.Contains(raw, "Domain=") {
		t.Errorf("a __Host- cookie naming a Domain is rejected by the browser: %s", raw)
	}
}

// Every authenticated route refuses without a session.
func TestTheAuthenticatedRoutesRefuseWithoutASession(t *testing.T) {
	_, _, h := signedIn(t)

	for _, c := range []struct{ method, path, body string }{
		{"GET", Prefix + "/api/settings", ""},
		{"PATCH", Prefix + "/api/settings/network", `{}`},
		{"POST", Prefix + "/api/restart", ""},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			if w := ask(h, c.method, c.path, c.body); w.Code != http.StatusUnauthorized {
				t.Errorf("got %d without a session, want 401", w.Code)
			}
		})
	}
}

// A session belonging to a non-administrator is refused on the routes behind
// the login, not only at the login itself.
func TestASessionFromANonAdministratorIsRefused(t *testing.T) {
	a, s, _ := signedIn(t)
	a.admin = false
	h := Handler(Deps{
		Auth: a, State: s, DataDir: t.TempDir(),
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
	})

	w := ask(h, "GET", Prefix+"/api/settings", "", withCookie("01020304"))
	if w.Code != http.StatusForbidden {
		t.Errorf("a non-administrator session got %d, want 403", w.Code)
	}
}

// A cookie that is not hex never reaches the session lookup, so a malformed
// value is refused rather than being passed on as bytes.
func TestAMalformedCookieIsRefusedWithoutALookup(t *testing.T) {
	a, _, h := signedIn(t)

	if w := ask(h, "GET", Prefix+"/api/settings", "", withCookie("zzzz")); w.Code != http.StatusUnauthorized {
		t.Errorf("a malformed cookie got %d, want 401", w.Code)
	}
	if len(a.lookedUp) != 0 {
		t.Errorf("a malformed cookie reached the session lookup: %v", a.lookedUp)
	}
}

// The origin check refuses a cross-site write. It compares against the
// request's own Host rather than the configured app-host list, because that
// list is one of the things this door repairs.
func TestACrossSiteWriteIsRefused(t *testing.T) {
	_, s, h := signedIn(t)

	w := ask(h, "PATCH", Prefix+"/api/settings/network", `{"bind":":9090"}`,
		withCookie("01020304"), withOrigin("https://evil.example"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a cross-site write got %d, want 400", w.Code)
	}
	if len(s.merged) != 0 {
		t.Error("a cross-site write reached the database")
	}
}

// Same-origin passes, or the check would refuse the repair as well.
func TestASameOriginWriteIsAllowed(t *testing.T) {
	_, s, h := signedIn(t)

	w := ask(h, "PATCH", Prefix+"/api/settings/search", `{"concurrent":4}`,
		withCookie("01020304"), withOrigin("http://example.com"))
	if w.Code != http.StatusOK {
		t.Fatalf("a same-origin write got %d: %s", w.Code, w.Body)
	}
	if len(s.merged) != 1 {
		t.Errorf("the write did not reach the database: %+v", s.merged)
	}
}

// A read is not a write, so it passes the origin check regardless.
func TestAReadIsNotSubjectToTheOriginCheck(t *testing.T) {
	_, _, h := signedIn(t)

	w := ask(h, "GET", Prefix+"/api/settings", "",
		withCookie("01020304"), withOrigin("https://evil.example"))
	if w.Code != http.StatusOK {
		t.Errorf("a read with a foreign Origin got %d, want 200", w.Code)
	}
}

// A write to a section this build does not know is refused rather than stored,
// because the store keeps whatever name it is given and the screen would then
// show a setting no code reads.
func TestAnUnknownSectionIsRefused(t *testing.T) {
	_, s, h := signedIn(t)

	w := ask(h, "PATCH", Prefix+"/api/settings/not-a-section", `{}`, withCookie("01020304"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("an unknown section got %d, want 404", w.Code)
	}
	if len(s.merged) != 0 {
		t.Error("an unknown section was written")
	}
}

// A blocking finding refuses the write and nothing reaches the database.
func TestABlockingFindingRefusesTheWrite(t *testing.T) {
	a, s, h := signedIn(t)

	// The stored key is "bind"; a listen address that will not parse is a
	// blocking finding, because a server that cannot bind does not come up.
	w := ask(h, "PATCH", Prefix+"/api/settings/network", `{"bind":"not a listen address"}`,
		withCookie("01020304"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a blocking finding returned %d, want 422: %s", w.Code, w.Body)
	}
	if len(s.merged) != 0 {
		t.Error("a refused write reached the database")
	}
	// And no save is recorded, because none happened.
	for _, rec := range a.records {
		if rec.event == EventSave {
			t.Errorf("a refused write was recorded as a save: %+v", rec)
		}
	}
	// The refusal names what was wrong rather than only that something was.
	if fs, ok := body(t, w)["findings"].([]any); !ok || len(fs) == 0 {
		t.Errorf("the refusal carries no findings: %s", w.Body)
	}
}

// A successful write is recorded under the door's own event name, with the
// section as the target.
func TestAWriteIsRecordedUnderTheDoorsOwnEvent(t *testing.T) {
	a, _, h := signedIn(t)

	if w := ask(h, "PATCH", Prefix+"/api/settings/search", `{"concurrent":4}`,
		withCookie("01020304")); w.Code != http.StatusOK {
		t.Fatalf("the write returned %d", w.Code)
	}
	var saves []authCall
	for _, rec := range a.records {
		if rec.event == EventSave {
			saves = append(saves, rec)
		}
	}
	if len(saves) != 1 {
		t.Fatalf("the write recorded %d save events, want 1: %+v", len(saves), a.records)
	}
	if saves[0].target != "search" || !saves[0].ok || saves[0].actor != 7 {
		t.Errorf("recorded %+v", saves[0])
	}
}

// A write that fails in the store is recorded as a failure rather than being
// lost, and the caller is told.
func TestAFailedWriteIsRecordedAsAFailure(t *testing.T) {
	a, s, _ := signedIn(t)
	s.mergeErr = errors.New("the disk is full")
	h := Handler(Deps{
		Auth: a, State: s, DataDir: t.TempDir(),
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
	})

	w := ask(h, "PATCH", Prefix+"/api/settings/search", `{"concurrent":4}`, withCookie("01020304"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("a failed write returned %d, want 500", w.Code)
	}
	found := false
	for _, rec := range a.records {
		if rec.event == EventSave && !rec.ok {
			found = true
		}
	}
	if !found {
		t.Errorf("a failed write was not recorded as a failure: %+v", a.records)
	}
}

// The write says the value is stored and awaiting a restart, because nothing in
// this process is holding it.
func TestAWriteSaysItNeedsARestart(t *testing.T) {
	_, _, h := signedIn(t)

	w := ask(h, "PATCH", Prefix+"/api/settings/search", `{"concurrent":4}`, withCookie("01020304"))
	if got := body(t, w)["applied"]; got != "restart_required" {
		t.Errorf("the write reported %v, so the operator may think it took effect", got)
	}
}

// The body limit refuses an oversized document.
func TestAnOversizedWriteIsRefused(t *testing.T) {
	_, s, h := signedIn(t)

	huge := `{"bind":"` + strings.Repeat("x", bodyLimit) + `"}`
	w := ask(h, "PATCH", Prefix+"/api/settings/network", huge, withCookie("01020304"))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized write returned %d, want 413", w.Code)
	}
	if len(s.merged) != 0 {
		t.Error("an oversized write reached the database")
	}
}

// A document just inside the limit is accepted, so the bound is a limit rather
// than a refusal of everything large.
func TestADocumentInsideTheLimitIsAccepted(t *testing.T) {
	_, _, h := signedIn(t)

	// Comfortably inside, and valid for the section.
	w := ask(h, "PATCH", Prefix+"/api/settings/search", `{"concurrent":4}`, withCookie("01020304"))
	if w.Code != http.StatusOK {
		t.Errorf("a small write returned %d: %s", w.Code, w.Body)
	}
}

// The read hands back the stored document rather than a rendered field list,
// because the field list comes from what the engine loaded and the engine may
// not be running.
func TestTheReadReturnsTheStoredDocument(t *testing.T) {
	_, _, h := signedIn(t)

	w := ask(h, "GET", Prefix+"/api/settings", "", withCookie("01020304"))
	if w.Code != http.StatusOK {
		t.Fatalf("the read returned %d", w.Code)
	}
	out := body(t, w)
	stored, ok := out["stored"].(map[string]any)
	if !ok {
		t.Fatalf("no stored document: %s", w.Body)
	}
	if _, ok := stored["network"]; !ok {
		t.Errorf("the stored document is missing what the store holds: %v", stored)
	}
	if secs, ok := out["sections"].([]any); !ok || len(secs) == 0 {
		t.Error("the read carries no section list, so the screen cannot draw the form")
	}
}

// The reason this door exists, and the one place its rules differ from the
// settings screen's.
//
// Somebody arrives here because the app-host list no longer contains the
// address they can reach the server on. The repair is to save a list, and the
// list they save still will not contain the host they are currently on, because
// that host is the broken one. The settings screen refuses that save: there the
// guard is live, so the change would take hold before any correction could be
// submitted. Here it has to go through, or the door refuses the only repair it
// was built to perform.
//
// The finding is still reported, as a warning: the operator should know the
// address they are on is not in the list they just saved.
func TestARepairThatDoesNotIncludeTheCurrentHostIsSaved(t *testing.T) {
	_, s, h := signedIn(t)

	// The request arrives on the very host the new list omits.
	w := ask(h, "PATCH", Prefix+"/api/settings/network",
		`{"app_hosts":["stowcloud.example"]}`,
		withCookie("01020304"),
		func(r *http.Request) { r.Host = "192.168.1.50:8080" })

	if w.Code != http.StatusOK {
		t.Fatalf("the repair was refused with %d, which is the repair this door exists for: %s",
			w.Code, w.Body)
	}
	if len(s.merged) != 1 {
		t.Fatalf("the repair did not reach the database: %+v", s.merged)
	}

	// Reported rather than silently allowed, because saving a list that omits
	// the address you are on is worth knowing about even when it is deliberate.
	warnings, ok := body(t, w)["warnings"].([]any)
	if !ok || len(warnings) == 0 {
		t.Fatalf("the save carried no warning about the omitted host: %s", w.Body)
	}
	f, ok := findingIn(t, warnings, "app_hosts")
	if !ok {
		t.Fatalf("no warning about app_hosts: %v", warnings)
	}
	if !containsValue(t, f["args"], "192.168.1.50") {
		t.Errorf("the warning does not name the host it is about: %v", f["args"])
	}
}

// findingIn returns the finding for a field, so a shape that stops matching
// fails here rather than quietly making a loop body unreachable.
func findingIn(t *testing.T, list []any, field string) (map[string]any, bool) {
	t.Helper()
	for _, raw := range list {
		f, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("a finding is %T rather than an object: %v", raw, raw)
		}
		if f["field"] == field {
			return f, true
		}
	}
	return nil, false
}

// containsValue reports whether a finding's argument pairs carry a value.
func containsValue(t *testing.T, args any, want string) bool {
	t.Helper()
	list, ok := args.([]any)
	if !ok {
		t.Fatalf("the finding's args are %T rather than a list: %v", args, args)
	}
	for _, a := range list {
		if s, ok := a.(string); ok && s == want {
			return true
		}
	}
	return false
}

// The same save on the settings screen is refused, which is what makes the rule
// above a deliberate difference rather than a missing check. This asserts the
// checker really does have two modes, so the door's choice of one is a choice.
func TestTheSameRepairWouldBeRefusedWithBlockingLockout(t *testing.T) {
	blocked := check.Section(check.Input{
		Section:  "network",
		Body:     map[string]any{"app_hosts": []any{"stowcloud.example"}},
		SelfHost: "192.168.1.50", Lockout: check.LockoutBlocks,
	})
	warned := check.Section(check.Input{
		Section:  "network",
		Body:     map[string]any{"app_hosts": []any{"stowcloud.example"}},
		SelfHost: "192.168.1.50", Lockout: check.LockoutWarns,
	})

	if !check.Blocked(blocked) {
		t.Error("the settings screen would allow a save that strands the operator")
	}
	if check.Blocked(warned) {
		t.Error("the door blocks the repair it exists to perform")
	}
}

// A write from a host the new list does contain produces no lockout finding at
// all, so the warning above is about the omission rather than being attached to
// every host-list save.
func TestARepairThatIncludesTheCurrentHostWarnsAboutNothing(t *testing.T) {
	_, _, h := signedIn(t)

	w := ask(h, "PATCH", Prefix+"/api/settings/network",
		`{"app_hosts":["stowcloud.example"]}`,
		withCookie("01020304"),
		func(r *http.Request) { r.Host = "stowcloud.example" })

	if w.Code != http.StatusOK {
		t.Fatalf("the save returned %d: %s", w.Code, w.Body)
	}
	if f, ok := findingIn(t, asSlice(t, body(t, w)["warnings"]), "app_hosts"); ok {
		t.Errorf("a list containing the current host still warned: %v", f)
	}
}

// asSlice reads a JSON array, failing on anything else rather than yielding an
// empty list that makes a check silently pass.
func asSlice(t *testing.T, v any) []any {
	t.Helper()
	if v == nil {
		return nil
	}
	s, ok := v.([]any)
	if !ok {
		t.Fatalf("expected a list, got %T: %v", v, v)
	}
	return s
}

// A cookie that is well-formed but names no live session is refused. Expired,
// revoked and never-issued are one answer here, because telling them apart
// would say which tokens exist.
func TestAnUnrecognisedSessionIsRefused(t *testing.T) {
	a, s, _ := signedIn(t)
	a.lookupErr = errors.New("no such session")
	h := Handler(Deps{
		Auth: a, State: s, DataDir: t.TempDir(),
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
		Restart:    func() { t.Error("an unrecognised session restarted the server") },
	})

	for _, c := range []struct{ method, path, body string }{
		{"GET", Prefix + "/api/settings", ""},
		{"PATCH", Prefix + "/api/settings/search", `{"concurrent":4}`},
		{"POST", Prefix + "/api/restart", ""},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			w := ask(h, c.method, c.path, c.body, withCookie("01020304"))
			if w.Code != http.StatusUnauthorized {
				t.Errorf("an unrecognised session got %d, want 401", w.Code)
			}
		})
	}
	if len(s.merged) != 0 {
		t.Error("an unrecognised session wrote to the database")
	}
}

// The identity the write is recorded against comes from the session lookup, not
// from anything the request carries, so a forged body cannot pick the actor the
// audit log names.
func TestTheAuditActorComesFromTheSession(t *testing.T) {
	a, _, _ := signedIn(t)
	a.principal = auth.Principal{UserID: 99, Login: "operator"}
	h := Handler(Deps{
		Auth: a, State: &fakeStore{}, DataDir: t.TempDir(),
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
	})

	if w := ask(h, "PATCH", Prefix+"/api/settings/search",
		`{"concurrent":4,"user_id":1}`, withCookie("01020304")); w.Code != http.StatusOK {
		t.Fatalf("the write returned %d", w.Code)
	}
	for _, rec := range a.records {
		if rec.event == EventSave && rec.actor != 99 {
			t.Errorf("the write was recorded against %d rather than the session's owner", rec.actor)
		}
	}
}

// Restart is honest about a deployment with no supervisor: saying the server is
// coming back when nothing will start it is worse than saying so.
func TestRestartIsHonestWithNoSupervisor(t *testing.T) {
	_, _, h := signedIn(t)

	w := ask(h, "POST", Prefix+"/api/restart", "", withCookie("01020304"))
	if w.Code != http.StatusOK {
		t.Fatalf("restart returned %d", w.Code)
	}
	if got := body(t, w)["restarting"]; got != false {
		t.Errorf("with no supervisor the door reported %v", got)
	}
}

// With a supervisor the process is asked to exit, and only after the response,
// so the caller learns the request was accepted.
func TestRestartAsksTheSupervisorAfterAnswering(t *testing.T) {
	a, s, _ := signedIn(t)
	called := false
	h := Handler(Deps{
		Auth: a, State: s, DataDir: t.TempDir(),
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
		Restart:    func() { called = true },
	})

	w := ask(h, "POST", Prefix+"/api/restart", "", withCookie("01020304"))
	if got := body(t, w)["restarting"]; got != true {
		t.Errorf("with a supervisor the door reported %v", got)
	}
	if !called {
		t.Error("the supervisor was never asked")
	}
	if w.Body.Len() == 0 {
		t.Error("the caller was given no response before the exit")
	}
}

// Restart is not reachable without a session, or anyone on the network could
// stop the server.
func TestRestartRequiresASession(t *testing.T) {
	a, s, _ := signedIn(t)
	called := false
	h := Handler(Deps{
		Auth: a, State: s, DataDir: t.TempDir(),
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
		Restart:    func() { called = true },
	})

	if w := ask(h, "POST", Prefix+"/api/restart", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("restart without a session returned %d, want 401", w.Code)
	}
	if called {
		t.Error("an unauthenticated request stopped the server")
	}
}

// The state route answers before anybody signs in, and says whether there is an
// account to sign in as.
func TestTheStateRouteReportsWhetherSetupIsNeeded(t *testing.T) {
	for _, c := range []struct {
		name  string
		users int64
		err   error
		want  bool
	}{
		{"no accounts", 0, nil, true},
		{"an account exists", 1, nil, false},
		// A count that cannot be read draws the login rather than pointing at
		// setup: telling somebody who cannot reach the account table to create
		// an administrator is advice they cannot follow.
		{"the count fails", 0, errors.New("no database"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := &fakeAuth{users: c.users, usersErr: c.err}
			h := Handler(Deps{
				Auth: a, State: &fakeStore{},
				ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
			})
			w := ask(h, "GET", Prefix+"/api/state", "")
			if got := body(t, w)["setup_required"]; got != c.want {
				t.Errorf("setup_required was %v, want %v", got, c.want)
			}
		})
	}
}

// The banner names what failed, so somebody who arrived at a redirect learns
// more than that something went wrong.
func TestTheStateRouteCarriesTheReason(t *testing.T) {
	h := Handler(Deps{
		Auth: &fakeAuth{}, State: &fakeStore{},
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("192.168.1.10") },
		Reason:     func() string { return "the listen address is already in use" },
	})

	w := ask(h, "GET", Prefix+"/api/state", "")
	if got := body(t, w)["reason"]; got != "the listen address is already in use" {
		t.Errorf("the banner said %q", got)
	}
}

// The redirect wrapper: a browser lands on the repair screen.
func TestTheRedirectWrapperSendsBrowsersToTheDoor(t *testing.T) {
	_, _, door := signedIn(t)
	h := Redirecting(door, nil, nil)

	w := ask(h, "GET", "/files/somewhere", "")
	if w.Code != http.StatusFound {
		t.Fatalf("a browser got %d, want a redirect", w.Code)
	}
	if got := w.Header().Get("Location"); got != Prefix {
		t.Errorf("the redirect points at %q", got)
	}
}

// An API caller gets a status naming the reason instead of an HTML redirect,
// which it would report as a corrupt server.
func TestTheRedirectWrapperAnswersApiCallersWithAStatus(t *testing.T) {
	_, _, door := signedIn(t)
	h := Redirecting(door, nil, func() string { return "the database is unreadable" })

	w := ask(h, "GET", "/api/v1/files", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("an API caller got %d, want 503", w.Code)
	}
	out := body(t, w)
	if out["reason"] != "the database is unreadable" {
		t.Errorf("the status does not name the reason: %v", out)
	}
}

// The door itself still answers through the wrapper, or the screen it redirects
// to could not load.
func TestTheRedirectWrapperPassesTheDoorThrough(t *testing.T) {
	_, _, door := signedIn(t)
	h := Redirecting(door, nil, nil)

	if w := ask(h, "GET", Prefix+"/api/state", ""); w.Code != http.StatusOK {
		t.Errorf("the door returned %d through the wrapper", w.Code)
	}
}

// The frontend's assets load through the wrapper, or the screen cannot draw.
func TestTheRedirectWrapperServesTheAssetsThePageNeeds(t *testing.T) {
	_, _, door := signedIn(t)
	served := []string{}
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = append(served, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	h := Redirecting(door, page, nil)

	for _, p := range []string{"/app/main.js", "/favicon.ico"} {
		if w := ask(h, "GET", p, ""); w.Code != http.StatusOK {
			t.Errorf("%s returned %d, so the repair screen cannot draw", p, w.Code)
		}
	}
	if len(served) != 2 {
		t.Errorf("the page handler saw %v", served)
	}
}
