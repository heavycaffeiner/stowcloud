package state

import "github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"

// Schema history alone; no aggregate keeps its working statements here. Each
// table stores something the filesystem cannot regenerate, which is what makes
// this file the data backup.
//
// The shapes follow whatever the current build wrote. Any existing state.db must
// open at its recorded version, so a released step is preserved verbatim and
// never modified; corrections arrive as newly appended steps.
//
// Dead properties, locks and favorites key on the identity tuple rather than a
// node id. Keying on an id that only the cache generates is what once forced the
// cache to hold a pin declaring that something durable referenced the row. An
// identity is a property of the file itself, so discarding the cache costs a
// lookup instead of leaving rows pointing nowhere.
//
// Birth time occupies two columns rather than the single one node uses, and that
// difference comes from SQLite rather than preference: a WITHOUT ROWID table
// imposes NOT NULL across every primary key column, so a nullable btime_ns there
// would reject exactly the rows produced by a filesystem lacking birth times.
// The pair stores the derivation's own flag byte.
//
// A grant's principal uses two nullable columns guarded by a CHECK instead of a
// kind and id pair, because a polymorphic reference cannot carry a foreign key,
// and a grant referencing a deleted user is precisely what that key prevents.
//
// "group" is quoted as a SQLite keyword, and "grant" is quoted to match.
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

// Step 2 gives share links a single coherent target representation and joins the
// encrypted token to the key version that sealed it.
//
// In version 1 all four identity columns were nullable with no constraint across
// any combination, leaving path-only, path-plus-identity and partial tuples
// indistinguishable on disk. Links are the one durable row that must not follow
// a rename: they stat the stored path and demand the stored identity match, and
// a link unable to separate the original inode from one reused after a delete
// cannot honour that.
//
// Rows are preserved here. Nothing exists to rebuild this database from, so any
// row this step cannot represent halts the migration instead of being dropped or
// weakened, and the precondition identifies which row.
const schemaV2 = `
CREATE TABLE share_link_next (
  id            INTEGER PRIMARY KEY,
  token_hash    BLOB NOT NULL UNIQUE,
  token_enc     BLOB,
  token_key_ver INTEGER,
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
  created_ns    INTEGER NOT NULL,
  CHECK (
    (dev IS NULL AND ino IS NULL AND btime_present IS NULL AND btime_ns IS NULL)
    OR (dev IS NOT NULL AND ino IS NOT NULL AND btime_ns IS NOT NULL
        AND btime_present = 1 AND NOT (dev = 0 AND ino = 0))
  ),
  CHECK (
    (token_enc IS NULL AND token_key_ver IS NULL)
    OR (token_enc IS NOT NULL AND token_key_ver IS NOT NULL AND token_key_ver >= 0)
  )
);

INSERT INTO share_link_next(id, token_hash, token_enc, token_key_ver, share, path,
                            dev, ino, btime_present, btime_ns,
                            owner, perms, password_hash, expires_ns, max_downloads,
                            downloads, label, note, created_ns)
SELECT id, token_hash, token_enc,
       CASE WHEN token_enc IS NULL THEN NULL ELSE 0 END,
       share, path, dev, ino, btime_present, btime_ns,
       owner, perms, password_hash, expires_ns, max_downloads,
       downloads, label, note, created_ns
FROM share_link;

DROP TABLE share_link;
ALTER TABLE share_link_next RENAME TO share_link;
CREATE INDEX share_link_owner ON share_link(owner);
CREATE INDEX share_link_target ON share_link(share, path);
`

// Step 3 lands the durable auth state: the singleton key version named by
// every sealed ciphertext, the encrypted-at-rest SMB NT hash, and the TOTP
// replay-guard steps still inside the accepted window. These are empty
// tables; the re-seal that gives them meaning needs the master key and is no
// migration's business.
const schemaV3 = `
CREATE TABLE key_version (
  id  INTEGER PRIMARY KEY CHECK (id = 1),
  ver INTEGER NOT NULL
);

CREATE TABLE user_smb_secret (
  user       INTEGER PRIMARY KEY REFERENCES user(id) ON DELETE CASCADE,
  nt_hash_ct BLOB NOT NULL,
  key_ver    INTEGER NOT NULL
);

CREATE TABLE totp_used (
  user    INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  step    INTEGER NOT NULL,
  used_ns INTEGER NOT NULL,
  PRIMARY KEY (user, step)
) WITHOUT ROWID;
CREATE INDEX totp_used_user ON totp_used(user);
`

