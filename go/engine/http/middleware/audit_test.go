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

// recordingSink collects what a request left behind.
type recordingSink struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (s *recordingSink) Record(e AuditEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *recordingSink) last(t *testing.T) AuditEvent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		t.Fatal("nothing was recorded")
	}
	return s.events[len(s.events)-1]
}

// The record has no field that could hold a credential, a header map or a
// body. "Log the request" is how a cookie ends up in a log file, so the record
// is a named list and this is what keeps it one.
func TestTheAuditRecordCannotHoldACredential(t *testing.T) {
	rt := reflect.TypeOf(AuditEvent{})
	for i := range rt.NumField() {
		f := rt.Field(i)
		name := strings.ToLower(f.Name)
		// Credential is the kind, not the value: an enum naming which sort
		// proved the principal.
		if name == "credential" && f.Type == reflect.TypeOf(CredentialKind(0)) {
			continue
		}
		for _, banned := range []string{
			"credential", "cookie", "token", "secret", "password",
			"header", "body", "csrf", "authorization",
		} {
			if strings.Contains(name, banned) {
				t.Errorf("AuditEvent carries the field %s (%s)", f.Name, f.Type)
			}
		}
	}
}

// The record names the route rather than the path. A path carries ids and
// filenames, which are data about the person making the request.
func TestTheRecordNamesTheRouteNotThePath(t *testing.T) {
	sink := &recordingSink{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		SetRequirement(c, route.Requirement{Access: route.AccessPublic}, route.BodyNone, "files.read")
		return c.Next()
	})
	if err := Mount(app, Chain(), Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
		Audit:   sink,
	}, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.All("/*", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "http://app.example.test/files/alice/taxes-2025.pdf", nil)
	if got := send(t, app, req).status; got != fiber.StatusOK {
		t.Fatalf("the request answered %d", got)
	}

	e := sink.last(t)
	if e.Route != "files.read" {
		t.Errorf("the record names %q", e.Route)
	}
	if strings.Contains(e.Route, "taxes") || strings.Contains(e.Route, "alice") {
		t.Errorf("the record carries the path: %q", e.Route)
	}
	if e.Method != "GET" {
		t.Errorf("the method is %q", e.Method)
	}
	if e.Trace == "" {
		t.Error("the record carries no trace id")
	}
}

// The recorded status is the one actually sent, which is why this step wraps
// the error mapper rather than sitting inside it.
func TestTheRecordedStatusIsTheOneSent(t *testing.T) {
	sink := &recordingSink{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if err := Mount(app, Chain(), Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
		Audit:   sink,
	}, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.All("/*", func(c *fiber.Ctx) error {
		return fiber.NewError(fiber.StatusNotFound)
	})

	req := httptest.NewRequest("GET", "http://app.example.test/gone", nil)
	if got := send(t, app, req).status; got != fiber.StatusNotFound {
		t.Fatalf("the request answered %d", got)
	}
	if e := sink.last(t); e.Status != fiber.StatusNotFound {
		t.Errorf("the record holds status %d, want 404", e.Status)
	}
}

// A refused request is recorded too. A boundary refusal is exactly the event
// an administrator is looking for, and one that leaves no trace is one nobody
// can investigate.
func TestARefusedRequestIsRecorded(t *testing.T) {
	sink := &recordingSink{}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if err := Mount(app, Chain(), Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
		Audit:   sink,
	}, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.All("/*", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "http://evil.example.test/anything", nil)
	if got := send(t, app, req).status; got != fiber.StatusMisdirectedRequest {
		t.Fatalf("the request answered %d", got)
	}

	sink.mu.Lock()
	n := len(sink.events)
	sink.mu.Unlock()
	if n == 0 {
		t.Fatal("a refused request left no record")
	}
	if e := sink.last(t); e.Status != fiber.StatusMisdirectedRequest {
		t.Errorf("the record holds status %d, want 421", e.Status)
	}
}

// An anonymous request is recorded as anonymous rather than not recorded,
// since who did not identify themselves is itself worth knowing.
func TestAnAnonymousRequestIsRecordedAsAnonymous(t *testing.T) {
	e := AuditRecordFor("trace-1", "GET", "files.read", 200,
		mustAddr(t, "192.168.1.9"), Principal{}, OriginApp)

	if e.Principal != 0 {
		t.Errorf("an anonymous request recorded principal %d", e.Principal)
	}
	if e.Credential != CredentialNone {
		t.Errorf("an anonymous request recorded the credential %v", e.Credential)
	}

	// A signed-in request records the account and which kind proved it, never
	// the value that proved it.
	signed := AuditRecordFor("trace-2", "POST", "account.password", 200,
		mustAddr(t, "192.168.1.9"),
		Principal{UserID: 7, Kind: CredentialSessionCookie},
		OriginApp)
	if signed.Principal != 7 || signed.Credential != CredentialSessionCookie {
		t.Errorf("the signed-in record is %+v", signed)
	}
}

// A server with no sink wired records nothing rather than failing.
func TestNoSinkIsNotAFailure(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if err := Mount(app, Chain(), Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
	}, nil); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	app.All("/*", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "http://app.example.test/x", nil)
	if got := send(t, app, req).status; got != fiber.StatusOK {
		t.Errorf("a request with no audit sink answered %d", got)
	}
}
