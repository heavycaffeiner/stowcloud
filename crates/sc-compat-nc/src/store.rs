//! Compat-owned persistent state.
//!
//! Everything compat-specific that has to survive a restart lives here, in
//! tables prefixed `nc_`, in this crate. No core crate knows these exist, and
//! dropping `feature = "compat-nc"` drops the whole schema with the code.
//!
//! The trait/impl split is not gratuitous: see `Cargo.toml` for why the
//! rusqlite-backed implementation is behind an off-by-default feature.

use std::collections::HashMap;

use parking_lot::Mutex;

use crate::ports::{FileId, PortError, PortResult, ShareId, SessionId, UserId};

/// DDL for the compat tables. Applied by whoever owns the connection.
///
/// Deviation from: `nc_login_flow` carries two extra
/// columns the design sketch omits — `last_poll_ns` (required to rate-limit the
/// poll endpoint per §6.2, which is otherwise unimplementable) and
/// `login_name`/`client_ip` (shown on the consent screen per §6.2). Both are in
/// our own table, so the isolation contract is untouched.
pub const NC_SCHEMA_SQL: &str = r#"
CREATE TABLE IF NOT EXISTS nc_instance (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS nc_favorite (
    user   INTEGER NOT NULL,
    fileid INTEGER NOT NULL,
    PRIMARY KEY (user, fileid)
);

CREATE TABLE IF NOT EXISTS nc_upload_alias (
    tid     TEXT NOT NULL,
    user    INTEGER NOT NULL,
    -- The engine's session handle is 16 opaque bytes, so it is stored in
    -- its transport (base64url) form rather than squeezed into an integer.
    session TEXT NOT NULL,
    -- The share MKCOL's `Destination` resolved to, and that same header's
    -- vpath (minus the grant label) — captured once, at session-creation
    -- time, so every later PUT/MOVE/DELETE/PROPFIND on this `tid` addresses
    -- the same share the WRITE-checked resolve actually approved, instead
    -- of recomputing `home_root()` (which need not be that share at all).
    -- `migrate_upload_alias_share_dest` backfills both on a database from
    -- before they existed.
    share   INTEGER NOT NULL DEFAULT 0,
    dest    TEXT NOT NULL DEFAULT '',
    created_ns INTEGER NOT NULL,
    -- The client picks {tid}. Scoping the primary key by user means one user
    -- cannot collide with, or guess into, another user's session.
    PRIMARY KEY (user, tid)
);

CREATE TABLE IF NOT EXISTS nc_login_flow (
    poll_hash    BLOB PRIMARY KEY,
    flow_hash    BLOB NOT NULL UNIQUE,
    client_name  TEXT NOT NULL,
    client_ip    TEXT NOT NULL,
    created_ns   INTEGER NOT NULL,
    expires_ns   INTEGER NOT NULL,
    last_poll_ns INTEGER NOT NULL DEFAULT 0,
    login_name   TEXT,
    app_password TEXT
);
"#;

/// Key under which the instance id is stored in `nc_instance`.
pub const INSTANCE_ID_KEY: &str = "instanceid";

/// An approved-but-not-yet-collected login flow result.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FlowResult {
    pub login_name: String,
    pub app_password: String,
}

/// A pending login flow, as seen by the consent screen.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FlowRecord {
    pub client_name: String,
    pub client_ip: String,
    pub created_ns: i64,
    pub expires_ns: i64,
    pub approved: bool,
}

/// Everything bound to a client-chosen transfer id at `MKCOL` time.
///
/// `share` and `dest` are captured once, from the WRITE-checked resolution
/// `MKCOL` performed, and reused verbatim for every later `PUT`/`MOVE`/
/// `DELETE`/`PROPFIND` on the same `tid` — recomputing either (e.g. from
/// `home_root()`) would answer for whichever share happens to be first
/// rather than the one the session actually opened against.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct UploadBinding {
    pub session: SessionId,
    pub share: ShareId,
    /// The `Destination` header's vpath (`{label}/{rest}`), unchanged from
    /// what `MKCOL` resolved it from — the same string a `CorePort` call
    /// needs to re-derive the share-relative path at assemble time
    /// (`NcCore::vpath` reconstructs `{label}/{rest}` from a share-relative
    /// half; keeping the label here means the caller only has to strip it,
    /// never re-attach a subpath it does not have).
    pub dest: String,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum PollOutcome {
    /// Token unknown or expired. Both are reported identically so polling
    /// cannot distinguish them.
    Unknown,
    /// Known and live, but nobody has approved it yet.
    Pending,
    /// Polled again too soon.
    RateLimited,
}

