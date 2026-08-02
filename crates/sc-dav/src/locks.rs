//! RFC 4918 Class 2 write locks (`DESIGN-WEBDAV.md` §5).
//!
//! Two identities per lock, on purpose:
//!
//! * the **`FileId`** is what the lock is *on* — so it survives a rename;
//! * the **virtual path** recorded at lock time is what depth-infinity
//!   ancestor checks compare against, by path prefix. It is deliberately *not*
//!   a fileid chain: `node` rows are allocated lazily, so an ancestor may have
//!   no fileid at all, and forcing one to be minted just to answer a lock query
//!   would defeat the point of lazy allocation. The active-lock set is small,
//!   so a prefix scan over it is cheap.

use std::collections::HashMap;
use std::sync::Arc;

use sc_vfs::{FileId, ShareId, UserId};
use parking_lot::RwLock;
use uuid::Uuid;

use crate::xml::{escape_into, LockScope};

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Depth {
    Zero,
    One,
    Infinity,
}

impl Depth {
    pub fn as_str(&self) -> &'static str {
        match self {
            Depth::Zero => "0",
            Depth::One => "1",
            Depth::Infinity => "infinity",
        }
    }
}

#[derive(Clone, Debug)]
pub struct DavLock {
    pub token: Uuid,
    pub fileid: FileId,
    pub share: ShareId,
    /// Virtual path at lock time. Ancestor checks use this, see module docs.
    pub path: String,
    pub principal: UserId,
    /// Text content of the client's `<d:owner>`, re-serialised on output.
    pub owner: String,
    pub depth: Depth,
    pub scope: LockScope,
    pub expires_ns: i128,
    pub timeout_s: u32,
}

impl DavLock {
    pub fn token_urn(&self) -> String {
        format!("urn:uuid:{}", self.token)
    }
    pub fn is_expired(&self, now_ns: i128) -> bool {
        self.expires_ns <= now_ns
    }
    /// Does this lock cover `path`?
    pub fn covers(&self, share: ShareId, path: &str) -> bool {
        if self.share != share {
            return false;
        }
        if self.path == path {
            return true;
        }
        if self.depth == Depth::Infinity {
            if self.path.is_empty() {
                return true;
            }
            return path.starts_with(&self.path) && path.as_bytes().get(self.path.len()) == Some(&b'/');
        }
        false
    }
    /// Would this lock be violated by a depth-infinity lock request on `path`?
    /// (i.e. is this lock *under* `path`?)
    pub fn is_under(&self, share: ShareId, path: &str) -> bool {
        if self.share != share {
            return false;
        }
        if self.path == path {
            return true;
        }
        if path.is_empty() {
            return true;
        }
        self.path.starts_with(path) && self.path.as_bytes().get(path.len()) == Some(&b'/')
    }
}

// --------------------------------------------------------------- persistence

/// Durable backing for the lock table. Locks must outlive a restart or MS
/// Office gets very confused about who owns a document.
pub trait LockStore: Send + Sync {
    fn load_all(&self) -> anyhow::Result<Vec<DavLock>>;
    fn insert(&self, l: &DavLock) -> anyhow::Result<()>;
    fn refresh(&self, token: &Uuid, expires_ns: i128, timeout_s: u32) -> anyhow::Result<()>;
    fn remove(&self, token: &Uuid) -> anyhow::Result<()>;
    fn purge_expired(&self, now_ns: i128) -> anyhow::Result<()>;
}

/// Non-durable store. Sharing one `Arc<MemLockStore>` across two `LockManager`
/// instances is exactly how the restart test simulates a reload.
#[derive(Default)]
pub struct MemLockStore {
    rows: RwLock<HashMap<Uuid, DavLock>>,
}

impl MemLockStore {
    pub fn new() -> Self {
        Self::default()
    }
}

