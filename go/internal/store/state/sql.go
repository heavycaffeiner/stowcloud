package state

import "github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"

// The schema as it first shipped. Every table here holds something the
// filesystem cannot regenerate, which is what makes this file the data backup.
//
// Three tables key by the identity tuple rather than by a node id: dead
// properties, locks and favorites. Keying by an id only the cache mints is what
// forced the cache to carry a pin saying "do not reap this, something durable
// points at it". An identity is a fact about the file, so deleting the cache now
// costs a lookup rather than leaving a row pointing at nothing.
//
// A share link is the fourth durable row that names a file and the one with a
// different contract: it keeps a path and an optional identity, and it does not
// follow a rename. Migration 2 below is what makes that representation coherent;
// the identity columns here admit combinations that cannot be told apart.
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

// Migration 2 gives a share link one coherent target representation and pairs
// the encrypted token with the key version that sealed it.
// Version 1 made all four identity columns nullable and constrained no
// combination of them, so "path only", "path plus identity" and "the importer
// could not work it out" were the same shape on disk. A link is the one durable
// row that must not follow a rename: it stats the stored path and requires the
// stored identity to match, and a link that cannot tell the original inode from
// one reused after a delete cannot keep that contract. So there are two
// representations and no third.
//
// (dev, ino) not both zero, and btime_present pinned to 1, are what refuse the
// tuple the Phase 2 importer fabricated. A birth time is what distinguishes the
// original file from a replacement at the same inode number.
//
// This one preserves rows. Unlike the cache there is nothing to rebuild it
// from, so a row it cannot represent stops the migration rather than being
// dropped or weakened; the precondition is what names which row.
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

// migration 3 lands the durable auth state the auth package owns: the
// singleton key version named by every sealed ciphertext, the encrypted-at-rest
// SMB NT hash, and the TOTP replay-guard steps still inside the accepted
// window. These are empty tables produced by migration; the re-seal that gives
// them meaning is the auth package's startup step, because it needs the master
// key and a password hash is no SQL migration's business.
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

// migration 4 lands the Phase 4 durable tables: the persisted share registry
// (admin-created shares and the editable properties of config-defined ones,
// which replace the older shares.db) and the operation store (the bounded,
// restart-visible history of long operations, replacing jobs.db). None of it
// is reconstructible from the filesystem, which is what makes it the data
// backup: deleting cache.db rebuilds; deleting these rows does not.
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

// migration 5 lands the three upload tables the engine needs beside the
// session rows migration 1 already created: the transfer-id alias, the
// persisted chunk floor and default, and the set of directories a part file
// has ever been created in.
//
// The touched-directory set outlives the sessions that added to it, and that
// is the point of it being a table rather than a query over the sessions. The
// orphan the sweep exists for is a part file whose session row is gone, so the
// rows cannot be what says where to look; without this the sweep can only find
// debt it already had a record of, which is the half that was never the
// problem. It is never deleted from, because a directory that held one part
// file will hold another.
//
// The alias is what makes a named chunk collection resumable after a restart.
// Its primary key is scoped by the account, and that is the whole of its
// security: the transfer id is chosen by the client, so it is guessable and
// collidable and can never be a session key on its own. A tid belonging to
// another account has to read as "not found", identically to one that never
// existed, or the lookup is an existence oracle.
//
// The chunk settings are a single row by the same convention as the settings
// table, and the row's absence is the fact that matters: it is what tells the
// settings screen "this fell back to the config file" rather than "an admin
// stored these numbers". Collapsing the two makes both the same pair of
// integers and the screen has nothing left to report.
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

// migrations is a function rather than a package-level slice so the list
// cannot be reassigned. Position is version, so a step that has shipped is
// never edited, renumbered or reordered.
func migrations() []dbfile.Migration {
	return []dbfile.Migration{
		{Name: "1: fileid_override", SQL: schemaV1},
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
	}
}

// schemaV11 holds the settings that are credentials.
//
// Separate from the settings document because that document is read whole by
// everything that reads any setting, and rendered to the settings screen. A
// credential in it would be a credential in every one of those. These rows are
// sealed under the master key and read only by the wiring that needs the
// plaintext.
const schemaV11 = `
CREATE TABLE config_secret (
  name    TEXT PRIMARY KEY,
  value   BLOB NOT NULL,
  key_ver INTEGER NOT NULL
) WITHOUT ROWID;
`

// schemaV10 folds every share into one table.
//
// There used to be two kinds. A share the config file declared took its id
// from its position in that file and could not be deleted, and an admin's
// edits to it went into two override tables that registration read back on
// top of the file. A share created from the screen owned a row and needed
// none of that. With the config file gone there is one kind, so the
// properties that lived in the overrides become columns and the override
// tables go.
//
// The trash toggle is carried across rather than reset: it applies to shares
// an administrator created, and dropping the table without moving it would
// silently turn trash off on a deployment that had turned it on. The identity
// overrides are not carried, because the rows they edited were the config
// file's and there is no config file to edit.
const schemaV10 = `
ALTER TABLE share_definition ADD COLUMN shared_externally INTEGER NOT NULL DEFAULT 0;
ALTER TABLE share_definition ADD COLUMN trash_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE share_definition ADD COLUMN symlink_policy TEXT NOT NULL DEFAULT 'deny';

UPDATE share_definition SET trash_enabled = coalesce(
  (SELECT enabled FROM share_trash_override WHERE share_id = share_definition.id + 1000000), 0);

DROP TABLE share_identity_override;
DROP TABLE share_trash_override;
`

