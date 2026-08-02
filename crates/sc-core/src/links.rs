//! Public share links —
//!
//! This lives in `sc-core` rather than in a protocol crate because a share
//! link is domain state, not a wire concern: `sc-http`'s `/api/shares` and the
//! compatibility layer's own share API are two translations of the *same*
//! rows, and only one of them can own the table.
//!
//! Four properties are load-bearing and are each covered by a test in
//! `crate::tests`:
//!
//! 1. **The plaintext token is never persisted.** 128 bits of CSPRNG, rendered
//!    base64url (22 chars), returned exactly once from [`Core::create_link`];
//!    the row holds `sha256(token)` and nothing else. A database read gives an
//!    attacker no usable link.
//! 2. **Passwords are Argon2id**, with `sc-auth`'s parameters — link password
//!    checks are rare, so a deliberately slow hash is the right trade
//!    (§7.1: "Argon2 — low frequency, so a slow hash is fine").
//! 3. **Path *and* fileid are both checked on access** (§7.1). Keying on the
//!    fileid alone kills the link whenever the file is legitimately replaced;
//!    keying on the path alone will happily serve a *different* file that
//!    later took the same name. Storing both and requiring them to agree is
//!    the safe intersection — a mismatch means the link is dead, not that it
//!    should follow one of the two.
//! 4. **The download counter is an atomic conditional `UPDATE`** and is never
//!    given back when a transfer breaks (§7.2: abuse prevention beats exact
//!    accounting).
//!
//! File-drop links (`Perms::CREATE` with no `READ`) get no listing, no read
//! and no overwrite; a colliding upload is renamed rather than replacing what
//! is already there.

use std::path::Path;
use std::sync::Arc;

use sc_acl::Perms;
use sc_auth::AuthConfig;
use sc_vfs::{FileId, Kind, SafePath, ShareId, ShareRoot, UserId};
use r2d2::Pool;
use r2d2_sqlite::SqliteConnectionManager;
use rusqlite::{Connection, OpenFlags, OptionalExtension};
use secrecy::SecretString;
use sha2::{Digest, Sha256};

use crate::argon_gate::ArgonGate;
use crate::entry::Entry;
use crate::error::CoreError;

/// Bytes of CSPRNG behind a token. 16 bytes → 22 base64url characters, which
/// is the length specifies.
const TOKEN_BYTES: usize = 16;

const SCHEMA_SQL: &str = "
CREATE TABLE IF NOT EXISTS share_link (
  id            INTEGER PRIMARY KEY,
  token_hash    BLOB UNIQUE NOT NULL,
  share         INTEGER NOT NULL,
  path          TEXT NOT NULL,
  fileid        INTEGER,
  owner         INTEGER NOT NULL,
  perms         INTEGER NOT NULL,
  password_hash TEXT,
  expires_ns    INTEGER,
  max_downloads INTEGER,
  downloads     INTEGER NOT NULL DEFAULT 0,
  label         TEXT,
  created_ns    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS share_link_owner ON share_link(owner);
CREATE INDEX IF NOT EXISTS share_link_node  ON share_link(share, fileid);
";

/// One public link, as every caller above `sc-core` sees it. Deliberately
/// carries no password material and no token — only whether a password is set.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ShareLink {
    pub id: i64,
    pub share: ShareId,
    pub path: SafePath,
    /// The target's fileid at the moment the link was minted. `None` only if
    /// the metadata store could not allocate one; the cross-check then
    /// degrades to path-only rather than failing link creation outright.
    pub fileid_at_creation: Option<FileId>,
    pub owner: UserId,
    pub perms: Perms,
    pub expires_ns: Option<i128>,
    pub max_downloads: Option<u32>,
    pub downloads: u32,
    pub label: Option<String>,
    pub has_password: bool,
    pub created_ns: i128,
}

impl ShareLink {
    /// A file-drop link: upload-only. `Perms::CREATE` and nothing that would
    /// let the holder see what is already there.
    pub fn is_drop(&self) -> bool {
        self.perms.contains(Perms::CREATE) && !self.perms.intersects(Perms::READ | Perms::DOWNLOAD)
    }