pub trait NcStore: Send + Sync {
    // -- instance identity --------------------------------------------------
    /// Read the instance id, creating and persisting it on first call.
    fn instance_id(&self) -> PortResult<String>;

    // -- favourites ---------------------------------------------------------
    fn is_favorite(&self, user: UserId, file: FileId) -> PortResult<bool>;
    fn set_favorite(&self, user: UserId, file: FileId, on: bool) -> PortResult<()>;

    // -- upload aliases -----------------------------------------------------
    /// Bind a client-chosen transfer id to an internal session id (plus the
    /// share and destination vpath `MKCOL` resolved it to), scoped to the
    /// user. Fails with `Conflict` if this user already has that tid.
    fn bind_upload(
        &self,
        tid: &str,
        user: UserId,
        share: ShareId,
        session: SessionId,
        dest: &str,
        now_ns: i64,
    ) -> PortResult<()>;
    /// Resolve a transfer id **within the calling user's namespace only**.
    fn lookup_upload(&self, tid: &str, user: UserId) -> PortResult<Option<UploadBinding>>;
    fn unbind_upload(&self, tid: &str, user: UserId) -> PortResult<()>;

    // -- login flow v2 ------------------------------------------------------
    fn flow_create(
        &self,
        poll_hash: [u8; 32],
        flow_hash: [u8; 32],
        client_name: &str,
        client_ip: &str,
        created_ns: i64,
        expires_ns: i64,
    ) -> PortResult<()>;

    /// Look up a live (non-expired) flow by its *flow* token hash. Used to
    /// render the consent screen.
    fn flow_peek(&self, flow_hash: &[u8; 32], now_ns: i64) -> PortResult<Option<FlowRecord>>;

    /// Attach a result to a flow. Returns `false` when the flow is unknown,
    /// expired, or already approved (approval is not idempotent — a second
    /// approval would issue a second app password).
    fn flow_approve(
        &self,
        flow_hash: &[u8; 32],
        now_ns: i64,
        result: &FlowResult,
    ) -> PortResult<bool>;

    /// Poll. On success the result stays put — it can be retrieved again by
    /// the same `poll_hash` until the flow's TTL expires or it is swept.
    ///
    /// The Android client 34.1.0 permanently kills its own poll loop if a
    /// single poll attempt throws (`scheduleWithFixedDelay` suppresses all
    /// future runs after one exception; see `login_flow.rs` module docs). If
    /// the server deletes the credential the instant it is served, a client
    /// that never gets to finish processing that one response — a dropped
    /// connection, the app backgrounded mid-parse, a transient IOException —
    /// loses it forever with no way to retry. The reference server
    /// (`LoginFlowV2Service::poll()`) deletes the row before it even attempts
    /// to decrypt the password, so it has the same failure mode; we
    /// deliberately diverge here. Approval itself is still single-use
    /// (`flow_approve` refuses a second approval), so this cannot mint or
    /// hand out more than one app password per flow — it only widens the
    /// window in which the *already-issued* one can be collected.
    fn flow_poll(
        &self,
        poll_hash: &[u8; 32],
        now_ns: i64,
        min_interval_ns: i64,
    ) -> PortResult<Result<FlowResult, PollOutcome>>;

    /// Delete expired rows. Returns how many were removed.
    fn flow_sweep(&self, now_ns: i64) -> PortResult<usize>;
}

// ---------------------------------------------------------------------------
// in-memory implementation
// ---------------------------------------------------------------------------

