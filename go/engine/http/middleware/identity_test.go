// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"errors"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
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
	if CSPAdmitsUploadedContent(policy, "files.example.test") {
		t.Errorf("the content host is an executable source: %q", policy)
	}

	// The check itself discriminates, or the assertion above would be empty.
	bad := policy + "; frame-src 'self' files.example.test"
	if !CSPAdmitsUploadedContent(bad, "files.example.test") {
		t.Error("a policy that does admit the content host was reported clean")
	}
}

// The always-on headers are present and say what they should.
func TestTheSecurityHeadersAreSet(t *testing.T) {
	h := SecurityHeaders()
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if h[k] != want {
			t.Errorf("%s is %q, want %q", k, h[k], want)
		}
	}
	if h["Referrer-Policy"] == "" {
		t.Error("no referrer policy is set")
	}
}

// A session reaches every class; an app password does not reach a session
// route, because a credential handed to a device must not be able to change
// the password that revokes it.
func TestSessionRoutesRefuseAnAppPassword(t *testing.T) {
	session := Principal{Kind: CredentialSessionCookie, Mask: SessionMask()}
	app := Principal{Kind: CredentialBasicApp, Mask: acl.Read | acl.Write}
	none := Principal{Kind: CredentialNone}

	req := route.Requirement{Access: route.AccessSession}
	if err := Scope(req, session); err != nil {
		t.Errorf("a session was refused: %v", err)
	}
	if err := Scope(req, app); !errors.Is(err, ErrSessionRequired) {
		t.Errorf("an app password on a session route returned %v", err)
	}
	if err := Scope(req, none); !errors.Is(err, ErrCredentialRequired) {
		t.Errorf("no credential on a session route returned %v", err)
	}
}

// A permission route needs every declared bit, and only a session reaches it.
func TestAPermissionRouteNeedsEveryBitAndASession(t *testing.T) {
	req := route.Requirement{Access: route.AccessPerms, Perms: acl.Read | acl.Write}

	// A device credential does not reach the interface's own API at all, so
	// the bits it carries are never consulted.
	full := Principal{Kind: CredentialBearerApp, Mask: acl.Read | acl.Write | acl.Delete}
	if err := Scope(req, full); !errors.Is(err, ErrSessionRequired) {
		t.Errorf("an app password carrying both bits returned %v", err)
	}

	partial := Principal{Kind: CredentialSessionCookie, Mask: acl.Read}
	if err := Scope(req, partial); !errors.Is(err, ErrInsufficientPermission) {
		t.Errorf("a session carrying one of two bits returned %v", err)
	}

	// A session carries every bit, so it satisfies any permission route.
	if err := Scope(req, Principal{Kind: CredentialSessionCookie, Mask: SessionMask()}); err != nil {
		t.Errorf("a session on a permission route: %v", err)
	}
}

// Public needs nothing. Every other class needs the browser session: the
// native API is the interface's own surface, and a device credential belongs
// to the compatibility mount and the file protocol.
func TestTheOtherAccessClasses(t *testing.T) {
	none := Principal{Kind: CredentialNone}
	app := Principal{Kind: CredentialBasicApp}
	session := Principal{Kind: CredentialSessionCookie, Mask: SessionMask()}

	if err := Scope(route.Requirement{Access: route.AccessPublic}, none); err != nil {
		t.Errorf("a public route refused an anonymous request: %v", err)
	}
	if err := Scope(route.Requirement{Access: route.AccessAnyCredential}, session); err != nil {
		t.Errorf("any-credential refused a session: %v", err)
	}
	if err := Scope(route.Requirement{Access: route.AccessAnyCredential}, app); !errors.Is(err, ErrSessionRequired) {
		t.Errorf("any-credential admitted an app password: %v", err)
	}
	if err := Scope(route.Requirement{Access: route.AccessAnyCredential}, none); !errors.Is(err, ErrCredentialRequired) {
		t.Error("any-credential admitted an anonymous request")
	}
}

// An unset class is refused rather than treated as public. route.Validate
// already refuses it, and this is the second line: a class that slipped
// through must not default into the permissive one.
func TestAnUnsetAccessClassIsRefused(t *testing.T) {
	if err := Scope(route.Requirement{}, Principal{Kind: CredentialSessionCookie}); err == nil {
		t.Fatal("a route with no declared access admitted a request")
	}
}

// The session mask carries every bit the model defines, so adding a bit does
// not silently narrow what a session can do.
func TestTheSessionMaskCoversEveryBit(t *testing.T) {
	mask := SessionMask()
	for _, np := range acl.NamedPerms() {
		if !mask.Has(np.Perm) {
			t.Errorf("the session mask omits %s", np.Name)
		}
	}
}

