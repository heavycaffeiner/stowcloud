package dbfile

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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

func createOne() []Migration {
	return []Migration{{Name: "one", SQL: `CREATE TABLE thing (id INTEGER PRIMARY KEY, v TEXT)`}}
}

// The order is the whole point of the list, and it is asserted rather than
// commented: the driver takes the same pragmas as DSN parameters and sorts
// them, so nothing outside this package guarantees which one runs first.
func TestBusyTimeoutLeadsTheBatch(t *testing.T) {
	p := pragmas()
	if !strings.Contains(p[0], "busy_timeout") {
		t.Fatalf("the batch starts with %q, not busy_timeout", p[0])
	}
	for i, s := range p {
		if strings.Contains(s, "journal_mode") && i == 0 {
			t.Fatal("journal_mode leads the batch: it is the pragma that needs an exclusive lock")
		}
	}
}

// Every connection the pool opens, not just the first. A pragma applied on one
// connection says nothing about the seven others a request can land on.
func TestPragmasOnEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: createOne()})

	want := []struct {
		pragma string
		value  string
	}{
		{"PRAGMA busy_timeout", "5000"},
		{"PRAGMA journal_mode", "wal"},
		{"PRAGMA synchronous", "1"},
		{"PRAGMA wal_autocheckpoint", "1000"},
		{"PRAGMA journal_size_limit", "67108864"},
		{"PRAGMA cache_size", "-16000"},
		{"PRAGMA temp_store", "2"},
		{"PRAGMA foreign_keys", "1"},
	}

	// Held at once, so the pool has to open all of them rather than handing
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

// The shape that produced "database is locked" in the tree this replaces: a
// database that does not exist yet, and every connection in the pool opening
// at once to set journal_mode on it.
func TestFreshDatabaseUnderConcurrentOpen(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: createOne()})

	var wg sync.WaitGroup
	errs := make([]error, poolSize*2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = d.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO thing(v) VALUES (?)`, "v")
				return err
			})
		}()
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
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: createOne()})

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

	// Re-opening applies nothing: a step whose version is already stored is
	// not re-run, which is what stops a CREATE TABLE failing on the second
	// start.
	again, err := Open(ctx, Spec{Path: path, Migrations: mig})
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer func() {
		if err := again.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	if v, err := again.Version(ctx); err != nil || v != 2 {
		t.Fatalf("version %d (err %v) after re-opening, want 2", v, err)
	}
}

func TestSchemaAheadRefusesToOpen(t *testing.T) {
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

	// The same file, opened by a binary that knows one migration: a downgrade.
	_, err := Open(ctx, Spec{Path: path, Migrations: two[:1]})
	if !errors.Is(err, ErrSchemaAhead) {
		t.Fatalf("opening a newer database returned %v, want ErrSchemaAhead", err)
	}
}

// A migration and its version bump are one transaction. Half of a step and a
// version claiming the whole of it is the state this proves cannot happen.
func TestFailedMigrationLeavesTheOldVersionAndShape(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	bad := []Migration{
		{Name: "one", SQL: `CREATE TABLE a (id INTEGER PRIMARY KEY)`},
		{Name: "two", SQL: `CREATE TABLE b (id INTEGER PRIMARY KEY); CREATE TABLE b (id INTEGER)`},
	}
	_, err := Open(ctx, Spec{Path: path, Migrations: bad})
	if !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("a failing migration returned %v, want ErrMigrationFailed", err)
	}

	// The first step stands, the second left nothing behind.
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
		t.Errorf("table b survived a rolled-back migration")
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

	// The second step cannot carry the rows across, so it says so and names
	// what it drops.
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

func TestDiscardIsRefusedOnADurableDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")

	mig := []Migration{{Name: "one", SQL: `CREATE TABLE a (id INTEGER PRIMARY KEY)`},
		{Name: "two", Discard: true, SQL: `DROP TABLE IF EXISTS a`}}
	_, err := Open(ctx, Spec{Path: path, Migrations: mig, Rebuildable: false})
	if !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("a discard on a durable database returned %v, want ErrMigrationFailed", err)
	}
}

func TestWritesBlockedGatesGrowthAndNothingElse(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: createOne()})

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

	// Reads and the writes that free space are not gated: the guard exists so
	// a full volume can be recovered from, not so it becomes permanent.
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

func TestSizeBytesAndVacuum(t *testing.T) {
	ctx := context.Background()
	d := open(t, Spec{Path: filepath.Join(t.TempDir(), "x.db"), Migrations: createOne()})

	size, err := d.SizeBytes(ctx)
	if err != nil {
		t.Fatalf("SizeBytes: %v", err)
	}
	if size <= 0 {
		t.Errorf("SizeBytes reported %d for a database with a table in it", size)
	}
	if err := d.Vacuum(ctx); err != nil {
		t.Errorf("Vacuum: %v", err)
	}
}
