// Package dbfile is one SQLite file: its pragmas, its migration runner and
// version table, and the one serialised path every write to it takes.
//
// Three databases use it and none of them holds an opinion about any of the
// three. The pragmas in particular are applied here, in one order, on every
// connection the pool opens, because the failure they prevent appears only
// when several connections open at once against a database that does not
// exist yet.
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

// The errors this package answers with. Nothing here chooses an exit code or
// an HTTP status: an unopenable cache and an unopenable account database are
// the same error and very different decisions.
var (
	// ErrSchemaAhead is a database written by a newer binary. Opening it is a
	// refusal rather than a best effort, because a downgrade quietly writing
	// an old shape into a new file is how a rollback turns into data loss.
	ErrSchemaAhead = errors.New("database was written by a newer binary")

	// ErrMigrationFailed carries a rolled-back migration. The old version and
	// the old shape both still stand.
	ErrMigrationFailed = errors.New("migration failed")

	// ErrWritesBlocked is the size guard. Reads continue.
	ErrWritesBlocked = errors.New("writes are blocked: free space is below the floor")

	// ErrBusy is a busy_timeout that expired. It is surfaced rather than
	// retried forever: a caller that waited five seconds for a lock wants to
	// know, and something else is writing this file.
	ErrBusy = errors.New("database is busy")
)

// poolSize is the connection count. WAL gives one writer and many readers, so
// this is sized for the readers; writers queue on one mutex rather than
// discovering the single-writer rule by contending for it.
const poolSize = 8

// pragmas are applied to every connection the pool opens, in this order.
//
// busy_timeout leads, and it is not a stylistic ordering. It governs how every
// statement after it behaves under contention, and journal_mode is the one
// that needs an exclusive lock: setting the timeout afterwards leaves the
// pragma most likely to contend running without it, which is what produced
// "database is locked" on a fresh database in the tree this replaces.
//
// A function rather than a package-level slice, so the set cannot be
// reassigned by anything that imports this package.
func pragmas() []string {
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

// bootstrapPragmas are database-level rather than per-connection: they take
// effect only while the database has no tables, so they are applied once, on a
// connection of their own, before the pool exists.
func bootstrapPragmas() []string {
	return []string{
		"PRAGMA page_size = 4096",
		"PRAGMA auto_vacuum = INCREMENTAL",
	}
}

// Spec is one database file and what it should hold.
type Spec struct {
	Path       string
	Migrations []Migration

	// Rebuildable lets a migration declare itself unmigratable and discard
	// what is already there. It is true for the cache and false for
	// everything else: discarding a database nothing can rebuild is data loss
	// with a version bump on top.
	Rebuildable bool

	// Adopt inspects a file that carries no version row and reports the version
	// its shape already amounts to, or zero for one this runner should migrate
	// from scratch. It is for a database another implementation wrote and this
	// one inherits: running migration 1 against an existing table is an error,
	// and an empty file that happens to be the same name is not.
	//
	// Refusing is its other answer. A shape that nearly matches is not adopted
	// on the strength of the resemblance, because the runner would then record
	// a version the file does not have.
	Adopt func(context.Context, *sql.DB) (int, error)
}

// DB is one open database file.
type DB struct {
	sql  *sql.DB
	path string

	// wmu is the single serialised write path.
	wmu sync.Mutex

	// blocked is the size guard, and it gates growth alone. Refusing a delete
	// because the volume is full would block the only thing that could fix it.
	blocked atomic.Bool

	// Closing twice is what a shutdown path and a deferred close do between
	// them, and it is not an error worth propagating.
	closeOnce sync.Once
	closeErr  error
}

// Open applies the pragmas, runs the pending migrations and returns the pool.
// A database at a version this binary does not know refuses with
// ErrSchemaAhead and is not touched.
func Open(ctx context.Context, spec Spec) (*DB, error) {
	if err := bootstrap(ctx, spec.Path); err != nil {
		return nil, err
	}

	// _txlock=immediate because every transaction this store opens is a write
	// transaction. A deferred one takes the write lock on its first write
	// instead, which is where an upgrade can fail after the reads it made are
	// already stale.
	pool := sql.OpenDB(&connector{
		drv:     &sqlite.Driver{},
		dsn:     spec.Path + "?_txlock=immediate",
		pragmas: pragmas(),
	})
	pool.SetMaxOpenConns(poolSize)
	pool.SetMaxIdleConns(poolSize)

	d := &DB{sql: pool, path: spec.Path}
	if err := migrate(ctx, d, spec); err != nil {
		return nil, errors.Join(err, pool.Close())
	}
	return d, nil
}

// bootstrap applies the two database-level pragmas and proves they landed.
//
// The proof is only available on a database this call created: SQLite ignores
// both silently once a table exists, which is what makes re-opening an
// existing store harmless and what makes the check meaningless there.
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
	for _, p := range bootstrapPragmas() {
		if _, err := db.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("%s on %s: %w", p, filepath.Base(path), err)
		}
	}
	if objects > 0 {
		return nil
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
			"the database-level pragmas ran after the schema", filepath.Base(path), pageSize, autoVacuum)
	}
	return nil
}