#[derive(Debug)]
struct FlowRow {
    poll_hash: [u8; 32],
    flow_hash: [u8; 32],
    client_name: String,
    client_ip: String,
    created_ns: i64,
    expires_ns: i64,
    last_poll_ns: i64,
    result: Option<FlowResult>,
}

#[derive(Default)]
struct MemInner {
    instance: HashMap<String, String>,
    favorites: std::collections::HashSet<(u32, i64)>,
    uploads: HashMap<(u32, String), UploadBinding>,
    flows: Vec<FlowRow>,
}

/// Process-local store. Real deployments use the SQLite implementation; this
/// exists so the crate (and its tests) build with no C toolchain.
pub struct MemStore {
    inner: Mutex<MemInner>,
    /// Injectable so tests get a deterministic instance id.
    fixed_instance: Option<String>,
}

impl Default for MemStore {
    fn default() -> Self {
        Self::new()
    }
}

impl MemStore {
    pub fn new() -> Self {
        Self { inner: Mutex::new(MemInner::default()), fixed_instance: None }
    }

    pub fn with_instance_id(id: impl Into<String>) -> Self {
        Self {
            inner: Mutex::new(MemInner::default()),
            fixed_instance: Some(id.into()),
        }
    }
}

/// Generate a fresh instance id: 10 lowercase base32 characters, matching the
/// shape the reference server's `OC_Util::getInstanceId()` produces (`oc` + random).
///
/// # Do not regenerate this value
///
/// The instance id is baked into every `oc:id` this server has ever handed out.
/// Changing it changes every file identifier, and **every connected client will
/// discard its local state and perform a full resync** — for a large
/// deployment that is potentially terabytes of re-download and hours of
/// unavailability. It must be included in backups and restored verbatim. See
/// the loud warning on `NcConfig` and `DEPLOYMENT.md`.
pub fn generate_instance_id() -> String {
    let mut buf = [0u8; 8];
    getrandom::getrandom(&mut buf).expect("OS entropy unavailable");
    let enc = data_encoding::BASE32_NOPAD
        .encode(&buf)
        .to_ascii_lowercase();
    format!("oc{}", &enc[..8])
}

impl NcStore for MemStore {
    fn instance_id(&self) -> PortResult<String> {
        let mut g = self.inner.lock();
        if let Some(v) = g.instance.get(INSTANCE_ID_KEY) {
            return Ok(v.clone());
        }
        let id = self
            .fixed_instance
            .clone()
            .unwrap_or_else(generate_instance_id);
        g.instance.insert(INSTANCE_ID_KEY.into(), id.clone());
        Ok(id)
    }

    fn is_favorite(&self, user: UserId, file: FileId) -> PortResult<bool> {
        Ok(self.inner.lock().favorites.contains(&(user.0, file.0)))
    }

    fn set_favorite(&self, user: UserId, file: FileId, on: bool) -> PortResult<()> {
        let mut g = self.inner.lock();
        if on {
            g.favorites.insert((user.0, file.0));
        } else {
            g.favorites.remove(&(user.0, file.0));
        }
        Ok(())
    }

    fn bind_upload(
        &self,
        tid: &str,
        user: UserId,
        share: ShareId,
        session: SessionId,
        dest: &str,
        _now_ns: i64,
    ) -> PortResult<()> {
        let mut g = self.inner.lock();
        let key = (user.0, tid.to_owned());
        if g.uploads.contains_key(&key) {
            return Err(PortError::Conflict("upload session already exists".into()));
        }
        g.uploads.insert(key, UploadBinding { session, share, dest: dest.to_owned() });
        Ok(())
    }

    fn lookup_upload(&self, tid: &str, user: UserId) -> PortResult<Option<UploadBinding>> {
        Ok(self
            .inner
            .lock()
            .uploads
            .get(&(user.0, tid.to_owned()))
            .cloned())
    }

    fn unbind_upload(&self, tid: &str, user: UserId) -> PortResult<()> {
        self.inner.lock().uploads.remove(&(user.0, tid.to_owned()));
        Ok(())
    }

