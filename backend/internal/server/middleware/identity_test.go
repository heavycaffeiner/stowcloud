// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/protocol/limits"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// The id is a UUID v4 in the canonical form, and every one differs.
func TestTheTraceIDIsAUniqueUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for range 256 {
		id, err := NewTraceID()
		if err != nil {
			t.Fatalf("NewTraceID: %v", err)
		}
		if len(id) != 36 {
			t.Fatalf("the id %q is %d characters", id, len(id))
		}
		// Version 4 and the RFC 4122 variant are what distinguish a UUID from
		// hex with dashes in it.
		if id[14] != '4' {
			t.Fatalf("the id %q is not version 4", id)
		}
		if !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("the id %q does not carry the RFC 4122 variant", id)
		}
		if seen[id] {
			t.Fatalf("the id %q was minted twice", id)
		}
		seen[id] = true
	}
}

// The policy admits the hydration hashes without unsafe-inline, which would
// admit every other inline script alongside them.
func TestTheCSPCarriesHashesAndNotUnsafeInline(t *testing.T) {
	policy := CSP([]string{"sha256-abc", "sha256-def"})

	if strings.Contains(policy, "unsafe-inline") && strings.Contains(scriptSrc(policy), "unsafe-inline") {
		t.Error("script-src admits unsafe-inline")
	}
	for _, h := range []string{"'sha256-abc'", "'sha256-def'"} {
		if !strings.Contains(scriptSrc(policy), h) {
			t.Errorf("script-src omits %s: %q", h, scriptSrc(policy))
		}
	}
	// The two the design names explicitly, each in its own directive so
	// neither reaches script.
	if !strings.Contains(policy, "font-src 'self' data:") {
		t.Errorf("the policy omits the data font source: %q", policy)
	}
	if !strings.Contains(policy, "worker-src 'self' blob:") {
		t.Errorf("the policy omits the blob worker source: %q", policy)
	}
}

func scriptSrc(policy string) string {
	for _, d := range strings.Split(policy, ";") {
		if strings.HasPrefix(strings.TrimSpace(d), "script-src ") {
			return strings.TrimSpace(d)
		}
	}
	return ""
}

// The content host is absent from every directive that can execute or frame
// what it points at. A file a user uploaded must not run as the application.
func TestUploadedContentIsNotAScriptSource(t *testing.T) {
	policy := CSP([]string{"sha256-abc"})
	if admitsUploadedContent(policy, "files.example.test") {
		t.Errorf("the content host is an executable source: %q", policy)
	}

	// The check itself discriminates, or the assertion above would be empty.
	bad := policy + "; frame-src 'self' files.example.test"
	if !admitsUploadedContent(bad, "files.example.test") {
		t.Error("a policy that does admit the content host was reported clean")
	}
}

// The session mask carries every bit the model defines, so adding a bit does
// not silently narrow what a session can do.
func TestTheSessionMaskCoversEveryBit(t *testing.T) {
	mask := sessionMask()
	for _, np := range acl.NamedPerms() {
		if !mask.Has(np.Perm) {
			t.Errorf("the session mask omits %s", np.Name)
		}
	}
}

