package state

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// The schema. Every table here holds something the filesystem cannot
// regenerate.
//
// The birth time is two columns rather than the one node carries, and the
// difference is SQLite's rather than a choice: a WITHOUT ROWID table enforces
// NOT NULL on every column of its primary key, so a nullable btime_ns there
// would refuse exactly the rows a filesystem with no birth time produces. The
// pair is the derivation's own flag byte, stored.
const schemaV1 = `
CREATE TABLE fileid_override (
  share         INTEGER NOT NULL,
  dev           INTEGER NOT NULL,
  ino           INTEGER NOT NULL,
  btime_present INTEGER NOT NULL,
  btime_ns      INTEGER NOT NULL,
  id            INTEGER NOT NULL UNIQUE,
  PRIMARY KEY (share, dev, ino, btime_present, btime_ns)
) WITHOUT ROWID;
`

// migrations is a function rather than a package-level slice so the list
// cannot be reassigned. Position is version, so a step that has shipped is
// never edited, renumbered or reordered.
func migrations() []dbfile.Migration {
	return []dbfile.Migration{
		{Name: "1: fileid_override", SQL: schemaV1},
	}
}

// Every statement, as a constant. Nothing here is assembled from parts.
const (
	sqlReadFileIDOverride = `
SELECT id FROM fileid_override
WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?`

	sqlWriteFileIDOverride = `
INSERT INTO fileid_override(share, dev, ino, btime_present, btime_ns, id)
VALUES (?, ?, ?, ?, ?, ?)`

	sqlCountFileIDOverrides = `SELECT count(*) FROM fileid_override`
)