    fn flow_create(
        &self,
        poll_hash: [u8; 32],
        flow_hash: [u8; 32],
        client_name: &str,
        client_ip: &str,
        created_ns: i64,
        expires_ns: i64,
    ) -> PortResult<()> {
        self.inner.lock().flows.push(FlowRow {
            poll_hash,
            flow_hash,
            client_name: client_name.to_owned(),
            client_ip: client_ip.to_owned(),
            created_ns,
            expires_ns,
            last_poll_ns: 0,
            result: None,
        });
        Ok(())
    }

    fn flow_peek(&self, flow_hash: &[u8; 32], now_ns: i64) -> PortResult<Option<FlowRecord>> {
        let g = self.inner.lock();
        Ok(g.flows
            .iter()
            .find(|r| &r.flow_hash == flow_hash && r.expires_ns > now_ns)
            .map(|r| FlowRecord {
                client_name: r.client_name.clone(),
                client_ip: r.client_ip.clone(),
                created_ns: r.created_ns,
                expires_ns: r.expires_ns,
                approved: r.result.is_some(),
            }))
    }

    fn flow_approve(
        &self,
        flow_hash: &[u8; 32],
        now_ns: i64,
        result: &FlowResult,
    ) -> PortResult<bool> {
        let mut g = self.inner.lock();
        match g
            .flows
            .iter_mut()
            .find(|r| &r.flow_hash == flow_hash && r.expires_ns > now_ns)
        {
            Some(r) if r.result.is_none() => {
                r.result = Some(result.clone());
                Ok(true)
            }
            _ => Ok(false),
        }
    }

    fn flow_poll(
        &self,
        poll_hash: &[u8; 32],
        now_ns: i64,
        min_interval_ns: i64,
    ) -> PortResult<Result<FlowResult, PollOutcome>> {
        let mut g = self.inner.lock();
        let Some(idx) = g
            .flows
            .iter()
            .position(|r| &r.poll_hash == poll_hash && r.expires_ns > now_ns)
        else {
            return Ok(Err(PollOutcome::Unknown));
        };
        if now_ns - g.flows[idx].last_poll_ns < min_interval_ns {
            return Ok(Err(PollOutcome::RateLimited));
        }
        g.flows[idx].last_poll_ns = now_ns;
        match g.flows[idx].result.clone() {
            // Left in place (not removed): a client that drops this response
            // can poll the same token again and get the same credential,
            // until expiry/sweep takes the row.
            Some(res) => Ok(Ok(res)),
            None => Ok(Err(PollOutcome::Pending)),
        }
    }

    fn flow_sweep(&self, now_ns: i64) -> PortResult<usize> {
        let mut g = self.inner.lock();
        let before = g.flows.len();
        g.flows.retain(|r| r.expires_ns > now_ns);
        Ok(before - g.flows.len())
    }
}

// ---------------------------------------------------------------------------
// sqlite implementation
// ---------------------------------------------------------------------------

#[cfg(feature = "sqlite")]
mod sqlite_impl {
    use super::*;
    use rusqlite::{params, Connection, OptionalExtension};

    fn map_err(e: rusqlite::Error) -> PortError {
        PortError::Backend(e.to_string())
    }

    /// SQLite-backed `NcStore`. One connection behind a mutex: this store sees
    /// a handful of writes per login and nothing on the hot PROPFIND path
    /// except favourite lookups, so contention is a non-issue.
    pub struct SqliteStore {
        conn: Mutex<Connection>,
    }

    impl SqliteStore {
        pub fn open(path: &std::path::Path) -> PortResult<Self> {
            let conn = Connection::open(path).map_err(map_err)?;
            Self::from_conn(conn)
        }

        pub fn open_in_memory() -> PortResult<Self> {
            let conn = Connection::open_in_memory().map_err(map_err)?;
            Self::from_conn(conn)
        }

        pub fn from_conn(conn: Connection) -> PortResult<Self> {
            conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")
                .map_err(map_err)?;
            conn.execute_batch(NC_SCHEMA_SQL).map_err(map_err)?;
            migrate_upload_alias_share_dest(&conn).map_err(map_err)?;
            Ok(Self { conn: Mutex::new(conn) })
        }
    }

