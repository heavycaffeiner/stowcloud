// Linux only, matching the file under test.
//go:build linux

package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
)

// The notation translates once, and only in the ways the documents use.
func TestThePathNotationTranslates(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/api/v1/auth/login", "/api/v1/auth/login"},
		{"/api/v1/account/sessions/{id}", "/api/v1/account/sessions/:id"},
		{"/api/v1/files/{path...}", "/api/v1/files/*"},
		{"/api/v1/shares/{id}/files/{path...}", "/api/v1/shares/:id/files/*"},
		{"/", "/"},
	} {
		if got := FiberPath(c.in); got != c.want {
			t.Errorf("FiberPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// handlersFor builds one handler per route, each answering with its own name
// so a test can tell which route matched.
func handlersFor(table []route.Route) Handlers {
	h := make(Handlers, len(table))
	for _, r := range table {
		name := r.Name
		h[name] = func(c *fiber.Ctx) error {
			// The name goes in a header as well as the body: HEAD has no body
			// by definition, so a body-only answer cannot identify which route
			// matched for those.
			c.Set("Sc-Test-Route", name)
			return c.SendString(name)
		}
	}
	return h
}

// The whole shipped table registers, which is the first thing that would break
// if a path used notation the translation does not handle.
func TestTheShippedTableRegisters(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	table := Table()
	if err := Register(app, table, handlersFor(table)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(table) == 0 {
		t.Fatal("the table is empty")
	}
}

// A registered route dispatches to its own handler, checked by asking the
// handler which route it is. This is what makes the table's paths real rather
// than a list nothing reads.
func TestEachRouteDispatchesToItsOwnHandler(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	table := Table()
	if err := Register(app, table, handlersFor(table)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A sample across the notations rather than all 87: a literal, a named
	// parameter and a tail each exercise a different branch of the
	// translation, and every route goes through one of the three.
	for _, r := range table {
		params := route.Params(r.Path)
		concrete := r.Path
		for _, p := range params {
			concrete = strings.Replace(concrete, "{"+p+"...}", "some/nested/path", 1)
			concrete = strings.Replace(concrete, "{"+p+"}", "42", 1)
		}
		if strings.Contains(concrete, "{") {
			t.Errorf("the path %q was not fully substituted: %q", r.Path, concrete)
			continue
		}

		req := httptest.NewRequest(r.Method, "http://app.test"+concrete, nil)
		req.RequestURI = ""
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("%s %s: %v", r.Method, concrete, err)
		}
		got := res.Header.Get("Sc-Test-Route")
		status := res.StatusCode
		if cerr := res.Body.Close(); cerr != nil {
			t.Errorf("closing the body: %v", cerr)
		}

		if status != fiber.StatusOK {
			t.Errorf("%s %s answered %d", r.Method, concrete, status)
			continue
		}
		if got != r.Name {
			t.Errorf("%s %s dispatched to %q, want %q", r.Method, concrete, got, r.Name)
		}
	}
}

// A route with no handler and a handler naming no route are both refused, and
// both at once, so a misassembly is one report rather than a sequence of them.
func TestRegisterRefusesAMismatchedHandlerSet(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	table := Table()

	h := handlersFor(table)
	delete(h, table[0].Name)
	h["nothing.names.this"] = func(c *fiber.Ctx) error { return nil }

	err := Register(app, table, h)
	if err == nil {
		t.Fatal("a mismatched handler set was registered")
	}
	if !strings.Contains(err.Error(), "has no handler") {
		t.Errorf("the report omits the missing handler: %v", err)
	}
	if !strings.Contains(err.Error(), "names no route") {
		t.Errorf("the report omits the orphaned handler: %v", err)
	}
}

// Registration attaches each route's own metadata. Without the per-iteration
// capture every route would carry the last one's requirement, which is the
// classic loop-variable defect and would hand the whole API one access class.
func TestEachRouteCarriesItsOwnMetadata(t *testing.T) {
	table := Table()

	// Two routes with different access classes, so carrying the wrong one is
	// visible. The table is checked for having such a pair at all, since a
	// table where every route agreed would make this test vacuous.
	var public, session route.Route
	for _, r := range table {
		if r.Requirement.Access == route.AccessPublic && public.Name == "" {
			public = r
		}
		if r.Requirement.Access == route.AccessSession && session.Name == "" {
			session = r
		}
	}
	if public.Name == "" || session.Name == "" {
		t.Skip("the table has no public and session pair to distinguish")
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	seen := map[string]route.Access{}
	h := make(Handlers, len(table))
	for _, r := range table {
		name := r.Name
		h[name] = func(c *fiber.Ctx) error {
			// What the chain would read is what registration left behind.
			if req, ok := middleware.RequirementOf(c); ok {
				seen[name] = req.Access
			}
			return c.SendString(name)
		}
	}
	if err := Register(app, table, h); err != nil {
		t.Fatalf("Register: %v", err)
	}

	for _, r := range []route.Route{public, session} {
		concrete := concretePath(r.Path)
		req := httptest.NewRequest(r.Method, "http://app.test"+concrete, nil)
		req.RequestURI = ""
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("%s: %v", r.Name, err)
		}
		if cerr := res.Body.Close(); cerr != nil {
			t.Errorf("closing the body: %v", cerr)
		}
		if seen[r.Name] != r.Requirement.Access {
			t.Errorf("%s carried %v, want %v", r.Name, seen[r.Name], r.Requirement.Access)
		}
	}
}

func concretePath(path string) string {
	out := path
	for _, p := range route.Params(path) {
		out = strings.Replace(out, "{"+p+"...}", "some/nested/path", 1)
		out = strings.Replace(out, "{"+p+"}", "42", 1)
	}
	return out
}
