// Package cache holds the rebuildable portion of the store. All of it can be
// reconstructed by walking the filesystem, which makes deleting the file a
// supported action rather than an incident.
//
// It exists entirely to supply two facts a POSIX filesystem withholds: an id for
// a file that survives renames, which a sync client uses as the key for its whole
// local journal, and whether anything beneath a directory changed, which is what
// keeps every sync from becoming a full crawl.
//
// Any method here may answer that it does not know, leaving the caller to fall
// back to the filesystem. Nothing may interpret a missing row as a missing
// file.
package cache

import (
	"context"
	"database/sql"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// Overrides durably records which of two colliding files claimed the derived id.
// It is consulted ahead of any derivation and is authoritative, because the
// original outcome depended on insertion order and a rebuild does not reproduce
// that.
//
// It resides in the durable half of the store, hence the interface here: this
// package must not import whichever package holds it. A cache depending on the
// durable half's full surface would be a cache nobody could safely delete.
type Overrides interface {
	// LookupFileID reports the recorded id for an identity, if one was ever
	// recorded.
	LookupFileID(ctx context.Context, id ident.Ident) (ident.FileID, bool, error)

	// LookupFileIDOwner reports which identity, if any, reserved an id. The
	// reservation stands whether or not the owner's node row exists, which is
	// how it survives a deleted cache: following a rebuild the owner may not
	// have been walked yet.
	LookupFileIDOwner(ctx context.Context, id ident.FileID) (ident.Ident, bool, error)

	// RecordFileIDs commits all assignments within a single transaction,
	// landing before any node row that references those ids.
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

// Spec describes this database's file. Being rebuildable is what permits a
// migration to discard it rather than carry rows through a shape change.
func Spec(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations(), Rebuildable: true}
}

// New wraps an open file and prepares each statement against it. ov supplies the
// override table, which belongs to the durable half and is injected rather than
// fetched.
//
// No Close exists: the prepared statements become invalid once the parent
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

// File exposes the underlying database for the size guard and the vacuum.
func (d *DB) File() *dbfile.DB { return d.f }

// btimeArg binds a birth time, or SQL NULL where the filesystem carries
// none.
func btimeArg(i ident.Ident) any {
	if i.Btime == nil {
		return nil
	}
	return *i.Btime
}

// identArgs binds an identity for a lookup. Where no birth time exists it is
// omitted entirely, since the statement covering that case tests the column with
// IS NULL instead of accepting it as a parameter.
func identArgs(i ident.Ident) []any {
	dev, ino, present, btime := i.ToSQL()
	args := []any{int64(i.Share), dev, ino}
	if present != 0 {
		args = append(args, btime)
	}
	return args
}