// The trace header comes back on a served request, and on a refused one.
func TestTheTraceHeaderIsOnEveryResponse(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if err := Mount(app, Chain(), Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
	}, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.All("/*", func(c *fiber.Ctx) error { return c.SendString("handled") })

	for _, c := range []struct{ what, host string }{
		{"a served request", "app.example.test"},
		{"a refused host", "evil.example.test"},
	} {
		res, err := app.Test(httptest.NewRequest("GET", "http://"+c.host+"/x", nil), -1)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		id := res.Header.Get(TraceHeader)
		if cerr := res.Body.Close(); cerr != nil {
			t.Errorf("closing the response body: %v", cerr)
		}
		if id == "" {
			t.Errorf("%s carried no trace header", c.what)
		}
	}
}

// The scope step refuses through the chain, and its status distinguishes a
// missing credential from an insufficient one.
func TestScopeRefusesThroughTheChain(t *testing.T) {
	build := func(req route.Requirement, p Principal) int {
		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		// Registration attaches the route's metadata, which is what the chain
		// reads. Set before the chain so ACLScope sees it.
		app.Use(func(c *fiber.Ctx) error {
			SetRequirement(c, req, route.BodyNone, "test.route")
			return c.Next()
		})
		if err := Mount(app, Chain(), Deps{
			Hosts:     func() Hosts { return namedHosts() },
			Trusted:   func() []netip.Prefix { return nil },
			Limiter:   NewLimiter(newStepClock(), 1000, 1000),
			Principal: func(Credential) (Principal, bool) { return p, true },
		}, nil); err != nil {
			t.Fatalf("Mount: %v", err)
		}
		app.All("/*", func(c *fiber.Ctx) error { return c.SendString("handled") })

		r := httptest.NewRequest("GET", "http://app.example.test/x", nil)
		if p.Kind == CredentialSessionCookie {
			r.AddCookie(sessionCookie())
		} else if p.Kind != CredentialNone {
			r.Header.Set("Authorization", "Bearer token")
		}
		return send(t, app, r).status
	}

	if got := build(route.Requirement{Access: route.AccessPublic}, Principal{Kind: CredentialNone}); got != fiber.StatusOK {
		t.Errorf("a public route answered %d", got)
	}
	// Every refusal answers as a path that is not there, so a stranger with a
	// word list cannot tell a real route from an absent one.
	if got := build(route.Requirement{Access: route.AccessSession}, Principal{Kind: CredentialNone}); got != fiber.StatusNotFound {
		t.Errorf("a session route with no credential answered %d, want 404", got)
	}
	if got := build(
		route.Requirement{Access: route.AccessPerms, Perms: acl.Read | acl.Write},
		Principal{Kind: CredentialSessionCookie, Mask: acl.Read},
	); got != fiber.StatusNotFound {
		t.Errorf("a session missing a bit answered %d, want 404", got)
	}
	if got := build(
		route.Requirement{Access: route.AccessPerms, Perms: acl.Read},
		Principal{Kind: CredentialBearerApp, Mask: acl.Read | acl.Write},
	); got != fiber.StatusNotFound {
		t.Errorf("an app password on the native API answered %d, want 404", got)
	}
}

// chainWith builds a server whose routes all carry one requirement and body
// class, so a test can drive one rule through the whole chain.
func chainWith(t *testing.T, req route.Requirement, body route.BodyClass, p Principal, key []byte) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		SetRequirement(c, req, body, "test.route")
		return c.Next()
	})
	if err := Mount(app, Chain(), Deps{
		Hosts:     func() Hosts { return namedHosts() },
		Trusted:   func() []netip.Prefix { return nil },
		Limiter:   NewLimiter(newStepClock(), 1000, 1000),
		Principal: func(Credential) (Principal, bool) { return p, true },
		CSRFKey:   func() []byte { return key },
	}, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.All("/*", func(c *fiber.Ctx) error { return c.SendString("handled") })
	return app
}

// A cookie mutation without the token is refused, and with it goes through.
func TestCSRFIsCheckedThroughTheChain(t *testing.T) {
	key := []byte("deployment key material")
	session := Principal{Kind: CredentialSessionCookie, Mask: SessionMask()}
	app := chainWith(t, route.Requirement{Access: route.AccessSession}, route.BodyNone, session, key)

	// The cookie's value is what the token derives from, so the test derives
	// from the same value the request carries.
	cookie := sessionCookie()
	post := func(token string) int {
		r := httptest.NewRequest("POST", "http://app.example.test/thing", nil)
		r.AddCookie(cookie)
		r.Header.Set("Origin", "https://app.example.test")
		if token != "" {
			r.Header.Set(CSRFHeader, token)
		}
		return send(t, app, r).status
	}

	if got := post(""); got != fiber.StatusForbidden {
		t.Errorf("a mutation with no token answered %d, want 403", got)
	}
	if got := post("wrong"); got != fiber.StatusForbidden {
		t.Errorf("a mutation with a wrong token answered %d, want 403", got)
	}
	if got := post(CSRFToken(key, cookie.Value)); got != fiber.StatusOK {
		t.Errorf("a mutation with the right token answered %d", got)
	}

	// A read needs no token at all.
	r := httptest.NewRequest("GET", "http://app.example.test/thing", nil)
	r.AddCookie(cookie)
	if got := send(t, app, r).status; got != fiber.StatusOK {
		t.Errorf("a read with no token answered %d", got)
	}
}