    /// Databases created before `nc_upload_alias` carried `share`/`dest`
    /// don't get the columns from `NC_SCHEMA_SQL` above — `CREATE TABLE IF
    /// NOT EXISTS` is a no-op against an existing table (see the identical
    /// pattern and rationale in `sc-auth/src/db.rs::migrate_user_role`).
    /// Unlike that migration there is no data to carry forward: a row here
    /// names an upload session still in flight at the moment of upgrade, and
    /// `share = 0`/`dest = ""` (the columns' own defaults) simply make that
    /// stale session fail its next `PUT`/`MOVE` with `NotFound`, the same
    /// outcome an expired session already produces — the client re-issues
    /// `MKCOL` and resumes.
    fn migrate_upload_alias_share_dest(conn: &Connection) -> Result<(), rusqlite::Error> {
        let has_share: i64 = conn.query_row(
            "SELECT COUNT(*) FROM pragma_table_info('nc_upload_alias') WHERE name = 'share'",
            [],
            |r| r.get(0),
        )?;
        if has_share == 0 {
            conn.execute_batch(
                "ALTER TABLE nc_upload_alias ADD COLUMN share INTEGER NOT NULL DEFAULT 0",
            )?;
        }
        let has_dest: i64 = conn.query_row(
            "SELECT COUNT(*) FROM pragma_table_info('nc_upload_alias') WHERE name = 'dest'",
            [],
            |r| r.get(0),
        )?;
        if has_dest == 0 {
            conn.execute_batch(
                "ALTER TABLE nc_upload_alias ADD COLUMN dest TEXT NOT NULL DEFAULT ''",
            )?;
        }
        Ok(())
    }

    impl NcStore for SqliteStore {
        fn instance_id(&self) -> PortResult<String> {
            let conn = self.conn.lock();
            let existing: Option<String> = conn
                .query_row(
                    "SELECT v FROM nc_instance WHERE k = ?1",
                    params![INSTANCE_ID_KEY],
                    |r| r.get(0),
                )
                .optional()
                .map_err(map_err)?;
            if let Some(v) = existing {
                return Ok(v);
            }
            let id = generate_instance_id();
            // INSERT OR IGNORE + re-read: two processes racing on first boot
            // must converge on one id, never two.
            conn.execute(
                "INSERT OR IGNORE INTO nc_instance (k, v) VALUES (?1, ?2)",
                params![INSTANCE_ID_KEY, &id],
            )
            .map_err(map_err)?;
            conn.query_row(
                "SELECT v FROM nc_instance WHERE k = ?1",
                params![INSTANCE_ID_KEY],
                |r| r.get(0),
            )
            .map_err(map_err)
        }

        fn is_favorite(&self, user: UserId, file: FileId) -> PortResult<bool> {
            let conn = self.conn.lock();
            let n: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM nc_favorite WHERE user = ?1 AND fileid = ?2",
                    params![user.0, file.0],
                    |r| r.get(0),
                )
                .map_err(map_err)?;
            Ok(n > 0)
        }

        fn set_favorite(&self, user: UserId, file: FileId, on: bool) -> PortResult<()> {
            let conn = self.conn.lock();
            if on {
                conn.execute(
                    "INSERT OR IGNORE INTO nc_favorite (user, fileid) VALUES (?1, ?2)",
                    params![user.0, file.0],
                )
            } else {
                conn.execute(
                    "DELETE FROM nc_favorite WHERE user = ?1 AND fileid = ?2",
                    params![user.0, file.0],
                )
            }
            .map_err(map_err)?;
            Ok(())
        }

