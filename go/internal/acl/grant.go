package acl

import "strings"

// Path is the share-relative path as components, already validated. It is the
// ACL layer's own spelling of the D10 vocabulary; the HTTP layer maps the
// client's Vpath onto it.
type Path struct {
	comps []string
}

// NewPath builds a path from its components. The empty comps slice is the
// share root.
func NewPath(comps ...string) Path { return Path{comps: comps} }

// Components returns the path's components. The caller must not mutate.
func (p Path) Components() []string { return p.comps }

// Len is the depth.
func (p Path) Len() int { return len(p.comps) }

// IsPrefixOf reports whether p is a prefix of q (or equal).
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

// subpathEquals reports whether p denotes exactly q. Two paths are only equal
// when their component lists are, which the prefix test plus equal length
// gives.
func subpathEquals(p, q Path) bool { return len(p.comps) == len(q.comps) && p.IsPrefixOf(q) }

// ParsePath turns a "/"-separated subpath into components, dropping a single
// leading or trailing slash (the stored grant spelling).
func ParsePath(s string) Path {
	if s == "" || s == "/" {
		return Path{}
	}
	raw := strings.Split(strings.Trim(s, "/"), "/")
	if len(raw) == 1 && raw[0] == "" {
		return Path{}
	}
	return Path{comps: raw}
}

// Name is the last component, empty at the root.
func (p Path) Name() string {
	if len(p.comps) == 0 {
		return ""
	}
	return p.comps[len(p.comps)-1]
}

// String joins the components for a diagnostic.
func (p Path) String() string { return "/" + strings.Join(p.comps, "/") }

// Grant is one row of the grant table as the evaluator needs it. Exactly one
// of User and Group is set.
type Grant struct {
	ID      int64
	User    int64
	Group   int64
	Share   int64
	Subpath Path
	Allow   Perms
	Deny    Perms
	Inherit bool
	Label   string
}