// The trace header comes back on a served request, and on a refused one.
func TestTheTraceHeaderIsOnEveryResponse(t *testing.T) {
	app := chainWith(t, Public, Principal{Kind: CredentialNone}, nil)

	for _, c := range []struct{ what, host string }{
		{"a served request", "app.example.test"},
		{"a refused host", "evil.example.test"},
	} {
		req := httptest.NewRequest("GET", "http://"+c.host+"/x", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if id := rec.Header().Get(TraceHeader); id == "" {
			t.Errorf("%s carried no trace header", c.what)
		}
	}
}

// The session group refuses anything but a session, and answers as a path
// that is not there so a stranger with a word list cannot tell a real route
// from an absent one.
func TestTheSessionGroupRefusesThroughTheChain(t *testing.T) {
	build := func(access func(Deps) []gin.HandlerFunc, p Principal) int {
		app := chainWith(t, access, p, nil)
		r := httptest.NewRequest("GET", "http://app.example.test/x", nil)
		if p.Kind == CredentialSessionCookie {
			r.AddCookie(sessionCookie())
		} else if p.Kind != CredentialNone {
			r.Header.Set("Authorization", "Bearer token")
		}
		return send(t, app, r).status
	}

	if got := build(Public, Principal{Kind: CredentialNone}); got != http.StatusOK {
		t.Errorf("a public route answered %d", got)
	}
	if got := build(Session, Principal{Kind: CredentialNone}); got != http.StatusNotFound {
		t.Errorf("a session route with no credential answered %d, want 404", got)
	}
	if got := build(Session, Principal{Kind: CredentialBearerApp, Mask: acl.Read | acl.Write}); got != http.StatusNotFound {
		t.Errorf("an app password on the native API answered %d, want 404", got)
	}
	if got := build(Session, Principal{Kind: CredentialSessionCookie, Mask: sessionMask()}); got != http.StatusOK {
		t.Errorf("a session on a session route answered %d", got)
	}
}

// The principal the auth step resolved reaches a net/http handler through the
// request context, not only through Gin's.
func TestThePrincipalIsOnTheRequestContext(t *testing.T) {
	want := Principal{UserID: 7, Kind: CredentialSessionCookie, Mask: sessionMask()}
	var got Principal
	var ok bool
	app := chainWith(t, Session, want, nil, func(c *gin.Context) {
		got, ok = PrincipalFrom(c.Request.Context())
	})
	r := httptest.NewRequest("GET", "http://app.example.test/x", nil)
	r.AddCookie(sessionCookie())
	if status := send(t, app, r).status; status != http.StatusOK {
		t.Fatalf("the request answered %d", status)
	}
	if !ok || got.UserID != want.UserID || got.Kind != want.Kind {
		t.Errorf("the request context carried %+v (%v), want %+v", got, ok, want)
	}
}

// chainWith builds a server whose routes all sit behind one access group, so
// a test can drive one rule through the whole chain. Extra handlers run
// before the answering one.
func chainWith(t *testing.T, access func(Deps) []gin.HandlerFunc, p Principal, key []byte, extra ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	app := gin.New()
	d := Deps{
		Hosts:     func() Hosts { return namedHosts() },
		Trusted:   func() []netip.Prefix { return nil },
		Limiter:   NewLimiter(newStepClock(), 1000, 1000),
		Errors:    apierr.NewClassifier(nil),
		Principal: func(Credential) (Principal, bool) { return p, true },
		CSRFKey:   func() []byte { return key },
	}
	global, err := Global(d)
	if err != nil {
		t.Fatalf("Global: %v", err)
	}
	app.Use(global...)
	handlers := append(extra, func(c *gin.Context) { c.String(http.StatusOK, "handled") })
	app.Group("", access(d)...).Any("/*path", handlers...)
	return app
}

// A cookie mutation without the token is refused, and with it goes through.
func TestCSRFIsCheckedThroughTheChain(t *testing.T) {
	key := []byte("deployment key material")
	session := Principal{Kind: CredentialSessionCookie, Mask: sessionMask()}
	app := chainWith(t, Session, session, key)

	// The cookie's value is what the token derives from, so the test derives
	// from the same value the request carries.
	cookie := sessionCookie()
	post := func(token string) int {
		r := httptest.NewRequest("POST", "http://app.example.test/thing", nil)
		r.AddCookie(cookie)
		r.Header.Set("Origin", "http://app.example.test")
		if token != "" {
			r.Header.Set(CSRFHeader, token)
		}
		return send(t, app, r).status
	}

	if got := post(""); got != http.StatusForbidden {
		t.Errorf("a mutation with no token answered %d, want 403", got)
	}
	if got := post("wrong"); got != http.StatusForbidden {
		t.Errorf("a mutation with a wrong token answered %d, want 403", got)
	}
	if got := post(CSRFToken(key, cookie.Value)); got != http.StatusOK {
		t.Errorf("a mutation with the right token answered %d", got)
	}

	// A read needs no token at all.
	r := httptest.NewRequest("GET", "http://app.example.test/thing", nil)
	r.AddCookie(cookie)
	if got := send(t, app, r).status; got != http.StatusOK {
		t.Errorf("a read with no token answered %d", got)
	}
}

// An app password is not asked for a token, because an Authorization header is
// not ambient browser authority.
//
// Driven through the device group, which is where an app password is served.
func TestAnAppPasswordSkipsCSRFThroughTheChain(t *testing.T) {
	app := chainWith(t, Device,
		Principal{Kind: CredentialBearerApp, Mask: acl.Read | acl.Write},
		[]byte("deployment key material"))

	r := httptest.NewRequest("POST", "http://app.example.test/thing", nil)
	r.Header.Set("Authorization", "Bearer token")
	if got := send(t, app, r).status; got != http.StatusOK {
		t.Errorf("an app password mutation answered %d", got)
	}
}

// A deployment with no CSRF key refuses the mutation rather than serving it
// unchecked.
//
// Both spellings of "no key": no accessor at all, and an accessor that returns
// nothing. The second is the one that matters, because HMAC accepts an empty
// key without complaint, so a token would derive from nothing and every
// deployment would agree on the same value.
func TestNoCSRFKeyRefusesTheMutation(t *testing.T) {
	session := Principal{Kind: CredentialSessionCookie, Mask: sessionMask()}

	for _, c := range []struct {
		what string
		key  func() []byte
	}{
		{"no accessor", nil},
		{"an empty key", func() []byte { return nil }},
	} {
		app := gin.New()
		d := Deps{
			Hosts:     func() Hosts { return namedHosts() },
			Trusted:   func() []netip.Prefix { return nil },
			Limiter:   NewLimiter(newStepClock(), 1000, 1000),
			Errors:    apierr.NewClassifier(nil),
			Principal: func(Credential) (Principal, bool) { return session, true },
			CSRFKey:   c.key,
		}
		global, err := Global(d)
		if err != nil {
			t.Fatalf("Global: %v", err)
		}
		app.Use(global...)
		app.Group("", Session(d)...).Any("/*path", func(fc *gin.Context) { fc.String(http.StatusOK, "handled") })

		// The token an empty key derives, not an arbitrary string. An
		// arbitrary one fails the comparison anyway, which would make this
		// pass whether or not the guard exists: the attack is a caller who
		// knows there is no key, since without one every deployment agrees on
		// this same value.
		r := httptest.NewRequest("POST", "http://app.example.test/thing", nil)
		cookie := sessionCookie()
		r.AddCookie(cookie)
		r.Header.Set("Origin", "http://app.example.test")
		r.Header.Set(CSRFHeader, CSRFToken(nil, cookie.Value))
		if got := send(t, app, r).status; got != http.StatusForbidden {
			t.Errorf("%s answered %d, want 403", c.what, got)
		}
	}
}

// A declared length past the JSON bound is refused before the handler runs,
// and a route without LimitJSON is not bounded by it.
func TestLimitJSONRefusesByDeclaredLength(t *testing.T) {
	// The body really is this long: a declaration that overstates the bytes
	// fails in the transport before any middleware sees it, which would test
	// the transport rather than this step.
	oversized := strings.Repeat("x", int(limits.RequestBody)+1)

	jsonApp := chainWith(t, Public, Principal{Kind: CredentialNone}, nil, LimitJSON)
	r := httptest.NewRequest("POST", "http://app.example.test/thing", strings.NewReader(oversized))
	if got := send(t, jsonApp, r).status; got != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized body answered %d, want 413", got)
	}

	// The same body on a stream route is served: TUS sends far more than the
	// JSON bound and must not meet it here.
	streamApp := chainWith(t, Public, Principal{Kind: CredentialNone}, nil)
	r = httptest.NewRequest("POST", "http://app.example.test/thing", strings.NewReader(oversized))
	if got := send(t, streamApp, r).status; got != http.StatusOK {
		t.Errorf("an oversized stream answered %d", got)
	}

	r = httptest.NewRequest("POST", "http://app.example.test/thing", strings.NewReader(`{}`))
	if got := send(t, jsonApp, r).status; got != http.StatusOK {
		t.Errorf("a body within the bound answered %d", got)
	}
}

