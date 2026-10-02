//go:build linux

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/server"
)

// The rebuilt engine comes up on a real socket and answers a real request.
//
// Everything before this was verified one package at a time. This is the first
// check that the store opens, the services construct, the route table
// registers and a client gets an answer, all in one process.
func TestTheEngineServesARealRequest(t *testing.T) {
	t.Parallel()
	base := boot(t)

	status, body := get(t, base+"/api/v1/system/health")
	if status != http.StatusOK {
		t.Fatalf("the health probe answered %d: %s", status, body)
	}

	var health struct {
		Status  string   `json:"status"`
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("the health body does not parse: %v\n%s", err, body)
	}
	if health.Status == "" {
		t.Errorf("the health body carries no status: %s", body)
	}
}

// A declared route answers. A route the client knows and the server does not
// answer is one a client discovers and then cannot use.
//
// A credential-gated route has to be driven with a session to prove anything
// now. Anonymously it answers 404, and so does a path that was never mounted,
// which is the concealment working: this test would pass against a server
// with no routes at all if it asked anonymously.
func TestEveryDeclaredRouteAnswers(t *testing.T) {
	t.Parallel()
	base, _, sess := bootWithUser(t)

	// Public: served to anybody, and the one route that must answer before a
	// caller has any credential.
	if status, body := get(t, base+"/api/v1/system/health"); status != http.StatusOK {
		t.Errorf("the health probe answered %d, want 200: %s", status, body)
	}

	// Bound and credential-gated. The change channel is bookkeeping a caller
	// does about its own work, so a session reaches it and the upgrade is
	// demanded after that, of a caller the route will actually serve.
	//
	// The events entry has moved every time the route it named was bound,
	// from auth/oidc/config to system/setup to here. A route named as an
	// example of one shape has to move when it stops being that shape, or
	// the test quietly asserts the opposite of what it says.
	for _, c := range []struct {
		path string
		want int
	}{
		{"/api/v1/jobs", http.StatusOK},
		{"/api/v1/events", http.StatusUpgradeRequired},
	} {
		t.Run(c.path, func(t *testing.T) {
			status, body := authed(t, http.MethodGet, base+c.path, sess)
			if status != c.want {
				t.Errorf("%s answered %d, want %d: %s", c.path, status, c.want, body)
			}
			if status == http.StatusNotFound {
				t.Errorf("%s is declared but not registered", c.path)
			}
		})
	}
}

// A path no route names is a 404 rather than a hang or a crash.
func TestAnUnknownPathIsNotFound(t *testing.T) {
	t.Parallel()
	base := boot(t)

	status, _ := get(t, base+"/api/v1/nothing/here")
	if status != http.StatusNotFound {
		t.Errorf("an unknown path answered %d", status)
	}
}

// Every response is JSON, including a failure. The framework's own error page
// is HTML, and an HTML body in an API response is one a client cannot read.
func TestEveryAnswerIsJSON(t *testing.T) {
	t.Parallel()
	base := boot(t)

	for _, path := range []string{
		"/api/v1/system/health",
		"/api/v1/jobs",
		"/api/v1/auth/oidc/config",
		"/api/v1/nothing",
	} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, base+path)

			var into any
			if err := json.Unmarshal(body, &into); err != nil {
				t.Errorf("%s answered something that is not JSON: %v\n%s", path, err, body)
			}
		})
	}
}

// A correct assembly mounts without error.
func TestMountingACorrectAssembly(t *testing.T) {
	t.Parallel()
	e, err := server.Open(context.Background(), server.Options{DataDir: t.TempDir(), PasswordParams: fastPasswordParams(), Logger: discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	app := gin.New()
	if err := e.Mount(app); err != nil {
		t.Fatalf("a correct assembly was refused: %v", err)
	}
	if app == nil {
		t.Fatal("mounting returned no application and no error")
	}
}

// Closing releases the files. A boot that failed and left its databases open
// holds the data directory against the next attempt.
func TestClosingReleasesTheDatabases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := context.Background()

	e, err := server.Open(ctx, server.Options{DataDir: dir, PasswordParams: fastPasswordParams(), Logger: discard()})
	if err != nil {
		t.Fatal(err)
	}

	// Write through the engine, so there is something whose durability the
	// close has to settle.
	if _, werr := e.State.SweepDavLocks(ctx, 1); werr != nil {
		t.Fatalf("writing: %v", werr)
	}
	if cerr := e.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	// A closed database has checkpointed: the write-ahead log is emptied on
	// the way out, so the file on disk is the whole deployment. A close that
	// released nothing leaves the log holding the write.
	wal := filepath.Join(dir, "state.db-wal")
	info, err := os.Stat(wal)
	switch {
	case os.IsNotExist(err):
		// Removed entirely, which is also a settled state.
	case err != nil:
		t.Fatalf("stat: %v", err)
	case info.Size() != 0:
		t.Errorf("the write-ahead log still holds %d bytes after close", info.Size())
	}
}

// The chain runs on every request, checked by observing what only a running
// chain produces. A chain that was built and not mounted leaves the routes
// answering exactly as they do now, so nothing but its effects proves it.
func TestTheMiddlewareChainIsLive(t *testing.T) {
	t.Parallel()
	base := boot(t)

	resp, err := testClient().Get(base + "/api/v1/system/health")
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	// The security headers step. Each of these changes what a browser will do
	// with the response, so a missing one is a real weakening rather than a
	// cosmetic gap.
	for _, header := range []string{
		"Content-Security-Policy",
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
		"Cross-Origin-Opener-Policy",
		"Cross-Origin-Resource-Policy",
	} {
		if resp.Header.Get(header) == "" {
			t.Errorf("%s is not set, so that step did not run", header)
		}
	}
}

// The rate limiter throttles. Its absence is invisible until something is
// hammering the server, which is exactly when nobody is reading tests.
func TestTheRateLimiterThrottles(t *testing.T) {
	t.Parallel()
	base := boot(t)
	client := testClient()

	// Past the burst, by enough that the refill during the loop cannot make
	// up the difference.
	const attempts = 400

	var served, throttled int
	for i := 0; i < attempts; i++ {
		resp, err := client.Get(base + "/api/v1/system/health")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		switch resp.StatusCode {
		case http.StatusOK:
			served++
		case http.StatusTooManyRequests:
			throttled++
		default:
			t.Errorf("request %d answered %d", i, resp.StatusCode)
		}
		if cerr := resp.Body.Close(); cerr != nil {
			t.Fatalf("closing: %v", cerr)
		}
	}

	if throttled == 0 {
		t.Errorf("%d rapid requests were all served; the limiter is not running", attempts)
	}
	if served == 0 {
		t.Error("every request was throttled, so the limit is not usable")
	}
}

// The chain is mounted before the routes. Gin executes middleware in
// registration order, so every step sees a route before its handler writes.
func TestTheChainRunsBeforeTheRoutes(t *testing.T) {
	t.Parallel()
	base := boot(t)

	// A route's own response carries the headers the chain sets. If the chain
	// ran after the route, the response would already have been written.
	resp, err := testClient().Get(base + "/api/v1/jobs")
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("a route answered without the chain having run")
	}
}