impl LockStore for MemLockStore {
    fn load_all(&self) -> anyhow::Result<Vec<DavLock>> {
        Ok(self.rows.read().values().cloned().collect())
    }
    fn insert(&self, l: &DavLock) -> anyhow::Result<()> {
        self.rows.write().insert(l.token, l.clone());
        Ok(())
    }
    fn refresh(&self, token: &Uuid, expires_ns: i128, timeout_s: u32) -> anyhow::Result<()> {
        if let Some(l) = self.rows.write().get_mut(token) {
            l.expires_ns = expires_ns;
            l.timeout_s = timeout_s;
        }
        Ok(())
    }
    fn remove(&self, token: &Uuid) -> anyhow::Result<()> {
        self.rows.write().remove(token);
        Ok(())
    }
    fn purge_expired(&self, now_ns: i128) -> anyhow::Result<()> {
        self.rows.write().retain(|_, l| !l.is_expired(now_ns));
        Ok(())
    }
}

#[cfg(feature = "sqlite")]
mod sqlite_store {
    use super::*;
    use rusqlite::Connection;
    use std::path::Path;

    pub struct SqliteLockStore {
        conn: parking_lot::Mutex<Connection>,
    }

    impl SqliteLockStore {
        pub fn open(path: &Path) -> anyhow::Result<Self> {
            let conn = Connection::open(path)?;
            // WAL, per `DESIGN-FOOTPRINT.md` §4. Locks are written on every
            // LOCK/UNLOCK and read on every write method; with the default
            // rollback journal a single writer locks out all readers, which
            // surfaces as `database is locked` under real concurrency.
            conn.pragma_update(None, "journal_mode", "WAL")?;
            conn.execute_batch(
                "PRAGMA synchronous = NORMAL;\
                 PRAGMA busy_timeout = 5000;\
                 PRAGMA journal_size_limit = 67108864;",
            )?;
            Self::init(&conn)?;
            Ok(SqliteLockStore {
                conn: parking_lot::Mutex::new(conn),
            })
        }

        pub fn open_in_memory() -> anyhow::Result<Self> {
            let conn = Connection::open_in_memory()?;
            Self::init(&conn)?;
            Ok(SqliteLockStore {
                conn: parking_lot::Mutex::new(conn),
            })
        }