// Step 4 lands the persisted share registry and the operation store: the
// bounded, restart-visible history of long operations. Neither is
// reconstructible from the filesystem.
const schemaV4 = `
CREATE TABLE share_definition (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  host_path  TEXT NOT NULL,
  created_ns INTEGER NOT NULL
);

CREATE TABLE share_identity_override (
  share_id  INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  host_path TEXT NOT NULL
);

CREATE TABLE share_trash_override (
  share_id INTEGER PRIMARY KEY,
  enabled  INTEGER NOT NULL
);

CREATE TABLE operation (
  id           INTEGER PRIMARY KEY,
  user         INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  kind         INTEGER NOT NULL,
  state        INTEGER NOT NULL,
  progress     INTEGER NOT NULL DEFAULT 0,
  total        INTEGER NOT NULL DEFAULT 0,
  message      TEXT,
  cancellation INTEGER NOT NULL DEFAULT 0,
  created_ns   INTEGER NOT NULL,
  finished_ns  INTEGER
);
CREATE INDEX operation_user ON operation(user);

CREATE TABLE operation_result (
  operation INTEGER NOT NULL REFERENCES operation(id) ON DELETE CASCADE,
  idx       INTEGER NOT NULL,
  path      TEXT NOT NULL,
  ok        INTEGER NOT NULL,
  reason    INTEGER,
  text      TEXT,
  PRIMARY KEY (operation, idx)
) WITHOUT ROWID;
`

// Step 5 lands the three upload tables beside the session rows step 1
// created: the transfer-id alias, the persisted chunk floor and default, and
// the set of directories a part file has ever been created in.
//
// The touched-directory set outlives the sessions that added to it, which is
// the point of it being a table rather than a query over the sessions: the
// orphan the sweep exists for is a part file whose session row is gone.
//
// The alias is what makes a named chunk collection resumable after a
// restart. Its primary key is scoped by the account, and that is the whole
// of its security: the transfer id is chosen by the client, so it is
// guessable and collidable and can never be a session key on its own.
//
// The chunk settings are a single row, and the row's absence is the fact
// that matters: it is what tells the settings screen "this fell back to the
// compiled-in defaults" rather than "an admin stored these numbers".
const schemaV5 = `
CREATE TABLE upload_alias (
  tid        TEXT NOT NULL,
  user       INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  session    BLOB NOT NULL REFERENCES upload_session(id) ON DELETE CASCADE,
  share      INTEGER NOT NULL,
  dest       TEXT NOT NULL,
  created_ns INTEGER NOT NULL,
  PRIMARY KEY (user, tid)
) WITHOUT ROWID;
CREATE INDEX upload_alias_session ON upload_alias(session);

CREATE TABLE upload_chunk_settings (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  chunk_min     INTEGER NOT NULL,
  chunk_default INTEGER NOT NULL
);

CREATE TABLE upload_touched_dir (
  share INTEGER NOT NULL,
  dir   TEXT NOT NULL,
  PRIMARY KEY (share, dir)
) WITHOUT ROWID;

ALTER TABLE upload_session ADD COLUMN chunk_min_at_creation INTEGER NOT NULL DEFAULT 0;
`

// Step 6 lands what the compatibility layer owns durably. Both rows are
// durable for the same reason: a client that saw one answer and then a
// different one treats the server as a different server.
//
// The favorite gains a path column, because a client asks for a list of
// starred files and wants their paths; the identity tuple stays the key, so
// a star follows the file through a rename and the path is what was last
// seen rather than what identifies the row.
const schemaV6 = `
CREATE TABLE compat_kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) WITHOUT ROWID;

ALTER TABLE favorite ADD COLUMN path TEXT NOT NULL DEFAULT '';

CREATE TABLE compat_login_flow (
  poll_digest    BLOB PRIMARY KEY,
  login_digest   BLOB NOT NULL,
  created_ns     INTEGER NOT NULL,
  approved_user  INTEGER,
  approved_login TEXT NOT NULL DEFAULT '',
  last_poll_ns   INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
CREATE UNIQUE INDEX compat_login_flow_login ON compat_login_flow(login_digest);

CREATE TABLE compat_upload_alias (
  user    INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  tid     TEXT NOT NULL,
  session BLOB NOT NULL,
  PRIMARY KEY (user, tid)
) WITHOUT ROWID;
`

