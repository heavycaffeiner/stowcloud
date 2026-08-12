package cache_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
)

// insertNode writes a node row by hand, which is how a test reaches the
// constraint rather than the application lookup that sits in front of it.
const insertNode = `
INSERT INTO node(id, share, parent, name, dev, ino, btime_ns, flags, size, mtime_ns)
VALUES (?, ?, 0, ?, ?, ?, ?, 0, 0, 0)`

// openV1 builds a database at the version that shipped, with the rows the test
// wants in it, and closes it.
func openV1(t *testing.T, path string, write func(*sql.Tx) error) {
	t.Helper()
	ctx := context.Background()
	f, err := dbfile.Open(ctx, cache.SpecV1(path))
	if err != nil {
		t.Fatalf("opening a version 1 cache: %v", err)
	}
	if werr := f.Write(ctx, write); werr != nil {
		t.Fatalf("writing to a version 1 cache: %v", werr)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("closing a version 1 cache: %v", cerr)
	}
}

// Version 1's single index over (share, dev, ino, btime_ns) does not constrain
// a filesystem that reports no birth time, because SQLite holds every NULL
// distinct. This is that database, with the duplicate it admits, and what
// opening it with this binary has to do about it.
func TestVersion1IsRebuiltAsAnEmptyVersion2(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	openV1(t, path, func(tx *sql.Tx) error {
		for i, id := range []int64{100, 101} {
			if _, err := tx.ExecContext(ctx, insertNode,
				id, int64(testShare), string(rune('a'+i)), int64(testDev), 42, nil); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO share_gen(share, gen) VALUES (?, 9)`, int64(testShare))
		return err
	})

	f, err := dbfile.Open(ctx, cache.Spec(path))
	if err != nil {
		t.Fatalf("opening the migrated cache: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	if v, verr := f.Version(ctx); verr != nil || v != 2 {
		t.Fatalf("version %d, %v; want 2 and no error", v, verr)
	}
	for table, count := range map[string]string{
		"node":      `SELECT count(*) FROM node`,
		"share_gen": `SELECT count(*) FROM share_gen`,
	} {
		var n int
		if qerr := f.SQL().QueryRowContext(ctx, count).Scan(&n); qerr != nil {
			t.Fatalf("counting %s: %v", table, qerr)
		}
		if n != 0 {
			t.Errorf("%s kept %d rows across the discard", table, n)
		}
	}

	var indexes int
	if qerr := f.SQL().QueryRowContext(ctx, `
SELECT count(*) FROM sqlite_schema
WHERE type = 'index' AND name IN ('node_ident_with_btime', 'node_ident_without_btime')`).
		Scan(&indexes); qerr != nil {
		t.Fatalf("reading the schema: %v", qerr)
	}
	if indexes != 2 {
		t.Errorf("the migrated database carries %d of the two partial identity indexes", indexes)
	}
}

// The rebuilt shape refuses what the old one admitted, and the refusal comes
// from the database rather than from a lookup the application does first.
func TestMigratedCacheRefusesADuplicateNoBtimeIdentity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	openV1(t, path, func(*sql.Tx) error { return nil })

	f, err := dbfile.Open(ctx, cache.Spec(path))
	if err != nil {
		t.Fatalf("opening the migrated cache: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	if werr := f.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, insertNode, 200, int64(testShare), "a", int64(testDev), 42, nil)
		return ierr
	}); werr != nil {
		t.Fatalf("inserting the first row: %v", werr)
	}

	werr := f.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, insertNode, 201, int64(testShare), "b", int64(testDev), 42, nil)
		return ierr
	})
	if werr == nil {
		t.Fatal("the same no-btime identity was inserted twice")
	}
	if !strings.Contains(werr.Error(), "UNIQUE") && !strings.Contains(werr.Error(), "constraint") {
		t.Fatalf("the second row failed with %v, which is not the uniqueness constraint", werr)
	}
}
