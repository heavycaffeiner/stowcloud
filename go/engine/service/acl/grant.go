package acl

import "strings"

// Path is this package's share-relative path vocabulary, built from
// already-validated components. It touches no filesystem and repeats no
// validation the vfs already did.
type Path struct {
	comps []string
}

// NewPath builds a path from components. The slice is copied, so a caller
// reusing its backing array cannot mutate a grant's subpath after the fact.
func NewPath(comps ...string) Path {
	if len(comps) == 0 {
		return Path{}
	}
	return Path{comps: append([]string(nil), comps...)}
}

// ParsePath reads the stored "/"-separated spelling. An empty string and a
// lone slash both fold to the root.
func ParsePath(s string) Path {
	trimmed := strings.Trim(s, "/")
	if trimmed == "" {
		return Path{}
	}
	return Path{comps: strings.Split(trimmed, "/")}
}

// Components returns a copy of the path's components.
func (p Path) Components() []string {
	if len(p.comps) == 0 {
		return nil
	}
	return append([]string(nil), p.comps...)
}

// Len is the number of components, and the path's depth.
func (p Path) Len() int { return len(p.comps) }

// Name is the last component, empty at the root.
func (p Path) Name() string {
	if len(p.comps) == 0 {
		return ""
	}
	return p.comps[len(p.comps)-1]
}

// String renders the path as "/" followed by the joined components.
func (p Path) String() string { return "/" + strings.Join(p.comps, "/") }

// IsPrefixOf reports whether p names q or an ancestor of it.
func (p Path) IsPrefixOf(q Path) bool {
	if len(p.comps) > len(q.comps) {
		return false
	}
	for i := range p.comps {
		if p.comps[i] != q.comps[i] {
			return false
		}
	}
	return true
}

// subpathEquals reports whether the two paths name exactly the same place.
func subpathEquals(p, q Path) bool {
	return len(p.comps) == len(q.comps) && p.IsPrefixOf(q)
}

// Grant is one permission rule. Exactly one of User and Group is set; the
// write side enforces that before a row exists. A grant naming neither
// matches nobody, so a malformed one is inert rather than permissive.
//
// Inherit decides what the grant covers, not whether it applies: an
// inheriting grant covers its Subpath and everything under it, a
// non-inheriting one covers only the path exactly equal to its Subpath.
type Grant struct {
	ID        int64
	User      int64
	Group     int64
	Share     int64
	Subpath   Path
	Allow     Perms
	Deny      Perms
	Inherit   bool
	Label     string
	CreatedNs int64
}

// Membership is one user-to-group edge, in this package's own domain shape.
type Membership struct {
	User  int64
	Group int64
}