// Step 7 carries a link flow across the outbound redirect and the returning
// callback.
//
// State and binding are stored solely as digests. Both reach the browser, so
// keeping them intact would make a read of this table sufficient to complete
// someone else's link, and the callback tests equality rather than the value
// itself. The verifier and nonce are stored intact because each must be
// transmitted and neither authenticates anything alone.
const schemaV7 = `
CREATE TABLE oidc_flow (
  state_digest    BLOB PRIMARY KEY,
  user            INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  nonce           TEXT NOT NULL,
  binding_digest  BLOB NOT NULL,
  code_verifier   TEXT NOT NULL,
  redirect_uri    TEXT NOT NULL,
  return_to       TEXT NOT NULL DEFAULT '',
  created_ns      INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX oidc_flow_created ON oidc_flow(created_ns);
`

// Step 8 records what an operation was asked to do, item by item, when it is
// created rather than when it finishes. Without it a job that stopped short
// could say how many items it did not reach and never which ones.
//
// The per-item state is derived rather than stored: an item with a result is
// settled, one marked started without a result was in flight, and one that
// is neither was never reached. A stored status would be a second answer to
// the same question that can disagree with the first.
const schemaV8 = `
CREATE TABLE operation_item (
  operation INTEGER NOT NULL REFERENCES operation(id) ON DELETE CASCADE,
  idx       INTEGER NOT NULL,
  path      TEXT NOT NULL,
  started   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (operation, idx)
) WITHOUT ROWID;
`

// Step 9 introduces the upload cache spool: its switch plus the two columns a
// session requires to survive a restart while chunks remain cached.
//
// cache_merged counts bytes already copied into the part file and made durable
// there. It marks the frontier a restart resumes merging from, and is why a
// cache backed by tmpfs cannot lose an acknowledged byte.
const schemaV9 = `
CREATE TABLE upload_cache_settings (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  enabled INTEGER NOT NULL
);

ALTER TABLE upload_session ADD COLUMN cache_dir TEXT;
ALTER TABLE upload_session ADD COLUMN cache_merged INTEGER NOT NULL DEFAULT 0;
`

// Step 10 folds every share into one table. There used to be two kinds: a
// share the config file declared, whose id came from its position in that
// file and whose edits went into two override tables, and a share created
// from the screen, which owned a row. With the config file gone there is one
// kind, so the override properties become columns.
//
// The trash toggle is carried across rather than reset: dropping the table
// without moving it would silently turn trash off on a deployment that had
// turned it on. The identity overrides are not carried, because the rows
// they edited were the config file's and there is no config file to edit.
const schemaV10 = `
ALTER TABLE share_definition ADD COLUMN shared_externally INTEGER NOT NULL DEFAULT 0;
ALTER TABLE share_definition ADD COLUMN trash_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE share_definition ADD COLUMN symlink_policy TEXT NOT NULL DEFAULT 'deny';

UPDATE share_definition SET trash_enabled = coalesce(
  (SELECT enabled FROM share_trash_override WHERE share_id = share_definition.id + 1000000), 0);

DROP TABLE share_identity_override;
DROP TABLE share_trash_override;
`

// Step 11 holds the settings that are credentials, separately from the
// settings document because that document is read whole by everything that
// reads any setting and is rendered to the settings screen. A credential in
// it would be a credential in every one of those.
const schemaV11 = `
CREATE TABLE config_secret (
  name    TEXT PRIMARY KEY,
  value   BLOB NOT NULL,
  key_ver INTEGER NOT NULL
) WITHOUT ROWID;
`

