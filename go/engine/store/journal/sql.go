package journal

import "github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"

// One row per (account, file), holding the last thing that account did to
// it. No history: this is a listing, not an activity stream.
//
// The stored column is "user" and the index is "write_event_by_user". The Go
// API calls it an account; renaming the columns would rewrite every row for
// nothing.
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

// Every statement this package runs, written out whole.
const (
	sqlUpsertEvent = `
INSERT INTO write_event(user, share, path, op, at_ns) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user, share, path) DO UPDATE SET op = excluded.op, at_ns = excluded.at_ns`

	// Everything past the cap, deleted by rowid so that rows sharing a
	// timestamp are still ordered. LIMIT -1 with an offset is how SQLite
	// says "all of them after the first n".
	sqlTrimAccount = `
DELETE FROM write_event WHERE rowid IN (
  SELECT rowid FROM write_event WHERE user = ?
  ORDER BY at_ns DESC, rowid DESC LIMIT -1 OFFSET ?
)`

	sqlRecentForAccount = `
SELECT share, path, op, at_ns FROM write_event
WHERE user = ? ORDER BY at_ns DESC, rowid DESC LIMIT ?`

	// The windowed form is its own statement rather than the one above with
	// a clause appended: a statement assembled from parts is what these
	// being constants prevents. A caller with no window takes the first.
	sqlRecentSince = `
SELECT share, path, op, at_ns FROM write_event
WHERE user = ? AND at_ns >= ? ORDER BY at_ns DESC, rowid DESC LIMIT ?`
)
