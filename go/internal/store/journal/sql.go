package journal

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// One row per (account, file), holding the last thing that account did to it.
// No history: this is a listing, not an activity stream.
const schemaV1 = `
CREATE TABLE write_event (
  account INTEGER NOT NULL,
  share   INTEGER NOT NULL,
  path    TEXT    NOT NULL,
  op      TEXT    NOT NULL,
  at_ns   INTEGER NOT NULL,
  PRIMARY KEY (account, share, path)
);
CREATE INDEX write_event_recent ON write_event(account, at_ns DESC);
`

func migrations() []dbfile.Migration {
	return []dbfile.Migration{
		{Name: "1: write_event", SQL: schemaV1},
	}
}

// Every statement, as a constant.
const (
	sqlUpsertEvent = `
INSERT INTO write_event(account, share, path, op, at_ns) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(account, share, path) DO UPDATE SET op = excluded.op, at_ns = excluded.at_ns`

	// Everything past the cap, oldest first, deleted by rowid so that rows
	// sharing a timestamp are still ordered. LIMIT -1 with an offset is how
	// SQLite says "all of them after the first n".
	sqlTrimAccount = `
DELETE FROM write_event WHERE rowid IN (
  SELECT rowid FROM write_event WHERE account = ?
  ORDER BY at_ns DESC, rowid DESC LIMIT -1 OFFSET ?
)`

	sqlRecentForAccount = `
SELECT share, path, op, at_ns FROM write_event
WHERE account = ? ORDER BY at_ns DESC, rowid DESC LIMIT ?`
)
