// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
)

// accessRecorder collects the lines a request produced.
type accessRecorder struct {
	mu    sync.Mutex
	lines []AccessEvent
}

func (r *accessRecorder) Access(e AccessEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, e)
}

func (r *accessRecorder) all() []AccessEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AccessEvent(nil), r.lines...)
}

// accessServer mounts the chain with an access sink and one handler.
func accessServer(t *testing.T, req route.Requirement, answer fiber.Handler) (*fiber.App, *accessRecorder) {
	t.Helper()
	rec := &accessRecorder{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		SetRequirement(c, req, route.BodyNone, "files.read")
		return c.Next()
	})
	if err := Mount(app, Chain(), Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
		Access:  rec,
	}, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.All("/*", answer)
	return app, rec
}

// The line has no field that could hold a credential. This log is written on
// every request including the unauthenticated ones, so it is the last place a
// header or a cookie may be copied into.
func TestTheAccessLineCannotHoldACredential(t *testing.T) {
	rt := reflect.TypeOf(AccessEvent{})
	for i := range rt.NumField() {
		f := rt.Field(i)
		name := strings.ToLower(f.Name)
		if name == "credential" && f.Type == reflect.TypeOf(CredentialKind(0)) {
			continue
		}
		for _, banned := range []string{
			"credential", "cookie", "token", "secret", "password",
			"header", "body", "csrf", "authorization", "query",
		} {
			if strings.Contains(name, banned) {
				t.Errorf("AccessEvent carries the field %s (%s)", f.Name, f.Type)
			}
		}
	}
}

// One line per request, whatever answered it, carrying the method, the path,
// the route, the status and the resolved client.
func TestEveryRequestLeavesOneLine(t *testing.T) {
	app, rec := accessServer(t,
		route.Requirement{Access: route.AccessPublic},
		func(c *fiber.Ctx) error { return c.SendString("ok") })

	for range 3 {
		req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
		if got := send(t, app, req).status; got != fiber.StatusOK {
			t.Fatalf("the request answered %d", got)
		}
	}

	lines := rec.all()
	if len(lines) != 3 {
		t.Fatalf("three requests produced %d lines", len(lines))
	}
	e := lines[0]
	switch {
	case e.Method != "GET":
		t.Errorf("the method is %q", e.Method)
	case e.Path != "/files/read":
		t.Errorf("the path is %q", e.Path)
	case e.Route != "files.read":
		t.Errorf("the route is %q", e.Route)
	case e.Status != fiber.StatusOK:
		t.Errorf("the status is %d", e.Status)
	case e.Trace == "":
		t.Error("the line carries no trace id")
	case !e.Client.IsValid():
		t.Error("the line carries no client address")
	}
}

// A refusal is a line too. An access log that held only the served requests
// could not answer whether a client ever reached the server.
func TestARefusalIsRecordedWithItsStatus(t *testing.T) {
	app, rec := accessServer(t,
		route.Requirement{Access: route.AccessSession},
		func(c *fiber.Ctx) error { return c.SendString("never reached") })

	req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
	if got := send(t, app, req).status; got != fiber.StatusNotFound {
		t.Fatalf("an anonymous request answered %d, want the concealed 404", got)
	}

	lines := rec.all()
	if len(lines) != 1 {
		t.Fatalf("the refused request produced %d lines", len(lines))
	}
	if lines[0].Status != fiber.StatusNotFound {
		t.Errorf("the refusal recorded status %d", lines[0].Status)
	}
	if lines[0].Credential != CredentialNone {
		t.Errorf("the refusal recorded credential %v", lines[0].Credential)
	}
}

// The status is the one actually sent, which is what makes this step wrap the
// error mapper rather than sit inside it.
func TestTheRecordedStatusIsTheOneAnswered(t *testing.T) {
	app, rec := accessServer(t,
		route.Requirement{Access: route.AccessPublic},
		func(c *fiber.Ctx) error { return fiber.NewError(fiber.StatusTeapot) })

	req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
	if got := send(t, app, req).status; got != fiber.StatusTeapot {
		t.Fatalf("the handler's status did not reach the client: %d", got)
	}
	if got := rec.all()[0].Status; got != fiber.StatusTeapot {
		t.Errorf("the line recorded status %d", got)
	}
}

// A token that rides in the URL is replaced before the line is written. A log
// file is copied into a bug report, and a link token is the whole credential.
func TestRedactPathRemovesATokenSegment(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"/s/AbCdEf123", "/s/{token}"},
		{"/s/AbCdEf123/download", "/s/{token}/download"},
		{"/s/AbCdEf123/zip", "/s/{token}/zip"},
		{"/index.php/s/AbCdEf123", "/index.php/s/{token}"},
		{"/index.php/s/AbCdEf123/download", "/index.php/s/{token}/download"},
		{"/login/v2/flow/AbCdEf123", "/login/v2/flow/{token}"},
		{"/index.php/login/v2/flow/AbCdEf123", "/index.php/login/v2/flow/{token}"},

		// Nothing to redact, and nothing changed. A path that merely starts
		// with the same letters is not a token.
		{"/api/v1/files/read", "/api/v1/files/read"},
		{"/settings", "/settings"},
		{"/s/", "/s/"},
		{"/status.php", "/status.php"},
	} {
		if got := RedactPath(tc.in); got != tc.want {
			t.Errorf("RedactPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The line carries the redacted path, not the raw one, when the request went
// to a surface whose URL holds a secret.
func TestTheLineCarriesTheRedactedPath(t *testing.T) {
	app, rec := accessServer(t,
		route.Requirement{Access: route.AccessPublic},
		func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "http://app.example.test/s/secret-link-token/download", nil)
	if got := send(t, app, req).status; got != fiber.StatusOK {
		t.Fatalf("the request answered %d", got)
	}

	got := rec.all()[0].Path
	if strings.Contains(got, "secret-link-token") {
		t.Errorf("the line carries the link token: %q", got)
	}
	if got != "/s/{token}/download" {
		t.Errorf("the line's path is %q", got)
	}
}

// No sink is not a failure. A server assembled without one logs nothing and
// serves everything.
func TestNoAccessSinkIsNotAFailure(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if err := Mount(app, Chain(), Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
	}, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.All("/*", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
	if got := send(t, app, req).status; got != fiber.StatusOK {
		t.Errorf("a server with no access sink answered %d", got)
	}
}