        fn init(conn: &Connection) -> anyhow::Result<()> {
            conn.execute_batch(
                "CREATE TABLE IF NOT EXISTS dav_lock (
                     token      TEXT PRIMARY KEY,
                     fileid     INTEGER NOT NULL,
                     share      INTEGER NOT NULL,
                     path       TEXT NOT NULL,
                     principal  INTEGER NOT NULL,
                     owner      TEXT NOT NULL,
                     depth      INTEGER NOT NULL,
                     scope      INTEGER NOT NULL,
                     expires_ns TEXT NOT NULL,
                     timeout_s  INTEGER NOT NULL
                 );
                 CREATE INDEX IF NOT EXISTS dav_lock_path ON dav_lock(share, path);",
            )?;
            Ok(())
        }
    }

    impl LockStore for SqliteLockStore {
        fn load_all(&self) -> anyhow::Result<Vec<DavLock>> {
            let conn = self.conn.lock();
            let mut st = conn.prepare(
                "SELECT token, fileid, share, path, principal, owner, depth, scope, expires_ns, timeout_s FROM dav_lock",
            )?;
            let rows = st.query_map([], |r| {
                let token: String = r.get(0)?;
                let expires: String = r.get(8)?;
                let depth: i64 = r.get(6)?;
                let scope: i64 = r.get(7)?;
                Ok(DavLock {
                    token: Uuid::parse_str(&token).unwrap_or_else(|_| Uuid::nil()),
                    fileid: FileId(r.get::<_, i64>(1)?),
                    share: ShareId(r.get::<_, i64>(2)? as u32),
                    path: r.get(3)?,
                    principal: UserId(r.get::<_, i64>(4)? as u32),
                    owner: r.get(5)?,
                    depth: match depth {
                        0 => Depth::Zero,
                        1 => Depth::One,
                        _ => Depth::Infinity,
                    },
                    scope: if scope == 0 {
                        LockScope::Exclusive
                    } else {
                        LockScope::Shared
                    },
                    expires_ns: expires.parse().unwrap_or(0),
                    timeout_s: r.get::<_, i64>(9)? as u32,
                })
            })?;
            let mut out = Vec::new();
            for r in rows {
                out.push(r?);
            }
            Ok(out)
        }

        fn insert(&self, l: &DavLock) -> anyhow::Result<()> {
            self.conn.lock().execute(
                "INSERT OR REPLACE INTO dav_lock
                 (token, fileid, share, path, principal, owner, depth, scope, expires_ns, timeout_s)
                 VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10)",
                rusqlite::params![
                    l.token.to_string(),
                    l.fileid.0,
                    l.share.0 as i64,
                    l.path,
                    l.principal.0 as i64,
                    l.owner,
                    match l.depth {
                        Depth::Zero => 0i64,
                        Depth::One => 1,
                        Depth::Infinity => 2,
                    },
                    match l.scope {
                        LockScope::Exclusive => 0i64,
                        LockScope::Shared => 1,
                    },
                    l.expires_ns.to_string(),
                    l.timeout_s as i64,
                ],
            )?;
            Ok(())
        }

        fn refresh(&self, token: &Uuid, expires_ns: i128, timeout_s: u32) -> anyhow::Result<()> {
            self.conn.lock().execute(
                "UPDATE dav_lock SET expires_ns = ?2, timeout_s = ?3 WHERE token = ?1",
                rusqlite::params![token.to_string(), expires_ns.to_string(), timeout_s as i64],
            )?;
            Ok(())
        }

        fn remove(&self, token: &Uuid) -> anyhow::Result<()> {
            self.conn.lock().execute(
                "DELETE FROM dav_lock WHERE token = ?1",
                rusqlite::params![token.to_string()],
            )?;
            Ok(())
        }

        fn purge_expired(&self, now_ns: i128) -> anyhow::Result<()> {
            // expires_ns is TEXT because i128 does not fit a SQLite integer;
            // compare numerically by pulling the (small) table.
            let conn = self.conn.lock();
            let mut st = conn.prepare("SELECT token, expires_ns FROM dav_lock")?;
            let dead: Vec<String> = st
                .query_map([], |r| {
                    let t: String = r.get(0)?;
                    let e: String = r.get(1)?;
                    Ok((t, e))
                })?
                .filter_map(|r| r.ok())
                .filter(|(_, e)| e.parse::<i128>().unwrap_or(0) <= now_ns)
                .map(|(t, _)| t)
                .collect();
            drop(st);
            for t in dead {
                conn.execute("DELETE FROM dav_lock WHERE token = ?1", [t])?;
            }
            Ok(())
        }
    }
}

#[cfg(feature = "sqlite")]
pub use sqlite_store::SqliteLockStore;

// ------------------------------------------------------------------ manager

#[derive(Debug, PartialEq, Eq)]
pub enum LockOutcome {
    Granted,
    /// Someone else's conflicting lock, and no matching token was submitted.
    Conflict,
}

pub struct LockManager {
    locks: RwLock<HashMap<Uuid, DavLock>>,
    store: Arc<dyn LockStore>,
    default_timeout_s: u32,
    max_timeout_s: u32,
}

impl LockManager {
    pub fn new(store: Arc<dyn LockStore>, default_timeout_s: u32, max_timeout_s: u32) -> Self {
        let m = LockManager {
            locks: RwLock::new(HashMap::new()),
            store,
            default_timeout_s,
            max_timeout_s,
        };
        m.reload();
        m
    }

    /// Re-hydrate from the store. Called at construction; this is what makes a
    /// lock survive a restart.
    pub fn reload(&self) {
        match self.store.load_all() {
            Ok(rows) => {
                let now = now_ns();
                let mut g = self.locks.write();
                g.clear();
                for l in rows {
                    if !l.is_expired(now) {
                        g.insert(l.token, l);
                    }
                }
            }
            Err(e) => tracing::error!("lock store reload failed: {e}"),
        }
    }