        fn bind_upload(
            &self,
            tid: &str,
            user: UserId,
            share: ShareId,
            session: SessionId,
            dest: &str,
            now_ns: i64,
        ) -> PortResult<()> {
            let conn = self.conn.lock();
            let n = conn
                .execute(
                    "INSERT OR IGNORE INTO nc_upload_alias (tid, user, session, share, dest, created_ns) \
                     VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
                    params![tid, user.0, session.to_b64(), share.get(), dest, now_ns],
                )
                .map_err(map_err)?;
            if n == 0 {
                return Err(PortError::Conflict("upload session already exists".into()));
            }
            Ok(())
        }

        fn lookup_upload(&self, tid: &str, user: UserId) -> PortResult<Option<UploadBinding>> {
            let conn = self.conn.lock();
            let row: Option<(String, i64, String)> = conn
                .query_row(
                    "SELECT session, share, dest FROM nc_upload_alias WHERE tid = ?1 AND user = ?2",
                    params![tid, user.0],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
                )
                .optional()
                .map_err(map_err)?;
            Ok(row.and_then(|(session, share, dest)| {
                Some(UploadBinding {
                    session: SessionId::parse_b64(&session)?,
                    share: ShareId::new(share as u32),
                    dest,
                })
            }))
        }

        fn unbind_upload(&self, tid: &str, user: UserId) -> PortResult<()> {
            let conn = self.conn.lock();
            conn.execute(
                "DELETE FROM nc_upload_alias WHERE tid = ?1 AND user = ?2",
                params![tid, user.0],
            )
            .map_err(map_err)?;
            Ok(())
        }

        fn flow_create(
            &self,
            poll_hash: [u8; 32],
            flow_hash: [u8; 32],
            client_name: &str,
            client_ip: &str,
            created_ns: i64,
            expires_ns: i64,
        ) -> PortResult<()> {
            let conn = self.conn.lock();
            conn.execute(
                "INSERT INTO nc_login_flow \
                 (poll_hash, flow_hash, client_name, client_ip, created_ns, expires_ns, last_poll_ns) \
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, 0)",
                params![
                    poll_hash.as_slice(),
                    flow_hash.as_slice(),
                    client_name,
                    client_ip,
                    created_ns,
                    expires_ns
                ],
            )
            .map_err(map_err)?;
            Ok(())
        }

        fn flow_peek(&self, flow_hash: &[u8; 32], now_ns: i64) -> PortResult<Option<FlowRecord>> {
            let conn = self.conn.lock();
            conn.query_row(
                "SELECT client_name, client_ip, created_ns, expires_ns, app_password \
                 FROM nc_login_flow WHERE flow_hash = ?1 AND expires_ns > ?2",
                params![flow_hash.as_slice(), now_ns],
                |r| {
                    Ok(FlowRecord {
                        client_name: r.get(0)?,
                        client_ip: r.get(1)?,
                        created_ns: r.get(2)?,
                        expires_ns: r.get(3)?,
                        approved: r.get::<_, Option<String>>(4)?.is_some(),
                    })
                },
            )
            .optional()
            .map_err(map_err)
        }

        fn flow_approve(
            &self,
            flow_hash: &[u8; 32],
            now_ns: i64,
            result: &FlowResult,
        ) -> PortResult<bool> {
            let conn = self.conn.lock();
            // `app_password IS NULL` in the WHERE clause makes double approval
            // a no-op at the storage layer, not just in the handler.
            let n = conn
                .execute(
                    "UPDATE nc_login_flow SET login_name = ?1, app_password = ?2 \
                     WHERE flow_hash = ?3 AND expires_ns > ?4 AND app_password IS NULL",
                    params![
                        &result.login_name,
                        &result.app_password,
                        flow_hash.as_slice(),
                        now_ns
                    ],
                )
                .map_err(map_err)?;
            Ok(n == 1)
        }

