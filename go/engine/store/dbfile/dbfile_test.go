package dbfile

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

func open(t *testing.T, spec Spec) *DB {
	t.Helper()
	d, err := Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open(%s): %v", filepath.Base(spec.Path), err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return d
}

func oneTable() []Migration {
	return []Migration{{Name: "one", SQL: `CREATE TABLE thing (id INTEGER PRIMARY KEY, v TEXT)`}}
}

// The order is the point of the list, so it is asserted rather than
// commented: the same pragmas can be passed as DSN parameters, which the
// driver is free to sort, and then nothing guarantees which one runs first.
func TestBusyTimeoutLeadsTheConnectionPragmas(t *testing.T) {
	p := connPragmas()
	if !strings.Contains(p[0], "busy_timeout") {
		t.Fatalf("the list starts with %q, not busy_timeout", p[0])
	}
	journal := -1
	for i, s := range p {
		if strings.Contains(s, "journal_mode") {
			journal = i
		}
	}
	if journal <= 0 {
		t.Fatalf("journal_mode is at position %d; it has to follow busy_timeout", journal)
	}
}

// Every connection the pool opens, not only the first: a pragma set on one
// connection says nothing about the seven others a caller can land on.
func TestPragmasOnEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: oneTable()})

	want := []struct{ pragma, value string }{
		{"PRAGMA busy_timeout", "5000"},
		{"PRAGMA journal_mode", "wal"},
		{"PRAGMA synchronous", "1"},
		{"PRAGMA wal_autocheckpoint", "1000"},
		{"PRAGMA journal_size_limit", "67108864"},
		{"PRAGMA cache_size", "-16000"},
		{"PRAGMA temp_store", "2"},
		{"PRAGMA foreign_keys", "1"},
	}

	// Held at once, so the pool has to open all of them instead of handing
	// the same connection back eight times.
	conns := make([]*sql.Conn, 0, poolSize)
	for range poolSize {
		c, err := d.SQL().Conn(ctx)
		if err != nil {
			t.Fatalf("Conn: %v", err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		for _, w := range want {
			var got string
			if err := c.QueryRowContext(ctx, w.pragma).Scan(&got); err != nil {
				t.Fatalf("connection %d: %s: %v", i, w.pragma, err)
			}
			if got != w.value {
				t.Errorf("connection %d: %s = %q, want %q", i, w.pragma, got, w.value)
			}
		}
		if err := c.Close(); err != nil {
			t.Errorf("closing connection %d: %v", i, err)
		}
	}
}

// The shape that produces "database is locked" when the timeout is set after
// journal_mode: a file that does not exist yet, and the whole pool opening
// against it at once.
func TestFreshDatabaseUnderConcurrentOpen(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: oneTable()})

	var wg sync.WaitGroup
	errs := make([]error, poolSize*2)
	for i := range errs {
		wg.Add(1)
		task.Go(ctx, "dbfile: concurrent writer", func() {
			defer wg.Done()
			errs[i] = d.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO thing(v) VALUES (?)`, "v")
				return err
			})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	var n int
	if err := d.SQL().QueryRowContext(ctx, `SELECT count(*) FROM thing`).Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != len(errs) {
		t.Errorf("wrote %d rows, want %d", n, len(errs))
	}
}

func TestBootstrapPragmasLandBeforeTheSchema(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: oneTable()})

	var pageSize, autoVacuum int
	if err := d.SQL().QueryRowContext(ctx, sqlReadPageSize).Scan(&pageSize); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if err := d.SQL().QueryRowContext(ctx, sqlReadAutoVacuum).Scan(&autoVacuum); err != nil {
		t.Fatalf("auto_vacuum: %v", err)
	}
	if pageSize != wantPageSize || autoVacuum != wantAutoVacuum {
		t.Errorf("page_size %d, auto_vacuum %d; want %d and %d",
			pageSize, autoVacuum, wantPageSize, wantAutoVacuum)
	}
}

// Re-opening an existing file skips the bootstrap pragmas, so a database
// created under a different page size still opens rather than failing the
// proof read that only applies to a file this process just made.
func TestReopeningAnExistingFileSkipsTheBootstrapProof(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	// A file created outside this package, with neither bootstrap pragma.
	raw, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA page_size = 8192`); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE foreign_thing (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	d := open(t, Spec{Path: path, Migrations: oneTable()})
	var pageSize int
	if err := d.SQL().QueryRowContext(ctx, sqlReadPageSize).Scan(&pageSize); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if pageSize != 8192 {
		t.Errorf("page_size %d after re-opening a foreign file, want the file's own 8192", pageSize)
	}
}

func TestMigrationsApplyOnceAndReportTheVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")
	mig := []Migration{
		{Name: "one", SQL: `CREATE TABLE a (id INTEGER PRIMARY KEY)`},
		{Name: "two", SQL: `CREATE TABLE b (id INTEGER PRIMARY KEY)`},
	}

	d := open(t, Spec{Path: path, Migrations: mig})
	switch v, err := d.Version(ctx); {
	case err != nil:
		t.Fatalf("Version: %v", err)
	case v != 2:
		t.Fatalf("version %d after two migrations, want 2", v)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A step whose version is already stored is not re-run, which is what
	// stops a CREATE TABLE from failing on the second start.
	again := open(t, Spec{Path: path, Migrations: mig})
	if v, err := again.Version(ctx); err != nil || v != 2 {
		t.Fatalf("version %d (err %v) after re-opening, want 2", v, err)
	}
}

func TestSchemaAheadRefusesAndLeavesTheFileAlone(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	two := []Migration{
		{Name: "one", SQL: `CREATE TABLE a (id INTEGER PRIMARY KEY)`},
		{Name: "two", SQL: `CREATE TABLE b (id INTEGER PRIMARY KEY)`},
	}
	d := open(t, Spec{Path: path, Migrations: two})
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file: %v", err)
	}

	// The same file, opened by a binary that knows one step: a downgrade.
	if _, oerr := Open(ctx, Spec{Path: path, Migrations: two[:1]}); !errors.Is(oerr, ErrSchemaAhead) {
		t.Fatalf("opening a newer database returned %v, want ErrSchemaAhead", oerr)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading the file: %v", err)
	}
	if string(before) != string(after) {
		t.Error("a refused open modified the database file")
	}
}

// A step and its version bump are one transaction. Half a step beside a
// version claiming the whole of it is the state this proves cannot happen.
func TestFailedMigrationLeavesTheOldVersionAndShape(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	bad := []Migration{
		{Name: "one", SQL: `CREATE TABLE a (id INTEGER PRIMARY KEY)`},
		{Name: "two", SQL: `CREATE TABLE b (id INTEGER PRIMARY KEY); CREATE TABLE b (id INTEGER)`},
	}
	if _, err := Open(ctx, Spec{Path: path, Migrations: bad}); !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("a failing migration returned %v, want ErrMigrationFailed", err)
	}

	d := open(t, Spec{Path: path, Migrations: bad[:1]})
	if v, err := d.Version(ctx); err != nil || v != 1 {
		t.Fatalf("version %d (err %v) after a failed second step, want 1", v, err)
	}
	var n int
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'b'`).Scan(&n); err != nil {
		t.Fatalf("looking for table b: %v", err)
	}
	if n != 0 {
		t.Error("table b survived a rolled-back migration")
	}
}

// The precondition exists to name the offending row, so the test asserts the
// refusal carries that name and that the step's SQL never ran.
func TestPreconditionRefusesInsideTheStepsTransaction(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	first := []Migration{{Name: "one", SQL: `CREATE TABLE a (id INTEGER PRIMARY KEY, v TEXT)`}}
	d := open(t, Spec{Path: path, Migrations: first})
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO a(id, v) VALUES (7, 'unmigratable')`)
		return err
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := append(first, Migration{
		Name: "two",
		SQL:  `CREATE TABLE b (id INTEGER PRIMARY KEY)`,
		Precondition: func(ctx context.Context, tx *sql.Tx) error {
			var id int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM a WHERE v = 'unmigratable'`).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			return errors.New("row 7 cannot be migrated")
		},
	})

	_, err := Open(ctx, Spec{Path: path, Migrations: second})
	if !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("a refused precondition returned %v, want ErrMigrationFailed", err)
	}
	if !strings.Contains(err.Error(), "row 7") {
		t.Errorf("the refusal does not name the row: %v", err)
	}

	back := open(t, Spec{Path: path, Migrations: first})
	if v, err := back.Version(ctx); err != nil || v != 1 {
		t.Fatalf("version %d (err %v) after a refused precondition, want 1", v, err)
	}
	var n int
	if err := back.SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'b'`).Scan(&n); err != nil {
		t.Fatalf("looking for table b: %v", err)
	}
	if n != 0 {
		t.Error("the step's SQL ran even though its precondition refused")
	}
}

func TestDiscardRebuildsARebuildableDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	first := []Migration{{Name: "one", SQL: `CREATE TABLE thing (id INTEGER PRIMARY KEY, v TEXT)`}}
	d := open(t, Spec{Path: path, Migrations: first, Rebuildable: true})
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO thing(v) VALUES ('before')`)
		return err
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := append(first, Migration{
		Name:    "two: a shape the first cannot become",
		Discard: true,
		SQL: `DROP TABLE IF EXISTS thing;
		      CREATE TABLE thing (id INTEGER PRIMARY KEY, v TEXT, w TEXT NOT NULL)`,
	})

	rebuilt := open(t, Spec{Path: path, Migrations: second, Rebuildable: true})
	if v, err := rebuilt.Version(ctx); err != nil || v != 2 {
		t.Fatalf("version %d (err %v) after a discard, want 2", v, err)
	}
	var n int
	if err := rebuilt.SQL().QueryRowContext(ctx, `SELECT count(*) FROM thing`).Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != 0 {
		t.Errorf("a discarded database kept %d rows", n)
	}
}

// Refused before any SQL runs: the table the step would drop is still there
// afterwards, and the version has not moved.
func TestDiscardIsRefusedBeforeRunningAnySQL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	mig := []Migration{
		{Name: "one", SQL: `CREATE TABLE a (id INTEGER PRIMARY KEY)`},
		{Name: "two", Discard: true, SQL: `DROP TABLE IF EXISTS a`},
	}
	if _, err := Open(ctx, Spec{Path: path, Migrations: mig}); !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("a discard on a durable database returned %v, want ErrMigrationFailed", err)
	}

	d := open(t, Spec{Path: path, Migrations: mig[:1]})
	if v, err := d.Version(ctx); err != nil || v != 1 {
		t.Fatalf("version %d (err %v) after a refused discard, want 1", v, err)
	}
	var n int
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'a'`).Scan(&n); err != nil {
		t.Fatalf("looking for table a: %v", err)
	}
	if n != 1 {
		t.Error("the refused discard dropped the table anyway")
	}
}