    pub fn is_expired(&self, now_ns: i128) -> bool {
        matches!(self.expires_ns, Some(e) if e <= now_ns)
    }

    pub fn is_exhausted(&self) -> bool {
        matches!(self.max_downloads, Some(m) if self.downloads >= m)
    }
}

/// What [`Core::create_link`] is asked to mint.
#[derive(Clone, Debug)]
pub struct LinkSpec {
    pub perms: Perms,
    /// Plaintext; hashed with Argon2id before it touches the database.
    pub password: Option<String>,
    pub expires_ns: Option<i128>,
    pub max_downloads: Option<u32>,
    pub label: Option<String>,
}

impl Default for LinkSpec {
    /// A read-only link with no password, no expiry and no download cap —
    /// the least surprising thing a caller that filled in nothing could mean.
    fn default() -> Self {
        Self {
            perms: Perms::READ | Perms::DOWNLOAD,
            password: None,
            expires_ns: None,
            max_downloads: None,
            label: None,
        }
    }
}

/// A partial update. The doubled `Option` is not an accident: the outer layer
/// distinguishes "field absent from the request" from "field explicitly set to
/// null", which is exactly the difference between leaving a password alone and
/// removing it.
#[derive(Clone, Debug, Default)]
pub struct LinkPatch {
    pub perms: Option<Perms>,
    pub password: Option<Option<String>>,
    pub expires_ns: Option<Option<i128>>,
    pub max_downloads: Option<Option<u32>>,
    pub label: Option<Option<String>>,
}

// ---------------------------------------------------------------------------
// storage
// ---------------------------------------------------------------------------

/// SQLite-backed persistence for [`ShareLink`]s.
///
/// Its own database rather than a table inside `sc-meta`: `sc-meta` is
/// explicitly a *cache* that "can be deleted at any time and the service keeps
/// working". Share links are not reconstructible from
/// the filesystem, so putting them in that file would quietly turn a
/// documented-as-disposable database into one that must be backed up.
pub struct LinkStore {
    pool: Pool<SqliteConnectionManager>,
    cfg: AuthConfig,
    /// Argon2 hash of a secret nobody holds. Verified against when a token
    /// lookup found nothing, so "no such link" costs the same as "wrong
    /// password".
    dummy_hash: String,
    /// Bounds concurrent Argon2 invocations this store makes (hashing on
    /// create/update, verifying on `check_link_password`) — see
    /// `crate::argon_gate` for why a public share link needs this exactly as
    /// much as a login attempt does, and why this is a second gate rather
    /// than `sc-auth`'s own.
    argon_gate: Arc<ArgonGate>,
    _keepalive: parking_lot::Mutex<Option<Connection>>,
}

enum Target {
    File(std::path::PathBuf),
    Memory(String),
}

impl LinkStore {
    pub fn open(path: &Path) -> anyhow::Result<Self> {
        Self::open_with(Target::File(path.to_path_buf()), AuthConfig::default())
    }

    /// Shared-cache in-memory store, for tests. A plain `:memory:` would give
    /// every pooled connection its own empty database.
    pub fn open_in_memory() -> anyhow::Result<Self> {
        Self::open_in_memory_with_config(AuthConfig::default())
    }

    /// As [`LinkStore::open_in_memory`], but with explicit Argon2 parameters
    /// — used by tests that need a small `argon2_parallelism` to observe the
    /// concurrency gate deterministically.
    pub fn open_in_memory_with_config(cfg: AuthConfig) -> anyhow::Result<Self> {
        static COUNTER: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let n = COUNTER.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let uri = format!("file:sc_links_mem_{}_{n}?mode=memory&cache=shared", std::process::id());
        Self::open_with(Target::Memory(uri), cfg)
    }