    pub fn clamp_timeout(&self, requested: Option<u32>) -> u32 {
        match requested {
            None | Some(0) => self.default_timeout_s,
            Some(t) => t.min(self.max_timeout_s),
        }
    }

    /// Active (non-expired) locks that cover `path`, including via a
    /// depth-infinity ancestor.
    pub fn covering(&self, share: ShareId, path: &str) -> Vec<DavLock> {
        let now = now_ns();
        self.locks
            .read()
            .values()
            .filter(|l| !l.is_expired(now) && l.covers(share, path))
            .cloned()
            .collect()
    }

    /// Locks recorded exactly at `path` — what `lockdiscovery` reports.
    pub fn at(&self, share: ShareId, path: &str) -> Vec<DavLock> {
        let now = now_ns();
        self.locks
            .read()
            .values()
            .filter(|l| !l.is_expired(now) && l.share == share && l.path == path)
            .cloned()
            .collect()
    }

    pub fn by_token(&self, token: &str) -> Option<DavLock> {
        let uuid = parse_token(token)?;
        let now = now_ns();
        self.locks
            .read()
            .get(&uuid)
            .filter(|l| !l.is_expired(now))
            .cloned()
    }

    /// May `who` write to `path` given the submitted tokens?
    ///
    /// Returns the blocking lock when the answer is no.
    pub fn check_write(
        &self,
        share: ShareId,
        path: &str,
        submitted: &[&str],
        _who: UserId,
    ) -> Option<DavLock> {
        let submitted: Vec<Uuid> = submitted.iter().filter_map(|t| parse_token(t)).collect();
        self.covering(share, path)
            .into_iter()
            .find(|l| !submitted.contains(&l.token))
    }

    /// Attempt to take a lock. `path` is the virtual path (no leading slash).
    #[allow(clippy::too_many_arguments)]
    pub fn acquire(
        &self,
        share: ShareId,
        path: &str,
        fileid: FileId,
        principal: UserId,
        owner: String,
        depth: Depth,
        scope: LockScope,
        timeout_s: u32,
        submitted: &[&str],
    ) -> Result<DavLock, LockOutcome> {
        let submitted: Vec<Uuid> = submitted.iter().filter_map(|t| parse_token(t)).collect();
        let now = now_ns();
        let mut g = self.locks.write();
        g.retain(|_, l| !l.is_expired(now));

        for l in g.values() {
            let clash = l.covers(share, path)
                || (depth == Depth::Infinity && l.is_under(share, path));
            if !clash {
                continue;
            }
            // Shared locks coexist with shared locks; everything else conflicts
            // unless the caller already holds the offending lock.
            let compatible = scope == LockScope::Shared && l.scope == LockScope::Shared;
            if !compatible && !submitted.contains(&l.token) {
                return Err(LockOutcome::Conflict);
            }
            if !compatible && submitted.contains(&l.token) && l.principal != principal {
                return Err(LockOutcome::Conflict);
            }
        }

        let lock = DavLock {
            token: Uuid::new_v4(),
            fileid,
            share,
            path: path.to_string(),
            principal,
            owner,
            depth,
            scope,
            expires_ns: now + (timeout_s as i128) * 1_000_000_000,
            timeout_s,
        };
        if let Err(e) = self.store.insert(&lock) {
            tracing::error!("lock persist failed: {e}");
        }
        g.insert(lock.token, lock.clone());
        Ok(lock)
    }

    /// LOCK with an `If`/`Lock-Token` refresh (no body).
    pub fn refresh(&self, token: &str, timeout_s: u32) -> Option<DavLock> {
        let uuid = parse_token(token)?;
        let now = now_ns();
        let mut g = self.locks.write();
        let l = g.get_mut(&uuid)?;
        if l.is_expired(now) {
            return None;
        }
        l.expires_ns = now + (timeout_s as i128) * 1_000_000_000;
        l.timeout_s = timeout_s;
        let out = l.clone();
        drop(g);
        let _ = self.store.refresh(&uuid, out.expires_ns, timeout_s);
        Some(out)
    }