func TestWritesBlockedGatesGrowthAndNothingElse(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: oneTable()})

	if err := d.EnsureWritable(); err != nil {
		t.Fatalf("a store with room refused a write: %v", err)
	}
	d.SetWritesBlocked(true)
	if !d.WritesBlocked() {
		t.Fatal("the guard did not trip")
	}
	if err := d.EnsureWritable(); !errors.Is(err, ErrWritesBlocked) {
		t.Fatalf("a tripped guard returned %v, want ErrWritesBlocked", err)
	}

	// Write itself never consults the flag: the caller that knows whether
	// its own statement grows the file is the one that checks.
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM thing`)
		return err
	}); err != nil {
		t.Errorf("a delete was refused while writes were blocked: %v", err)
	}

	d.SetWritesBlocked(false)
	if err := d.EnsureWritable(); err != nil {
		t.Errorf("clearing the guard did not restore writes: %v", err)
	}
}

// Observed rather than inferred: the callback records how many of its peers
// are inside it at the same moment.
func TestWriteSerializesConcurrentCallers(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: oneTable()})

	var inside, maxSeen atomic.Int64
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		task.Go(ctx, "dbfile: overlap probe", func() {
			defer wg.Done()
			errs[i] = d.Write(ctx, func(tx *sql.Tx) error {
				n := inside.Add(1)
				defer inside.Add(-1)
				for {
					got := maxSeen.Load()
					if n <= got || maxSeen.CompareAndSwap(got, n) {
						break
					}
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO thing(v) VALUES ('x')`)
				return err
			})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	if got := maxSeen.Load(); got != 1 {
		t.Errorf("%d writers were inside Write at once, want 1", got)
	}
}

