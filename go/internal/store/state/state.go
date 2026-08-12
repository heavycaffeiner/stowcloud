// Package state is the durable half of the store. Nothing in it can be
// reconstructed from the filesystem, which is what makes it the data backup and
// what makes it a different kind of thing from the cache.
//
// The master key is not in here and is not in that backup either: it has its own
// artifact and its own protected lifecycle, because one artifact holding both
// the encrypted state and the key that opens it has encrypted nothing.
package state

import (
	"context"
	"database/sql"
	"sync/atomic"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
)

// DB is the durable half.
type DB struct {
	f *dbfile.DB

	// overrides is how many collision records exist, and -1 for "not counted
	// yet". The cache consults that table for every id it allocates, so on a
	// cold walk of a large tree the count is what stands between one query and
	// several million against a table that is almost always empty. Writing a
	// record puts it back to -1 rather than adding to it, because a recount
	// costs nothing at the rate collisions actually happen.
	overrides atomic.Int64
}

// Spec is this database's file. It is not rebuildable: a migration that
// discards it is data loss with a version bump on top, and the runner refuses
// one.
func Spec(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations(), Rebuildable: false}
}

// New wraps an open file.
func New(f *dbfile.DB) *DB {
	d := &DB{f: f}
	d.overrides.Store(-1)
	return d
}

// Write runs fn in this database's single serialised write path.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	return d.f.Write(ctx, fn)
}

// SQL is the pool, for readers.
func (d *DB) SQL() *sql.DB { return d.f.SQL() }

// File is the underlying database, for the size guard and the vacuum.
func (d *DB) File() *dbfile.DB { return d.f }
