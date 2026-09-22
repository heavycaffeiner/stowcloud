// Package capability defines the small, dependency-free contract shared by
// storage backends. A backend advertises an optional operation by implementing
// the corresponding interface; callers must use a Go type assertion and treat
// a failed assertion as unsupported.
package capability

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidPath      = errors.New("storage capability: invalid path")
	ErrInvalidComponent = errors.New("storage capability: invalid path component")
)

// Path is a validated, hierarchy-relative path. The empty path is the root.
// It never starts or ends with '/', contains empty, '.' or '..' components, or
// contains NUL, '\\', or invalid UTF-8. Path values are immutable.
type Path struct{ raw string }

// ParsePath validates a slash-separated hierarchy-relative path.
func ParsePath(raw string) (Path, error) {
	if raw == "" {
		return Path{}, nil
	}
	if !utf8.ValidString(raw) || strings.HasPrefix(raw, "/") || strings.HasSuffix(raw, "/") || strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\\') {
		return Path{}, ErrInvalidPath
	}
	for _, component := range strings.Split(raw, "/") {
		if err := validateComponent(component); err != nil {
			return Path{}, err
		}
	}
	return Path{raw: raw}, nil
}

// RootPath returns the hierarchy root.
func RootPath() Path { return Path{} }

func validateComponent(component string) error {
	if component == "" || component == "." || component == ".." || strings.ContainsRune(component, '\x00') || strings.ContainsRune(component, '/') || strings.ContainsRune(component, '\\') || !utf8.ValidString(component) {
		return ErrInvalidComponent
	}
	return nil
}

// Join appends one validated component to p.
func (p Path) Join(component string) (Path, error) {
	if err := validateComponent(component); err != nil {
		return Path{}, err
	}
	if p.raw == "" {
		return Path{raw: component}, nil
	}
	return Path{raw: p.raw + "/" + component}, nil
}

// Parent returns the parent path. The root is its own parent.
func (p Path) Parent() Path {
	if i := strings.LastIndexByte(p.raw, '/'); i >= 0 {
		return Path{raw: p.raw[:i]}
	}
	return Path{}
}

// Name returns the final component, or "" for the root.
func (p Path) Name() string {
	if i := strings.LastIndexByte(p.raw, '/'); i >= 0 {
		return p.raw[i+1:]
	}
	return p.raw
}

// Components returns a fresh slice of components. The root returns nil.
func (p Path) Components() []string {
	if p.raw == "" {
		return nil
	}
	return strings.Split(p.raw, "/")
}

func (p Path) IsRoot() bool   { return p.raw == "" }
func (p Path) String() string { return p.raw }

// Under reports whether p is p itself or below ancestor, component-wise.
func (p Path) Under(ancestor Path) bool {
	return p.raw == ancestor.raw || (ancestor.raw != "" && strings.HasPrefix(p.raw, ancestor.raw+"/"))
}

// Equal reports whether two paths have the same validated spelling.
func (p Path) Equal(other Path) bool { return p.raw == other.raw }
