// Package dbfile is one SQLite file: its pragmas, its migration runner, and
// the single serialized write path every write to it takes. The databases
// built on it (state, cache, journal) hold no opinion about any of the
// three, and this package holds no opinion about what they store.
package dbfile

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	// ErrSchemaAhead reports a file written by a newer binary. The file is
	// left untouched: writing an older shape into it would be the data loss
	// a rollback is supposed to avoid.
	ErrSchemaAhead = errors.New("database was written by a newer binary")

	// ErrMigrationFailed reports a step that rolled back. The stored
	// version is whatever the last step that committed left behind.
	ErrMigrationFailed = errors.New("migration failed")

	// ErrWritesBlocked is the size guard's refusal. Reads and the writes
	// that shrink the file continue.
	ErrWritesBlocked = errors.New("writes are blocked: free space is below the floor")

	// ErrBusy reports a lock still held after busy_timeout expired. It is
	// surfaced rather than retried: a caller that already waited wants to
	// know who is holding the file.
	ErrBusy = errors.New("database is busy")
)

// poolSize is both the maximum and the idle count, so a connection that
// paid for the per-connection pragma list is kept rather than reopened.
const poolSize = 8

// connPragmas run on every connection the pool opens, in this order.
// busy_timeout leads because journal_mode is the one pragma that contends
// for an exclusive lock while several connections race a first open, and a
// lock wait configured after it is a wait that never happens.
func connPragmas() []string {
	return []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA wal_autocheckpoint = 1000",
		"PRAGMA journal_size_limit = 67108864",
		"PRAGMA cache_size = -16000",
		"PRAGMA temp_store = MEMORY",
		"PRAGMA foreign_keys = ON",
	}
}

// bootPragmas take effect only while the file holds no tables, so they run
// once, on their own connection, before the pool exists.
func bootPragmas() []string {
	return []string{
		"PRAGMA page_size = 4096",
		"PRAGMA auto_vacuum = INCREMENTAL",
	}
}

// Spec is what a caller has to decide about its own file: where it lives,
// the ordered migrations that shape it, and whether its contents can be
// thrown away and rebuilt.
type Spec struct {
	Path       string
	Migrations []Migration

	// Rebuildable admits a Discard migration. Only the cache sets it;
	// nothing rebuilds what state and journal hold.
	Rebuildable bool
}

// DB is one open file: the pool, the write mutex that serializes every
// transaction against it, and the size guard's flag.
type DB struct {
	sql  *sql.DB
	path string

	wmu sync.Mutex

	blocked atomic.Bool

	closeOnce sync.Once
	closeErr  error
}

// Open bootstraps the file, opens the pool, and runs the migrations. A
// migration that fails closes the pool before returning, so a failed Open
// leaves nothing to close.
func Open(ctx context.Context, spec Spec) (*DB, error) {
	if err := bootstrap(ctx, spec.Path); err != nil {
		return nil, err
	}

	// Every transaction this package opens is a write transaction, so the
	// write lock is taken at BEGIN rather than at the first write
	// statement, which is where a transaction can otherwise fail after
	// already having read stale rows.
	pool := sql.OpenDB(&connector{
		drv:     &sqlite.Driver{},
		dsn:     spec.Path + "?_txlock=immediate",
		pragmas: connPragmas(),
	})
	pool.SetMaxOpenConns(poolSize)
	pool.SetMaxIdleConns(poolSize)

	d := &DB{sql: pool, path: spec.Path}
	if err := migrate(ctx, d, spec); err != nil {
		return nil, errors.Join(err, pool.Close())
	}
	return d, nil
}

// bootstrap applies the database-level pragmas to a file that has no schema
// yet, then reads them back to prove they landed before any table exists. A
// file that already holds objects skips both: SQLite would ignore the
// pragmas, and the read-back says nothing about a database this process did
// not create.
func bootstrap(ctx context.Context, path string) (err error) {
	db, err := sql.Open(driverName, path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", filepath.Base(path), err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	var objects int
	if err := db.QueryRowContext(ctx, sqlCountObjects).Scan(&objects); err != nil {
		return fmt.Errorf("reading the schema of %s: %w", filepath.Base(path), err)
	}
	if objects > 0 {
		return nil
	}

	for _, p := range bootPragmas() {
		if _, err := db.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("%s on %s: %w", p, filepath.Base(path), err)
		}
	}

	var pageSize, autoVacuum int
	if err := db.QueryRowContext(ctx, sqlReadPageSize).Scan(&pageSize); err != nil {
		return fmt.Errorf("reading page_size of %s: %w", filepath.Base(path), err)
	}
	if err := db.QueryRowContext(ctx, sqlReadAutoVacuum).Scan(&autoVacuum); err != nil {
		return fmt.Errorf("reading auto_vacuum of %s: %w", filepath.Base(path), err)
	}
	if pageSize != wantPageSize || autoVacuum != wantAutoVacuum {
		return fmt.Errorf("%s was created with page_size %d and auto_vacuum %d: "+
			"the database-level pragmas ran after the schema",
			filepath.Base(path), pageSize, autoVacuum)
	}
	return nil
}

