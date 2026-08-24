// Package acl is the permission engine: the eight permission bits, grants that
// name a user or a group, and evaluation against the virtual root with the
// default that a path outside every grant is an empty view.
//
// Two of the eight bits are splits a reimplementation collapses by accident:
// DOWNLOAD is separate from READ, so a view-only grant does not also hand the
// bytes out as a file; and MOVE is separate from RENAME, so an account cannot
// carry a file out of the only subtree it was granted.
package acl

import "strings"

// Perms is the set of permission bits somebody holds at a path.
type Perms uint16

// The eight permission bits.
const (
	Read     Perms = 1 << iota // list and read
	Write                      // modify an existing file
	Create                     // make a new one
	Delete                     // remove one
	Rename                     // change a name within one directory
	Move                       // cross a directory boundary
	Share                      // mint a share link
	Download                   // take the bytes; off leaves preview and streaming intact
)

// Has reports whether every bit in want is set.
func (p Perms) Has(want Perms) bool { return p&want == want }

// Intersects reports whether any bit in want is set.
func (p Perms) Intersects(want Perms) bool { return p&want != 0 }

// Remove clears the bits in other.
func (p Perms) Remove(other Perms) Perms { return p &^ other }

// IsEmpty reports whether no bit is set.
func (p Perms) IsEmpty() bool { return p == 0 }

// String renders the set as a colon-separated list of bit names, for a
// diagnostic and for deciding which bit an evaluator probed.
func (p Perms) String() string {
	if p == 0 {
		return "-"
	}
	var b strings.Builder
	for i, bit := range orderedBits() {
		if p.Has(bit) {
			if i > 0 {
				b.WriteByte('/') //nolint:errcheck // strings.Builder.Write never fails.
			}
			b.WriteString(nameOf(bit)) //nolint:errcheck // strings.Builder.Write never fails.
		}
	}
	return b.String()
}

// orderedBits is the fixed order String and effective probe in, so results
// are deterministic however the bits are set.
func orderedBits() [8]Perms {
	return [8]Perms{Read, Write, Create, Delete, Rename, Move, Share, Download}
}

// nameOf is the human name of a single bit.
func nameOf(bit Perms) string {
	switch bit {
	case Read:
		return "read"
	case Write:
		return "write"
	case Create:
		return "create"
	case Delete:
		return "delete"
	case Rename:
		return "rename"
	case Move:
		return "move"
	case Share:
		return "share"
	case Download:
		return "download"
	default:
		return "?"
	}
}

// PermByName is the inverse of nameOf, for the admin surface, which receives
// permission names from a client rather than bits.
//
// An unknown name is refused rather than ignored, because ignoring one stores
// a grant missing a permission somebody asked for, and the screen then shows
// what they wrote while the server holds something weaker.
func PermByName(name string) (Perms, bool) {
	for _, bit := range orderedBits() {
		if nameOf(bit) == name {
			return bit, true
		}
	}
	return 0, false
}

// NamedPerm pairs a bit with its name, in the fixed order, so a rendering of a
// set is the same every time.
type NamedPerm struct {
	Name string
	Perm Perms
}

// NamedPerms is every bit with its name, in that order.
func NamedPerms() []NamedPerm {
	bits := orderedBits()
	out := make([]NamedPerm, 0, len(bits))
	for _, bit := range bits {
		out = append(out, NamedPerm{Name: nameOf(bit), Perm: bit})
	}
	return out
}
