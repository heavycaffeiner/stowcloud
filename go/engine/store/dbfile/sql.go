package dbfile

// Every statement this package runs, written out whole. Nothing here is
// assembled from parts, so no input of any kind can reach a query.
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

	sqlWriteVersion = `
INSERT INTO schema_version(id, version) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET version = excluded.version`
)

// What the bootstrap pragmas must read back on a file this process just
// created. auto_vacuum answers 2 for INCREMENTAL.
const (
	wantPageSize   = 4096
	wantAutoVacuum = 2
)
