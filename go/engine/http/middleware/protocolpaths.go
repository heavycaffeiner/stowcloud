// Linux only, for the same reason as the rest of this package.
//go:build linux

// Protocol mounts declare their paths as data. The chain reads the declaration
// and never imports a protocol package, so WebDAV and the compat surfaces
// cannot reach into the middleware to make themselves special cases.
//
// The old code accepted whatever lists it was handed. Validation here is the
// deliberate change: three sets that must not overlap, credential flows that
// must be POST, and public reads that must not change state.
package middleware

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// MethodPath is one declared route: a verb and a canonical path.
type MethodPath struct {
	Method string
	Path   string
}

func (m MethodPath) String() string { return m.Method + " " + m.Path }

// ProtocolPaths is what one protocol mount declares about itself.
type ProtocolPaths struct {
	// FilePrefixes are challenge mounts. A credential is attempted, and an
	// absent or bad one reaches the mount so the protocol can issue its own
	// Basic challenge rather than the app's JSON refusal.
	FilePrefixes []string

	// PublicReads need no credential and must not change anything.
	PublicReads []MethodPath

	// CredentialFlows are token-authorised POSTs: the token in the body is the
	// authority, so the chain does not demand a session.
	CredentialFlows []MethodPath
}

// safeMethods are the verbs that may appear in PublicReads. OPTIONS is here
// because protocol discovery has to work before a credential exists.
func safeMethods() []string {
	return []string{http.MethodGet, http.MethodHead, http.MethodOptions}
}

// ValidateProtocolPaths reports every problem in a declaration at once.
//
// A mount is declared once at startup, so an operator or a developer reading
// all of it together is better than being walked through it one failure at a
// time.
func ValidateProtocolPaths(p ProtocolPaths) error {
	var problems []string

	for _, prefix := range p.FilePrefixes {
		if !strings.HasPrefix(prefix, "/") {
			problems = append(problems,
				fmt.Sprintf("the file prefix %q does not begin with /", prefix))
		}
		if prefix == "/" {
			problems = append(problems,
				"the file prefix / would make every path a challenge mount")
		}
	}
	problems = append(problems, duplicatePrefixes(p.FilePrefixes)...)

	for _, r := range p.PublicReads {
		problems = append(problems, checkMethodPath("a public read", r)...)
		if !slices.Contains(safeMethods(), strings.ToUpper(r.Method)) {
			// A state-changing public read is an unauthenticated mutation
			// wearing the wrong label, which is exactly what nothing checked
			// before.
			problems = append(problems,
				fmt.Sprintf("the public read %s changes state", r))
		}
	}

	for _, f := range p.CredentialFlows {
		problems = append(problems, checkMethodPath("a credential flow", f)...)
		if strings.ToUpper(f.Method) != http.MethodPost {
			problems = append(problems,
				fmt.Sprintf("the credential flow %s is not a POST", f))
		}
	}

	problems = append(problems, checkDisjoint(p)...)

	if len(problems) == 0 {
		return nil
	}
	slices.Sort(problems)
	problems = slices.Compact(problems)
	return fmt.Errorf("protocol paths: %s", strings.Join(problems, "; "))
}

// checkDisjoint reports a path claimed by more than one of the three sets.
//
// An overlap is not a harmless duplicate. A path that is both a public read
// and a file prefix is a path whose credential requirement depends on which
// check runs first, and that is a decision no one wrote down.
func checkDisjoint(p ProtocolPaths) []string {
	var problems []string

	for _, r := range p.PublicReads {
		for _, f := range p.CredentialFlows {
			if samePath(r.Path, f.Path) {
				problems = append(problems,
					fmt.Sprintf("%q is both a public read and a credential flow", r.Path))
			}
		}
	}
	for _, prefix := range p.FilePrefixes {
		for _, r := range p.PublicReads {
			if underPrefix(r.Path, prefix) {
				problems = append(problems,
					fmt.Sprintf("the public read %q is under the file prefix %q", r.Path, prefix))
			}
		}
		for _, f := range p.CredentialFlows {
			if underPrefix(f.Path, prefix) {
				problems = append(problems,
					fmt.Sprintf("the credential flow %q is under the file prefix %q", f.Path, prefix))
			}
		}
	}
	return problems
}

func checkMethodPath(what string, mp MethodPath) []string {
	var problems []string
	if strings.TrimSpace(mp.Method) == "" {
		problems = append(problems, fmt.Sprintf("%s names no method for %q", what, mp.Path))
	}
	if !strings.HasPrefix(mp.Path, "/") {
		problems = append(problems, fmt.Sprintf("%s has the path %q, which does not begin with /", what, mp.Path))
	}
	return problems
}

func duplicatePrefixes(prefixes []string) []string {
	var problems []string
	seen := map[string]bool{}
	for _, p := range prefixes {
		if seen[p] {
			problems = append(problems, fmt.Sprintf("the file prefix %q is declared twice", p))
		}
		seen[p] = true
	}
	return problems
}

func samePath(a, b string) bool { return strings.EqualFold(trimSlash(a), trimSlash(b)) }

// UnderFilePrefix reports whether a path belongs to a challenge mount.
//
// Component-wise: "/dav2" is not under "/dav", because a prefix match on the
// raw string would pull an unrelated mount into another protocol's challenge
// behaviour.
func UnderFilePrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if underPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func underPrefix(path, prefix string) bool {
	p, pre := trimSlash(path), trimSlash(prefix)
	if pre == "" {
		return false
	}
	if strings.EqualFold(p, pre) {
		return true
	}
	return len(p) > len(pre) && strings.EqualFold(p[:len(pre)], pre) && p[len(pre)] == '/'
}

func trimSlash(s string) string { return strings.TrimRight(s, "/") }
