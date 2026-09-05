// Linux only, matching the file under test.
//go:build linux

package server

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
)

// shellApp mounts the real v1 table and the fallback behind it, which is the
// order the assembly uses.
func shellApp(t *testing.T) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	table := Table()
	h := make(Handlers, len(table))
	for _, r := range table {
		name := r.Name
		h[name] = func(c *fiber.Ctx) error {
			c.Set("Sc-Test-Route", name)
			return c.SendString(name)
		}
	}
	if err := Register(app, table, h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := InstallFallback(app, func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/html")
		return c.SendString("<!doctype html><title>shell</title>")
	}); err != nil {
		t.Fatalf("InstallFallback: %v", err)
	}
	return app
}

// ask returns the status and body for a path.
func ask(t *testing.T, app *fiber.App, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "http://app.test"+path, nil)
	req.RequestURI = ""
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("Test %s: %v", path, err)
	}
	body, rerr := io.ReadAll(res.Body)
	if cerr := res.Body.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if rerr != nil {
		t.Fatalf("reading the body of %s: %v", path, rerr)
	}
	return res.StatusCode, string(body)
}

// The interface answers an unmatched path, which is what lets a browser deep
// link into a route the server does not know about.
func TestTheShellAnswersAnUnmatchedPath(t *testing.T) {
	app := shellApp(t)
	for _, path := range []string{"/", "/files", "/settings/network", "/anything/at/all"} {
		status, body := ask(t, app, path)
		if status != fiber.StatusOK {
			t.Errorf("%s answered %d", path, status)
		}
		if !strings.Contains(body, "shell") {
			t.Errorf("%s answered %q", path, body)
		}
	}
}

// A mistyped path under a reserved mount is a 404, never the shell. A client
// receiving HTML with a 200 parses it as its own format and reports a failure
// that has nothing to do with what went wrong.
func TestAMistypedReservedPathIsNotTheShell(t *testing.T) {
	app := shellApp(t)
	for _, path := range []string{
		"/api/v1/nosuchthing",
		"/api",
		"/dav/files/alice/gone.txt",
		"/remote.php/dav/files",
		"/ocs/v2.php/anything",
		"/s/sometoken",
		"/c/someclaim",
		"/emergency",
		"/emergency/settings",
	} {
		status, body := ask(t, app, path)
		if status == fiber.StatusOK {
			t.Errorf("%s answered 200 with %q", path, body)
		}
		if strings.Contains(body, "shell") {
			t.Errorf("%s answered with the interface shell", path)
		}
	}
}

// A real route still answers. The fallback is registered behind the table, so
// reserving a prefix must not take the routes under it.
func TestTheRealRoutesStillAnswer(t *testing.T) {
	app := shellApp(t)
	table := Table()

	var checked int
	for _, r := range table {
		if r.Method != "GET" || len(route.Params(r.Path)) > 0 {
			continue
		}
		status, _ := ask(t, app, r.Path)
		if status != fiber.StatusOK {
			t.Errorf("%s %s answered %d", r.Method, r.Path, status)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no parameterless GET route was checked")
	}
}

// The prefix match is component-wise, so a path that merely starts with the
// same letters is still the interface's.
func TestReservationIsComponentWise(t *testing.T) {
	for _, c := range []struct {
		path     string
		reserved bool
	}{
		{"/api", true},
		{"/api/", true},
		{"/api/v1/files", true},
		{"/apidocs", false},
		{"/apiary/thing", false},
		{"/dav", true},
		{"/dav2", false},
		{"/s", true},
		{"/s/token", true},
		{"/settings", false},
		{"/c/claim", true},
		{"/contacts", false},
		{"/", false},
		{"/files", false},
	} {
		if got := IsReserved(c.path); got != c.reserved {
			t.Errorf("IsReserved(%q) = %v", c.path, got)
		}
	}
}

// Every route in the shipped table sits under a declared root, which is what
// makes the origin split able to serve it at all.
func TestEveryShippedRouteIsUnderARoot(t *testing.T) {
	paths := make([]string, 0, len(Table()))
	for _, r := range Table() {
		paths = append(paths, r.Path)
	}
	if err := CheckRouteRoots(paths, []string{"/api"}); err != nil {
		t.Errorf("the shipped table: %v", err)
	}

	// The check discriminates, or the assertion above is empty.
	err := CheckRouteRoots(append(paths, "/stray/route"), []string{"/api"})
	if err == nil {
		t.Fatal("a route outside every root was accepted")
	}
	if !strings.Contains(err.Error(), "/stray/route") {
		t.Errorf("the report does not name the stray route: %v", err)
	}
}

// A fallback with no handler is a startup refusal rather than a server that
// answers unmatched paths with nothing.
func TestAFallbackNeedsAHandler(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if err := InstallFallback(app, nil); err == nil {
		t.Error("a fallback with no handler was installed")
	}
}
