// Linux only, because it serves a Linux-only engine.
//go:build linux

// The startup checks, run as one step before anything binds a socket.
//
// Every check here already exists on its own. What this adds is that they all
// run, together, at a point where failing costs nothing: a route mounted under
// no root, a chain missing a step and a forgotten sweep are each a defect that
// otherwise surfaces as a user reporting that something does not work.
package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
)

// Preflight is everything the checks need to see.
type Preflight struct {
	// Routes is the complete table about to be mounted.
	Routes []route.Route
	// Roots are the mount roots the origin split serves.
	Roots []string
	// Chain is the middleware order.
	Chain []middleware.Step
	// Protocols are the mounts that declare their own paths.
	Protocols []middleware.ProtocolPaths
	// Tasks is the periodic table.
	Tasks []PeriodicTask
	// Handlers are the functions bound to the routes, by name.
	Handlers Handlers
}

// Check runs every startup check and reports all of them at once.
//
// All at once rather than the first: an operator or a developer meeting these
// one restart at a time learns the list slowly, and the list is short enough
// to read whole.
func Check(p Preflight) error {
	var problems []string

	if err := route.Validate(p.Routes); err != nil {
		problems = append(problems, err.Error())
	}
	if err := checkHandlers(p.Routes, p.Handlers); err != nil {
		problems = append(problems, err.Error())
	}

	paths := make([]string, 0, len(p.Routes))
	for _, r := range p.Routes {
		paths = append(paths, r.Path)
	}
	if err := CheckRouteRoots(paths, p.Roots); err != nil {
		problems = append(problems, err.Error())
	}

	if err := middleware.ValidateChain(p.Chain); err != nil {
		problems = append(problems, err.Error())
	}
	for i, decl := range p.Protocols {
		if err := middleware.ValidateProtocolPaths(decl); err != nil {
			problems = append(problems, fmt.Sprintf("protocol mount %d: %s", i, err))
		}
	}
	if err := ValidateTasks(p.Tasks); err != nil {
		problems = append(problems, err.Error())
	}

	// A native route sitting under a protocol mount's file prefix is served by
	// whichever registered first, which is a decision nobody wrote down. It is
	// checked here rather than in either half, because neither half can see
	// the other.
	problems = append(problems, checkNativeAgainstProtocols(p)...)

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("startup: %s", strings.Join(problems, "; "))
}

// checkNativeAgainstProtocols reports a native route under a protocol's file
// prefix, and a native route the interface fallback would swallow.
func checkNativeAgainstProtocols(p Preflight) []string {
	var problems []string
	for _, r := range p.Routes {
		for _, decl := range p.Protocols {
			if middleware.UnderFilePrefix(r.Path, decl.FilePrefixes) {
				problems = append(problems, fmt.Sprintf(
					"the route %s is under a protocol file prefix", r.Name))
			}
		}
		if !IsReserved(r.Path) {
			// The fallback answers anything not reserved, so a native route
			// outside every reserved prefix is one the interface shell would
			// claim if its own registration were ever dropped.
			problems = append(problems, fmt.Sprintf(
				"the route %s is outside every reserved prefix, so the interface fallback could claim it",
				r.Name))
		}
	}
	return problems
}
