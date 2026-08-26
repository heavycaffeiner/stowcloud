// Linux only, because what it tests is.
//go:build linux

package emergency

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
)

// door builds the mux over a real store with one administrator in it.
func testDoor(t *testing.T) (http.Handler, *auth.Service, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir, store.Options{Clock: clock.System()})
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})
	svc := auth.New(auth.Config{Store: st.State(), StoreDir: dir, Clock: clock.System()})
	if _, kerr := svc.OpenMasterKey(context.Background()); kerr != nil {
		t.Fatalf("the master key: %v", kerr)
	}
	return Handler(Deps{Auth: svc, State: st.State(), DataDir: dir}), svc, st
}

// ask sends one request from a given peer address.
func ask(h http.Handler, method, path, peer, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.RemoteAddr = peer
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The network scope, which is the outermost thing this package does.
//
// A 404 rather than a 403, because a 403 confirms to whoever asked that the
// path exists and is guarded, which is an invitation to keep asking.
func TestTheDoorIsInvisibleFromOutsideTheLocalNetwork(t *testing.T) {
	h, _, _ := testDoor(t)

	for _, peer := range []string{"203.0.113.9:1234", "8.8.8.8:1234", "[2001:db8::1]:1234"} {
		w := ask(h, http.MethodGet, Prefix+"/api/state", peer, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("a public address %s got %d, want 404", peer, w.Code)
		}
		// And nothing about the deployment leaks in the body.
		if strings.Contains(w.Body.String(), "setup_required") {
			t.Errorf("the refusal to %s answered with the door's own state", peer)
		}
	}

	for _, peer := range []string{"192.168.1.50:1234", "10.0.0.7:1234", "127.0.0.1:1234", "100.101.102.103:1234"} {
		w := ask(h, http.MethodGet, Prefix+"/api/state", peer, "")
		if w.Code != http.StatusOK {
			t.Errorf("a private address %s got %d, want 200", peer, w.Code)
		}
	}
}

// A forwarded header from a peer that is not a trusted proxy is a string the
// client wrote. Believing it would make the private-address gate a header
// anybody can set.
func TestAPublicClientCannotClaimAPrivateAddress(t *testing.T) {
	h, _, _ := testDoor(t)

	r := httptest.NewRequest(http.MethodGet, Prefix+"/api/state", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("X-Forwarded-For", "192.168.1.50")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("a claimed private address got %d, want 404", w.Code)
	}
}

// The settings are behind the credential, and the credential is behind the
// administrator check. This is the whole settings document, which is every
// permission decision the deployment makes.
func TestTheSettingsNeedAnAdministrator(t *testing.T) {
	h, svc, _ := testDoor(t)
	ctx := context.Background()
	const peer = "192.168.1.50:1234"

	if w := ask(h, http.MethodGet, Prefix+"/api/settings", peer, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated read got %d, want 401", w.Code)
	}

	// An ordinary account is refused too, and it is refused after the
	// credential rather than instead of it.
	if _, err := svc.CreateUser(ctx, "plain", "plain", secret.New([]byte("a-long-enough-password"))); err != nil {
		t.Fatalf("creating an account: %v", err)
	}
	w := ask(h, http.MethodPost, Prefix+"/api/login", peer,
		`{"username":"plain","password":"a-long-enough-password"}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an ordinary account signed in with %d, want 401", w.Code)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("a refused login still set a cookie")
	}
}

// An administrator signs in, reads the document and writes one section.
func TestAnAdministratorCanRepairASetting(t *testing.T) {
	h, svc, st := testDoor(t)
	ctx := context.Background()
	const peer = "192.168.1.50:1234"
	const pw = "a-long-enough-password"

	if _, err := svc.CreateAdmin(ctx, "root", "root", secret.New([]byte(pw))); err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	login := ask(h, http.MethodPost, Prefix+"/api/login", peer,
		`{"username":"root","password":"`+pw+`"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("the administrator could not sign in: %d %s", login.Code, login.Body)
	}
	cookies := login.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("a successful login set no cookie")
	}

	with := func(method, path, body string) *httptest.ResponseRecorder {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		}
		r.RemoteAddr = peer
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := with(http.MethodGet, Prefix+"/api/settings", ""); w.Code != http.StatusOK {
		t.Fatalf("reading the settings: %d %s", w.Code, w.Body)
	}

	// The repair: a hardening policy this kernel cannot satisfy is the failure
	// that brings somebody here, so putting a valid one back is the test.
	w := with(http.MethodPatch, Prefix+"/api/settings/security", `{"hardening":"off"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("the save was refused: %d %s", w.Code, w.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding the save: %v", err)
	}
	// Stored and not in effect, which is the honest answer: the process that
	// would apply it is the one this screen exists because of.
	if got["applied"] != "restart_required" {
		t.Errorf("applied = %v, want restart_required", got["applied"])
	}

	// And it is actually in the database, which is what the next start reads.
	doc, derr := st.State().Settings(ctx)
	if derr != nil {
		t.Fatalf("reading the stored settings: %v", derr)
	}
	sec, _ := doc["security"].(map[string]any)
	if sec["hardening"] != "off" {
		t.Errorf("stored security = %v, want the repair", doc["security"])
	}

	// A value the probes refuse is refused here too, with the same rules the
	// settings screen uses. A door that stored anything would be a door that
	// writes the next unbootable configuration.
	if w := with(http.MethodPatch, Prefix+"/api/settings/security",
		`{"hardening":"nonsense"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown hardening policy was accepted with %d", w.Code)
	}
	// A section nobody recognises stores nothing.
	if w := with(http.MethodPatch, Prefix+"/api/settings/nope", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("an unknown section answered %d, want 404", w.Code)
	}
}

// The lockout finding warns here and blocks on the settings screen.
//
// This is the screen somebody reaches when a host list already locked them
// out, so refusing over the host they are currently on would refuse the
// repair itself.
func TestAHostListThatOmitsTheCallerIsSavedWithAWarning(t *testing.T) {
	h, svc, st := testDoor(t)
	ctx := context.Background()
	const peer = "192.168.1.50:1234"
	const pw = "a-long-enough-password"

	if _, err := svc.CreateAdmin(ctx, "root", "root", secret.New([]byte(pw))); err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	login := ask(h, http.MethodPost, Prefix+"/api/login", peer,
		`{"username":"root","password":"`+pw+`"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("the administrator could not sign in: %d", login.Code)
	}

	r := httptest.NewRequest(http.MethodPatch, Prefix+"/api/settings/network",
		strings.NewReader(`{"app_hosts":["nas.example.test"]}`))
	r.RemoteAddr = peer
	r.Host = "192.168.1.10:8443"
	for _, c := range login.Result().Cookies() {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("the repair was refused: %d %s", w.Code, w.Body)
	}

	var got struct {
		Warnings []Finding `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	// Saved, and the operator is told what they just did.
	found := false
	for _, f := range got.Warnings {
		if f.ReasonKey == "settings.would_lock_you_out" {
			found = true
		}
	}
	if !found {
		t.Errorf("the lockout was not reported: %+v", got.Warnings)
	}
	doc, _ := st.State().Settings(ctx)
	net, _ := doc["network"].(map[string]any)
	if net == nil || net["app_hosts"] == nil {
		t.Errorf("the host list was not stored: %v", doc["network"])
	}
}

// Finding is the probe result as this package's callers read it. Declared here
// rather than imported so the test asserts the wire shape.
type Finding struct {
	Level     string `json:"level"`
	ReasonKey string `json:"reason_key"`
}

// Every write is recorded, so reading the log answers whether the safe-mode
// door was used at all.
func TestEveryWriteIsAudited(t *testing.T) {
	h, svc, _ := testDoor(t)
	ctx := context.Background()
	const peer = "192.168.1.50:1234"
	const pw = "a-long-enough-password"

	if _, err := svc.CreateAdmin(ctx, "root", "root", secret.New([]byte(pw))); err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	login := ask(h, http.MethodPost, Prefix+"/api/login", peer,
		`{"username":"root","password":"`+pw+`"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("sign-in: %d", login.Code)
	}
	r := httptest.NewRequest(http.MethodPatch, Prefix+"/api/settings/security",
		strings.NewReader(`{"hardening":"off"}`))
	r.RemoteAddr = peer
	for _, c := range login.Result().Cookies() {
		r.AddCookie(c)
	}
	h.ServeHTTP(httptest.NewRecorder(), r)

	rows, _, err := svc.AuditPage(ctx, auth.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	var sawLogin, sawSave bool
	for _, row := range rows {
		switch row.Event {
		case EventLogin:
			sawLogin = true
		case EventSave:
			sawSave = true
		}
	}
	if !sawLogin {
		t.Error("the emergency login was not recorded")
	}
	if !sawSave {
		t.Error("the emergency write was not recorded")
	}
}

// A write from another site is refused. The cookie is SameSite=Lax so it does
// not travel on one anyway; this is the second layer, for the browser that
// gets that wrong.
func TestACrossSiteWriteIsRefused(t *testing.T) {
	h, svc, _ := testDoor(t)
	ctx := context.Background()
	const peer = "192.168.1.50:1234"
	const pw = "a-long-enough-password"

	if _, err := svc.CreateAdmin(ctx, "root", "root", secret.New([]byte(pw))); err != nil {
		t.Fatalf("creating the administrator: %v", err)
	}
	login := ask(h, http.MethodPost, Prefix+"/api/login", peer,
		`{"username":"root","password":"`+pw+`"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("sign-in: %d", login.Code)
	}

	r := httptest.NewRequest(http.MethodPatch, Prefix+"/api/settings/security",
		strings.NewReader(`{"hardening":"off"}`))
	r.RemoteAddr = peer
	r.Host = "192.168.1.10:8443"
	r.Header.Set("Origin", "https://attacker.example")
	for _, c := range login.Result().Cookies() {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a cross-site write got %d, want 400", w.Code)
	}
}

// The degraded server's fronting: every path lands on the door, and an API
// path says so with a status rather than a redirect a client cannot read.
func TestADegradedServerFrontsTheDoor(t *testing.T) {
	h, _, _ := testDoor(t)
	front := Redirecting(h, nil, func() string { return "the watcher could not be started" })

	w := ask(front, http.MethodGet, "/b/photos", "192.168.1.50:1234", "")
	if w.Code != http.StatusFound {
		t.Fatalf("browsing a degraded server got %d, want a redirect", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != Prefix {
		t.Errorf("Location = %q, want %q", loc, Prefix)
	}

	// A running client following a redirect to a document reports a corrupt
	// server. A 503 naming the reason is something it can show.
	api := ask(front, http.MethodGet, "/api/fs/list?path=x", "192.168.1.50:1234", "")
	if api.Code != http.StatusServiceUnavailable {
		t.Fatalf("an API call on a degraded server got %d, want 503", api.Code)
	}
	if !strings.Contains(api.Body.String(), "watcher") {
		t.Errorf("the reason did not reach the client: %s", api.Body)
	}

	// The door itself still answers rather than redirecting to itself.
	if w := ask(front, http.MethodGet, Prefix+"/api/state", "192.168.1.50:1234", ""); w.Code != http.StatusOK {
		t.Errorf("the door answered %d on a degraded server", w.Code)
	}
}

// A data directory with no administrator has nothing to authenticate, so the
// door says so instead of drawing a login that cannot succeed.
func TestWithNoAdministratorTheDoorPointsAtSetup(t *testing.T) {
	h, _, _ := testDoor(t)
	w := ask(h, http.MethodGet, Prefix+"/api/state", "192.168.1.50:1234", "")
	if w.Code != http.StatusOK {
		t.Fatalf("the door answered %d", w.Code)
	}
	var got struct {
		SetupRequired bool `json:"setup_required"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !got.SetupRequired {
		t.Error("an empty data directory did not report that setup is required")
	}
}
