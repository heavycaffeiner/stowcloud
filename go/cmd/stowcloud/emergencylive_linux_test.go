//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/emergency"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// This lives in cmd rather than beside the package because of the layer rule:
// engine/http may not import engine/store, and a test that builds the real
// state database has to. cmd is where the tiers legitimately meet, which is
// also where the door is actually mounted, so the wiring under test here is the
// wiring the product ships.
//
// The package's own tests drive the handler with scripted dependencies, which
// is the right shape for pinning its policy but proves nothing about whether
// the door works. Everything it touches is real here: a SQLite state database
// on disk, the auth service with its own limiter and password hashing, an
// http.Server on a loopback listener, and a stdlib client with a cookie jar.
//
// What this is for is the claim the whole package rests on. The door has to
// keep working when the settings it edits have already broken the engine, and
// no assertion against a fake can establish that. The final test here writes a
// document that makes the engine unstartable, confirms the loader really does
// refuse it, and then repairs it through the door over HTTP.

const testPassword = "correct horse battery staple"

type live struct {
	client *http.Client
	base   string
	store  *state.DB
	svc    *auth.Service
	dir    string

	restarts int
}

// start builds the real stack behind a real listener.
// serve puts a handler behind a real loopback listener and returns its base URL.
//
// Loopback is a private address, so the door's network gate admits it, which is
// what makes an end-to-end test over a real socket possible at all.
func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// A header timeout because a server without one is a slow-loris target, and
	// a test server is still a server; the value is generous enough that no test
	// here can hit it.
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second}
	task.Go(t.Context(), "emergency test server", func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("the test server stopped: %v", err)
		}
	})
	t.Cleanup(func() {
		if cerr := srv.Close(); cerr != nil {
			t.Errorf("closing the server: %v", cerr)
		}
	})
	return "http://" + ln.Addr().String()
}

// realStore opens a state database on disk and an auth service over it.
func realStore(t *testing.T) (*state.DB, *auth.Service, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := dbfile.Open(t.Context(), state.Spec(filepath.Join(dir, "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing the state database: %v", cerr)
		}
	})
	store := state.New(f)
	svc := auth.New(auth.Config{Store: store, StoreDir: dir})
	t.Setenv("SC_MASTER_KEY_FILE", filepath.Join(dir, "master.key"))
	if _, kerr := svc.OpenMasterKey(t.Context()); kerr != nil {
		t.Fatalf("OpenMasterKey: %v", kerr)
	}
	return store, svc, dir
}

func start(t *testing.T) *live {
	t.Helper()
	store, svc, dir := realStore(t)
	l := &live{store: store, svc: svc, dir: dir}

	l.base = serve(t, emergency.Handler(emergency.Deps{
		Auth: svc, State: store, DataDir: dir,
		Restart: func() { l.restarts++ },
		Reason:  func() string { return "the engine did not start" },
	}))
	l.client = newClient()
	return l
}