    /// As [`LinkStore::open`], but with explicit Argon2 parameters — the
    /// server passes the same `AuthConfig` it gave `sc-auth`.
    pub fn open_with_config(path: &Path, cfg: AuthConfig) -> anyhow::Result<Self> {
        Self::open_with(Target::File(path.to_path_buf()), cfg)
    }

    fn open_with(target: Target, cfg: AuthConfig) -> anyhow::Result<Self> {
        let flags = OpenFlags::SQLITE_OPEN_READ_WRITE | OpenFlags::SQLITE_OPEN_CREATE | OpenFlags::SQLITE_OPEN_URI;
        let bootstrap = match &target {
            Target::File(p) => Connection::open(p)?,
            Target::Memory(uri) => Connection::open_with_flags(uri, flags)?,
        };
        bootstrap.execute_batch(
            "PRAGMA journal_mode = WAL;
             PRAGMA synchronous = NORMAL;
             PRAGMA busy_timeout = 5000;",
        )?;
        bootstrap.execute_batch(SCHEMA_SQL)?;

        let manager = match &target {
            Target::File(p) => SqliteConnectionManager::file(p),
            Target::Memory(uri) => SqliteConnectionManager::file(uri).with_flags(flags),
        }
        .with_init(|c| c.execute_batch("PRAGMA busy_timeout = 5000; PRAGMA foreign_keys = ON;"));
        let pool = Pool::builder().max_size(4).build(manager)?;

        let dummy_hash = sc_auth::password::dummy_phc(&cfg)?;
        let argon_gate = Arc::new(ArgonGate::new(cfg.argon2_parallelism));
        let keepalive = match target {
            Target::File(_) => None,
            Target::Memory(_) => Some(bootstrap),
        };
        Ok(Self {
            pool,
            cfg,
            dummy_hash,
            argon_gate,
            _keepalive: parking_lot::Mutex::new(keepalive),
        })
    }

    fn conn(&self) -> Result<r2d2::PooledConnection<SqliteConnectionManager>, CoreError> {
        self.pool.get().map_err(|e| CoreError::Internal(format!("share-link db: {e}")))
    }

    /// Peak concurrent Argon2 invocations this store has made since
    /// construction. Test-only: proves `argon_gate` actually bounds the
    /// share-link password path rather than merely existing unused.
    #[cfg(test)]
    pub(crate) fn argon2_high_water(&self) -> usize {
        self.argon_gate.high_water()
    }
}

fn db(e: rusqlite::Error) -> CoreError {
    CoreError::Internal(format!("share-link db: {e}"))
}

/// `sha256(token)` — the only representation of a token that is ever written
/// down.
pub fn token_hash(token: &str) -> [u8; 32] {
    let mut h = Sha256::new();
    h.update(token.as_bytes());
    h.finalize().into()
}

fn mint_token() -> Result<String, CoreError> {
    let mut buf = [0u8; TOKEN_BYTES];
    getrandom::getrandom(&mut buf).map_err(|e| CoreError::Internal(format!("getrandom: {e}")))?;
    Ok(data_encoding::BASE64URL_NOPAD.encode(&buf))
}

pub fn now_ns() -> i128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i128)
        .unwrap_or(0)
}

/// SQLite integers are 64-bit; nanosecond timestamps stay inside that range
/// until the year 2262, and `sc-meta` already stores `mtime_ns` the same way.
fn ns_to_i64(v: i128) -> i64 {
    v.clamp(i64::MIN as i128, i64::MAX as i128) as i64
}

fn row_to_link(row: &rusqlite::Row<'_>, max_depth: u16) -> rusqlite::Result<ShareLink> {
    let path_str: String = row.get("path")?;
    let path = SafePath::parse(&path_str, max_depth).unwrap_or_else(|_| SafePath::root());
    let fileid: Option<i64> = row.get("fileid")?;
    let expires: Option<i64> = row.get("expires_ns")?;
    let max_downloads: Option<i64> = row.get("max_downloads")?;
    let pw: Option<String> = row.get("password_hash")?;
    Ok(ShareLink {
        id: row.get("id")?,
        share: ShareId::new(row.get::<_, i64>("share")? as u32),
        path,
        fileid_at_creation: fileid.map(FileId::new),
        owner: UserId::new(row.get::<_, i64>("owner")? as u32),
        perms: Perms::from_bits_truncate(row.get::<_, i64>("perms")? as u16),
        expires_ns: expires.map(|v| v as i128),
        max_downloads: max_downloads.map(|v| v as u32),
        downloads: row.get::<_, i64>("downloads")? as u32,
        label: row.get("label")?,
        has_password: pw.is_some(),
        created_ns: row.get::<_, i64>("created_ns")? as i128,
    })
}