        fn flow_poll(
            &self,
            poll_hash: &[u8; 32],
            now_ns: i64,
            min_interval_ns: i64,
        ) -> PortResult<Result<FlowResult, PollOutcome>> {
            let mut conn = self.conn.lock();
            let tx = conn.transaction().map_err(map_err)?;
            let row: Option<(i64, Option<String>, Option<String>)> = tx
                .query_row(
                    "SELECT last_poll_ns, login_name, app_password FROM nc_login_flow \
                     WHERE poll_hash = ?1 AND expires_ns > ?2",
                    params![poll_hash.as_slice(), now_ns],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
                )
                .optional()
                .map_err(map_err)?;
            let Some((last_poll_ns, login_name, app_password)) = row else {
                return Ok(Err(PollOutcome::Unknown));
            };
            if now_ns - last_poll_ns < min_interval_ns {
                return Ok(Err(PollOutcome::RateLimited));
            }
            tx.execute(
                "UPDATE nc_login_flow SET last_poll_ns = ?1 WHERE poll_hash = ?2",
                params![now_ns, poll_hash.as_slice()],
            )
            .map_err(map_err)?;
            // Not deleted: left for expiry/sweep so a client that fails to
            // consume this response (dropped connection, backgrounded
            // mid-parse) can poll the same token again and get the same
            // credential instead of a permanent 404.
            let out = match (login_name, app_password) {
                (Some(login_name), Some(app_password)) => {
                    Ok(FlowResult { login_name, app_password })
                }
                _ => Err(PollOutcome::Pending),
            };
            tx.commit().map_err(map_err)?;
            Ok(out)
        }

        fn flow_sweep(&self, now_ns: i64) -> PortResult<usize> {
            let conn = self.conn.lock();
            let n = conn
                .execute(
                    "DELETE FROM nc_login_flow WHERE expires_ns <= ?1",
                    params![now_ns],
                )
                .map_err(map_err)?;
            Ok(n)
        }
    }
}

#[cfg(feature = "sqlite")]
pub use sqlite_impl::SqliteStore;

#[cfg(test)]
mod tests {
    use super::*;

    /// A distinguishable session handle. The engine's ids are 16 opaque
    /// CSPRNG bytes; the tests only need them to differ from each other.
    fn sid(n: u8) -> SessionId {
        SessionId([n; 16])
    }

