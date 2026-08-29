// Linux only, because it serves a Linux-only engine.
//go:build linux

// The fallback that serves the interface, and the prefixes it may never take.
//
// A single-page application needs an unmatched path to return its shell so the
// browser can route it. That is the opposite of what an API needs: a request
// to a mistyped API path must be a 404 the client can act on, not an HTML page
// it will try to parse as JSON. The list below is where the shell stops.
package server

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// ReservedPrefixes are the mounts the interface fallback may never answer for.
//
// Each is a surface with its own protocol and its own idea of a missing path.
// A WebDAV client receiving an HTML page where it expected a multistatus reads
// it as a server that has broken, and a sync client can do real damage acting
// on that.
func ReservedPrefixes() []string {
	return []string{
		"/api",
		"/dav",
		"/remote.php",
		"/ocs",
		"/s",
		"/c",
		"/emergency",
	}
}

// IsReserved reports whether a path belongs to a mount the fallback must not
// claim.
//
// Component-wise, so "/apidocs" is not under "/api": a prefix match on the raw
// string would reserve paths that merely start with the same letters, and the
// interface would lose them for no reason.
func IsReserved(path string) bool {
	for _, prefix := range ReservedPrefixes() {
		if underPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// underPrefix reports whether path is prefix or sits beneath it.
func underPrefix(path, prefix string) bool {
	p := strings.TrimRight(path, "/")
	pre := strings.TrimRight(prefix, "/")
	if p == pre {
		return true
	}
	return len(p) > len(pre) && strings.HasPrefix(p, pre) && p[len(pre)] == '/'
}

// InstallFallback mounts the interface shell for unmatched paths.
//
// Registered last, so every real route matched first. The reserved check is
// what stops it answering for a mount whose own routes did not match: without
// it, a mistyped API path returns the shell with a 200 and a client parses
// HTML as JSON.
func InstallFallback(app *fiber.App, shell fiber.Handler) error {
	if shell == nil {
		return fmt.Errorf("the interface fallback needs a handler")
	}
	app.Use(func(c *fiber.Ctx) error {
		if IsReserved(c.Path()) {
			// The mount that owns this prefix already declined, so this is a
			// miss within that mount rather than an interface path.
			//
			// Answering 404 here rather than calling Next is belt and braces:
			// nothing is registered behind this middleware, so both produce a
			// 404 today. What actually keeps the shell off these paths is the
			// check above, and a later reader should not mistake this line for
			// the guard.
			return fiber.NewError(fiber.StatusNotFound)
		}
		return shell(c)
	})
	return nil
}

// CheckRouteRoots reports any route whose path does not sit under a declared
// mount root.
//
// Run before the listener binds. A route mounted outside every root is
// unreachable through the origin split, and finding that at startup beats
// finding it when a user reports a screen that does nothing.
func CheckRouteRoots(paths []string, roots []string) error {
	var problems []string
	for _, p := range paths {
		under := slices.ContainsFunc(roots, func(root string) bool {
			return underPrefix(p, root)
		})
		if !under {
			problems = append(problems, fmt.Sprintf("%q is under no declared root", p))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("routes: %s", strings.Join(problems, "; "))
}