const SELECT_COLS: &str = "id, token_hash, share, path, fileid, owner, perms, password_hash, \
                           expires_ns, max_downloads, downloads, label, created_ns";

// ---------------------------------------------------------------------------
// Core API
// ---------------------------------------------------------------------------

impl crate::Core {
    /// Attach the share-link store. Idempotent-by-refusal: a second call is a
    /// wiring bug, not a runtime condition, so it returns an error rather than
    /// silently replacing a store other threads may already be reading.
    pub fn attach_links(&self, store: LinkStore) -> anyhow::Result<()> {
        self.links
            .set(Arc::new(store))
            .map_err(|_| anyhow::anyhow!("share-link store already attached"))
    }

    /// `true` when a store is attached. Protocol layers use this to answer
    /// "does this deployment do share links at all" honestly instead of
    /// failing every call with a 500.
    pub fn links_enabled(&self) -> bool {
        self.links.get().is_some()
    }

    fn store(&self) -> Result<&LinkStore, CoreError> {
        self.links
            .get()
            .map(|s| s.as_ref())
            .ok_or_else(|| CoreError::NotSupported("share links are not configured on this server".into()))
    }

    fn root_of(&self, share: ShareId) -> Result<Arc<ShareRoot>, CoreError> {
        self.share(share).ok_or(CoreError::NotFound)
    }

    /// Mint a link. Returns the row **and the plaintext token, which is the
    /// only time it exists** — it is not stored and cannot be recovered.
    ///
    /// Requires `Perms::SHARE` at the target, and the link can never carry
    /// permissions the creator does not itself hold there.
    pub fn create_link(&self, user: UserId, vpath: &str, spec: &LinkSpec) -> Result<(ShareLink, String), CoreError> {
        let store = self.store()?;
        let r = self.resolve_want(user, vpath, Perms::SHARE)?;

        if spec.perms.is_empty() {
            return Err(CoreError::InvalidPath("a share link must grant at least one permission".into()));
        }
        // Escalation guard: a link is a delegation of the creator's own
        // access, so it is bounded by it.
        if !r.perms.contains(spec.perms) {
            return Err(CoreError::Denied { by: None });
        }
        if let Some(exp) = spec.expires_ns {
            if exp <= now_ns() {
                return Err(CoreError::InvalidPath("expiry is in the past".into()));
            }
        }

        let st = r.root.stat(&r.path)?;
        let is_drop = spec.perms.contains(Perms::CREATE) && !spec.perms.intersects(Perms::READ | Perms::DOWNLOAD);
        if is_drop && st.kind != Kind::Dir {
            return Err(CoreError::InvalidPath("a file-drop link must target a directory".into()));
        }

        // Best-effort: a fileid that cannot be allocated leaves the link
        // path-only, which is weaker but still correct. Refusing to create the
        // link would be worse — the metadata store is a cache and may be cold.
        //
        // The share root itself is the one path `ensure_fileid_chain` can
        // never give a *checkable* answer for: an empty `SafePath` walks zero
        // components and returns the `FileId(0)` sentinel without allocating
        // anything (`aggregate.rs` — the root has no parent to be named
        // under, so nothing is ever inserted for it). `link_target`'s
        // fileid cross-check works by looking that id back up later, and a
        // lookup for a row that was never written always comes back `None`
        // — indistinguishable from "the file was swapped", so a root link
        // would fail its own cross-check on the very next read, every time.
        // Same remedy as a cold cache: leave the link path-only instead.
        let fileid = if r.path.is_empty() {
            None
        } else {
            self.ensure_fileid_chain(&r.root, r.share, &r.path).ok()
        };

        let password_hash = match &spec.password {
            Some(pw) => {
                // §2.2's concurrency gate, not just its parameters — a link
                // creator is authenticated, but the hash cost is identical to
                // the unauthenticated verify path, so the same bound applies.
                let _permit = store.argon_gate.acquire();
                Some(
                    sc_auth::password::hash_phc(&store.cfg, &SecretString::from(pw.clone()))
                        .map_err(|e| CoreError::Internal(format!("argon2: {e}")))?,
                )
            }
            None => None,
        };

        let token = mint_token()?;
        let created = now_ns();
        let conn = store.conn()?;
        conn.execute(
            "INSERT INTO share_link (token_hash, share, path, fileid, owner, perms, password_hash, \
                                     expires_ns, max_downloads, downloads, label, created_ns) \
             VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,0,?10,?11)",
            rusqlite::params![
                &token_hash(&token)[..],
                r.share.get() as i64,
                r.path.to_display_string(),
                fileid.map(|f| f.get()),
                user.get() as i64,
                spec.perms.bits() as i64,
                password_hash,
                spec.expires_ns.map(ns_to_i64),
                spec.max_downloads.map(|v| v as i64),
                spec.label.clone(),
                ns_to_i64(created),
            ],
        )
        .map_err(db)?;
        let id = conn.last_insert_rowid();
        drop(conn);

        let link = ShareLink {
            id,
            share: r.share,
            path: r.path,
            fileid_at_creation: fileid,
            owner: user,
            perms: spec.perms,
            expires_ns: spec.expires_ns,
            max_downloads: spec.max_downloads,
            downloads: 0,
            label: spec.label.clone(),
            has_password: password_hash_present(&spec.password),
            created_ns: created,
        };
        Ok((link, token))
    }

