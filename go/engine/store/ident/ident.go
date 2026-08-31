// Package ident holds the file identity both database packages spell, and
// nothing else. It sits below state and cache so neither has to import the
// other for the tuple they both store, and it holds no database handle and
// no SQL of its own.
package ident

import (
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// Ident is a file's identity as the kernel reports it, and the only thing
// the store recognizes a file by. Durable rows reference it rather than a
// minted id, so deleting the rebuildable half costs a lookup instead of
// leaving rows pointing at ids nothing mints any more.
//
// Btime is a pointer because an absent birth time and a zero one are
// different facts about a file; folding them together would let two
// distinct files derive the same id. That makes an Ident not comparable
// with == and unusable as a map key. Equal is the comparison.
type Ident struct {
	Share vfs.ShareID
	Dev   uint64
	Ino   uint64
	Btime *int64
}

// Of is the identity of what a stat call just reported.
func Of(share vfs.ShareID, st vfs.Stat) Ident {
	return Ident{Share: share, Dev: st.Dev, Ino: st.Ino, Btime: st.BtimeNs}
}

// Equal compares two identities by value, keeping an absent birth time
// distinct from a zero one.
func (i Ident) Equal(o Ident) bool { return i.key() == o.key() }

// key is Ident flattened into something comparable, for Equal and for a
// caller that needs a map key.
type key struct {
	share   vfs.ShareID
	dev     uint64
	ino     uint64
	present bool
	btime   int64
}

func (i Ident) key() key {
	k := key{share: i.Share, dev: i.Dev, ino: i.Ino}
	if i.Btime != nil {
		k.present, k.btime = true, *i.Btime
	}
	return k
}

// ToSQL is the identity as SQLite stores it. present is 1 when the file
// carries a birth time and 0 when it does not, which is what tells a caller
// whether to bind btime or bind NULL.
func (i Ident) ToSQL() (dev, ino, present, btime int64) {
	dev, ino = signed(i.Dev), signed(i.Ino)
	if i.Btime != nil {
		return dev, ino, 1, *i.Btime
	}
	return dev, ino, 0, 0
}

// FromSQL rebuilds an identity from a stored row.
//
// The share is narrowed rather than reinterpreted: it was written from a
// share id, so a value that no longer fits one is a corrupt row and worth
// saying so instead of truncating. The device and inode numbers are
// reinterpreted, because that is how they were stored.
func FromSQL(share, dev, ino, present, btime int64) (Ident, error) {
	s, err := num.Narrow[uint32](share)
	if err != nil {
		return Ident{}, fmt.Errorf("stored identity carries share %d: %w", share, err)
	}
	id := Ident{Share: vfs.ShareID(s), Dev: unsigned(dev), Ino: unsigned(ino)}
	if present != 0 {
		b := btime
		id.Btime = &b
	}
	return id, nil
}

// SQLite offers a single signed integer type, so a device or inode number with
// its high bit set round-trips as the same bit pattern. Numeric ordering never
// applies to these values: they are only compared for equality and hashed, and
// both operations are unaffected by the representation.
//
// Conversion proceeds byte by byte instead of through a whole-word cast, so the
// value never traverses a conversion capable of discarding part of the range
// the kernel may legitimately produce.
func signed(v uint64) int64 {
	var out int64
	for i := range 8 {
		out = out<<8 | int64(v>>(56-8*i)&0xff)
	}
	return out
}

// unsigned is the same reinterpretation in the other direction.
func unsigned(v int64) uint64 {
	var out uint64
	for i := range 8 {
		out = out<<8 | uint64(v>>(56-8*i)&0xff)
	}
	return out
}

// FileID is a node's durable identifier and the key a sync client uses for its
// local journal. Deriving it from the file's identity instead of assigning it
// from the database means a deleted cache rebuilds with identical ids.
type FileID int64

// RootID is the "no id" sentinel and the parent id of a share root, so a
// parent-chain walk terminates on it without any sentinel row existing.
const RootID FileID = 0

// Assignment pairs an identity with the id it owns, the binding a collision
// makes permanent.
type Assignment struct {
	Ident Ident
	ID    FileID
}