// Step 12 makes redelivery real.
//
// The old shape minted an app password and deleted the flow before knowing the
// client had received it, so a dropped response left a credential live that
// nobody had learned. The result is now sealed under the master key and kept
// until the flow expires, which is what lets the same poll token collect the
// same credential instead of minting a second one.
//
// The sealed bytes carry the key version they were sealed under, so a rotation
// does not strand a flow that is already deliverable. delivered_ns records
// that a response was written; the ciphertext stays until sweep either way,
// because a connection lost after the write is exactly the case this exists
// for.
const schemaV12 = `
ALTER TABLE compat_login_flow ADD COLUMN claimed_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE compat_login_flow ADD COLUMN sealed_result BLOB;
ALTER TABLE compat_login_flow ADD COLUMN sealed_key_ver INTEGER NOT NULL DEFAULT 0;
ALTER TABLE compat_login_flow ADD COLUMN credential_id INTEGER;
ALTER TABLE compat_login_flow ADD COLUMN delivered_ns INTEGER NOT NULL DEFAULT 0;
`

// Step 13 refuses a second grant naming the same subject over the same share
// and subpath with the same reach, rather than letting it insert silently.
// Before this step a duplicate landed as a second row indistinguishable from
// the first except by id, and the only symptom was an operator's retry or a
// client's double submit quietly doubling the rows.
//
// The expression substitutes -1 for a NULL principal column because SQLite
// treats NULL as distinct from every other NULL in a unique index, and a
// second grant naming the same group would otherwise still collide against
// nothing: -1 is never a real row id, so both nullable columns still narrow
// to one comparable value.
//
// inherit is part of the key because the two states are different grants, not
// two spellings of one. "This folder" and "this folder and everything under
// it" cannot be folded into a single row, and the evaluator reads the flag to
// decide which of them covers a descendant.
//
// The rows already stored are folded first, by foldDuplicateGrants. Creating
// the index against a database that holds duplicates fails, and the failure
// is a server that will not start over rows the server itself accepted.
const schemaV13 = `
CREATE UNIQUE INDEX grant_subject_target ON "grant"(
  coalesce(user, -1), coalesce("group", -1), share, subpath, inherit
);
`

// Step 14 gives a share a backend other than a local directory. The
// default folds every existing row onto BackendLocal, since that is the
// only kind a row written before this step could ever have named. The
// config is a string rather than a blob because it is JSON a screen may
// one day want to read directly; the secret stays a blob because it never
// is anything but sealed bytes.
const schemaV14 = `
ALTER TABLE share_definition ADD COLUMN backend TEXT NOT NULL DEFAULT 'local';
ALTER TABLE share_definition ADD COLUMN backend_config TEXT NOT NULL DEFAULT '';
ALTER TABLE share_definition ADD COLUMN backend_secret BLOB;
ALTER TABLE share_definition ADD COLUMN backend_secret_keyver INTEGER NOT NULL DEFAULT 0;
`

// migrations is a function instead of a package-level slice so nothing can
// reassign the list. Position determines version, so a released step is never
// modified, renumbered or moved.
func migrations() []dbfile.Migration {
	return []dbfile.Migration{
		{Name: "1: the durable tables", SQL: schemaV1},
		{
			Name:         "2: one share-link target representation",
			SQL:          schemaV2,
			Precondition: checkShareLinkTargets,
		},
		{Name: "3: the durable auth state", SQL: schemaV3},
		{Name: "4: the share registry and operation store", SQL: schemaV4},
		{Name: "5: upload aliases and the persisted chunk settings", SQL: schemaV5},
		{Name: "6: the compat layer's durable rows", SQL: schemaV6},
		{Name: "7: the single-sign-on link flow", SQL: schemaV7},
		{Name: "8: the paths an operation was asked for", SQL: schemaV8},
		{Name: "9: the upload cache spool switch", SQL: schemaV9},
		{Name: "10: one kind of share", SQL: schemaV10},
		{Name: "11: configuration secrets at rest", SQL: schemaV11},
		{Name: "12: login flow delivery state", SQL: schemaV12},
		{
			Name:         "13: one grant per subject, share, subpath and reach",
			SQL:          schemaV13,
			Precondition: foldDuplicateGrants,
		},
		{Name: "14: share backends", SQL: schemaV14},
	}
}
