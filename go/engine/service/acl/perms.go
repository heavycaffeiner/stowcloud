// Package acl answers one question: what permission bits does a user hold at
// a virtual path. It holds an in-memory grant and membership table, refreshed
// wholesale by a caller, and never touches a database.
package acl

import "strings"

// Perms is a set of permission bits.
type Perms uint16

// The eight bits. Download is separate from Read so a view-only grant does
// not also hand out the bytes, and Move is separate from Rename so a grant
// scoped to one subtree does not let its holder carry a file out of it.
const (
	Read Perms = 1 << iota
	Write
	Create
	Delete
	Rename
	Move
	Share
	Download
)

// Has reports whether every bit in want is set.
func (p Perms) Has(want Perms) bool { return p&want == want }

// Intersects reports whether any bit in want is set.
func (p Perms) Intersects(want Perms) bool { return p&want != 0 }

// Remove clears the bits in other.
func (p Perms) Remove(other Perms) Perms { return p &^ other }

// IsEmpty reports whether no bit is set.
func (p Perms) IsEmpty() bool { return p == 0 }

// String renders the set as slash-joined names in orderedBits order, or "-"
// when empty.
func (p Perms) String() string {
	if p == 0 {
		return "-"
	}
	bits := orderedBits()
	names := make([]string, 0, len(bits))
	for _, bit := range bits {
		if p.Has(bit) {
			names = append(names, nameOf(bit))
		}
	}
	return strings.Join(names, "/")
}

// orderedBits fixes one rendering and probing order, so String, Effective and
// the admin surface's permission list agree everywhere.
func orderedBits() [8]Perms {
	return [8]Perms{Read, Write, Create, Delete, Rename, Move, Share, Download}
}

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

// PermByName maps a permission name to its bit. An unknown name is refused
// rather than ignored: dropping an unrecognized permission from a grant a
// client asked for would store something weaker than what the screen shows.
func PermByName(name string) (Perms, bool) {
	for _, bit := range orderedBits() {
		if nameOf(bit) == name {
			return bit, true
		}
	}
	return 0, false
}

// NamedPerm pairs a bit with its wire name.
type NamedPerm struct {
	Name string
	Perm Perms
}

// NamedPerms lists every bit with its name, in orderedBits order.
func NamedPerms() []NamedPerm {
	bits := orderedBits()
	out := make([]NamedPerm, 0, len(bits))
	for _, bit := range bits {
		out = append(out, NamedPerm{Name: nameOf(bit), Perm: bit})
	}
	return out
}