    /// Every link `user` owns, optionally narrowed to one virtual path.
    pub fn list_links(&self, user: UserId, vpath: Option<&str>) -> Result<Vec<ShareLink>, CoreError> {
        let store = self.store()?;
        let filter = match vpath {
            Some(v) => {
                let r = self.resolve_want(user, v, Perms::READ)?;
                Some((r.share, r.path.to_display_string()))
            }
            None => None,
        };
        let conn = store.conn()?;
        let sql = format!("SELECT {SELECT_COLS} FROM share_link WHERE owner = ?1 ORDER BY id");
        let mut stmt = conn.prepare(&sql).map_err(db)?;
        let rows = stmt
            .query_map([user.get() as i64], |row| {
                let share = ShareId::new(row.get::<_, i64>("share")? as u32);
                let max_depth = self
                    .shares
                    .get(&share)
                    .map(|e| e.def.policy.max_depth)
                    .unwrap_or(64);
                row_to_link(row, max_depth)
            })
            .map_err(db)?;
        let mut out = Vec::new();
        for r in rows {
            let link = r.map_err(db)?;
            match &filter {
                Some((s, p)) if link.share != *s || &link.path.to_display_string() != p => continue,
                _ => out.push(link),
            }
        }
        Ok(out)
    }

    /// One link by id, scoped to its owner. A link belonging to somebody else
    /// answers `NotFound`, not `Denied` — an id-probing client learns nothing.
    pub fn get_link(&self, user: UserId, id: i64) -> Result<ShareLink, CoreError> {
        let link = self.link_by_id(id)?.ok_or(CoreError::NotFound)?;
        if link.owner != user {
            return Err(CoreError::NotFound);
        }
        Ok(link)
    }

