// Package route is the presentation tier's route metadata: what a route is,
// what credential it demands, and what shape of body it accepts.
//
// It carries no handler type and imports nothing from the web framework. The
// server binds a handler to each entry at registration; keeping that out of
// here means the table, its validation and its tests exist before any
// framework is chosen, and a change of framework does not touch the contract.
//
// The path notation is the documents' own: {id} for a named parameter and
// {path...} for a tail. Translation into a framework's syntax happens once, at
// registration, and this canonical form is what route dumps and metadata carry.
// Nothing pastes a table path directly into a router.
package route

import (
	"fmt"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// Access is the credential class a route demands.
type Access uint8

const (
	// AccessUnset is the zero value and is never valid. A route that forgot to
	// declare its class must not default into the permissive one, so the zero
	// value is the one that fails validation.
	AccessUnset Access = iota

	// AccessPublic needs no credential. A stranger's browser reaches it.
	AccessPublic

	// AccessSession needs the browser session cookie specifically. An app
	// password does not satisfy it: these are the routes that manage the
	// account itself, and a filesystem credential handed to a device must not
	// be able to change the password that would revoke it.
	AccessSession

	// AccessAnyCredential needs any authenticated caller, session or app
	// password. It is for bookkeeping a device legitimately does on its own
	// behalf, such as reading the progress of a job it started.
	AccessAnyCredential

	// AccessPerms needs a credential carrying particular permission bits. The
	// bits travel in the requirement beside it.
	AccessPerms
)

func (a Access) String() string {
	switch a {
	case AccessPublic:
		return "public"
	case AccessSession:
		return "session"
	case AccessAnyCredential:
		return "any-credential"
	case AccessPerms:
		return "perms"
	}
	return "unset"
}

// Requirement is a route's complete credential demand.
type Requirement struct {
	Access Access

	// Perms are the bits AccessPerms demands, and must be zero otherwise. A
	// permission set on a route that never consults it reads as a guarantee
	// nothing enforces.
	Perms acl.Perms
}

// BodyClass is the shape of request body a route accepts. The boundary reads
// it to choose a size limit and a parser, so a route that forgets to declare
// one would be parsed by whatever ran last.
type BodyClass uint8

const (
	// BodyNone is a route that reads no body. A body arriving on one is
	// ignored rather than parsed.
	BodyNone BodyClass = iota
	// BodyJSON is the native API's own envelope.
	BodyJSON
	// BodyDAVXML is a WebDAV request document.
	BodyDAVXML
	// BodyStream is arbitrary bytes: an upload chunk or a file write.
	BodyStream
)

func (b BodyClass) String() string {
	switch b {
	case BodyJSON:
		return "json"
	case BodyDAVXML:
		return "dav-xml"
	case BodyStream:
		return "stream"
	}
	return "none"
}

// Route is one mounted endpoint's metadata.
//
// Handler is deliberately absent. The table declares what a route is and what
// it demands; the server supplies what runs, at registration, where the
// framework is already in scope.
type Route struct {
	// Method is an uppercase HTTP method.
	Method string

	// Path is the canonical pattern: a literal beginning with /, with {id} for
	// a named parameter and {path...} for a tail.
	Path string

	// Name identifies the route in dumps, metrics and error text. It is unique
	// across the table, so a log line naming a route names exactly one.
	Name string

	Requirement Requirement
	Body        BodyClass
}

// Validate reports every problem in a table at once.
//
// All of them, rather than the first: a startup that fails on one missing
// declaration at a time turns a table-wide mistake into a sequence of restarts.
// The caller runs this before binding a listener, so a malformed table never
// reaches a request.
func Validate(routes []Route) error {
	if len(routes) == 0 {
		return fmt.Errorf("route: the table is empty")
	}

	var problems []string
	seenRoute := map[string]bool{}
	seenName := map[string]bool{}

	for _, r := range routes {
		where := r.Method + " " + r.Path
		if r.Method == "" || r.Path == "" {
			problems = append(problems, fmt.Sprintf("a route has no method or path (name %q)", r.Name))
			continue
		}
		if r.Method != strings.ToUpper(r.Method) {
			problems = append(problems, fmt.Sprintf("%s: the method is not uppercase", where))
		}
		if !strings.HasPrefix(r.Path, "/") {
			problems = append(problems, fmt.Sprintf("%s: the path does not begin with /", where))
		}
		if r.Name == "" {
			problems = append(problems, fmt.Sprintf("%s: the route has no name", where))
		} else if seenName[r.Name] {
			problems = append(problems, fmt.Sprintf("%s: the name %q is already used", where, r.Name))
		} else {
			seenName[r.Name] = true
		}
		if seenRoute[where] {
			problems = append(problems, fmt.Sprintf("%s: the method and path are already mounted", where))
		} else {
			seenRoute[where] = true
		}

		problems = append(problems, checkRequirement(where, r.Requirement)...)
		if r.Body > BodyStream {
			problems = append(problems, fmt.Sprintf("%s: the body class %d is not one this build knows", where, r.Body))
		}
		problems = append(problems, checkPath(where, r.Path)...)
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("route: the table is not valid:\n  %s", strings.Join(problems, "\n  "))
}

// checkRequirement reports what is wrong with one route's credential demand.
func checkRequirement(where string, req Requirement) []string {
	var out []string
	switch req.Access {
	case AccessUnset:
		out = append(out, fmt.Sprintf("%s: the access class is unset", where))
	case AccessPerms:
		// A permission-scoped route with no bits demands nothing, which is a
		// public route wearing a stricter name.
		if req.Perms == 0 {
			out = append(out, fmt.Sprintf("%s: the access class is perms and no permission bits are named", where))
		}
	case AccessPublic, AccessSession, AccessAnyCredential:
		// Bits on a route that never consults them read as a guarantee nothing
		// enforces, which is worse than no guarantee at all.
		if req.Perms != 0 {
			out = append(out, fmt.Sprintf("%s: %s access carries permission bits nothing will check",
				where, req.Access))
		}
	default:
		out = append(out, fmt.Sprintf("%s: the access class %d is not one this build knows", where, req.Access))
	}
	return out
}

// checkPath reports a malformed pattern.
//
// The grammar is small on purpose: a literal, {name} for a parameter, and
// {name...} for a tail that must be last. Anything else is a spelling the
// registration step would have to guess at.
func checkPath(where, path string) []string {
	var out []string
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, seg := range segments {
		open := strings.Contains(seg, "{")
		closed := strings.Contains(seg, "}")
		if !open && !closed {
			if seg != strings.ToLower(seg) {
				out = append(out, fmt.Sprintf("%s: the segment %q is not lower case", where, seg))
			}
			continue
		}
		if !open || !closed || !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			out = append(out, fmt.Sprintf("%s: the segment %q is not a whole parameter", where, seg))
			continue
		}
		name := seg[1 : len(seg)-1]
		tail := strings.HasSuffix(name, "...")
		name = strings.TrimSuffix(name, "...")
		if name == "" {
			out = append(out, fmt.Sprintf("%s: a parameter has no name", where))
			continue
		}
		if tail && i != len(segments)-1 {
			out = append(out, fmt.Sprintf("%s: the tail parameter %q is not the last segment", where, name))
		}
	}
	return out
}

// Params returns a pattern's parameter names in order, tails included.
//
// The registration step needs them to translate the pattern, and a handler
// needs them to read what it matched. Both take the names from here rather
// than re-parsing, so the two cannot disagree about what a route captured.
func Params(path string) []string {
	var out []string
	for _, seg := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		out = append(out, strings.TrimSuffix(seg[1:len(seg)-1], "..."))
	}
	return out
}

// HasTail reports whether a pattern ends in a tail parameter, which decides
// whether the registered pattern can be followed by anything.
func HasTail(path string) bool {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	last := segments[len(segments)-1]
	return strings.HasPrefix(last, "{") && strings.HasSuffix(last, "...}")
}
