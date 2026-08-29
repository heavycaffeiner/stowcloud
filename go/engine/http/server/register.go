// Linux only, because it serves a Linux-only engine.
//go:build linux

// Registering the route table on the framework.
//
// The documents' path notation is translated here and nowhere else: {id}
// becomes fiber's :id and {path...} becomes its tail. One translation means
// there is no second spelling of a route to disagree with this one, and it is
// why the table itself stays framework-free.
package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
)

// Handlers supplies the function for each route, by name.
//
// By name rather than by index, so adding a route to the table does not
// silently shift what every later one runs.
type Handlers map[string]fiber.Handler

// Register mounts every route in the table.
//
// Validation runs first and reports everything wrong at once, before any
// listener is bound: a route missing its handler, a name the table does not
// have, and whatever route.Validate finds.
func Register(app *fiber.App, table []route.Route, h Handlers) error {
	if err := route.Validate(table); err != nil {
		return err
	}
	if err := checkHandlers(table, h); err != nil {
		return err
	}

	for _, r := range table {
		// Captured per iteration so each registration carries its own
		// metadata rather than the last route's.
		meta := r
		handler := h[r.Name]
		app.Add(r.Method, FiberPath(r.Path), func(c *fiber.Ctx) error {
			middleware.SetRequirement(c, meta.Requirement, meta.Body, meta.Name)
			return handler(c)
		})
	}
	return nil
}

// checkHandlers reports every route with no handler and every handler naming
// no route.
//
// Both directions. A handler for a name the table does not have is dead code
// that looks live, and it is how a route ends up mounted at the wrong path
// with the whole suite green.
func checkHandlers(table []route.Route, h Handlers) error {
	var problems []string

	named := make(map[string]bool, len(table))
	for _, r := range table {
		named[r.Name] = true
		if _, ok := h[r.Name]; !ok {
			problems = append(problems, fmt.Sprintf("the route %s has no handler", r.Name))
		}
	}
	for name := range h {
		if !named[name] {
			problems = append(problems, fmt.Sprintf("the handler %s names no route", name))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("routes: %s", strings.Join(problems, "; "))
}

// FiberPath translates the documents' notation into the framework's.
//
// {id} becomes :id and {path...} becomes *, which is what fiber matches a tail
// with. A handler reads a tail through the framework's wildcard accessor
// rather than by the name in the pattern, so the name is documentation on this
// side of the translation.
func FiberPath(path string) string {
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		inner := seg[1 : len(seg)-1]
		if strings.HasSuffix(inner, "...") {
			segments[i] = "*"
			continue
		}
		segments[i] = ":" + inner
	}
	return strings.Join(segments, "/")
}