// An app password is not asked for a token, because an Authorization header is
// not ambient browser authority.
//
// Driven through a public route, since the native API admits only the browser
// session now. The surfaces this rule actually serves are the compatibility
// mount and the file protocol, which carry no route requirement and so never
// reach the scope step, but pass through this one.
func TestAnAppPasswordSkipsCSRFThroughTheChain(t *testing.T) {
	app := chainWith(t,
		route.Requirement{Access: route.AccessPublic}, route.BodyNone,
		Principal{Kind: CredentialBearerApp, Mask: acl.Read | acl.Write},
		[]byte("deployment key material"))

	r := httptest.NewRequest("POST", "http://app.example.test/thing", nil)
	r.Header.Set("Authorization", "Bearer token")
	if got := send(t, app, r).status; got != fiber.StatusOK {
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
	session := Principal{Kind: CredentialSessionCookie, Mask: SessionMask()}

	for _, c := range []struct {
		what string
		deps func(*fiber.App) Deps
	}{
		{"no accessor", func(*fiber.App) Deps { return Deps{} }},
		{"an empty key", func(*fiber.App) Deps { return Deps{CSRFKey: func() []byte { return nil }} }},
	} {
		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Use(func(fc *fiber.Ctx) error {
			SetRequirement(fc, route.Requirement{Access: route.AccessSession}, route.BodyNone, "test.route")
			return fc.Next()
		})
		d := c.deps(app)
		d.Hosts = func() Hosts { return namedHosts() }
		d.Trusted = func() []netip.Prefix { return nil }
		d.Limiter = NewLimiter(newStepClock(), 1000, 1000)
		d.Principal = func(Credential) (Principal, bool) { return session, true }
		if err := Mount(app, Chain(), d, nil); err != nil {
			t.Fatalf("Mount: %v", err)
		}
		app.All("/*", func(fc *fiber.Ctx) error { return fc.SendString("handled") })

		// The token an empty key derives, not an arbitrary string. An
		// arbitrary one fails the comparison anyway, which would make this
		// pass whether or not the guard exists: the attack is a caller who
		// knows there is no key, since without one every deployment agrees on
		// this same value.
		r := httptest.NewRequest("POST", "http://app.example.test/thing", nil)
		cookie := sessionCookie()
		r.AddCookie(cookie)
		r.Header.Set("Origin", "https://app.example.test")
		r.Header.Set(CSRFHeader, CSRFToken(nil, cookie.Value))
		if got := send(t, app, r).status; got != fiber.StatusForbidden {
			t.Errorf("%s answered %d, want 403", c.what, got)
		}
	}
}

// A declared length past the route's class is refused before the handler runs,
// and a stream route is not bounded by it.
func TestTheBodyLimitRefusesByDeclaredLength(t *testing.T) {
	// The body really is this long: a declaration that overstates the bytes
	// fails in the transport before any middleware sees it, which would test
	// fiber rather than this step.
	oversized := strings.Repeat("x", int(limits.RequestBody)+1)

	jsonApp := chainWith(t,
		route.Requirement{Access: route.AccessPublic}, route.BodyJSON,
		Principal{Kind: CredentialNone}, nil)
	r := httptest.NewRequest("POST", "http://app.example.test/thing", strings.NewReader(oversized))
	if got := send(t, jsonApp, r).status; got != fiber.StatusRequestEntityTooLarge {
		t.Errorf("an oversized body answered %d, want 413", got)
	}

	// The same body on a stream route is served: TUS sends far more than the
	// JSON bound and must not meet it here.
	streamApp := chainWith(t,
		route.Requirement{Access: route.AccessPublic}, route.BodyStream,
		Principal{Kind: CredentialNone}, nil)
	r = httptest.NewRequest("POST", "http://app.example.test/thing", strings.NewReader(oversized))
	if got := send(t, streamApp, r).status; got != fiber.StatusOK {
		t.Errorf("an oversized stream answered %d", got)
	}

	// A body within the bound is served.
	okApp := chainWith(t,
		route.Requirement{Access: route.AccessPublic}, route.BodyJSON,
		Principal{Kind: CredentialNone}, nil)
	r = httptest.NewRequest("POST", "http://app.example.test/thing", strings.NewReader(`{}`))
	if got := send(t, okApp, r).status; got != fiber.StatusOK {
		t.Errorf("a body within the bound answered %d", got)
	}
}
