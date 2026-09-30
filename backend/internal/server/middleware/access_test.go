// Linux only, matching the package under test.
//go:build linux

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
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

// accessServer mounts the chain with an access sink and one handler, behind
// the session group when session is set and the public group otherwise.
func accessServer(t *testing.T, session bool, answer gin.HandlerFunc) (*gin.Engine, *accessRecorder) {
	t.Helper()
	rec := &accessRecorder{}
	app := gin.New()
	d := Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
		Errors:  apierr.NewClassifier(nil),
		Access:  rec,
	}
	global, err := Global(d)
	if err != nil {
		t.Fatalf("Global: %v", err)
	}
	app.Use(global...)
	access := Public(d)
	if session {
		access = Session(d)
	}
	g := app.Group("", access...)
	g.GET("/files/read", answer)
	g.GET("/s/:token/download", answer)
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

// A failure names its cause; a refusal does not.
//
// A 5xx is this server's own fault and the message is the only record of what
// went wrong: a thumbnail answering 500 with nothing written anywhere leaves
// an operator a status code and no next step, which is exactly what happened.
// A 4xx is the client's doing and the status plus the body it already holds
// describe it, so repeating the text would put one sentence in two places on
// the common path.
func TestOnlyAFailureRecordsItsCause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{"a failure", http.StatusInternalServerError, "the decoder worker died"},
		{"a refusal", http.StatusNotFound, ""},
		{"a success", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, rec := accessServer(t, false,
				func(c *gin.Context) {
					// The shape a handler that writes its own status has: the
					// error is recorded and nil is returned, so the chain
					// never sees it.
					SetCause(c, errors.New("the decoder worker died"))
					c.Status(tc.status)
				})

			req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
			if got := send(t, app, req).status; got != tc.status {
				t.Fatalf("the request answered %d, want %d", got, tc.status)
			}

			lines := rec.all()
			if len(lines) != 1 {
				t.Fatalf("the request produced %d lines", len(lines))
			}
			if got := lines[0].Cause; got != tc.want {
				t.Errorf("the line's cause is %q, want %q", got, tc.want)
			}
		})
	}
}

// The error the chain caught wins over one a handler recorded, since it is the
// one that actually decided the status.
func TestTheChainsOwnErrorIsThePreferredCause(t *testing.T) {
	app, rec := accessServer(t, false,
		func(c *gin.Context) {
			SetCause(c, errors.New("what the handler noticed"))
			if gerr := c.Error(errors.New("what the chain caught")); gerr != nil {
				c.Abort()
			}
			c.Status(http.StatusInternalServerError)
		})

	req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
	if got := send(t, app, req).status; got != http.StatusInternalServerError {
		t.Fatalf("a returned error answered %d", got)
	}

	lines := rec.all()
	if len(lines) != 1 {
		t.Fatalf("the request produced %d lines", len(lines))
	}
	if got := lines[0].Cause; got != "what the chain caught" {
		t.Errorf("the line's cause is %q", got)
	}
}

// One line per request, whatever answered it, carrying the method, the path,
// the route, the status and the resolved client.
func TestEveryRequestLeavesOneLine(t *testing.T) {
	app, rec := accessServer(t, false,
		func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	for range 3 {
		req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
		if got := send(t, app, req).status; got != http.StatusOK {
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
	case e.Route != "/files/read":
		t.Errorf("the route is %q", e.Route)
	case e.Status != http.StatusOK:
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
	app, rec := accessServer(t, true,
		func(c *gin.Context) { c.String(http.StatusOK, "never reached") })

	req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
	if got := send(t, app, req).status; got != http.StatusNotFound {
		t.Fatalf("an anonymous request answered %d, want the concealed 404", got)
	}

	lines := rec.all()
	if len(lines) != 1 {
		t.Fatalf("the refused request produced %d lines", len(lines))
	}
	if lines[0].Status != http.StatusNotFound {
		t.Errorf("the refusal recorded status %d", lines[0].Status)
	}
	if lines[0].Credential != CredentialNone {
		t.Errorf("the refusal recorded credential %v", lines[0].Credential)
	}
}

// The status is the one actually sent, which is what makes this step wrap the
// error mapper rather than sit inside it.
func TestTheRecordedStatusIsTheOneAnswered(t *testing.T) {
	app, rec := accessServer(t, false,
		func(c *gin.Context) { c.Status(http.StatusTeapot) })

	req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
	if got := send(t, app, req).status; got != http.StatusTeapot {
		t.Fatalf("the handler's status did not reach the client: %d", got)
	}
	if got := rec.all()[0].Status; got != http.StatusTeapot {
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
	app, rec := accessServer(t, false,
		func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest("GET", "http://app.example.test/s/secret-link-token/download", nil)
	if got := send(t, app, req).status; got != http.StatusOK {
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
	app := gin.New()
	global, err := Global(Deps{
		Hosts:   func() Hosts { return namedHosts() },
		Trusted: func() []netip.Prefix { return nil },
		Limiter: NewLimiter(newStepClock(), 1000, 1000),
		Errors:  apierr.NewClassifier(nil),
	})
	if err != nil {
		t.Fatalf("Global: %v", err)
	}
	app.Use(global...)
	app.Any("/*path", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	req := httptest.NewRequest("GET", "http://app.example.test/files/read", nil)
	if got := send(t, app, req).status; got != http.StatusOK {
		t.Errorf("a server with no access sink answered %d", got)
	}
}
