// Package cache is the rebuildable half of the store. Everything in it can be
// walked back out of the filesystem, which is what makes deleting the file a
// supported operation rather than an incident.
//
// Two facts a POSIX filesystem will not give you are the whole reason it
// exists: a stable id for a file across renames, which a sync client keys its
// entire local journal on, and whether anything under a directory changed,
// which is what stops every sync being a full crawl.
//
// Every method here is allowed to answer "I do not know" and have the caller
// fall back to the filesystem. Nothing may treat a missing row as a missing
// file.
package cache

import (
	"context"
	"database/sql"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// FileID is a node's stable id, and the value a sync client keys its local
// journal on. It is derived from the file's identity rather than assigned by
// the database, so a cache that was deleted rebuilds to the same ids.
//
// It is never zero: zero is the "no id" sentinel, and it is the parent of a
// share root.
type FileID int64

// RootID is where a parent-chain walk stops. No row carries it.
const RootID FileID = 0

// Ident is a file's identity as the kernel reports it, and the only thing this
// store recognises a file by. It is what durable rows elsewhere reference, so
// that deleting this database costs a lookup rather than leaving a row
// pointing at an id nothing mints any more.
//
// Btime is a pointer because an absent birth time and a zero one are different
// facts, which means an Ident is not comparable with == and must not be used
// as a map key. Equal is the comparison; key is the map key.
type Ident struct {
	Share vfs.ShareID
	Dev   uint64
	Ino   uint64
	Btime *int64
}

// IdentOf is the identity of what a stat call just reported.
func IdentOf(share vfs.ShareID, st vfs.Stat) Ident {
	return Ident{Share: share, Dev: st.Dev, Ino: st.Ino, Btime: st.BtimeNs}
}

// Equal compares two identities by value, including the difference between an
// absent birth time and a zero one.
func (i Ident) Equal(o Ident) bool { return i.key() == o.key() }

// identKey is Ident flattened into something comparable.
type identKey struct {
	share   vfs.ShareID
	dev     uint64
	ino     uint64
	present bool
	btime   int64
}

func (i Ident) key() identKey {
	k := identKey{share: i.Share, dev: i.Dev, ino: i.Ino}
	if i.Btime != nil {
		k.present, k.btime = true, *i.Btime
	}
	return k
}

// Assignment is one identity and the id it holds, which is what a collision
// makes durable.
type Assignment struct {
	Ident Ident
	ID    FileID
}

// Overrides is the durable record of which of two colliding files took the
// derived id. It is consulted before anything is derived and it is the
// authority, because the answer depends on insertion order and a rebuild does
// not reproduce that.
//
// It lives in the durable half of the store, which is why it is an interface
// here: this package cannot import the package that keeps it.
type Overrides interface {
	// LookupFileID reports the recorded id for ident, if one was ever
	// recorded.
	LookupFileID(ctx context.Context, ident Ident) (FileID, bool, error)

	// LookupFileIDOwner reports which identity reserved id, if any. A
	// reservation holds whether or not the owner's node row exists, which is
	// what makes it survive a deleted cache: after a rebuild the owner may not
	// have been walked yet.
	LookupFileIDOwner(ctx context.Context, id FileID) (Ident, bool, error)

	// RecordFileIDs writes every assignment in one transaction. It commits
	// before the node row that uses any of the ids does.
	RecordFileIDs(ctx context.Context, assignments ...Assignment) error
}

// DB is the cache.
type DB struct {
	f  *dbfile.DB
	ov Overrides

	// bits is the width the derivation folds into, and it is idBits for every
	// database this package hands out. It is a field so that the collision
	// path has a test; see derive.
	bits uint

	st *stmts
}

// Spec is this database's file. It is rebuildable, which is what lets a
// migration discard it instead of carrying rows across a shape change.
func Spec(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations(), Rebuildable: true}
}

// New wraps an open file and prepares every statement on it. ov is the
// override table, which lives in the durable half and is passed in rather than
// reached for.
func New(ctx context.Context, f *dbfile.DB, ov Overrides) (*DB, error) {
	st, err := prepare(ctx, f.SQL())
	if err != nil {
		return nil, err
	}
	return &DB{f: f, ov: ov, bits: idBits, st: st}, nil
}

// Write runs fn in this database's single serialised write path.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	return d.f.Write(ctx, fn)
}

// SQL is the pool, for readers.
func (d *DB) SQL() *sql.DB { return d.f.SQL() }

// File is the underlying database, for the size guard and the vacuum.
func (d *DB) File() *dbfile.DB { return d.f }

// btimeArg binds a birth time, or SQL NULL where the filesystem carries none.
func btimeArg(i Ident) any {
	if i.Btime == nil {
		return nil
	}
	return *i.Btime
}

// identArgs binds an identity for a lookup. The birth time is left off
// entirely where there is none, because the statement for that case names the
// column as IS NULL rather than taking it as a parameter: a bound parameter
// the planner cannot prove is non-NULL matches neither partial index.
func identArgs(i Ident) []any {
	args := []any{int64(i.Share), toSQL(i.Dev), toSQL(i.Ino)}
	if i.Btime != nil {
		args = append(args, *i.Btime)
	}
	return args
}