// A second process holding the write lock past busy_timeout surfaces as
// ErrBusy rather than being retried forever inside this package.
func TestErrBusySurfacesFromAHeldWriteLock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")
	d := open(t, Spec{Path: path, Migrations: oneTable()})

	// A raw connection with no busy_timeout of its own, holding an
	// exclusive lock for as long as its transaction stays open.
	raw, err := sql.Open(driverName, path+"?_txlock=exclusive&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() {
		if cerr := raw.Close(); cerr != nil {
			t.Errorf("raw close: %v", cerr)
		}
	}()
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("raw begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO thing(v) VALUES ('held')`); err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	// Five seconds of busy_timeout, then the refusal.
	werr := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO thing(v) VALUES ('waiting')`)
		return err
	})
	if !errors.Is(werr, ErrBusy) {
		t.Fatalf("a write against a held lock returned %v, want ErrBusy", werr)
	}
	if err := tx.Rollback(); err != nil {
		t.Errorf("raw rollback: %v", err)
	}
}

func TestSizeBytesMatchesPageCountTimesPageSize(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: oneTable()})

	got, err := d.SizeBytes(ctx)
	if err != nil {
		t.Fatalf("SizeBytes: %v", err)
	}
	var pages, size int64
	if err := d.SQL().QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	if err := d.SQL().QueryRowContext(ctx, `PRAGMA page_size`).Scan(&size); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if want := pages * size; got != want || want <= 0 {
		t.Errorf("SizeBytes = %d, want %d", got, want)
	}
}

func TestVacuumReclaims(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: oneTable()})

	if err := d.Write(ctx, func(tx *sql.Tx) error {
		for range 2000 {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO thing(v) VALUES (?)`, strings.Repeat("x", 512)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	full, err := d.SizeBytes(ctx)
	if err != nil {
		t.Fatalf("SizeBytes: %v", err)
	}
	if cerr := d.Write(ctx, func(tx *sql.Tx) error {
		_, xerr := tx.ExecContext(ctx, `DELETE FROM thing`)
		return xerr
	}); cerr != nil {
		t.Fatalf("clearing: %v", cerr)
	}
	if verr := d.Vacuum(ctx); verr != nil {
		t.Fatalf("Vacuum: %v", verr)
	}
	after, err := d.SizeBytes(ctx)
	if err != nil {
		t.Fatalf("SizeBytes: %v", err)
	}
	if after >= full {
		t.Errorf("size %d after vacuuming a cleared table, was %d before", after, full)
	}
}

// Close truncates the WAL, so the next start has nothing to replay.
func TestCloseLeavesNoWALToReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	d, err := Open(ctx, Spec{Path: path, Migrations: oneTable()})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO thing(v) VALUES ('v')`)
		return err
	}); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	switch info, err := os.Stat(path + "-wal"); {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		t.Fatalf("stat of the WAL: %v", err)
	case info.Size() != 0:
		t.Errorf("the WAL is %d bytes after Close, want it truncated or gone", info.Size())
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	d, err := Open(context.Background(), Spec{
		Path: filepath.Join(t.TempDir(), "x.db"), Migrations: oneTable(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