    pub fn release(&self, token: &str) -> bool {
        let Some(uuid) = parse_token(token) else {
            return false;
        };
        let removed = self.locks.write().remove(&uuid).is_some();
        if removed {
            let _ = self.store.remove(&uuid);
        }
        removed
    }

    /// Drop everything that has expired. Driven by a 60 s sweep in
    /// `DavService::spawn_lock_sweeper`, and called opportunistically here.
    pub fn sweep(&self) -> usize {
        let now = now_ns();
        let mut g = self.locks.write();
        let before = g.len();
        g.retain(|_, l| !l.is_expired(now));
        let removed = before - g.len();
        drop(g);
        if removed > 0 {
            let _ = self.store.purge_expired(now);
        }
        removed
    }

    /// Test seam: pretend `secs` have passed.
    #[doc(hidden)]
    pub fn expire_all_older_than(&self, cutoff_ns: i128) {
        let mut g = self.locks.write();
        for l in g.values_mut() {
            if l.expires_ns > cutoff_ns {
                l.expires_ns = cutoff_ns;
            }
        }
        let tokens: Vec<Uuid> = g.keys().copied().collect();
        drop(g);
        for t in tokens {
            let _ = self.store.refresh(&t, cutoff_ns, 0);
        }
    }

    pub fn default_timeout_s(&self) -> u32 {
        self.default_timeout_s
    }
}

pub fn now_ns() -> i128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i128)
        .unwrap_or(0)
}

/// Accept `urn:uuid:…`, `<urn:uuid:…>`, and a bare UUID — clients send all three.
pub fn parse_token(t: &str) -> Option<Uuid> {
    let t = t.trim();
    let t = t.strip_prefix('<').unwrap_or(t);
    let t = t.strip_suffix('>').unwrap_or(t);
    let t = t.strip_prefix("urn:uuid:").unwrap_or(t);
    Uuid::parse_str(t).ok()
}

/// `<d:lockdiscovery>` content for a set of locks. Generated by us, never
/// echoed from the client — `owner` is escaped on the way out.
pub fn lockdiscovery_xml(locks: &[DavLock], href_prefix: &str) -> String {
    let mut s = String::new();
    for l in locks {
        s.push_str("<d:activelock><d:locktype><d:write/></d:locktype><d:lockscope>");
        s.push_str(match l.scope {
            LockScope::Exclusive => "<d:exclusive/>",
            LockScope::Shared => "<d:shared/>",
        });
        s.push_str("</d:lockscope><d:depth>");
        s.push_str(l.depth.as_str());
        s.push_str("</d:depth><d:owner>");
        escape_into(&l.owner, &mut s);
        s.push_str("</d:owner><d:timeout>Second-");
        s.push_str(&remaining_secs(l).to_string());
        s.push_str("</d:timeout><d:locktoken><d:href>");
        escape_into(&l.token_urn(), &mut s);
        s.push_str("</d:href></d:locktoken><d:lockroot><d:href>");
        escape_into(href_prefix, &mut s);
        s.push_str("</d:href></d:lockroot></d:activelock>");
    }
    s
}

pub fn remaining_secs(l: &DavLock) -> u32 {
    let d = l.expires_ns - now_ns();
    if d <= 0 {
        0
    } else {
        (d / 1_000_000_000) as u32
    }
}

/// `Timeout: Second-300`, `Timeout: Infinite`. `Infinite` is refused and
/// clamped to the configured maximum rather than honoured.
pub fn parse_timeout_header(v: &str) -> Option<u32> {
    for part in v.split(',') {
        let p = part.trim();
        if p.eq_ignore_ascii_case("infinite") {
            return Some(u32::MAX);
        }
        if let Some(rest) = p
            .strip_prefix("Second-")
            .or_else(|| p.strip_prefix("second-"))
            .or_else(|| p.strip_prefix("SECOND-"))
        {
            if let Ok(n) = rest.trim().parse::<u32>() {
                return Some(n);
            }
        }
    }
    None
}