    pub fn update_link(&self, user: UserId, id: i64, patch: &LinkPatch) -> Result<ShareLink, CoreError> {
        let store = self.store()?;
        let link = self.get_link(user, id)?;

        if let Some(p) = patch.perms {
            if p.is_empty() {
                return Err(CoreError::InvalidPath("a share link must grant at least one permission".into()));
            }
            // Re-check against *current* access: a grant revoked since the
            // link was minted must not be re-widened through an update.
            let effective = self.acl.effective(user, link.share, &link.path);
            if !effective.contains(Perms::SHARE) || !effective.contains(p) {
                return Err(CoreError::Denied { by: None });
            }
        }
        if let Some(Some(exp)) = patch.expires_ns {
            if exp <= now_ns() {
                return Err(CoreError::InvalidPath("expiry is in the past".into()));
            }
        }

        let conn = store.conn()?;
        if let Some(p) = patch.perms {
            conn.execute("UPDATE share_link SET perms = ?1 WHERE id = ?2", rusqlite::params![p.bits() as i64, id])
                .map_err(db)?;
        }
        if let Some(pw) = &patch.password {
            let hash = match pw {
                Some(plain) => {
                    let _permit = store.argon_gate.acquire();
                    Some(
                        sc_auth::password::hash_phc(&store.cfg, &SecretString::from(plain.clone()))
                            .map_err(|e| CoreError::Internal(format!("argon2: {e}")))?,
                    )
                }
                None => None,
            };
            conn.execute("UPDATE share_link SET password_hash = ?1 WHERE id = ?2", rusqlite::params![hash, id])
                .map_err(db)?;
        }
        if let Some(exp) = patch.expires_ns {
            conn.execute(
                "UPDATE share_link SET expires_ns = ?1 WHERE id = ?2",
                rusqlite::params![exp.map(ns_to_i64), id],
            )
            .map_err(db)?;
        }
        if let Some(m) = patch.max_downloads {
            conn.execute(
                "UPDATE share_link SET max_downloads = ?1 WHERE id = ?2",
                rusqlite::params![m.map(|v| v as i64), id],
            )
            .map_err(db)?;
        }
        if let Some(l) = &patch.label {
            conn.execute("UPDATE share_link SET label = ?1 WHERE id = ?2", rusqlite::params![l, id]).map_err(db)?;
        }
        drop(conn);
        self.get_link(user, id)
    }

    pub fn delete_link(&self, user: UserId, id: i64) -> Result<(), CoreError> {
        let store = self.store()?;
        // Ownership check first, so deleting somebody else's link is a
        // `NotFound` rather than a silent success.
        let _ = self.get_link(user, id)?;
        store
            .conn()?
            .execute("DELETE FROM share_link WHERE id = ?1 AND owner = ?2", rusqlite::params![id, user.get() as i64])
            .map_err(db)?;
        Ok(())
    }

    /// Look a link up by its plaintext token — the only place a token is ever
    /// accepted, and it is hashed before it touches the query.
    pub fn resolve_link(&self, token: &str) -> Result<Option<ShareLink>, CoreError> {
        let store = self.store()?;
        let conn = store.conn()?;
        let sql = format!("SELECT {SELECT_COLS} FROM share_link WHERE token_hash = ?1");
        let mut stmt = conn.prepare(&sql).map_err(db)?;
        stmt.query_row([&token_hash(token)[..]], |row| {
            let share = ShareId::new(row.get::<_, i64>("share")? as u32);
            let max_depth = self.shares.get(&share).map(|e| e.def.policy.max_depth).unwrap_or(64);
            row_to_link(row, max_depth)
        })
        .optional()
        .map_err(db)
    }

    /// One link by id, **without** an ownership check. Only for the public
    /// surface, which is authorized by possession of the token rather than by
    /// a session; anything user-facing must go through [`Core::get_link`].
    pub fn link_by_id(&self, id: i64) -> Result<Option<ShareLink>, CoreError> {
        let store = self.store()?;
        let conn = store.conn()?;
        let sql = format!("SELECT {SELECT_COLS} FROM share_link WHERE id = ?1");
        let mut stmt = conn.prepare(&sql).map_err(db)?;
        stmt.query_row([id], |row| {
            let share = ShareId::new(row.get::<_, i64>("share")? as u32);
            let max_depth = self.shares.get(&share).map(|e| e.def.policy.max_depth).unwrap_or(64);
            row_to_link(row, max_depth)
        })
        .optional()
        .map_err(db)
    }

