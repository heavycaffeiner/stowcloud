package journal

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// One row per (account, file), holding the last thing that account did to it.
// No history: this is a listing, not an activity stream.
//
// The stored vocabulary is the tree this replaces: the column is "user" and the
// index is "write_event_by_user", not because those are better names but
// because this is the one database a Rust install keeps rather than migrates.
// The Go API calls it an account; the file on disk does not have to change for
// that. A schema that differed by a column name would make adoption a rewrite
// of every row for nothing.
const schemaV1 = `
CREATE TABLE write_event (
  user   INTEGER NOT NULL,
  share  INTEGER NOT NULL,
  path   TEXT    NOT NULL,
  op     TEXT    NOT NULL,
  at_ns  INTEGER NOT NULL,
  UNIQUE (user, share, path)
);
CREATE INDEX write_event_by_user ON write_event(user, at_ns DESC);
`

func migrations() []dbfile.Migration {
	return []dbfile.Migration{
		{Name: "1: write_event", SQL: schemaV1},
	}
}

// Every statement, as a constant.
const (
	sqlUpsertEvent = `
INSERT INTO write_event(user, share, path, op, at_ns) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user, share, path) DO UPDATE SET op = excluded.op, at_ns = excluded.at_ns`

	// Everything past the cap, oldest first, deleted by rowid so that rows
	// sharing a timestamp are still ordered. LIMIT -1 with an offset is how
	// SQLite says "all of them after the first n".
	sqlTrimAccount = `
DELETE FROM write_event WHERE rowid IN (
  SELECT rowid FROM write_event WHERE user = ?
  ORDER BY at_ns DESC, rowid DESC LIMIT -1 OFFSET ?
)`

	sqlRecentForAccount = `
SELECT share, path, op, at_ns FROM write_event
WHERE user = ? ORDER BY at_ns DESC, rowid DESC LIMIT ?`

	// The statements the adoption check reads the file's shape with.
	// schema_version is left out because it is the migration runner's own
	// bookkeeping, created before anything looks at the file, and a fresh
	// database holding only that is an empty one.
	sqlUserTables = `
SELECT name FROM sqlite_schema
WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_version'`
	sqlWriteEventCol = `SELECT name, type, "notnull" FROM pragma_table_info('write_event')`
	sqlWriteEventIdx = `SELECT name, origin FROM pragma_index_list('write_event')`
	sqlIndexColumns  = `SELECT name FROM pragma_index_info(?) ORDER BY seqno`
)