// SQL is the pool, for readers. A writer takes Write instead.
func (d *DB) SQL() *sql.DB { return d.sql }

// Path is the file this database is in.
func (d *DB) Path() string { return d.path }

// Close folds the write-ahead log back into the database and closes the pool.
//
// TRUNCATE rather than PASSIVE: passive gives up the moment another connection
// is mid-read, and shutdown is exactly when waiting is what we want. It is not
// a durability measure, the log is already durable; it means the next start
// has nothing to replay.
func (d *DB) Close() error {
	d.closeOnce.Do(func() {
		_, err := d.sql.ExecContext(context.Background(), sqlCheckpoint)
		d.closeErr = errors.Join(err, d.sql.Close())
	})
	return d.closeErr
}

// Write runs fn in this database's single serialised write path. Readers are
// not blocked. It is a method here rather than on the store above, because
// there is one of these per file and a write has to say which file it locks.
//
// Write does not consult the size guard. The guard gates growth, and the call
// that knows whether a statement grows the file is the one writing it.
func (d *DB) Write(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	d.wmu.Lock()
	defer d.wmu.Unlock()

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return mapErr(err)
	}
	defer func() {
		// A rollback after a successful commit reports ErrTxDone, which is the
		// ordinary path and not a failure. Anything else is one.
		if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
			err = errors.Join(err, mapErr(rerr))
		}
	}()

	if err := fn(tx); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit())
}

// SetWritesBlocked trips or clears the size guard. The caller is whatever
// samples free space on the volume this file sits on; this package never
// touches the filesystem itself.
func (d *DB) SetWritesBlocked(blocked bool) { d.blocked.Store(blocked) }

// WritesBlocked reports the guard.
func (d *DB) WritesBlocked() bool { return d.blocked.Load() }

// EnsureWritable is what a statement that makes the file bigger calls first.
// A statement that reclaims space, or rewrites a row in place, does not: those
// are how a full volume gets recovered.
func (d *DB) EnsureWritable() error {
	if d.blocked.Load() {
		return fmt.Errorf("%w: %s", ErrWritesBlocked, filepath.Base(d.path))
	}
	return nil
}

// SizeBytes is page_count times page_size, which answers for a file-backed and
// an in-memory database identically.
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

// Vacuum reclaims the free pages a delete left behind. It needs the
// incremental auto_vacuum the bootstrap sets, which is why that pragma has to
// run before the schema.
func (d *DB) Vacuum(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx, sqlIncrementalVacuum)
	return err
}

// connector applies the pragma list on every connection the pool opens.
//
// The driver takes the same list as DSN parameters and sorts it before
// applying it, which happens to put busy_timeout first today. Applying it here
// keeps the order this store depends on in this file, where a test can read
// it, rather than in a dependency's sort function.
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
		return nil, errors.Join(errors.New("the sqlite driver cannot execute a statement"), cn.Close())
	}
	for _, p := range c.pragmas {
		// context.Background rather than the caller's: a connection carrying
		// half the pragma set is worse than one that took a moment longer.
		if _, err := ex.ExecContext(context.Background(), p, nil); err != nil {
			return nil, errors.Join(fmt.Errorf("%s: %w", p, err), cn.Close())
		}
	}
	return cn, nil
}

// mapErr names the one driver error a caller can act on. Everything else is
// carried through as it came.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	// The low byte is the primary result code; the rest is the extended one.
	if errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return err
}