    /// Check a candidate password against link `id`.
    ///
    /// **A link that does not exist still runs a full Argon2 verify** against
    /// the store's dummy hash before answering `false`. Skipping it would make
    /// "no such link" measurably faster than "wrong password", which is the
    /// existence oracle §7.2 exists to close. CPU-bound: async callers must
    /// wrap this in `spawn_blocking`.
    ///
    /// **Every Argon2 verify below goes through `store.argon_gate` first.**
    /// This is the one call in `links.rs` reachable by an anonymous caller
    /// with nothing upstream of it — no session, no login rate limit — so
    /// it is exactly the surface `crate::argon_gate`'s module doc describes:
    /// without the gate, an attacker can open as many concurrent requests as
    /// they like and each one stands up its own Argon2id buffer.
    pub fn check_link_password(&self, id: i64, candidate: &str) -> Result<bool, CoreError> {
        let store = self.store()?;
        let conn = store.conn()?;
        let hash: Option<Option<String>> = conn
            .query_row("SELECT password_hash FROM share_link WHERE id = ?1", [id], |r| r.get(0))
            .optional()
            .map_err(db)?;
        drop(conn);
        match hash {
            // A link with no password accepts anything — there is nothing to
            // check, and callers gate on `has_password` before asking. No
            // Argon2 involved, so nothing to gate.
            Some(None) => Ok(true),
            Some(Some(h)) => {
                let _permit = store.argon_gate.acquire();
                Ok(sc_auth::password::verify_phc(&h, candidate))
            }
            None => {
                let _permit = store.argon_gate.acquire();
                let _ = sc_auth::password::verify_phc(&store.dummy_hash, candidate);
                Ok(false)
            }
        }
    }

    /// Consume one download against the link's cap, atomically.
    ///
    /// The conditional `UPDATE` is the whole mechanism: read-then-write would
    /// let N concurrent requests all observe `downloads < max` and all proceed.
    /// Nothing ever decrements this again — a transfer that dies mid-stream
    /// still counts (§7.2).
    pub fn note_link_download(&self, id: i64) -> Result<(), CoreError> {
        let store = self.store()?;
        let conn = store.conn()?;
        let n = conn
            .execute(
                "UPDATE share_link SET downloads = downloads + 1 \
                 WHERE id = ?1 AND (max_downloads IS NULL OR downloads < max_downloads)",
                [id],
            )
            .map_err(db)?;
        if n == 1 {
            return Ok(());
        }
        let exists: Option<i64> = conn
            .query_row("SELECT id FROM share_link WHERE id = ?1", [id], |r| r.get(0))
            .optional()
            .map_err(db)?;
        match exists {
            Some(_) => Err(CoreError::Gone),
            None => Err(CoreError::NotFound),
        }
    }

    /// Resolve a link to its target entry, enforcing every liveness rule:
    /// expiry, download cap, and the path+fileid cross-check.
    /// Any failure is [`CoreError::Gone`] — the
    /// link is dead, and the caller must not distinguish *why* to an anonymous
    /// visitor.
    pub fn link_target(&self, link: &ShareLink) -> Result<Entry, CoreError> {
        if link.is_expired(now_ns()) || link.is_exhausted() {
            return Err(CoreError::Gone);
        }
        let root = self.root_of(link.share).map_err(|_| CoreError::Gone)?;
        let st = root.stat(&link.path).map_err(|_| CoreError::Gone)?;

        if let Some(expected) = link.fileid_at_creation {
            let current = self.meta.lookup_fileid(link.share, &st).ok().flatten();
            // `None` means the metadata row was evicted, not that the file was
            // swapped — but we cannot tell those apart, and the safe reading
            // of an unverifiable identity is "dead link".
            if current != Some(expected) {
                return Err(CoreError::Gone);
            }
        }

        let name = link.path.name().unwrap_or("").to_string();
        Ok(self.build_entry(link.share, &root, &name, &link.path, &st, link.owner))
    }