// newClient keeps cookies across requests and reports redirects rather than
// following them, since the redirect wrapper is asserted by status.
func newClient() *http.Client {
	return &http.Client{
		Jar: &plainJar{},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// plainJar keeps cookies across requests the way a browser does.
//
// The real cookie is __Host- prefixed and therefore Secure-only, which a
// net/http/cookiejar will not return over plain HTTP. Rather than terminate TLS
// for this, the jar is a slice: what the browser rules require of the cookie is
// asserted on the Set-Cookie header in the package's own tests.
type plainJar struct{ cookies []*http.Cookie }

func (j *plainJar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	for _, c := range cs {
		replaced := false
		for i, have := range j.cookies {
			if have.Name == c.Name {
				j.cookies[i] = c
				replaced = true
			}
		}
		if !replaced {
			j.cookies = append(j.cookies, c)
		}
	}
}

func (j *plainJar) Cookies(*url.URL) []*http.Cookie { return j.cookies }

// admin creates the deployment's administrator.
func (l *live) admin(t *testing.T, name string) int64 {
	t.Helper()
	id, err := l.svc.CreateAdmin(t.Context(), name, "", secret.New([]byte(testPassword)))
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	return id
}

// reply is what a caller needs once do has consumed the body: the status, the
// decoded JSON, and the headers.
//
// The *http.Response is deliberately not returned. Its body is already read and
// closed inside do, so handing it back offers a reader with nothing in it.
type reply struct {
	Status int
	Header http.Header
	Body   map[string]any
}

func (l *live) do(t *testing.T, method, path, body string) (reply, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, l.base+path, rdr)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := l.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	// Read and closed here rather than deferred to the caller: every test in
	// this file wants the decoded body and none of them stream, so the response
	// has no reader left once this returns.
	raw, rerr := io.ReadAll(res.Body)
	if cerr := res.Body.Close(); cerr != nil {
		t.Errorf("closing the response body: %v", cerr)
	}
	if rerr != nil {
		t.Fatalf("reading the response: %v", rerr)
	}
	var out map[string]any
	if len(raw) > 0 && raw[0] == '{' {
		if uerr := json.Unmarshal(raw, &out); uerr != nil {
			t.Fatalf("the response is not JSON: %v\n%s", uerr, raw)
		}
	}
	return reply{Status: res.StatusCode, Header: res.Header, Body: out}, out
}

// The whole flow over a real socket: read the door's state, sign in with real
// credentials, read the settings, write one, and see the value in the database.
func TestTheDoorWorksEndToEnd(t *testing.T) {
	l := start(t)
	l.admin(t, "operator")

	res, out := l.do(t, "GET", emergency.Prefix+"/api/state", "")
	if res.Status != http.StatusOK {
		t.Fatalf("the state route returned %d", res.Status)
	}
	if out["setup_required"] != false {
		t.Errorf("an account exists but the door asks for setup: %v", out)
	}
	if out["reason"] != "the engine did not start" {
		t.Errorf("the banner said %v", out["reason"])
	}

	res, out = l.do(t, "POST", emergency.Prefix+"/api/login",
		`{"username":"operator","password":"`+testPassword+`"}`)
	if res.Status != http.StatusOK {
		t.Fatalf("login returned %d: %v", res.Status, out)
	}
	if out["status"] != "ok" {
		t.Fatalf("login reported %v", out["status"])
	}

	res, out = l.do(t, "GET", emergency.Prefix+"/api/settings", "")
	if res.Status != http.StatusOK {
		t.Fatalf("reading the settings returned %d: %v", res.Status, out)
	}
	if _, ok := out["stored"]; !ok {
		t.Errorf("no stored document came back: %v", out)
	}

	res, out = l.do(t, "PATCH", emergency.Prefix+"/api/settings/search", `{"concurrent":9}`)
	if res.Status != http.StatusOK {
		t.Fatalf("the write returned %d: %v", res.Status, out)
	}

	// The value is in the database, read back through the store rather than
	// from the response that claimed to have written it.
	doc, err := l.store.Settings(t.Context())
	if err != nil {
		t.Fatalf("reading the settings back: %v", err)
	}
	if got := number(t, section(t, doc, "search"), "concurrent"); got != 9 {
		t.Errorf("the stored value is %v, want 9", got)
	}
}

// section reads one settings section, failing rather than yielding an empty map
// that would make a later check pass for the wrong reason.
func section(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	raw, present := doc[name]
	if !present {
		t.Fatalf("no %s section was stored: %v", name, doc)
	}
	sec, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("the %s section is %T rather than an object: %v", name, raw, raw)
	}
	return sec
}

// number reads a JSON number from a section.
func number(t *testing.T, sec map[string]any, key string) float64 {
	t.Helper()
	raw, present := sec[key]
	if !present {
		t.Fatalf("%s was not stored: %v", key, sec)
	}
	n, ok := raw.(float64)
	if !ok {
		t.Fatalf("%s is %T rather than a number: %v", key, raw, raw)
	}
	return n
}

// list reads a JSON array from a section.
func list(t *testing.T, sec map[string]any, key string) []any {
	t.Helper()
	raw, present := sec[key]
	if !present {
		t.Fatalf("%s was not stored: %v", key, sec)
	}
	v, ok := raw.([]any)
	if !ok {
		t.Fatalf("%s is %T rather than a list: %v", key, raw, raw)
	}
	return v
}

