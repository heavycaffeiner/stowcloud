// Package state is the durable half of the store. Nothing in it can be
// reconstructed from the filesystem, which is what makes it the entire backup
// instruction and what makes it a different kind of thing from the cache.
package state

import (
	"context"
	"database/sql"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
)

// DB is the durable half.
type DB struct {
	f *dbfile.DB
}

// Spec is this database's file. It is not rebuildable: a migration that
// discards it is data loss with a version bump on top, and the runner refuses
// one.
func Spec(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations(), Rebuildable: false}
}

// New wraps an open file.
func New(f *dbfile.DB) *DB { return &DB{f: f} }

// Write runs fn in this database's single serialised write path.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	return d.f.Write(ctx, fn)
}

// SQL is the pool, for readers.
func (d *DB) SQL() *sql.DB { return d.f.SQL() }

// File is the underlying database, for the size guard and the vacuum.
func (d *DB) File() *dbfile.DB { return d.f }
