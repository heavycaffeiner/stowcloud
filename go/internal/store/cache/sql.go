package cache

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// The schema, carried over from the tree this replaces because the shape is
// right, with one change: node.id is supplied on insert rather than assigned.
//
// No path column, and no index besides node_ident. Path resolution walks the
// parent chain, and that is what makes renaming a directory one row update
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

// migrations is a function rather than a package-level slice so the list
// cannot be reassigned. Position is version, so a step that has shipped is
// never edited, renumbered or reordered.
func migrations() []dbfile.Migration {
	return []dbfile.Migration{
		{Name: "1: node, diretag and share_gen", SQL: schemaV1},
	}
}

// Every statement, as a constant. Nothing here is assembled from parts.
const (
	sqlNodeByIdent = `
SELECT id, parent, name, flags FROM node
WHERE share = ? AND dev = ? AND ino = ? AND btime_ns IS ?`

	sqlNodeIDByIdent = `
SELECT id FROM node
WHERE share = ? AND dev = ? AND ino = ? AND btime_ns IS ?`

	sqlNodeIdentByID = `SELECT share, dev, ino, btime_ns FROM node WHERE id = ?`

	sqlInsertNode = `
INSERT INTO node(id, share, parent, name, dev, ino, btime_ns, flags, size, mtime_ns)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	sqlMoveNode = `
UPDATE node SET parent = ?, name = ?, size = ?, mtime_ns = ?, flags = ? WHERE id = ?`

	sqlTouchNode = `UPDATE node SET size = ?, mtime_ns = ? WHERE id = ?`
)