// The gate, over a real connection.
//
// Every request in this file arrives from loopback, which is private, so the
// gate admits all of them and none of them can show that it refuses anything.
// A resolver standing in for a request forwarded from a public client is what
// exercises the other half, and the door has to answer 404 on every route
// including the page, since 403 would confirm the path is there.
func TestAPublicPeerGetsNothingOverARealConnection(t *testing.T) {
	store, svc, dir := realStore(t)
	if _, err := svc.CreateAdmin(t.Context(), "operator", "",
		secret.New([]byte(testPassword))); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	l := &live{store: store, svc: svc, dir: dir, client: newClient()}
	l.base = serve(t, emergency.Handler(emergency.Deps{
		Auth: svc, State: store, DataDir: dir,
		// What the main chain would resolve for a request relayed by a trusted
		// proxy on behalf of a client on the public internet.
		ClientAddr: func(*http.Request) netip.Addr { return netip.MustParseAddr("203.0.113.9") },
	}))

	for _, c := range []struct{ method, path, body string }{
		{"GET", emergency.Prefix, ""},
		{"GET", emergency.Prefix + "/api/state", ""},
		{"POST", emergency.Prefix + "/api/login", `{"username":"operator","password":"` + testPassword + `"}`},
		{"GET", emergency.Prefix + "/api/settings", ""},
		{"PATCH", emergency.Prefix + "/api/settings/search", `{"concurrent":4}`},
		{"POST", emergency.Prefix + "/api/restart", ""},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			res, _ := l.do(t, c.method, c.path, c.body)
			if res.Status != http.StatusNotFound {
				t.Errorf("a public peer got %d, want 404", res.Status)
			}
		})
	}

	// Nothing was written and no session was granted, so the refusal is total
	// rather than only affecting the status line.
	doc, err := store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, wrote := doc["search"]; wrote {
		t.Errorf("a public peer wrote to the settings: %v", doc)
	}
}

// A real password failure goes through the real hashing and the real limiter.
func TestABadPasswordIsRefusedByTheRealAuthService(t *testing.T) {
	l := start(t)
	l.admin(t, "operator")

	res, _ := l.do(t, "POST", emergency.Prefix+"/api/login",
		`{"username":"operator","password":"not the password"}`)
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("a wrong password returned %d, want 401", res.Status)
	}
	// And the session routes are still closed.
	if res, _ := l.do(t, "GET", emergency.Prefix+"/api/settings", ""); res.Status != http.StatusUnauthorized {
		t.Errorf("the settings were readable after a failed login: %d", res.Status)
	}
}

// An ordinary account authenticates and still cannot open the door, through the
// real role check rather than a scripted one.
func TestARealNonAdministratorCannotOpenTheDoor(t *testing.T) {
	l := start(t)
	l.admin(t, "operator")
	if _, err := l.svc.CreateUser(t.Context(), "bob", "", secret.New([]byte(testPassword))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	res, _ := l.do(t, "POST", emergency.Prefix+"/api/login",
		`{"username":"bob","password":"`+testPassword+`"}`)
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("an ordinary account signed in with %d", res.Status)
	}

	// The audit log holds the refusal, read from the database the service wrote
	// it to rather than from a recorded call.
	if !auditHas(t, l, emergency.EventLogin) {
		t.Error("the refusal was not written to the audit log")
	}
}