// schemaV9 lands the upload cache spool: its switch, and the two columns a
// session needs to survive a restart with chunks still in the cache.
//
// The switch is its own table rather than a column on upload_chunk_settings,
// because that table's row existing is itself a fact the settings screen
// reports: it is what separates "an admin stored these numbers" from "these
// fell back to the compiled-in defaults". Turning the cache on would create
// the row and make the screen claim an override of a floor nobody wrote.
//
// cache_merged is the byte count already copied into the part file and made
// durable there. It is the frontier a restart resumes merging from, and it is
// the reason a cache on tmpfs cannot lose an acknowledged byte: everything
// below it is in the destination, and everything above it is re-derived from
// the cache files that are actually still on disk.
const schemaV9 = `
CREATE TABLE upload_cache_settings (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  enabled INTEGER NOT NULL
);

ALTER TABLE upload_session ADD COLUMN cache_dir TEXT;
ALTER TABLE upload_session ADD COLUMN cache_merged INTEGER NOT NULL DEFAULT 0;
`

// schemaV8 records what an operation was asked to do, item by item, when it is
// created rather than when it finishes.
//
// Without it a job that stopped short could say how many items it did not
// reach and never which ones. A process killed mid-copy leaves rows here with
// no matching result, which is exactly the list the tray needs: what was in
// flight when it died, and what it never started. Both are unknowable from the
// result table, because a result is written only once an item is done with.
//
// The state is derived rather than stored: an item with a result is settled,
// one marked started without a result was in flight, and one that is neither
// was never reached. Storing a per-item status as well would be a second
// answer to the same question that can disagree with the first.
const schemaV8 = `
CREATE TABLE operation_item (
  operation INTEGER NOT NULL REFERENCES operation(id) ON DELETE CASCADE,
  idx       INTEGER NOT NULL,
  path      TEXT NOT NULL,
  started   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (operation, idx)
) WITHOUT ROWID;
`

// schemaV7 holds a link flow between the redirect out and the callback back.
//
// The state and the binding rest only as digests. Both are handed to the
// browser, so storing them would mean a read of this table is enough to
// complete somebody else's link, and what the callback checks is equality
// rather than the value.
//
// The verifier is the exception and is stored whole: the exchange has to send
// it, and its only counterpart is the challenge already published to the
// provider, so it authenticates nothing on its own.
const schemaV7 = `
CREATE TABLE oidc_flow (
  state_digest    BLOB PRIMARY KEY,
  user            INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  -- The nonce is stored whole, unlike the two above. It has to be handed to
  -- the token verifier, which checks it beside the issuer, the audience and
  -- the validity window: a nonce checked separately is a check that can be
  -- forgotten, and this one is what ties the token to this attempt.
  --
  -- It authenticates nothing on its own. The provider echoes it back in a
  -- token it signed, so holding the value is not holding a credential.
  nonce           TEXT NOT NULL,
  binding_digest  BLOB NOT NULL,
  code_verifier   TEXT NOT NULL,
  redirect_uri    TEXT NOT NULL,
  return_to       TEXT NOT NULL DEFAULT '',
  created_ns      INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX oidc_flow_created ON oidc_flow(created_ns);
`

// migration 6 lands what the compatibility layer owns durably.
//
// Two things, and both are durable for the same reason: a client that saw one
// answer and then a different one treats the server as a different server.
//
// The instance identity is minted once and never regenerated. The favourite
// gains a path column, because a client asks for a list of starred files and
// wants their paths; the identity tuple stays the key, so a star follows the
// file through a rename and the path is what was last seen rather than what
// identifies the row.
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
  -- Set once a human has approved, and the whole of what approval stores. No
  -- credential rests here: the polling request mints one at delivery, so an
  -- abandoned flow leaves nothing live behind.
  approved_user  INTEGER,
  approved_login TEXT NOT NULL DEFAULT '',
  last_poll_ns   INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
CREATE UNIQUE INDEX compat_login_flow_login ON compat_login_flow(login_digest);

CREATE TABLE compat_upload_alias (
  user    INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  -- The transfer id is client-chosen and is never a session key: it is
  -- guessable and collidable, so it is scoped by user and resolves through
  -- here rather than naming a session directly.
  tid     TEXT NOT NULL,
  session BLOB NOT NULL,
  PRIMARY KEY (user, tid)
) WITHOUT ROWID;
`

// Every statement, as a constant. Nothing here is assembled from parts.
const (
	sqlReadFileIDOverride = `
SELECT id FROM fileid_override
WHERE share = ? AND dev = ? AND ino = ? AND btime_present = ? AND btime_ns = ?`

	sqlReadFileIDOverrideOwner = `
SELECT share, dev, ino, btime_present, btime_ns FROM fileid_override WHERE id = ?`

	sqlWriteFileIDOverride = `
INSERT INTO fileid_override(share, dev, ino, btime_present, btime_ns, id)
VALUES (?, ?, ?, ?, ?, ?)`

	sqlCountFileIDOverrides = `SELECT count(*) FROM fileid_override`

	sqlReadShareLinkTargets = `SELECT id, dev, ino, btime_present, btime_ns FROM share_link`
)

// The administrator's stored overrides, as one document. See settings.go for
// why it is one document rather than a column per setting.
const (
	sqlReadSettings = `SELECT json FROM settings WHERE id = 1`

	sqlWriteSettings = `
INSERT INTO settings(id, json) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET json = excluded.json`
)
