//go:build linux

// What each method has to be allowed to do before it runs.
//
// The mount asks core for a resolution carrying these bits. A method handler
// never evaluates a grant itself, so there is one place where a permission
// question is answered and one place to read when asking what a method needs.
package dav

import (
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// Requirement is what one endpoint of a request must permit.
type Requirement struct {
	// Source is what the request's target must allow.
	Source acl.Perms
	// Dest is what a COPY or MOVE destination must allow. Zero when the
	// method has no second endpoint.
	Dest acl.Perms
}

// HasDest reports whether the method addresses a second endpoint.
func (r Requirement) HasDest() bool { return r.Dest != 0 }

// methodPerms is the whole table. A method absent from it is not served.
//
//nolint:gochecknoglobals // a lookup table, read-only after init.
var methodPerms = map[string]Requirement{
	"GET":      {Source: acl.Read},
	"HEAD":     {Source: acl.Read},
	"PROPFIND": {Source: acl.Read},
	"SEARCH":   {Source: acl.Read},
	"REPORT":   {Source: acl.Read},

	"PUT":       {Source: acl.Write},
	"MKCOL":     {Source: acl.Write},
	"PROPPATCH": {Source: acl.Write},
	"LOCK":      {Source: acl.Write},
	"UNLOCK":    {Source: acl.Write},

	"DELETE": {Source: acl.Delete},

	// COPY reads the source and writes a new destination. MOVE additionally
	// removes the source, which is why it needs Delete there and COPY does
	// not: the difference between the two methods is exactly that removal.
	"COPY": {Source: acl.Read, Dest: acl.Write | acl.Create},
	"MOVE": {Source: acl.Write | acl.Delete, Dest: acl.Write | acl.Create},
}

// MethodRequirement returns what a method needs, and whether it is served.
func MethodRequirement(method string) (Requirement, bool) {
	req, ok := methodPerms[method]
	return req, ok
}

// Methods returns every served method, sorted.
func Methods() []string {
	out := make([]string, 0, len(methodPerms)+1)
	for m := range methodPerms {
		out = append(out, m)
	}
	// OPTIONS is discovery and needs no permission, so it is not in the table
	// that says what permission a method needs. It is still served.
	out = append(out, "OPTIONS")
	sort.Strings(out)
	return out
}

// AllowSet describes what a resource accepts, for an Allow header.
type AllowSet struct {
	// IsDir selects the create method: a collection takes MKCOL and a file
	// takes PUT, since a PUT replaces bytes a collection does not have.
	IsDir bool
	// Exists is false for a path nothing occupies, where both create methods
	// apply because either would bring something into being.
	Exists bool
	// Locking is whether this deployment records locks. Advertising LOCK
	// without a table would have a client take one it believes is recorded.
	Locking bool
	// Extra names methods an extension registered, such as SEARCH. One that
	// is not a served method is dropped rather than advertised.
	Extra []string
}

// AllowHeader renders what a resource accepts.
//
// One function rather than a per-caller list, so the header beside a refusal
// and the header on an OPTIONS cannot disagree about the same resource.
func AllowHeader(set AllowSet) string {
	served := map[string]bool{}
	for _, m := range Methods() {
		served[m] = true
	}

	// SEARCH and REPORT exist only when something claims their vocabulary, so
	// they leave the base set and return through Extra.
	delete(served, "SEARCH")
	delete(served, "REPORT")
	for _, m := range set.Extra {
		if _, known := methodPerms[m]; known {
			served[m] = true
		}
	}

	if !set.Locking {
		delete(served, "LOCK")
		delete(served, "UNLOCK")
	}

	// The create methods, which depend on what is at the path.
	switch {
	case !set.Exists:
		// Nothing there: either method would create, so both are offered.
	case set.IsDir:
		delete(served, "PUT")
	default:
		delete(served, "MKCOL")
	}

	out := make([]string, 0, len(served))
	for m := range served {
		out = append(out, m)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
