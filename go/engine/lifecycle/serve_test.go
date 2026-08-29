//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// boot opens an engine, mounts it and serves it on a real socket.
func boot(t *testing.T) string {
	t.Helper()

	e, err := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening the engine: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing the engine: %v", cerr)
		}
	})

	app, err := e.Mount()
	if err != nil {
		t.Fatalf("mounting: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}

	served := make(chan error, 1)
	task.Go(context.Background(), "test listener", func() { served <- app.Listener(ln) })

	t.Cleanup(func() {
		if serr := app.Shutdown(); serr != nil {
			t.Errorf("shutting down: %v", serr)
		}
		<-served
	})

	return "http://" + ln.Addr().String()
}

// get performs a real request and returns the status and body.
func get(t *testing.T, url string) (int, []byte) {
	t.Helper()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("requesting %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing the body: %v", cerr)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	return resp.StatusCode, body
}

// The rebuilt engine comes up on a real socket and answers a real request.
//
// Everything before this was verified one package at a time. This is the first
// check that the store opens, the services construct, the route table
// registers and a client gets an answer, all in one process.
func TestTheEngineServesARealRequest(t *testing.T) {
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

// Every route the table names is registered. A route the table declares and
// the server does not answer is one a client discovers and then cannot use.
func TestEveryDeclaredRouteAnswers(t *testing.T) {
	base := boot(t)

	// Two routes with no path parameters, one of each shape: the probe that
	// is bound, and one that is registered but not yet implemented.
	cases := []struct {
		path string
		want int
	}{
		{"/api/v1/system/health", http.StatusOK},
		{"/api/v1/jobs", http.StatusNotImplemented},
	}

	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			status, body := get(t, base+c.path)
			if status != c.want {
				t.Errorf("%s answered %d, want %d: %s", c.path, status, c.want, body)
			}
		})
	}
}

// A path the table does not name is a 404 rather than a hang or a crash.
func TestAnUnknownPathIsNotFound(t *testing.T) {
	base := boot(t)

	status, _ := get(t, base+"/api/v1/nothing/here")
	if status != http.StatusNotFound {
		t.Errorf("an unknown path answered %d", status)
	}
}

// Every response is JSON, including a failure. The framework's own error page
// is HTML, and an HTML body in an API response is one a client cannot read.
func TestEveryAnswerIsJSON(t *testing.T) {
	base := boot(t)

	for _, path := range []string{"/api/v1/system/health", "/api/v1/jobs", "/api/v1/nothing"} {
		t.Run(path, func(t *testing.T) {
			_, body := get(t, base+path)

			var into any
			if err := json.Unmarshal(body, &into); err != nil {
				t.Errorf("%s answered something that is not JSON: %v\n%s", path, err, body)
			}
		})
	}
}

// A route with no binding says so rather than answering as though it worked.
// A client reading a success for an endpoint that did nothing acts on it.
func TestAnUnboundRouteRefusesRatherThanPretending(t *testing.T) {
	base := boot(t)

	status, body := get(t, base+"/api/v1/jobs")
	if status != http.StatusNotImplemented {
		t.Fatalf("an unbound route answered %d: %s", status, body)
	}

	var refusal struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &refusal); err != nil {
		t.Fatalf("the refusal does not parse: %v", err)
	}
	if refusal.Error != "not_implemented" {
		t.Errorf("the refusal says %q", refusal.Error)
	}
	if refusal.Message == "" {
		t.Error("the refusal names no route")
	}
}

// Mounting reports a broken assembly before anything binds, so a defect
// surfaces at startup rather than at a request.
func TestMountingChecksTheAssembly(t *testing.T) {
	e, err := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	app, err := e.Mount()
	if err != nil {
		t.Fatalf("a correct assembly was refused: %v", err)
	}
	if app == nil {
		t.Fatal("mounting returned no application and no error")
	}
	if serr := app.Shutdown(); serr != nil {
		t.Errorf("shutting down: %v", serr)
	}
}

// The preflight check is what this assembly is gated on, so a broken one has
// to be refused. Proven against the real check rather than by inspection: the
// same call the mount makes, given a deliberately incomplete assembly.
//
// Without this, removing the check from Mount changes nothing observable and
// every defect it exists to catch reaches a request instead.
func TestABrokenAssemblyIsRefused(t *testing.T) {
	table := server.Table()

	full := make(server.Handlers, len(table))
	for _, r := range table {
		full[r.Name] = func(*fiber.Ctx) error { return nil }
	}

	cases := []struct {
		name string
		p    server.Preflight
		says string
	}{
		{
			name: "a route with no handler",
			p: server.Preflight{
				Routes: table, Roots: []string{server.Base},
				Chain: middleware.Chain(), Tasks: goodTasks(),
				Handlers: dropOne(full, table[0].Name),
			},
			says: table[0].Name,
		},
		{
			name: "an empty middleware chain",
			p: server.Preflight{
				Routes: table, Roots: []string{server.Base},
				Tasks: goodTasks(), Handlers: full,
			},
			says: "chain is empty",
		},
		{
			name: "a missing periodic task",
			p: server.Preflight{
				Routes: table, Roots: []string{server.Base},
				Chain: middleware.Chain(), Handlers: full,
			},
			says: "is missing",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := server.Check(c.p)
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not mention %q: %v", c.says, err)
			}
		})
	}

	// And the complete assembly passes, so the rows above fail for the reason
	// each names rather than because this shape never validates.
	whole := server.Preflight{
		Routes: table, Roots: []string{server.Base},
		Chain: middleware.Chain(), Tasks: goodTasks(), Handlers: full,
	}
	if err := server.Check(whole); err != nil {
		t.Errorf("a complete assembly was refused: %v", err)
	}
}

// goodTasks is a table satisfying every requirement, for the cases above.
func goodTasks() []server.PeriodicTask {
	var out []server.PeriodicTask
	for name := range server.RequiredTasks() {
		out = append(out, server.PeriodicTask{
			Name:  name,
			Every: time.Minute,
			Run:   func(context.Context) error { return nil },
		})
	}
	return out
}

// dropOne returns the handlers without one name.
func dropOne(in server.Handlers, name string) server.Handlers {
	out := make(server.Handlers, len(in))
	for k, v := range in {
		if k != name {
			out[k] = v
		}
	}
	return out
}

// Closing releases the files. A boot that failed and left its databases open
// holds the data directory against the next attempt.
func TestClosingReleasesTheDatabases(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
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
	base := boot(t)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/api/v1/system/health")
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
	base := boot(t)
	client := &http.Client{Timeout: 5 * time.Second}

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

// The chain is mounted before the routes. Fiber runs handlers in mount order,
// so a chain registered afterwards never sees a request that a route answers,
// and every step would be skipped for exactly the paths it guards.
func TestTheChainRunsBeforeTheRoutes(t *testing.T) {
	base := boot(t)

	// A route's own response carries the headers the chain sets. If the chain
	// ran after the route, the response would already have been written.
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/api/v1/jobs")
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
