// Package state holds the durable half of the store. None of it can be rebuilt
// from the filesystem, which makes it the data backup and something categorically
// different from the cache.
//
// The master key lives neither here nor in that backup. It has a separate
// artifact and a separate lifecycle, because a single artifact carrying both the
// encrypted state and the key that opens it has encrypted nothing at all.
package state

import (
	"context"
	"database/sql"
	"sync/atomic"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
)

// DB is the durable half.
type DB struct {
	f *dbfile.DB

	// overrides counts existing collision records, with -1 meaning not yet
	// counted. The cache queries that table for every id it allocates, so during
	// a cold walk of a large tree this count is the difference between one query
	// and several million against a table that is nearly always empty. Writing a
	// record resets it to -1 rather than incrementing, since recounting is
	// negligible at the rate collisions actually occur.
	overrides atomic.Int64
}

// Spec describes this database's file. It cannot be rebuilt: a migration
// discarding it would be data loss with a version bump attached, and the runner
// rejects any such step.
func Spec(path string) dbfile.Spec {
	return dbfile.Spec{Path: path, Migrations: migrations(), Rebuildable: false}
}

// New wraps an open file.
func New(f *dbfile.DB) *DB {
	d := &DB{f: f}
	d.overrides.Store(-1)
	return d
}

// Write runs fn in this database's single serialized write path. Every
// aggregate that mutates a row goes through here, so every write to this
// file takes the same mutex and the same one-transaction guarantee.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	return d.f.Write(ctx, fn)
}

// SQL is the pool, for readers. Reads are not serialized: WAL mode gives
// many concurrent readers against one writer, and a read gains nothing from
// the write mutex.
func (d *DB) SQL() *sql.DB { return d.f.SQL() }

// File exposes the underlying database for the size guard and the vacuum.
func (d *DB) File() *dbfile.DB { return d.f }
