package state

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// The schema. Every table here holds something the filesystem cannot
// regenerate, which is what makes this file the whole of the backup
// instruction.
//
// Four tables key by the identity tuple rather than by a node id: dead
// properties, locks, favorites and the target of a share link. Keying by an id
// only the cache mints is what forced the cache to carry a pin saying "do not
// reap this, something durable points at it". An identity is a fact about the
// file, so deleting the cache now costs a lookup rather than leaving a row
// pointing at nothing.
//
// The birth time is two columns rather than the one node carries, and the
// difference is SQLite's rather than a choice: a WITHOUT ROWID table enforces
// NOT NULL on every column of its primary key, so a nullable btime_ns there
// would refuse exactly the rows a filesystem with no birth time produces. The
// pair is the derivation's own flag byte, stored.
//
// A grant's principal is two nullable columns with a CHECK rather than a kind
// and an id, because a polymorphic reference cannot carry a foreign key, and a
// grant belonging to a user who no longer exists is the thing the key is for.
//
// "group" is quoted because it is a SQLite keyword. "grant" is quoted for
// symmetry with it; neither name is worth losing over a parse.
const schemaV1 = `
CREATE TABLE user (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display     TEXT,
  pw_hash     TEXT NOT NULL,
  disabled    INTEGER NOT NULL DEFAULT 0,
  role        INTEGER NOT NULL DEFAULT 0,
  quota_bytes INTEGER,
  usage_bytes INTEGER NOT NULL DEFAULT 0,
  smb_opt_out INTEGER NOT NULL DEFAULT 0,
  smb_enabled INTEGER NOT NULL DEFAULT 1,
  created_ns  INTEGER NOT NULL
);

CREATE TABLE "group" (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE membership (
  user    INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  "group" INTEGER NOT NULL REFERENCES "group"(id) ON DELETE CASCADE,
  PRIMARY KEY (user, "group")
) WITHOUT ROWID;

CREATE TABLE session (
  id_hash            BLOB PRIMARY KEY,
  user               INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  created_ns         INTEGER NOT NULL,
  last_seen_ns       INTEGER NOT NULL,
  absolute_expiry_ns INTEGER NOT NULL,
  ip_first           TEXT,
  ua_first           TEXT,
  amr                INTEGER NOT NULL
);
CREATE INDEX session_user ON session(user);

CREATE TABLE app_password (
  id             INTEGER PRIMARY KEY,
  token_hash     BLOB NOT NULL UNIQUE,
  user           INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  scope_perms    INTEGER NOT NULL,
  scope_shares   BLOB,
  created_ns     INTEGER NOT NULL,
  last_used_ns   INTEGER,
  last_ip        TEXT,
  last_ua        TEXT,
  expires_ns     INTEGER,
  wipe_requested INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX app_password_user ON app_password(user);

CREATE TABLE totp_secret (
  user       INTEGER PRIMARY KEY REFERENCES user(id) ON DELETE CASCADE,
  secret_ct  BLOB NOT NULL,
  key_ver    INTEGER NOT NULL,
  created_ns INTEGER NOT NULL
);

CREATE TABLE recovery_code (
  user      INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  code_hash BLOB NOT NULL,
  used_ns   INTEGER
);
CREATE INDEX recovery_code_user ON recovery_code(user);

CREATE TABLE oidc_link (
  issuer        TEXT NOT NULL,
  subject       TEXT NOT NULL,
  user          INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  linked_ns     INTEGER NOT NULL,
  last_login_ns INTEGER,
  PRIMARY KEY (issuer, subject)
);
CREATE UNIQUE INDEX oidc_link_user ON oidc_link(user);

CREATE TABLE "grant" (
  id         INTEGER PRIMARY KEY,
  user       INTEGER REFERENCES user(id) ON DELETE CASCADE,
  "group"    INTEGER REFERENCES "group"(id) ON DELETE CASCADE,
  share      INTEGER NOT NULL,
  subpath    TEXT NOT NULL,
  allow      INTEGER NOT NULL,
  deny       INTEGER NOT NULL,
  inherit    INTEGER NOT NULL,
  label      TEXT,
  created_ns INTEGER NOT NULL,
  CHECK ((user IS NULL) <> ("group" IS NULL))
);
CREATE INDEX grant_user ON "grant"(user);
CREATE INDEX grant_group ON "grant"("group");
CREATE INDEX grant_share ON "grant"(share);

CREATE TABLE share_link (
  id            INTEGER PRIMARY KEY,
  token_hash    BLOB NOT NULL UNIQUE,
  token_enc     BLOB,
  share         INTEGER NOT NULL,
  path          TEXT NOT NULL,
  dev           INTEGER,
  ino           INTEGER,
  btime_present INTEGER,
  btime_ns      INTEGER,
  owner         INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  perms         INTEGER NOT NULL,
  password_hash TEXT,
  expires_ns    INTEGER,
  max_downloads INTEGER,
  downloads     INTEGER NOT NULL DEFAULT 0,
  label         TEXT,
  note          TEXT,
  created_ns    INTEGER NOT NULL
);
CREATE INDEX share_link_owner ON share_link(owner);
CREATE INDEX share_link_target ON share_link(share, path);

CREATE TABLE dav_prop (
  share         INTEGER NOT NULL,
  dev           INTEGER NOT NULL,
  ino           INTEGER NOT NULL,
  btime_present INTEGER NOT NULL,
  btime_ns      INTEGER NOT NULL,
  ns            TEXT NOT NULL,
  name          TEXT NOT NULL,
  value         TEXT NOT NULL,
  PRIMARY KEY (share, dev, ino, btime_present, btime_ns, ns, name)
) WITHOUT ROWID;

CREATE TABLE dav_lock (
  token         TEXT PRIMARY KEY,
  share         INTEGER NOT NULL,
  dev           INTEGER NOT NULL,
  ino           INTEGER NOT NULL,
  btime_present INTEGER NOT NULL,
  btime_ns      INTEGER NOT NULL,
  path          TEXT NOT NULL,
  principal     INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  owner         TEXT NOT NULL,
  depth         INTEGER NOT NULL,
  scope         INTEGER NOT NULL,
  expires_ns    INTEGER NOT NULL,
  timeout_s     INTEGER NOT NULL
);
CREATE INDEX dav_lock_path ON dav_lock(share, path);
CREATE INDEX dav_lock_principal ON dav_lock(principal);

CREATE TABLE favorite (
  user          INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  share         INTEGER NOT NULL,
  dev           INTEGER NOT NULL,
  ino           INTEGER NOT NULL,
  btime_present INTEGER NOT NULL,
  btime_ns      INTEGER NOT NULL,
  PRIMARY KEY (user, share, dev, ino, btime_present, btime_ns)
) WITHOUT ROWID;

CREATE TABLE upload_session (
  id            BLOB PRIMARY KEY,
  user          INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  share         INTEGER NOT NULL,
  dest          TEXT NOT NULL,
  part_name     TEXT NOT NULL,
  spool_dir     TEXT,
  mode          INTEGER NOT NULL,
  total_len     INTEGER,
  chunk_size    INTEGER NOT NULL,
  random_access INTEGER NOT NULL,
  next_name     INTEGER NOT NULL DEFAULT 1,
  write_head    INTEGER NOT NULL DEFAULT 0,
  spooled_names BLOB NOT NULL DEFAULT (X''),
  if_match      TEXT,
  filename      TEXT NOT NULL,
  mtime_ns      INTEGER,
  mime          TEXT,
  relative_path TEXT,
  verify        INTEGER,
  verify_digest BLOB,
  created_ns    INTEGER NOT NULL,
  expires_ns    INTEGER NOT NULL,
  state         INTEGER NOT NULL
);
CREATE INDEX upload_session_user ON upload_session(user);
CREATE INDEX upload_session_expires ON upload_session(expires_ns);

CREATE TABLE upload_interval (
  session BLOB NOT NULL REFERENCES upload_session(id) ON DELETE CASCADE,
  lo      INTEGER NOT NULL,
  hi      INTEGER NOT NULL,
  PRIMARY KEY (session, lo)
) WITHOUT ROWID;

CREATE TABLE settings (
  id   INTEGER PRIMARY KEY CHECK (id = 1),
  json TEXT NOT NULL
);

CREATE TABLE audit (
  ts_ns  INTEGER NOT NULL,
  actor  INTEGER REFERENCES user(id) ON DELETE SET NULL,
  event  TEXT NOT NULL,
  target TEXT,
  ip     TEXT,
  ua     TEXT,
  result INTEGER NOT NULL,
  detail TEXT
);
CREATE INDEX audit_ts ON audit(ts_ns);
CREATE INDEX audit_actor ON audit(actor, ts_ns);

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
