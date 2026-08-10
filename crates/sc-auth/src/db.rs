use anyhow::{Context, Result};
use r2d2::Pool;
use r2d2_sqlite::SqliteConnectionManager;
use std::path::Path;

pub(crate) type DbPool = Pool<SqliteConnectionManager>;

const SCHEMA_SQL: &str = r#"
CREATE TABLE IF NOT EXISTS user (
  id INTEGER PRIMARY KEY,
  name TEXT UNIQUE NOT NULL COLLATE NOCASE,
  display TEXT,
  pw_hash TEXT NOT NULL,
  totp_secret BLOB,
  disabled INTEGER NOT NULL DEFAULT 0,
  quota_bytes INTEGER,
  -- Running total of bytes this user's writes have added minus what their
  -- deletes have freed (`sc_core::quota`'s module doc) —
  -- not a live filesystem recomputation.
  usage_bytes INTEGER NOT NULL DEFAULT 0,
  created_ns INTEGER NOT NULL,
  smb_opt_out INTEGER NOT NULL DEFAULT 0,
  smb_enabled INTEGER NOT NULL DEFAULT 1,
  -- 0 = ordinary user, 1 = administrator. Replaces the earlier `id == 1`
  -- stand-in (see `AuthService::create_user`/`set_admin` in `users.rs`).
  role INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS group_ (
  id INTEGER PRIMARY KEY,
  name TEXT UNIQUE NOT NULL
);

CREATE TABLE IF NOT EXISTS membership (
  user INTEGER NOT NULL,
  group_ INTEGER NOT NULL,
  PRIMARY KEY (user, group_)
);

CREATE TABLE IF NOT EXISTS recovery_code (
  user INTEGER NOT NULL,
  code_hash BLOB NOT NULL,
  used_ns INTEGER
);
CREATE INDEX IF NOT EXISTS recovery_code_user ON recovery_code(user);

CREATE TABLE IF NOT EXISTS totp_used (
  user INTEGER NOT NULL,
  time_step INTEGER NOT NULL,
  PRIMARY KEY (user, time_step)
);

CREATE TABLE IF NOT EXISTS login_challenge (
  token_hash BLOB PRIMARY KEY,
  user INTEGER NOT NULL,
  expires_ns INTEGER NOT NULL,
  amr INTEGER NOT NULL
);

-- Separate table by design: ordinary user-lookup queries never touch it, so
-- there is no structural way for NT hashes to leak into admin API responses.
CREATE TABLE IF NOT EXISTS user_smb_secret (
  user INTEGER PRIMARY KEY,
  nt_hash_ct BLOB NOT NULL,   -- nonce(24) || XChaCha20-Poly1305 ciphertext
  key_ver INTEGER NOT NULL,
  source INTEGER NOT NULL,   -- NtSource: 0 = AccountPassword, 1 = Dedicated
  updated_ns INTEGER NOT NULL
);

-- Single-row counter: the `key_ver` every not-yet-rotated `user_smb_secret`/
-- `user.totp_secret` row is sealed under. Living in the same database as the
-- rows it describes (rather than a sidecar file) is what lets
-- `AuthService::rotate_master_key` bump it in the same transaction that
-- re-seals those rows — one commit, never two things that could disagree
-- after a crash.
CREATE TABLE IF NOT EXISTS key_version (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  ver INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS session (
  id_hash BLOB PRIMARY KEY,   -- sha256(token); plaintext token never stored
  user INTEGER NOT NULL,
  created_ns INTEGER NOT NULL,
  last_seen_ns INTEGER NOT NULL,
  absolute_expiry_ns INTEGER NOT NULL,
  ip_first TEXT,
  ua_first TEXT,
  amr INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS session_user ON session(user);

CREATE TABLE IF NOT EXISTS app_password (
  id INTEGER PRIMARY KEY,
  token_hash BLOB UNIQUE NOT NULL,
  user INTEGER NOT NULL,
  name TEXT NOT NULL,
  scope_perms INTEGER NOT NULL,   -- 0xFFFF sentinel = unrestricted
  scope_shares BLOB,              -- NULL = all shares; else JSON array of ids
  created_ns INTEGER NOT NULL,
  last_used_ns INTEGER,
  last_ip TEXT,
  last_ua TEXT,
  expires_ns INTEGER,
  -- Set when an operator marks this credential's device as lost. The
  -- credential keeps working until the device confirms it has erased its
  -- local copies, because a credential revoked outright can no longer ask
  -- whether it should wipe.
  wipe_requested INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS app_password_user ON app_password(user);

-- A local account's link to one identity at the configured IdP
-- (`docs/proposals/stowcloud-0-oidc-login.md` §4.2). `subject` is the ID
-- token's `sub`, the only identifier an IdP promises is immutable; `email`
-- and `preferred_username` are deliberately neither read nor stored (§3.2).
CREATE TABLE IF NOT EXISTS oidc_identity (
  issuer     TEXT    NOT NULL,       -- must equal the configured issuer exactly
  subject    TEXT    NOT NULL,
  user       INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  linked_ns  INTEGER NOT NULL,
  last_login_ns INTEGER,             -- display only; never an authentication condition
  PRIMARY KEY (issuer, subject)
);
-- One identity per account. Two subjects pointing at one account makes
-- "which one do I cut to cut off access" unanswerable. `issuer` sits in the
-- primary key so a row can move to a different provider without a schema
-- change; it does not open multi-provider support, which would have to drop
-- this index first.
CREATE UNIQUE INDEX IF NOT EXISTS oidc_identity_user ON oidc_identity(user);

-- One in-flight OIDC authorization-code round trip
-- (`docs/proposals/stowcloud-0-oidc-login.md` §4.2). Same nature as
-- `login_challenge` above: a server-side, single-use record with a short TTL,
-- created before the user leaves for the IdP and consumed when they come
-- back.
--
-- Nothing here is a bearer credential on its own. `state` and the flow cookie
-- are stored only as `sha256`, and `code_verifier` -- the one plaintext
-- column -- cannot exchange anything without the `code` the IdP hands the
-- browser and the client secret this server holds. It is plaintext because
-- PKCE requires submitting the verifier itself to the IdP, so a hash would be
-- unusable (§4.2).
CREATE TABLE IF NOT EXISTS oidc_flow (
  state_hash    BLOB PRIMARY KEY,   -- sha256(state)
  -- sha256 of the `__Host-sc_oidc` cookie value. Without this column `state`
  -- does not bind the flow to the browser that started it, and login-CSRF
  -- stays open (§4.3.1, RFC 9700).
  binding_hash  BLOB NOT NULL,
  nonce_hash    BLOB NOT NULL,      -- sha256(nonce), checked against the ID token's claim
  code_verifier TEXT NOT NULL,      -- PKCE; see above for why it is not hashed
  mode          INTEGER NOT NULL,   -- 0 = login, 1 = link
  link_user     INTEGER,            -- NOT NULL only when mode = 1
  return_to     TEXT,               -- validated same-origin path; NULL = the mode's default
  created_ns    INTEGER NOT NULL,
  expires_ns    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS oidc_flow_expiry ON oidc_flow(expires_ns);

CREATE TABLE IF NOT EXISTS audit (
  ts_ns INTEGER NOT NULL,
  actor INTEGER,
  event TEXT NOT NULL,
  target TEXT,
  ip TEXT,
  ua TEXT,
  result INTEGER NOT NULL,
  detail TEXT
);
CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts_ns);
CREATE INDEX IF NOT EXISTS audit_actor ON audit(actor, ts_ns);
"#;

pub(crate) fn open_pool(path: &Path) -> Result<DbPool> {
    if let Some(parent) = path.parent() {
        if !parent.as_os_str().is_empty() {
            std::fs::create_dir_all(parent).ok();
        }
    }
    // Switch the journal mode **once**, on a connection of our own, before the
    // pool exists.
    //
    // `Pool::builder().build()` eagerly opens `min_idle` connections — which
    // defaults to `max_size`, so eight at once — and runs `with_init` on each.
    // Setting `journal_mode=WAL` needs an exclusive lock, so on a database
    // being created for the first time those eight raced and one lost with
    // `database is locked`. The pragma that would have made it wait,
    // `busy_timeout`, was the *third* statement in the same batch: it had not
    // taken effect yet when the contended one ran.
    //
    // This is what `sc-meta` and `sc-core::LinkStore` already do. No unit test
    // reaches it — they open a connection at a time, so nothing contends —
    // which is why it surfaced only in `scripts/smoke.sh`, against the real
    // assembled binary.
    let bootstrap = rusqlite::Connection::open(path).context("opening sqlite")?;
    bootstrap
        .execute_batch("PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;")
        .context("setting journal mode")?;
    drop(bootstrap);

    // Per-connection settings only. `busy_timeout` leads so that it governs
    // everything after it, here and in any pragma added later.
    let manager = SqliteConnectionManager::file(path).with_init(|c| {
        c.execute_batch("PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL;")
    });
    let pool = Pool::builder()
        .max_size(8)
        .build(manager)
        .context("building sqlite pool")?;
    {
        let conn = pool.get().context("getting init connection")?;
        conn.execute_batch(SCHEMA_SQL).context("running schema")?;
        migrate_user_role(&conn).context("migrating user.role column")?;
        migrate_user_usage_bytes(&conn).context("migrating user.usage_bytes column")?;
        migrate_app_password_wipe(&conn).context("migrating app_password.wipe_requested column")?;
    }
    Ok(pool)
}

/// Databases created before the `role` column existed (every deployment
/// before the admin-role model landed) don't get it from `SCHEMA_SQL` above —
/// `CREATE TABLE IF NOT EXISTS` is a no-op against an existing table, columns
/// and all. SQLite's `ALTER TABLE ADD COLUMN` has no `IF NOT EXISTS` of its
/// own, so check `pragma_table_info` first; re-running this against a
/// database that already has the column (every fresh install, and every
/// restart after the first migration) is then just a cheap read.
fn migrate_user_role(conn: &rusqlite::Connection) -> Result<()> {
    let has_role: i64 = conn.query_row(
        "SELECT COUNT(*) FROM pragma_table_info('user') WHERE name = 'role'",
        [],
        |r| r.get(0),
    )?;
    if has_role == 0 {
        conn.execute_batch("ALTER TABLE user ADD COLUMN role INTEGER NOT NULL DEFAULT 0")
            .context("adding user.role column")?;

        // Carry the old rule forward. Before this column, "administrator"
        // *was* `id == 1` — so adding the column with `DEFAULT 0` and stopping
        // there does not introduce a role model, it silently removes the
        // administrator from every existing deployment. And it is not
        // recoverable from inside the product: promotion is an admin-only
        // operation, so the upgrade leaves nobody able to perform it.
        //
        // Observed, not theorised: after this migration ran against a live
        // database the first account came back `is_admin: false`.
        //
        // Guarded on the table being non-empty so a fresh install is
        // untouched — there, `/api/setup` promotes the account it creates,
        // explicitly, which is the point of having the column at all.
        let existing: i64 = conn.query_row("SELECT COUNT(*) FROM user", [], |r| r.get(0))?;
        if existing > 0 {
            let promoted = conn
                .execute("UPDATE user SET role = 1 WHERE id = 1", [])
                .context("promoting the pre-migration administrator")?;
            if promoted > 0 {
                tracing::info!(
                    "migrated user.role: account 1 promoted to administrator, preserving the \
                     pre-migration `id == 1` rule"
                );
            }
        }
    }
    Ok(())
}

/// Same rationale and pattern as `migrate_user_role` above: a database that
/// predates the quota-usage ledger needs the column
/// added explicitly, since `CREATE TABLE IF NOT EXISTS` won't touch it.
fn migrate_user_usage_bytes(conn: &rusqlite::Connection) -> Result<()> {
    let has_col: i64 = conn.query_row(
        "SELECT COUNT(*) FROM pragma_table_info('user') WHERE name = 'usage_bytes'",
        [],
        |r| r.get(0),
    )?;
    if has_col == 0 {
        conn.execute_batch("ALTER TABLE user ADD COLUMN usage_bytes INTEGER NOT NULL DEFAULT 0")
            .context("adding user.usage_bytes column")?;
    }
    Ok(())
}

/// Same rationale and pattern as the two migrations above: a database that
/// predates remote wipe needs the column added explicitly.
fn migrate_app_password_wipe(conn: &rusqlite::Connection) -> Result<()> {
    let has_col: i64 = conn.query_row(
        "SELECT COUNT(*) FROM pragma_table_info('app_password') WHERE name = 'wipe_requested'",
        [],
        |r| r.get(0),
    )?;
    if has_col == 0 {
        conn.execute_batch(
            "ALTER TABLE app_password ADD COLUMN wipe_requested INTEGER NOT NULL DEFAULT 0",
        )
        .context("adding app_password.wipe_requested column")?;
    }
    Ok(())
}

/// Reads the current key version, seeding the row with `1` on a database
/// that predates the `key_version` table (`CREATE TABLE IF NOT EXISTS` alone
/// won't populate it) — `1` matches the hardcoded value every such row was
/// already hardcoded to before this table existed (`AuthConfig::default`),
/// so seeding it is a no-op against reality, not a guess.
///
/// Takes `&rusqlite::Connection` rather than `&DbPool` so it can run inside
/// an existing transaction (`rusqlite::Transaction` derefs to `Connection`) —
/// `AuthService::rotate_master_key` needs to read and bump this value as
/// part of one atomic commit, not as two separate round trips.
pub(crate) fn current_key_version(conn: &rusqlite::Connection) -> Result<u32> {
    conn.execute("INSERT OR IGNORE INTO key_version (id, ver) VALUES (1, 1)", [])
        .context("seeding key_version")?;
    let ver: i64 = conn
        .query_row("SELECT ver FROM key_version WHERE id = 1", [], |r| r.get(0))
        .context("reading key_version")?;
    Ok(ver as u32)
}

/// Current unix time in nanoseconds.
pub(crate) fn now_ns() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos() as i64
}
