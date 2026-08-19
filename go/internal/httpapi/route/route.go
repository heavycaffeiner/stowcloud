// Package route is the route table: one place where every route declares its
// method, its pattern, what it requires of the credential that reaches it,
// and the handler that answers it. The scope layer reads this table, and a
// route that declares no requirement is a startup error rather than a runtime
// default, which is what makes step 9 a layer instead of something each
// handler remembers.
package route

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
)

// Access is the kind of credential a route accepts. It exists because an app
// password's scope is a filesystem-capability mask, and no combination of
// filesystem bits means "and also administer the account".
type Access uint8

const (
	// AccessUnset is the zero value, which is also "no requirement declared".
	// Validation refuses it: a route added without a requirement is a startup
	// error, not a runtime default, which is what makes the scope layer a
	// layer rather than something each handler remembers.
	AccessUnset Access = iota

	// AccessSelfAdmin is a self-service or administrative route. Sessions
	// only: an app password never reaches it, scoped or not, because the
	// admin surface is not a filesystem capability.
	AccessSelfAdmin

	// AccessAny is bookkeeping any authenticated caller may reach regardless
	// of scope: job status, logout, the event channel.
	AccessAny

	// AccessPerms requires the caller to hold Perms. An unrestricted app
	// password passes; a restricted one must hold every bit.
	AccessPerms
)

// Requirement is what a route demands of the credential.
type Requirement struct {
	Access Access
	Perms  acl.Perms
}

// Route is one entry in the table.
type Route struct {
	Method  string
	Pattern string
	Req     Requirement
	Handler http.HandlerFunc
}

// Lookup resolves the requirement a request needs, or reports that no route
// owns the method and path. The middleware consults this; the mux serves the
// same table, so the two cannot disagree about which route a request hit.
type Lookup func(method, path string) (Requirement, bool)

// From builds the lookup for a table. It is the one place patterns are
// matched, so the scope layer and the mux share one matcher.
func From(table []Route) Lookup {
	exact := map[string]Requirement{}
	var wild []Route
	for _, rt := range table {
		if strings.Contains(rt.Pattern, "{") {
			wild = append(wild, rt)
			continue
		}
		exact[rt.Method+" "+rt.Pattern] = rt.Req
	}
	return func(method, path string) (Requirement, bool) {
		if req, ok := exact[method+" "+path]; ok {
			return req, true
		}
		for _, rt := range wild {
			if rt.Method == method && Match(rt.Pattern, path) {
				return rt.Req, true
			}
		}
		return Requirement{}, false
	}
}

// Match reports whether a registered pattern matches a request path. Patterns
// are literal except for a single {name} wildcard segment, which is all this
// surface's routes use and all Go's ServeMux supports anyway.
func Match(pattern, path string) bool {
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	ap := strings.Split(strings.Trim(path, "/"), "/")
	if len(pp) != len(ap) {
		return false
	}
	for i := range pp {
		if strings.HasPrefix(pp[i], "{") && strings.HasSuffix(pp[i], "}") {
			continue
		}
		if pp[i] != ap[i] {
			return false
		}
	}
	return true
}

// Validate checks the properties the gate refuses at startup: every route has
// a declared requirement, no two routes share a method and pattern, and every
// pattern is one this surface can dispatch.
func Validate(table []Route) error {
	seen := map[string]string{}
	for _, rt := range table {
		if rt.Method == "" || rt.Pattern == "" {
			return fmt.Errorf("a route with an empty method or pattern")
		}
		if rt.Req.Access == AccessUnset {
			return fmt.Errorf("route %s %s declares no requirement; a route with no scope is refused at startup", rt.Method, rt.Pattern)
		}
		if rt.Req.Access > AccessPerms {
			return fmt.Errorf("route %s %s declares an unknown access kind", rt.Method, rt.Pattern)
		}
		if !validPattern(rt.Pattern) {
			return fmt.Errorf("route %s %s has a pattern this surface cannot dispatch", rt.Method, rt.Pattern)
		}
		key := rt.Method + " " + rt.Pattern
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("routes %s and %s are the same method and pattern", prev, rt.Pattern)
		}
		seen[key] = rt.Pattern
	}
	return nil
}

func validPattern(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "" {
			return false
		}
		if strings.HasPrefix(seg, "{") {
			if !strings.HasSuffix(seg, "}") || seg == "{}" {
				return false
			}
		} else if strings.ContainsAny(seg, "{}") {
			return false
		}
	}
	return true
}