// auditHas reports whether the audit log carries an event, read back from the
// database rather than from a recorded call.
func auditHas(t *testing.T, l *live, event string) bool {
	t.Helper()
	rows, _, err := l.svc.AuditPage(t.Context(), auth.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	for _, r := range rows {
		if r.Event == event {
			return true
		}
	}
	return false
}

// A successful write reaches the audit log in the database under the door's own
// event name, which is what makes the log able to answer whether safe mode was
// used.
func TestTheWriteIsAuditedInTheDatabase(t *testing.T) {
	l := start(t)
	l.admin(t, "operator")

	if res, out := l.do(t, "POST", emergency.Prefix+"/api/login",
		`{"username":"operator","password":"`+testPassword+`"}`); res.Status != http.StatusOK {
		t.Fatalf("login returned %d: %v", res.Status, out)
	}
	if res, out := l.do(t, "PATCH", emergency.Prefix+"/api/settings/search",
		`{"concurrent":5}`); res.Status != http.StatusOK {
		t.Fatalf("the write returned %d: %v", res.Status, out)
	}

	if !auditHas(t, l, emergency.EventSave) {
		t.Error("the write is not in the audit log")
	}
	if !auditHas(t, l, emergency.EventLogin) {
		t.Error("the login is not in the audit log")
	}
}

// The restart action runs the supervisor callback over a real request.
func TestRestartRunsOverARealRequest(t *testing.T) {
	l := start(t)
	l.admin(t, "operator")

	if res, _ := l.do(t, "POST", emergency.Prefix+"/api/login",
		`{"username":"operator","password":"`+testPassword+`"}`); res.Status != http.StatusOK {
		t.Fatal("login failed")
	}
	res, out := l.do(t, "POST", emergency.Prefix+"/api/restart", "")
	if res.Status != http.StatusOK {
		t.Fatalf("restart returned %d", res.Status)
	}
	if out["restarting"] != true {
		t.Errorf("the door reported %v", out["restarting"])
	}
	if l.restarts != 1 {
		t.Errorf("the supervisor ran %d times", l.restarts)
	}
}

// The reason this package exists, end to end.
//
// A settings document that stops the engine is written directly to the
// database, the way a bad save from the ordinary settings screen would leave
// it. The loader is then asked what it makes of that document, which is what
// the engine would do at startup, and it has to refuse the value: without that
// step this test would pass against a document that was never broken.
//
// Then the door repairs it over HTTP, and the loader accepts the result.
func TestABrokenSettingsDocumentIsRepairedThroughTheDoor(t *testing.T) {
	l := start(t)
	l.admin(t, "operator")

	// A listener nothing can bind, which is the failure that takes the web
	// interface down and leaves this door as the only way in.
	broken := map[string]any{"bind": "http://not a bind address:::"}
	if err := l.store.MergeSettings(t.Context(), "network", broken); err != nil {
		t.Fatalf("staging the broken document: %v", err)
	}

	// What the engine would do at startup. The loader drops a bind address it
	// cannot parse and falls back to the default, so the stored value is not
	// what the server would run on: the deployment is answering somewhere the
	// operator did not configure, and the settings screen they would use to fix
	// it is on the address that does not work.
	defaults := runtimecfg.Defaults()
	loaded := runtimecfg.Load(t.Context(), l.store, defaults, nil)
	if loaded.Listen != defaults.Listen {
		t.Fatalf("the loader accepted %q, so this document does not break anything and the test proves nothing",
			loaded.Listen)
	}

	// The repair, through the door, over HTTP.
	if res, out := l.do(t, "POST", emergency.Prefix+"/api/login",
		`{"username":"operator","password":"`+testPassword+`"}`); res.Status != http.StatusOK {
		t.Fatalf("login returned %d: %v", res.Status, out)
	}
	res, out := l.do(t, "PATCH", emergency.Prefix+"/api/settings/network",
		`{"bind":"127.0.0.1:8443"}`)
	if res.Status != http.StatusOK {
		t.Fatalf("the repair returned %d: %v", res.Status, out)
	}
	if out["applied"] != "restart_required" {
		t.Errorf("the repair reported %v", out["applied"])
	}

	// The loader now takes the stored value, so the next start comes up on it.
	repaired := runtimecfg.Load(t.Context(), l.store, runtimecfg.Defaults(), nil)
	if repaired.Listen != "127.0.0.1:8443" {
		t.Errorf("after the repair the loader runs on %q, want the repaired address", repaired.Listen)
	}
}

// The other half of the same story: the door refuses a repair that would leave
// the deployment just as broken, so it cannot be used to make things worse.
func TestTheDoorRefusesARepairThatWouldNotWork(t *testing.T) {
	l := start(t)
	l.admin(t, "operator")

	if res, _ := l.do(t, "POST", emergency.Prefix+"/api/login",
		`{"username":"operator","password":"`+testPassword+`"}`); res.Status != http.StatusOK {
		t.Fatal("login failed")
	}

	res, out := l.do(t, "PATCH", emergency.Prefix+"/api/settings/network",
		`{"bind":"still not a bind address:::"}`)
	if res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("an unbindable address was accepted with %d: %v", res.Status, out)
	}

	// Nothing was stored, so a refused repair leaves the previous value intact
	// rather than replacing one broken document with another.
	doc, err := l.store.Settings(t.Context())
	if err != nil {
		t.Fatalf("reading the settings: %v", err)
	}
	if sec, ok := doc["network"].(map[string]any); ok {
		if _, present := sec["bind"]; present {
			t.Errorf("the refused address was stored anyway: %v", sec)
		}
	}
}