func TestLimitJSONBoundsRejectedBodyDraining(t *testing.T) {
	for _, size := range []int{drainCap - 1, drainCap + 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			body := strings.NewReader(strings.Repeat("x", size))
			app := chainWith(t, Public, Principal{Kind: CredentialNone}, nil, LimitJSON)
			req := httptest.NewRequest("POST", "http://app.example.test/thing", body)
			resp := send(t, app, req)
			if resp.status != http.StatusRequestEntityTooLarge {
				t.Fatalf("answered %d, want 413", resp.status)
			}
			if consumed := size - body.Len(); consumed > drainCap+1 {
				t.Fatalf("read %d bytes from a rejected body, cap is %d", consumed, drainCap+1)
			}
			if size < drainCap && body.Len() != 0 {
				t.Fatal("left an unread body within the drain cap")
			}
			if size > drainCap && !resp.close {
				t.Fatal("did not close a connection with an unread oversized body")
			}
		})
	}
}

// sessionMask is every permission bit, the mask a session carries.
func sessionMask() acl.Perms {
	var all acl.Perms
	for _, np := range acl.NamedPerms() {
		all |= np.Perm
	}
	return all
}

// admitsUploadedContent reports whether a policy names the content host in a
// directive that can execute or frame what it points at.
func admitsUploadedContent(policy, contentHost string) bool {
	for _, d := range strings.Split(policy, ";") {
		name, sources, found := strings.Cut(strings.TrimSpace(d), " ")
		if !found {
			continue
		}
		switch name {
		case "script-src", "worker-src", "frame-src", "child-src", "default-src":
			if strings.Contains(sources, contentHost) {
				return true
			}
		}
	}
	return false
}
