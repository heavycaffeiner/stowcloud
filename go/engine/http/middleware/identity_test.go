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

// A permission route needs every declared bit, not any of them.
func TestAPermissionRouteNeedsEveryBit(t *testing.T) {
	req := route.Requirement{Access: route.AccessPerms, Perms: acl.Read | acl.Write}

	full := Principal{Kind: CredentialBearerApp, Mask: acl.Read | acl.Write | acl.Delete}
	if err := Scope(req, full); err != nil {
		t.Errorf("a mask carrying both bits was refused: %v", err)
	}

	partial := Principal{Kind: CredentialBearerApp, Mask: acl.Read}
	if err := Scope(req, partial); !errors.Is(err, ErrInsufficientPermission) {
		t.Errorf("a mask carrying one of two bits returned %v", err)
	}

	// A session carries every bit, so it satisfies any permission route.
	if err := Scope(req, Principal{Kind: CredentialSessionCookie, Mask: SessionMask()}); err != nil {
		t.Errorf("a session on a permission route: %v", err)
	}
}

// Public needs nothing; any-credential needs one of either kind.
func TestTheOtherAccessClasses(t *testing.T) {
	none := Principal{Kind: CredentialNone}
	app := Principal{Kind: CredentialBasicApp}

	if err := Scope(route.Requirement{Access: route.AccessPublic}, none); err != nil {
		t.Errorf("a public route refused an anonymous request: %v", err)
	}
	if err := Scope(route.Requirement{Access: route.AccessAnyCredential}, app); err != nil {
		t.Errorf("any-credential refused an app password: %v", err)
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
			SetRequirement(c, req)
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
	if got := build(route.Requirement{Access: route.AccessSession}, Principal{Kind: CredentialNone}); got != fiber.StatusUnauthorized {
		t.Errorf("a session route with no credential answered %d, want 401", got)
	}
	if got := build(
		route.Requirement{Access: route.AccessPerms, Perms: acl.Read | acl.Write},
		Principal{Kind: CredentialBearerApp, Mask: acl.Read},
	); got != fiber.StatusForbidden {
		t.Errorf("an app password missing a bit answered %d, want 403", got)
	}
}
