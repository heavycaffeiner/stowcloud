package dbfile

// Every statement this package runs, as a constant prepared from nothing.
// Nothing here is assembled from parts: a query built from a string is an
// injection waiting for an input, and the one place that would need it (the
// discard step, which has to name what it drops) writes the names out instead.
const (
	driverName = "sqlite"

	sqlCountObjects = `SELECT count(*) FROM sqlite_schema`

	sqlReadPageSize      = `PRAGMA page_size`
	sqlReadAutoVacuum    = `PRAGMA auto_vacuum`
	sqlReadPageCount     = `PRAGMA page_count`
	sqlIncrementalVacuum = `PRAGMA incremental_vacuum`
	sqlCheckpoint        = `PRAGMA wal_checkpoint(TRUNCATE)`

	sqlCreateSchemaVersion = `
CREATE TABLE IF NOT EXISTS schema_version (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  version INTEGER NOT NULL
)`

	sqlReadVersion = `SELECT version FROM schema_version WHERE id = 1`

	sqlSetVersion = `
INSERT INTO schema_version(id, version) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET version = excluded.version`
)

// The values the bootstrap pragmas have to report back on a database this
// process created. auto_vacuum reports 2 for INCREMENTAL.
const (
	wantPageSize   = 4096
	wantAutoVacuum = 2
)