// The repair that the settings screen would refuse and this door must allow,
// end to end: a host list that does not contain the host the request arrived
// on. This is the lockout rule over a real connection.
func TestTheLockoutRepairWorksOverARealConnection(t *testing.T) {
	l := start(t)
	l.admin(t, "operator")

	if res, _ := l.do(t, "POST", emergency.Prefix+"/api/login",
		`{"username":"operator","password":"`+testPassword+`"}`); res.Status != http.StatusOK {
		t.Fatal("login failed")
	}

	// The request arrives on 127.0.0.1 and the list names something else, which
	// is exactly the shape of the repair somebody comes here to make.
	res, out := l.do(t, "PATCH", emergency.Prefix+"/api/settings/network",
		`{"app_hosts":["stowcloud.example"]}`)
	if res.Status != http.StatusOK {
		t.Fatalf("the lockout repair was refused with %d: %v", res.Status, out)
	}

	warnings := list(t, map[string]any{"warnings": out["warnings"]}, "warnings")
	if len(warnings) == 0 {
		t.Error("the save carried no warning about the host it omits")
	}

	doc, err := l.store.Settings(t.Context())
	if err != nil {
		t.Fatalf("reading the settings: %v", err)
	}
	hosts := list(t, section(t, doc, "network"), "app_hosts")
	if len(hosts) != 1 || hosts[0] != "stowcloud.example" {
		t.Errorf("the repaired host list is %v", hosts)
	}
}

// The redirect wrapper over a real connection: a browser path lands on the
// door, and an API path gets a status naming the reason.
func TestTheRedirectWrapperOverARealConnection(t *testing.T) {
	store, svc, dir := realStore(t)

	door := emergency.Handler(emergency.Deps{Auth: svc, State: store, DataDir: dir})
	l := &live{
		store:  store,
		svc:    svc,
		dir:    dir,
		client: newClient(),
	}
	l.base = serve(t, emergency.Redirecting(door, nil,
		func() string { return "the database is unreadable" }))

	// A browser path lands on the door.
	res, _ := l.do(t, "GET", "/files/anything", "")
	if res.Status != http.StatusFound {
		t.Errorf("a browser path returned %d, want a redirect", res.Status)
	}
	if got := res.Header.Get("Location"); got != emergency.Prefix {
		t.Errorf("the redirect points at %q", got)
	}

	// An API path gets a status naming the reason, because a client expecting
	// JSON reads an HTML body as a broken server.
	ares, out := l.do(t, "GET", "/api/v1/files", "")
	if ares.Status != http.StatusServiceUnavailable {
		t.Errorf("an API path returned %d, want 503", ares.Status)
	}
	if out["reason"] != "the database is unreadable" {
		t.Errorf("the status does not name the reason: %v", out)
	}
}

// A door with no accounts says setup is needed rather than drawing a login
// nobody can pass, over the real store.
func TestAnEmptyDeploymentAsksForSetup(t *testing.T) {
	l := start(t)

	_, out := l.do(t, "GET", emergency.Prefix+"/api/state", "")
	if out["setup_required"] != true {
		t.Errorf("an empty deployment reported %v", out["setup_required"])
	}
}
