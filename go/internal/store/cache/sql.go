package cache

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// The schema as it first shipped, carried over from the tree this replaces
// because the shape is right, with one change: node.id is supplied on insert
// rather than assigned.
//
// Its single node_ident index is wrong and is left alone. A position in the
// list is a durable version, so a step that has shipped is not edited: the
// correction is migration 2 below, and editing this one would teach the next
// maintainer that an applied migration is something they may rewrite.
//
// No path column, and no index besides the identity ones. Path resolution walks
// the parent chain, and that is what makes renaming a directory one row update
// instead of a fan-out across its whole subtree.
const schemaV1 = `
CREATE TABLE node (
  id       INTEGER PRIMARY KEY,
  share    INTEGER NOT NULL,
  parent   INTEGER NOT NULL,
  name     TEXT    NOT NULL,
  dev      INTEGER NOT NULL,
  ino      INTEGER NOT NULL,
  btime_ns INTEGER,
  flags    INTEGER NOT NULL,
  size     INTEGER,
  mtime_ns INTEGER
);
CREATE UNIQUE INDEX node_ident ON node(share, dev, ino, btime_ns);

CREATE TABLE diretag (
  share  INTEGER NOT NULL,
  fileid INTEGER NOT NULL,
  etag   TEXT    NOT NULL,
  rsize  INTEGER NOT NULL,
  rcount INTEGER NOT NULL,
  gen    INTEGER NOT NULL,
  valid  INTEGER NOT NULL,
  PRIMARY KEY (share, fileid)
) WITHOUT ROWID;

CREATE TABLE share_gen (
  share INTEGER PRIMARY KEY,
  gen   INTEGER NOT NULL
) WITHOUT ROWID;
`

// Identity is two partial indexes rather than one, because SQLite holds every
// NULL distinct in a unique index: version 1's single index over
// (share, dev, ino, btime_ns) lets the same file appear twice on a filesystem
// that reports no birth time. Splitting it on whether the column is NULL is
// what makes the constraint mean what it says.
//
// It throws the rows away rather than repairing them. A version 1 database may
// already hold duplicate no-btime identities and there is no principled one to
// keep; this file is rebuildable from the tree, which is the whole reason it is
// a separate database, so discarding is the cheaper of the two mistakes.
const schemaV2 = `
DROP INDEX IF EXISTS node_ident;
DROP TABLE IF EXISTS node;
DROP TABLE IF EXISTS diretag;
DROP TABLE IF EXISTS share_gen;

CREATE TABLE node (
  id       INTEGER PRIMARY KEY,
  share    INTEGER NOT NULL,
  parent   INTEGER NOT NULL,
  name     TEXT    NOT NULL,
  dev      INTEGER NOT NULL,
  ino      INTEGER NOT NULL,
  btime_ns INTEGER,
  flags    INTEGER NOT NULL,
  size     INTEGER,
  mtime_ns INTEGER
);
CREATE UNIQUE INDEX node_ident_with_btime
  ON node(share, dev, ino, btime_ns) WHERE btime_ns IS NOT NULL;
CREATE UNIQUE INDEX node_ident_without_btime
  ON node(share, dev, ino) WHERE btime_ns IS NULL;

CREATE TABLE diretag (
  share  INTEGER NOT NULL,
  fileid INTEGER NOT NULL,
  etag   TEXT    NOT NULL,
  rsize  INTEGER NOT NULL,
  rcount INTEGER NOT NULL,
  gen    INTEGER NOT NULL,
  valid  INTEGER NOT NULL,
  PRIMARY KEY (share, fileid)
) WITHOUT ROWID;

CREATE TABLE share_gen (
  share INTEGER PRIMARY KEY,
  gen   INTEGER NOT NULL
) WITHOUT ROWID;
`

// migrations is a function rather than a package-level slice so the list
// cannot be reassigned. Position is version, so a step that has shipped is
// never edited, renumbered or reordered.
func migrations() []dbfile.Migration {
	return []dbfile.Migration{
		{Name: "1: node, diretag and share_gen", SQL: schemaV1},
		{Name: "2: the two partial identity indexes", SQL: schemaV2, Discard: true},
	}
}

// Every statement, as a constant. Nothing here is assembled from parts.
const (
	// A lookup comes in two shapes for the same reason the index does. The
	// planner cannot prove a bound parameter is not NULL, so "btime_ns IS ?"
	// matches neither partial index and reads the whole table: on a cold walk
	// of a million files that is the difference between an index seek and a
	// scan, per file.
	sqlNodeByIdent = `
SELECT id, parent, name, flags FROM node
WHERE share = ? AND dev = ? AND ino = ? AND btime_ns = ?`

	sqlNodeByIdentNoBtime = `
SELECT id, parent, name, flags FROM node
WHERE share = ? AND dev = ? AND ino = ? AND btime_ns IS NULL`

	sqlNodeIDByIdent = `
SELECT id FROM node
WHERE share = ? AND dev = ? AND ino = ? AND btime_ns = ?`

	sqlNodeIDByIdentNoBtime = `
SELECT id FROM node
WHERE share = ? AND dev = ? AND ino = ? AND btime_ns IS NULL`

	sqlNodeIdentByID = `SELECT share, dev, ino, btime_ns FROM node WHERE id = ?`

	sqlInsertNode = `
INSERT INTO node(id, share, parent, name, dev, ino, btime_ns, flags, size, mtime_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	sqlMoveNode = `
UPDATE node SET parent = ?, name = ?, size = ?, mtime_ns = ?, flags = ? WHERE id = ?`

	sqlTouchNode = `UPDATE node SET size = ?, mtime_ns = ? WHERE id = ?`

	sqlNodeRowByID = `SELECT share, parent, name FROM node WHERE id = ?`

	sqlRenameNode = `UPDATE node SET parent = ?, name = ? WHERE id = ?`

	sqlReadDiretag = `
SELECT etag, rsize, rcount, gen, valid FROM diretag WHERE share = ? AND fileid = ?`

	sqlPutDiretag = `
INSERT INTO diretag(share, fileid, etag, rsize, rcount, gen, valid)
VALUES (?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(share, fileid) DO UPDATE SET
  etag = excluded.etag, rsize = excluded.rsize, rcount = excluded.rcount,
  gen = excluded.gen, valid = 1`

	sqlDirtyDiretag = `
INSERT INTO diretag(share, fileid, etag, rsize, rcount, gen, valid)
VALUES (?, ?, '', 0, 0, 0, 0)
ON CONFLICT(share, fileid) DO UPDATE SET valid = 0`

	sqlBumpShareGen = `
INSERT INTO share_gen(share, gen) VALUES (?, 1)
ON CONFLICT(share) DO UPDATE SET gen = gen + 1`

	sqlReadShareGen = `SELECT gen FROM share_gen WHERE share = ?`
)