    #[test]
    fn instance_id_is_stable_and_shaped_like_nc() {
        let s = MemStore::new();
        let a = s.instance_id().unwrap();
        let b = s.instance_id().unwrap();
        assert_eq!(a, b, "instance id must never change once generated");
        assert!(a.starts_with("oc"));
        assert_eq!(a.len(), 10);
        assert!(a.chars().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit()));
    }

    /// Exercise every trait method against a store, so `MemStore` and
    /// `SqliteStore` are held to the same contract rather than drifting.
    fn store_contract(s: &dyn NcStore) {
        // instance id is generate-once.
        let a = s.instance_id().unwrap();
        assert_eq!(a, s.instance_id().unwrap());

        // favourites
        assert!(!s.is_favorite(UserId(1), FileId(9)).unwrap());
        s.set_favorite(UserId(1), FileId(9), true).unwrap();
        assert!(s.is_favorite(UserId(1), FileId(9)).unwrap());
        // ...are per user.
        assert!(!s.is_favorite(UserId(2), FileId(9)).unwrap());
        // ...and setting twice is idempotent.
        s.set_favorite(UserId(1), FileId(9), true).unwrap();
        s.set_favorite(UserId(1), FileId(9), false).unwrap();
        assert!(!s.is_favorite(UserId(1), FileId(9)).unwrap());

        // upload aliases
        let bind1 = UploadBinding { session: sid(11), share: ShareId::new(1), dest: "docs/a".into() };
        let bind2 = UploadBinding { session: sid(22), share: ShareId::new(2), dest: "docs/b".into() };
        s.bind_upload("t1", UserId(1), bind1.share, bind1.session, &bind1.dest, 0).unwrap();
        s.bind_upload("t1", UserId(2), bind2.share, bind2.session, &bind2.dest, 0).unwrap();
        assert!(s
            .bind_upload("t1", UserId(1), ShareId::new(3), sid(33), "docs/c", 0)
            .is_err());
        assert_eq!(s.lookup_upload("t1", UserId(1)).unwrap(), Some(bind1.clone()));
        assert_eq!(s.lookup_upload("t1", UserId(2)).unwrap(), Some(bind2.clone()));
        assert_eq!(s.lookup_upload("t1", UserId(3)).unwrap(), None);
        s.unbind_upload("t1", UserId(1)).unwrap();
        assert_eq!(s.lookup_upload("t1", UserId(1)).unwrap(), None);
        assert_eq!(s.lookup_upload("t1", UserId(2)).unwrap(), Some(bind2));

        // login flow
        let poll = [1u8; 32];
        let flow = [2u8; 32];
        s.flow_create(poll, flow, "client", "1.2.3.4", 0, 1_000).unwrap();
        let rec = s.flow_peek(&flow, 10).unwrap().unwrap();
        assert_eq!(rec.client_name, "client");
        assert!(!rec.approved);
        // expired lookups return nothing.
        assert!(s.flow_peek(&flow, 2_000).unwrap().is_none());

        // pending poll
        assert_eq!(
            s.flow_poll(&poll, 10, 0).unwrap().unwrap_err(),
            PollOutcome::Pending
        );
        // rate limit
        assert_eq!(
            s.flow_poll(&poll, 10, 5).unwrap().unwrap_err(),
            PollOutcome::RateLimited
        );

        let res = FlowResult {
            login_name: "alice".into(),
            app_password: "secret".into(),
        };
        assert!(s.flow_approve(&flow, 20, &res).unwrap());
        // second approval is refused at the storage layer.
        assert!(!s.flow_approve(&flow, 20, &res).unwrap());
        assert!(s.flow_peek(&flow, 20).unwrap().unwrap().approved);

        // A granted result survives being polled more than once — a client
        // that drops the first response must be able to retry and get the
        // same credential, not `Unknown`.
        assert_eq!(s.flow_poll(&poll, 30, 0).unwrap().unwrap(), res);
        assert_eq!(s.flow_poll(&poll, 40, 0).unwrap().unwrap(), res);

        // ...but not forever: once the flow's own TTL (expires at 1_000)
        // passes, it reads as `Unknown` just like a token that never existed,
        // even though no poll ever deleted it.
        assert_eq!(
            s.flow_poll(&poll, 1_000, 0).unwrap().unwrap_err(),
            PollOutcome::Unknown
        );

        // sweep physically removes both the now-expired granted flow above
        // and a freshly created one, once each one's TTL has passed.
        s.flow_create([3u8; 32], [4u8; 32], "c", "ip", 0, 100).unwrap();
        assert_eq!(s.flow_sweep(50).unwrap(), 0);
        assert_eq!(s.flow_sweep(1_000).unwrap(), 2);
    }

    #[test]
    fn mem_store_satisfies_the_contract() {
        store_contract(&MemStore::new());
    }

    #[cfg(feature = "sqlite")]
    #[test]
    fn sqlite_store_satisfies_the_same_contract() {
        store_contract(&SqliteStore::open_in_memory().unwrap());
    }

    #[test]
    fn upload_alias_is_scoped_per_user() {
        let s = MemStore::new();
        let bind1 = UploadBinding { session: sid(11), share: ShareId::new(1), dest: "docs/a".into() };
        let bind2 = UploadBinding { session: sid(22), share: ShareId::new(1), dest: "docs/b".into() };
        s.bind_upload("web-file-upload-abc", UserId(1), bind1.share, bind1.session, &bind1.dest, 0)
            .unwrap();
        // The same client-chosen tid from a different user is a *different*
        // binding, not a collision and not a hijack.
        s.bind_upload("web-file-upload-abc", UserId(2), bind2.share, bind2.session, &bind2.dest, 0)
            .unwrap();
        assert_eq!(
            s.lookup_upload("web-file-upload-abc", UserId(1)).unwrap(),
            Some(bind1)
        );
        assert_eq!(
            s.lookup_upload("web-file-upload-abc", UserId(2)).unwrap(),
            Some(bind2)
        );
        // Same user, same tid twice -> conflict, never a silent rebind.
        assert!(s
            .bind_upload("web-file-upload-abc", UserId(1), ShareId::new(1), sid(33), "docs/c", 0)
            .is_err());
    }
}