// SQL hands out the pool for reads. Writes go through Write.
func (d *DB) SQL() *sql.DB { return d.sql }

// Path is the file this database was opened from.
func (d *DB) Path() string { return d.path }

// Close truncates the WAL and closes the pool, once. TRUNCATE rather than
// PASSIVE because shutdown is exactly when waiting for a reader is correct:
// the goal is that the next start has nothing to replay.
func (d *DB) Close() error {
	d.closeOnce.Do(func() {
		_, err := d.sql.ExecContext(context.Background(), sqlCheckpoint)
		d.closeErr = errors.Join(err, d.sql.Close())
	})
	return d.closeErr
}

// Write runs fn inside one transaction, with every other writer against
// this file waiting. It does not consult the size guard: the statement that
// knows whether it grows the file is the one writing it, not the wrapper.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	d.wmu.Lock()
	defer d.wmu.Unlock()

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	defer func() {
		// A rollback after a successful commit reports ErrTxDone, which is
		// the ordinary path and not a failure.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, mapErr(rerr))
		}
	}()

	if err := fn(tx); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit())
}

// SetWritesBlocked flips the guard. The sampling that decides when to call
// it lives above this package; nothing here touches the filesystem.
func (d *DB) SetWritesBlocked(blocked bool) { d.blocked.Store(blocked) }

// WritesBlocked reports the guard's current state.
func (d *DB) WritesBlocked() bool { return d.blocked.Load() }

// EnsureWritable is what a statement that grows the file calls first. A
// statement that updates in place, deletes, or reclaims space never calls
// it: those are what let a full volume recover.
func (d *DB) EnsureWritable() error {
	if d.blocked.Load() {
		return fmt.Errorf("%w: %s", ErrWritesBlocked, filepath.Base(d.path))
	}
	return nil
}

// SizeBytes is page_count times page_size, which answers identically for a
// file-backed and an in-memory database and needs no filesystem call.
func (d *DB) SizeBytes(ctx context.Context) (int64, error) {
	var pages, size int64
	if err := d.sql.QueryRowContext(ctx, sqlReadPageCount).Scan(&pages); err != nil {
		return 0, err
	}
	if err := d.sql.QueryRowContext(ctx, sqlReadPageSize).Scan(&size); err != nil {
		return 0, err
	}
	return pages * size, nil
}

// Vacuum reclaims freed pages. It depends on the bootstrap's
// auto_vacuum = INCREMENTAL having landed before the schema existed, which
// is what the bootstrap read-back proves.
func (d *DB) Vacuum(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx, sqlIncrementalVacuum)
	return err
}

// connector applies the per-connection pragmas to every connection the pool
// opens, including ones opened later as the pool grows or reconnects. A
// pragma list applied once after Open would cover only the connections that
// happened to exist at that moment.
type connector struct {
	drv     *sqlite.Driver
	dsn     string
	pragmas []string
}

func (c *connector) Driver() driver.Driver { return c.drv }

func (c *connector) Connect(_ context.Context) (driver.Conn, error) {
	cn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	ex, ok := cn.(driver.ExecerContext)
	if !ok {
		return nil, errors.Join(
			errors.New("the sqlite driver cannot execute a statement"), cn.Close())
	}
	for _, p := range c.pragmas {
		// The connection is not in the pool yet, so there is no context to
		// carry here; the pragmas are fixed strings that run locally.
		if _, err := ex.ExecContext(context.Background(), p, nil); err != nil {
			return nil, errors.Join(fmt.Errorf("%s: %w", p, err), cn.Close())
		}
	}
	return cn, nil
}

// mapErr recognizes SQLITE_BUSY on the low byte of the driver's extended
// result code and wraps it. Every other driver error passes through: this
// package does not build a taxonomy for a driver it does not own.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return err
}
