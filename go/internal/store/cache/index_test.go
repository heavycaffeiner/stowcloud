package cache_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
)

// SQLite holds every NULL distinct in a unique index, so one index over
// (share, dev, ino, btime_ns) does not constrain a file whose filesystem
// reports no birth time. The two partial indexes are what make the constraint
// mean what it says, and this is the row that proves it.
func TestIdentityIsUniqueWithoutABirthTime(t *testing.T) {
	ctx := context.Background()
	p := openPair(t, t.TempDir(), 0)
	ids := populate(t, p.cache, []entry{{path: "a", ino: 42}})

	err := p.cache.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, `
INSERT INTO node(id, share, parent, name, dev, ino, btime_ns, flags, size, mtime_ns)
VALUES (?, ?, 0, 'a-again', ?, 42, NULL, 0, 0, 0)`,
			int64(ids["a"])+1, int64(testShare), int64(testDev))
		return ierr
	})
	if err == nil {
		t.Fatal("the same identity was inserted twice with no birth time")
	}
	if !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("the second row failed with %v, which is not the uniqueness constraint", err)
	}
}

// A partial index is only worth having if the query reaches it. A bound
// parameter the planner cannot prove is non-NULL matches neither one, and the
// lookup that runs once per file on a cold walk becomes a table scan.
func TestBothIdentityLookupsUseAnIndex(t *testing.T) {
	p := openPair(t, t.TempDir(), 0)
	populate(t, p.cache, shallowFirst(tree(), false))

	for _, tc := range []struct {
		name  string
		query string
		args  []any
	}{
		{
			"with a birth time",
			`SELECT id FROM node WHERE share = ? AND dev = ? AND ino = ? AND btime_ns = ?`,
			[]any{int64(testShare), int64(testDev), 17, 3_000},
		},
		{
			"without one",
			`SELECT id FROM node WHERE share = ? AND dev = ? AND ino = ? AND btime_ns IS NULL`,
			[]any{int64(testShare), int64(testDev), 12},
		},
	} {
		plan := explain(t, p.cache.SQL(), tc.query, tc.args...)
		if !strings.Contains(plan, "USING INDEX") && !strings.Contains(plan, "USING COVERING INDEX") {
			t.Errorf("the lookup %s does not use an index:\n%s", tc.name, plan)
		}
		if strings.Contains(plan, "SCAN node") {
			t.Errorf("the lookup %s scans the table:\n%s", tc.name, plan)
		}
	}
}

func explain(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explaining: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	var plan []string
	for rows.Next() {
		var (
			id, parent, notUsed int
			detail              string
		)
		if serr := rows.Scan(&id, &parent, &notUsed, &detail); serr != nil {
			t.Fatalf("scanning the plan: %v", serr)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explaining: %v", err)
	}
	return strings.Join(plan, "\n")
}

// The derivation does not change with the index, and this is the guard against
// a schema edit quietly moving an id.
func TestTheSchemaChangeDidNotMoveAnID(t *testing.T) {
	if got := cache.DeriveID(cache.Ident{Share: 1, Dev: 2, Ino: 3}, 0); got != 8682140122616183347 {
		t.Fatalf("DeriveID = %d", got)
	}
}