    /// Directory listing behind a link. Refused outright for file-drop links:
    /// upload-only means the holder never learns what is already in there.
    pub fn link_list(&self, link: &ShareLink) -> Result<Vec<Entry>, CoreError> {
        if link.is_drop() || !link.perms.contains(Perms::READ) {
            return Err(CoreError::Denied { by: None });
        }
        let target = self.link_target(link)?;
        if target.kind != Kind::Dir {
            return Err(CoreError::InvalidPath("not a directory".into()));
        }
        let root = self.root_of(link.share)?;
        let max_depth = root.policy().max_depth;
        let mut names: Vec<String> = root.read_dir(&link.path)?.into_iter().map(|e| e.name.to_string()).collect();
        names.sort();
        let mut out = Vec::with_capacity(names.len());
        for name in names {
            let p = link.path.join(&name, max_depth)?;
            let Ok(st) = root.stat(&p) else { continue };
            let mut e = self.build_entry(link.share, &root, &name, &p, &st, link.owner);
            // The visitor's permissions are the link's, not the owner's.
            e.perms = link.perms;
            out.push(e);
        }
        Ok(out)
    }

    /// Accept an upload through a file-drop link.
    ///
    /// Never overwrites: a name that is already taken gets the same
    /// `name (2).ext` treatment as `OnConflict::Rename`. That is the point of
    /// a drop box — an anonymous uploader must not be able to destroy, or
    /// probe for, what somebody else already put there.
    pub fn link_drop(&self, link: &ShareLink, name: &str, body: &[u8]) -> Result<Entry, CoreError> {
        if !link.perms.contains(Perms::CREATE) {
            return Err(CoreError::Denied { by: None });
        }
        if link.is_expired(now_ns()) {
            return Err(CoreError::Gone);
        }
        let dir = self.link_target(link)?;
        if dir.kind != Kind::Dir {
            return Err(CoreError::InvalidPath("not a directory".into()));
        }
        let root = self.root_of(link.share)?;
        let max_depth = root.policy().max_depth;
        let mut dest = link.path.join(name, max_depth)?;
        if crate::ops::path_exists(&root, &dest)? {
            dest = self.unique_name(&root, &link.path, name)?;
        }

        let mode = root.policy().mode_file;
        // `create_excl` is what makes "no overwrite" a kernel guarantee rather
        // than a check that a racing upload could slip past.
        let fh = root.create_excl(&dest, mode)?;
        crate::ops::write_all_at(&fh, body, 0)?;
        fh.sync_data()?;
        drop(fh);

        self.mark_dirty(link.share, &dest);
        let st = root.stat(&dest)?;
        let leaf = dest.name().unwrap_or("").to_string();
        let mut e = self.build_entry(link.share, &root, &leaf, &dest, &st, link.owner);
        e.perms = link.perms;
        Ok(e)
    }

    /// Which links point at `(share, fileid)` — used by protocol layers that
    /// have to decorate a directory listing with "this item is shared".
    pub fn links_for_node(&self, share: ShareId, fileid: FileId) -> Result<Vec<ShareLink>, CoreError> {
        let store = self.store()?;
        let conn = store.conn()?;
        let max_depth = self.shares.get(&share).map(|e| e.def.policy.max_depth).unwrap_or(64);
        let sql = format!("SELECT {SELECT_COLS} FROM share_link WHERE share = ?1 AND fileid = ?2");
        let mut stmt = conn.prepare(&sql).map_err(db)?;
        let rows = stmt
            .query_map(rusqlite::params![share.get() as i64, fileid.get()], |row| row_to_link(row, max_depth))
            .map_err(db)?;
        rows.collect::<rusqlite::Result<Vec<_>>>().map_err(db)
    }
}

fn password_hash_present(pw: &Option<String>) -> bool {
    pw.is_some()
}
