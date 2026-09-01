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

// boot opens an engine and serves it on a real socket.
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
	return serve(t, e)
}

// serve mounts an already-open engine and puts it behind a real listener.
func serve(t *testing.T, e *lifecycle.Engine) string {
	t.Helper()

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
		// Bounded, because Shutdown waits for every open connection and the
		// test client keeps one alive: measured, an unbounded wait hangs the
		// package for the client's full 90-second idle timeout on roughly one
		// run in fifteen.
		//
		// The budget is generous rather than tight. One package run starts
		// over a hundred of these, and under a full-tree run with race
		// instrumentation and eight packages in parallel a two-second budget
		// expired on its own: the failure was a shutdown timeout, not a test
		// finding anything. A shutdown that genuinely hangs still fails,
		// because this is far below the client's idle timeout.
		if serr := app.ShutdownWithTimeout(shutdownBudget); serr != nil {
			t.Errorf("shutting down: %v", serr)
		}
		<-served
	})

	return "http://" + ln.Addr().String()
}

// shutdownBudget bounds every test server's wind-down. Well under the
// client's 90-second idle timeout, so a real hang is still caught, and well
// above what a loaded machine needs to close an idle listener.
const shutdownBudget = 20 * time.Second

// testClient is a client that does not hold connections open.
//
// Keep-alive is what makes a shutdown wait: the server has an idle connection
// it must not cut, and closing after each response means the test's own
// cleanup does not race the client's idle timeout.
func testClient() *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
}

// get performs a real request and returns the status and body.
func get(t *testing.T, url string) (int, []byte) {
	t.Helper()

	resp, err := testClient().Get(url)
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

	// One of each shape a route can be in: bound and public, bound and
	// needing a credential, and registered with no binding yet.
	cases := []struct {
		path string
		want int
	}{
		{"/api/v1/system/health", http.StatusOK},
		{"/api/v1/jobs", http.StatusUnauthorized},
		// Bound and credential-gated: the change channel is bookkeeping a
		// caller does about its own work, so it takes any credential and
		// refuses a request carrying none. The upgrade is demanded after
		// that, of a caller the route will actually serve.
		//
		// This entry has moved every time the route it named was bound, from
		// auth/oidc/config to system/setup to here. A route named as an
		// example of one shape has to move when it stops being that shape, or
		// the test quietly asserts the opposite of what it says.
		{"/api/v1/events", http.StatusUnauthorized},
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

// Every route the table names has a binding.
//
// This replaces a test that drove the fallback through the last unbound route.
// There is no longer one, so what is checked is the property that mattered:
// a route the table declares and the switch does not handle answers the
// fallback, and a client discovering it would read a refusal rather than a
// success for an endpoint that did nothing.
func TestEveryTableRouteIsBound(t *testing.T) {
	unbound := lifecycle.UnboundRoutesForTest()
	if len(unbound) != 0 {
		t.Errorf("the table names %d routes with no binding: %v", len(unbound), unbound)
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

// The chain is mounted before the routes. Fiber runs handlers in mount order,
// so a chain registered afterwards never sees a request that a route answers,
// and every step would be skipped for exactly the paths it guards.
func TestTheChainRunsBeforeTheRoutes(t *testing.T) {
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

// Every route the table declares is registered, and no name is registered
// twice or under a name the table does not have.
//
// Register already refuses a missing handler, so what this adds is the other
// direction: a binding whose name was misspelled would silently fall through
// to the not-implemented default, and the route would look served while doing
// nothing. The count is what catches that.
func TestEveryRouteHasExactlyOneHandler(t *testing.T) {
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
		t.Fatalf("mounting: %v", err)
	}
	defer func() {
		if serr := app.ShutdownWithTimeout(shutdownBudget); serr != nil {
			t.Errorf("shutting down: %v", serr)
		}
	}()

	table := server.Table()
	if len(table) == 0 {
		t.Fatal("the route table is empty")
	}

	seen := map[string]bool{}
	for _, r := range table {
		if seen[r.Name] {
			t.Errorf("%s appears twice in the table", r.Name)
		}
		seen[r.Name] = true
	}
}

// A bound route does not answer not-implemented. That is what separates a
// binding from a misspelled one, which falls through to the default and looks
// served while doing nothing.
func TestABoundRouteIsNotTheDefault(t *testing.T) {
	base, token, _ := bootWithUser(t)

	// Every route bound so far that takes no path parameter and no share.
	bound := []string{
		"/api/v1/system/health",
		"/api/v1/jobs",
		"/api/v1/account/sessions",
		"/api/v1/account/app-passwords",
		"/api/v1/files/list?path=%2F",
	}

	for _, path := range bound {
		t.Run(path, func(t *testing.T) {
			status, body := authed(t, http.MethodGet, base+path, token)
			if status == http.StatusNotImplemented {
				t.Errorf("%s fell through to the default: %s", path, body)
			}
			if strings.Contains(string(body), "not_implemented") {
				t.Errorf("%s answered the default body: %s", path, body)
			}
		})
	}
}
