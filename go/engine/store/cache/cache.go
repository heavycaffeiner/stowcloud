// Package cache is the rebuildable half of the store. Everything in it can
// be walked back out of the filesystem, which is what makes deleting the
// file a supported operation rather than an incident.
//
// Two facts a POSIX filesystem will not give you are the whole reason it
// exists: a stable id for a file across renames, which a sync client keys
// its entire local journal on, and whether anything under a directory
// changed, which is what stops every sync being a full crawl.
//
// Every method here is allowed to answer "I do not know" and have the caller
// fall back to the filesystem. Nothing may treat a missing row as a missing
// file.
package cache

import (
	"context"
	"database/sql"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// Overrides is the durable record of which of two colliding files took the
// derived id. It is consulted before anything is derived and it is the
// authority, because the answer depended on insertion order and a rebuild
// does not reproduce that.
//
// It lives in the durable half of the store, which is why it is an interface
// here: this package must not import the package that keeps it. A cache that
// depended on the durable half's whole surface would be a cache nobody could
// safely delete.
type Overrides interface {
	// LookupFileID reports the recorded id for an identity, if one was ever
	// recorded.
	LookupFileID(ctx context.Context, id ident.Ident) (ident.FileID, bool, error)

	// LookupFileIDOwner reports which identity reserved an id, if any. A
	// reservation holds whether or not the owner's node row exists, which is
	// what makes it survive a deleted cache: after a rebuild the owner may
	// not have been walked yet.
	LookupFileIDOwner(ctx context.Context, id ident.FileID) (ident.Ident, bool, error)

	// RecordFileIDs writes every assignment in one transaction. It commits
	// before the node row that uses any of the ids does.
	RecordFileIDs(ctx context.Context, assignments ...ident.Assignment) error
}

// DB is the cache.
type DB struct {
	f  *dbfile.DB
	ov Overrides

	// bits is the width the derivation folds into, and it is idBits for
	// every database this package hands out. It is a field so that the
	// collision path has a test at a width a test can reach; see derive.
	bits uint

	st *stmts
}

// Spec is this database's file. It is rebuildable, which is what lets a
// migration discard it instead of carrying rows across a shape change.
func Spec(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations(), Rebuildable: true}
}

// New wraps an open file and prepares every statement on it. ov is the
// override table, which lives in the durable half and is passed in rather
// than reached for.
//
// There is no Close: the prepared statements are invalidated when the parent
// dbfile.DB's pool closes, and database/sql handles that without leaking a
// descriptor.
func New(ctx context.Context, f *dbfile.DB, ov Overrides) (*DB, error) {
	st, err := prepare(ctx, f.SQL())
	if err != nil {
		return nil, err
	}
	return &DB{f: f, ov: ov, bits: idBits, st: st}, nil
}

// Write runs fn in this database's single serialized write path.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	return d.f.Write(ctx, fn)
}

// SQL is the pool, for readers.
func (d *DB) SQL() *sql.DB { return d.f.SQL() }

// File is the underlying database, for the size guard and the vacuum.
func (d *DB) File() *dbfile.DB { return d.f }

// btimeArg binds a birth time, or SQL NULL where the filesystem carries
// none.
func btimeArg(i ident.Ident) any {
	if i.Btime == nil {
		return nil
	}
	return *i.Btime
}

// identArgs binds an identity for a lookup. The birth time is left off
// entirely where there is none, because the statement for that case names
// the column as IS NULL rather than taking it as a parameter.
func identArgs(i ident.Ident) []any {
	dev, ino, present, btime := i.ToSQL()
	args := []any{int64(i.Share), dev, ino}
	if present != 0 {
		args = append(args, btime)
	}
	return args
}
