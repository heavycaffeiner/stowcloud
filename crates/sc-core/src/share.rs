//! Share registration: opens the `ShareRoot` (which itself validates the
//! filesystem gate and records `root_dev`/`fstype` — `ShareRoot::open`,
//! `DESIGN-CORE.md` §1.1) and keeps it alongside the admin-facing `ShareDef`.
//!
//! ## Persisted, admin-created shares
//!
//! Config-file shares (`sc-server::app::register_shares`) get their
//! `ShareId` from their position in `config.toml`'s `shares` array
//! (`ShareId::new(i as u32 + 1)`) — a share created from the admin UI at
//! runtime has no array slot to take a number from. [`ShareStore`] persists
//! those shares in their own database (same reasoning as `acl_store.rs`'s
//! grants: a share is not reconstructible from the filesystem, so it cannot
//! live in `meta.db`), and [`DYNAMIC_SHARE_ID_BASE`] keeps their live
//! `ShareId`s out of the config-derived `1..=N` range so the two schemes can
//! never collide.
//!
//! ## Editing a config-file share
//!
//! A config-file share has no row of its own to rewrite, so an edit is kept
//! as an *override* keyed by its `ShareId` — `share_identity_override` for
//! name/host path, `share_trash_override` for trash. `register_shares`
//! applies them as it builds each `ShareDef` at startup, which is what makes
//! an edit survive a restart rather than being silently reverted by the
//! config file. The config file keeps declaring the share (so it is still
//! the thing that decides a share *exists*); the override decides what it is
//! called and where it points.

use std::path::{Path, PathBuf};
use std::sync::Arc;

use sc_vfs::{ShareId, SharePolicy, ShareRoot, TrashMode};
use r2d2::Pool;
use r2d2_sqlite::SqliteConnectionManager;
use rusqlite::{Connection, OpenFlags, OptionalExtension};

use crate::error::CoreError;

#[derive(Clone, Debug)]
pub struct ShareDef {
    pub id: ShareId,
    pub name: String,
    pub host_path: PathBuf,
    pub policy: SharePolicy,
    pub shared_externally: bool,
}

pub(crate) struct ShareEntry {
    pub def: ShareDef,
    pub root: Arc<ShareRoot>,
}

/// Below this, a `ShareId` came from `config.toml`'s array position and is
/// read-only from the admin API (editing it here would be silently reverted
/// on the next restart, since `register_shares` recomputes those ids from
/// the config file every time). At or above it, `(id - BASE)` is a `share_`
/// table rowid. No deployment configures anywhere near a million shares in
/// `config.toml`, so the ranges never meet.
pub const DYNAMIC_SHARE_ID_BASE: u32 = 1_000_000;

const SCHEMA_SQL: &str = "
CREATE TABLE IF NOT EXISTS share_ (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  host_path  TEXT NOT NULL,
  created_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS share_trash_override (
  share_id INTEGER PRIMARY KEY,
  enabled  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS share_identity_override (
  share_id  INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  host_path TEXT NOT NULL
);
";

enum Target {
    File(PathBuf),
    Memory(String),
}

/// SQLite-backed persistence for admin-created [`ShareDef`]s. See the module
/// doc for why these need their own database and their own id range.
pub struct ShareStore {
    pool: Pool<SqliteConnectionManager>,
    _keepalive: parking_lot::Mutex<Option<Connection>>,
}

impl ShareStore {
    pub fn open(path: &Path) -> anyhow::Result<Self> {
        Self::open_with(Target::File(path.to_path_buf()))
    }

    /// Shared-cache in-memory store, for tests — see `AclStore::open_in_memory`
    /// for why a plain `:memory:` won't do (every pooled connection would get
    /// its own empty database).
    pub fn open_in_memory() -> anyhow::Result<Self> {
        static COUNTER: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let n = COUNTER.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let uri = format!("file:sc_share_mem_{}_{n}?mode=memory&cache=shared", std::process::id());
        Self::open_with(Target::Memory(uri))
    }

    fn open_with(target: Target) -> anyhow::Result<Self> {
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
        .with_init(|c| c.execute_batch("PRAGMA busy_timeout = 5000;"));
        let pool = Pool::builder().max_size(4).build(manager)?;

        let keepalive = match target {
            Target::File(_) => None,
            Target::Memory(_) => Some(bootstrap),
        };
        Ok(Self { pool, _keepalive: parking_lot::Mutex::new(keepalive) })
    }

    fn conn(&self) -> Result<r2d2::PooledConnection<SqliteConnectionManager>, CoreError> {
        self.pool.get().map_err(|e| CoreError::Internal(format!("share db: {e}")))
    }

    /// Every persisted share, in no particular order — callers sort/filter
    /// as needed. `rowid` is what `DYNAMIC_SHARE_ID_BASE` is added to.
    fn list_rows(&self) -> Result<Vec<(i64, String, PathBuf)>, CoreError> {
        let conn = self.conn()?;
        let mut stmt = conn.prepare("SELECT id, name, host_path FROM share_").map_err(db)?;
        let rows = stmt
            .query_map([], |row| {
                let id: i64 = row.get(0)?;
                let name: String = row.get(1)?;
                let host_path: String = row.get(2)?;
                Ok((id, name, PathBuf::from(host_path)))
            })
            .map_err(db)?;
        let mut out = Vec::new();
        for r in rows {
            out.push(r.map_err(db)?);
        }
        Ok(out)
    }

    /// This share's persisted trash on/off override, if one was ever set.
    /// Keyed by the real `ShareId` rather than a `share_` rowid — unlike
    /// that table, this one applies uniformly to a `config.toml` share
    /// (id `1..`) and an admin-created one (id `DYNAMIC_SHARE_ID_BASE..`),
    /// which is what lets a config-file share's trash be toggled from the
    /// admin UI without touching `config.toml` (`Core::update_share`).
    fn trash_override(&self, share_id: u32) -> Result<Option<bool>, CoreError> {
        self.conn()?
            .query_row(
                "SELECT enabled FROM share_trash_override WHERE share_id = ?1",
                [share_id],
                |row| row.get::<_, i64>(0),
            )
            .optional()
            .map_err(db)
            .map(|v| v.map(|n| n != 0))
    }

    /// Upsert this share's trash override.
    fn set_trash_override(&self, share_id: u32, enabled: bool) -> Result<(), CoreError> {
        self.conn()?
            .execute(
                "INSERT INTO share_trash_override (share_id, enabled) VALUES (?1, ?2)
                 ON CONFLICT(share_id) DO UPDATE SET enabled = excluded.enabled",
                rusqlite::params![share_id, enabled as i64],
            )
            .map_err(db)?;
        Ok(())
    }

    /// This share's persisted name/host-path override, if one was ever set.
    /// Only a `config.toml` share ever has one — a dynamic share owns a
    /// `share_` row and is edited there instead.
    fn identity_override(&self, share_id: u32) -> Result<Option<(String, PathBuf)>, CoreError> {
        self.conn()?
            .query_row(
                "SELECT name, host_path FROM share_identity_override WHERE share_id = ?1",
                [share_id],
                |row| Ok((row.get::<_, String>(0)?, PathBuf::from(row.get::<_, String>(1)?))),
            )
            .optional()
            .map_err(db)
    }

    /// Upsert this share's name/host-path override.
    fn set_identity_override(&self, share_id: u32, name: &str, host_path: &Path) -> Result<(), CoreError> {
        self.conn()?
            .execute(
                "INSERT INTO share_identity_override (share_id, name, host_path) VALUES (?1, ?2, ?3)
                 ON CONFLICT(share_id) DO UPDATE SET name = excluded.name, host_path = excluded.host_path",
                rusqlite::params![share_id, name, host_path.to_string_lossy()],
            )
            .map_err(db)?;
        Ok(())
    }
}

fn db(e: rusqlite::Error) -> CoreError {
    CoreError::Internal(format!("share db: {e}"))
}

fn now_ns() -> i128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i128)
        .unwrap_or(0)
}

fn ns_to_i64(v: i128) -> i64 {
    v.clamp(i64::MIN as i128, i64::MAX as i128) as i64
}

/// Real vs. mode-bits-predicted access to a share's host root, probed at
/// registration time (item 45). `ShareRoot::open` resolves `host_path` with
/// `O_PATH` on Linux (`sc-vfs`'s `backend/linux.rs`), which only needs search
/// permission on the parent directories and proves nothing about whether
/// this process can actually read or write *through* the path — so a share
/// can be accepted here and only fail later, per-request, deep in a syscall
/// (the concrete case: a share co-accessed by Jellyfin and Samba where
/// ownership/mode drift after a `chown` or bind-mount).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AccessProbe {
    pub readable: bool,
    pub writable: bool,
    /// Owner/group/other mode bits predicted this process could write here,
    /// but the real access check disagrees — an ACL, a capability, or a
    /// read-only mount is overriding the mode bits. This is the "mismatch"
    /// item 45 warns on. The opposite direction (mode bits say no, the real
    /// check says yes — e.g. a capability grants more than the bits show)
    /// is not reported: extra access beyond what the bits suggest is not
    /// the failure mode this probe exists to catch, and a share that is
    /// readable but not writable is legitimate on its own.
    pub write_mismatch: bool,
}

/// Would a classic (ACL-oblivious) unix permission check predict this
/// process can write to a file owned by `file_uid`/`file_gid` with the
/// given `mode`? Pure integer logic, no syscalls, so it is unit-testable on
/// any platform — `probe_access`'s Linux-only body below is the only
/// caller, feeding it real ids from `geteuid`/`getegid`/`getgroups` and the
/// real `stat` mode bits.
// Only called from `probe_access`'s Linux body; on other platforms it is
// still compiled (so its unit tests below run on every dev platform) but
// otherwise unused.
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn mode_predicts_writable(euid: u32, egid: u32, groups: &[u32], file_uid: u32, file_gid: u32, mode: u32) -> bool {
    if euid == 0 {
        return true; // root's real access is gated by mounts/capabilities, not mode bits
    }
    if euid == file_uid {
        return mode & 0o200 != 0;
    }
    if egid == file_gid || groups.contains(&file_gid) {
        return mode & 0o020 != 0;
    }
    mode & 0o002 != 0
}

/// Probe `path`'s real access with `faccessat2` (via `rustix::fs::accessat`,
/// `AtFlags::EACCESS` — `faccessat2` on Linux 5.8+, emulated `faccessat` on
/// older kernels) rather than inferring from `stat` mode bits alone, which
/// ignore ACLs, capabilities and read-only mounts. `None` if `path` can't
/// even be `stat`ed (already reported elsewhere — `validate_host_path` or
/// `ShareRoot::open` itself).
#[cfg(target_os = "linux")]
pub fn probe_access(path: &Path) -> Option<AccessProbe> {
    use std::os::unix::fs::MetadataExt;

    use rustix::fs::{accessat, Access, AtFlags, CWD};
    use rustix::process::{geteuid, getegid, getgroups};

    let meta = std::fs::metadata(path).ok()?;
    let readable = accessat(CWD, path, Access::READ_OK | Access::EXEC_OK, AtFlags::EACCESS).is_ok();
    let writable = accessat(CWD, path, Access::WRITE_OK, AtFlags::EACCESS).is_ok();
    let groups: Vec<u32> = getgroups().map(|gs| gs.iter().map(|g| g.as_raw()).collect()).unwrap_or_default();
    let predicted_writable =
        mode_predicts_writable(geteuid().as_raw(), getegid().as_raw(), &groups, meta.uid(), meta.gid(), meta.mode());
    Some(AccessProbe { readable, writable, write_mismatch: predicted_writable && !writable })
}

#[cfg(not(target_os = "linux"))]
pub fn probe_access(_path: &Path) -> Option<AccessProbe> {
    None
}

/// Warn (never refuse — an over-strict startup check has already taken
/// production down once, when `sc-auth`'s master-key verification refused
/// to start over one unreadable SMB secret; this must not repeat that) when
/// [`probe_access`] finds the real access to `host_path` disagrees with
/// what a share is expected to have. Fires from every `register_share`
/// call, so it covers config-file shares at startup and admin-created
/// shares registered or edited at runtime alike; `diagnostics.rs` separately
/// reports the same check for config-file shares through the startup
/// diagnostics channel, ahead of any of this ever running.
fn warn_on_access_mismatch(name: &str, host_path: &Path) {
    let Some(probe) = probe_access(host_path) else { return };
    if !probe.readable {
        tracing::warn!(
            share = %name, path = %host_path.display(),
            "share root is not actually readable by this process (ACL, capability, or read-only mount overriding the mode bits) — reads through this share will fail at request time"
        );
    }
    if probe.write_mismatch {
        tracing::warn!(
            share = %name, path = %host_path.display(),
            "share root's permission bits look writable but the real access check disagrees (ACL, capability, or read-only mount) — writes through this share will fail at request time"
        );
    }
}

/// Pre-flight validation for a candidate `host_path`, ahead of
/// `ShareRoot::open`'s own (coarser) filesystem gate. Exists so the four
/// failure modes the admin UI needs to distinguish — nonexistent path, not a
/// directory, unreadable, overlapping share — actually reach the client:
/// the generic `VfsError -> CoreError` mapping (`error.rs`) collapses most
/// of `ShareRoot::open`'s own failures into an unhelpful `Io`/`Denied`.
fn validate_host_path(path: &Path) -> Result<(), CoreError> {
    let meta = std::fs::metadata(path).map_err(|e| {
        let p = path.display().to_string();
        match e.kind() {
            std::io::ErrorKind::NotFound => {
                CoreError::invalid("share.path_does_not_exist", vec![("path", p.clone())], format!("path does not exist: {p}"))
            }
            std::io::ErrorKind::PermissionDenied => CoreError::invalid(
                "share.path_permission_denied",
                vec![("path", p.clone())],
                format!("permission denied reading path: {p}"),
            ),
            _ => CoreError::invalid(
                "share.path_unreadable",
                vec![("path", p.clone()), ("error", e.to_string())],
                format!("cannot read path {p}: {e}"),
            ),
        }
    })?;
    if !meta.is_dir() {
        let p = path.display().to_string();
        return Err(CoreError::invalid("share.path_not_a_directory", vec![("path", p.clone())], format!("not a directory: {p}")));
    }
    Ok(())
}

impl crate::Core {
    /// Open `def.host_path` as a share root. `ShareRoot::open` itself
    /// rejects filesystems that can't provide stable inode identity
    /// end-to-end (overlayfs — `FsType::is_rejected`, `ARCHITECTURE.md`
    /// "filesystem gate").
    pub fn register_share(&self, def: ShareDef) -> anyhow::Result<()> {
        let root = Arc::new(ShareRoot::open(def.id, &def.host_path, def.policy.clone())?);
        warn_on_access_mismatch(&def.name, &def.host_path);
        self.shares.insert(def.id, Arc::new(ShareEntry { def, root }));
        Ok(())
    }

    pub fn share(&self, id: ShareId) -> Option<Arc<ShareRoot>> {
        self.shares.get(&id).map(|e| e.root.clone())
    }

    /// One share's definition. `roots()` needs this per entry, which
    /// `share_defs()` would answer by cloning and sorting the whole table.
    pub fn share_def(&self, id: ShareId) -> Option<ShareDef> {
        self.shares.get(&id).map(|e| e.def.clone())
    }

    /// Every registered share, in id order. The admin-facing surfaces
    /// (storage accounting, the Samba config renderer) have to enumerate
    /// shares rather than look one up, and `ShareId`s are not dense.
    pub fn share_defs(&self) -> Vec<ShareDef> {
        let mut out: Vec<ShareDef> = self.shares.iter().map(|e| e.def.clone()).collect();
        out.sort_by_key(|d| d.id.get());
        out
    }

    /// The share's host path. `sc-vfs` deliberately never exposes this from
    /// `ShareRoot` itself (`DESIGN-CORE.md` §1: nothing above `sc-vfs` sees
    /// a host path in the *request-handling* path — `ARCHITECTURE.md` §13's
    /// checklist item is about API responses/errors/logs, not trusted
    /// server-side infrastructure). `sc-watch` needs a real path to hand to
    /// the OS watch API (`inotify`/`ReadDirectoryChangesW`), so this is the
    /// one sanctioned escape hatch — callers outside this process's own
    /// infra must never surface it.
    pub fn share_host_path(&self, id: ShareId) -> Option<std::path::PathBuf> {
        self.shares.get(&id).map(|e| e.def.host_path.clone())
    }

    /// Attach the share store. Idempotent-by-refusal, same contract as
    /// [`Core::attach_acl_store`]: a second call is a wiring bug, not a
    /// runtime condition.
    pub fn attach_share_store(&self, store: ShareStore) -> anyhow::Result<()> {
        self.share_store.set(Arc::new(store)).map_err(|_| anyhow::anyhow!("share store already attached"))
    }

    pub fn share_store_enabled(&self) -> bool {
        self.share_store.get().is_some()
    }

    /// Apply this share's persisted trash override (if any) on top of
    /// `policy`. Every call site that builds a `ShareDef`'s policy funnels
    /// through here so a toggle set via `update_share`'s `trash_enabled`
    /// survives a restart and applies the same way whether the share came
    /// from `config.toml` or the admin API. No-op (keeps `policy.trash` as
    /// given, `TrashMode::Off` by default) when no store is attached or no
    /// override was ever set for this id.
    pub fn apply_trash_override(&self, id: ShareId, mut policy: SharePolicy) -> SharePolicy {
        if let Some(store) = self.share_store.get() {
            if let Ok(Some(enabled)) = store.trash_override(id.get()) {
                policy.trash = if enabled { TrashMode::ShareLocal } else { TrashMode::Off };
            }
        }
        policy
    }

    /// Apply this share's persisted name/host-path override, if one was set
    /// from the admin UI. `register_shares` calls it for every `config.toml`
    /// share so an edit made in the Web UI is what actually gets registered —
    /// without this the config file would win back on every restart, which is
    /// the only reason those edits used to be refused outright.
    ///
    /// Logged, because the config file then no longer describes what is
    /// running and an operator reading it deserves to be told so.
    pub fn apply_identity_override(&self, id: ShareId, name: String, host_path: PathBuf) -> (String, PathBuf) {
        let Some(store) = self.share_store.get() else { return (name, host_path) };
        match store.identity_override(id.get()) {
            Ok(Some((new_name, new_path))) => {
                if new_name != name || new_path != host_path {
                    tracing::info!(
                        config_name = %name, config_path = %host_path.display(),
                        name = %new_name, path = %new_path.display(),
                        "share was edited in the admin UI; the config.toml entry's name/path are overridden"
                    );
                }
                (new_name, new_path)
            }
            _ => (name, host_path),
        }
    }

    fn share_db(&self) -> Result<&ShareStore, CoreError> {
        self.share_store
            .get()
            .map(|s| s.as_ref())
            .ok_or_else(|| CoreError::NotSupported("share management is not configured on this server".into()))
    }

    /// Register every persisted share found in the store, same
    /// log-and-skip contract as `sc-server::app::register_shares` uses for
    /// config-file shares. A no-op if no store is attached (e.g. a test
    /// fixture) — that is not a wiring bug the way a second `attach` is.
    pub fn load_persisted_shares(&self) -> anyhow::Result<()> {
        let Some(store) = self.share_store.get() else { return Ok(()) };
        for (rowid, name, host_path) in store.list_rows()? {
            let id = ShareId::new(DYNAMIC_SHARE_ID_BASE + rowid as u32);
            let policy = self.apply_trash_override(id, SharePolicy::default());
            let def = ShareDef { id, name: name.clone(), host_path, policy, shared_externally: false };
            if let Err(e) = self.register_share(def) {
                tracing::error!(share = %name, error = %e, "persisted share rejected at startup; not registered");
            }
        }
        Ok(())
    }

    /// A share (by name or host path) that would conflict with `candidate`,
    /// if any — `exclude` skips a share being edited in place. Compares
    /// canonicalized paths so `/srv/photos` and `/srv/photos/` (or a
    /// symlinked equivalent) are recognized as the same or nested location.
    fn overlapping_share(&self, candidate: &Path, exclude: Option<ShareId>) -> Option<String> {
        let candidate = std::fs::canonicalize(candidate).unwrap_or_else(|_| candidate.to_path_buf());
        for def in self.share_defs() {
            if Some(def.id) == exclude {
                continue;
            }
            let existing = std::fs::canonicalize(&def.host_path).unwrap_or_else(|_| def.host_path.clone());
            if candidate.starts_with(&existing) || existing.starts_with(&candidate) {
                return Some(def.name);
            }
        }
        None
    }

    fn share_name_taken(&self, name: &str, exclude: Option<ShareId>) -> bool {
        self.share_defs().iter().any(|d| Some(d.id) != exclude && d.name == name)
    }

    /// Create and immediately register a new share — `POST /api/admin/shares`.
    /// Validates the four failure modes the admin UI needs to tell apart
    /// (empty/duplicate name, nonexistent path, not a directory, unreadable,
    /// overlapping an existing share) before ever touching the store or
    /// `ShareRoot::open`, so a rejected share never leaves a half-written row.
    pub fn create_share(&self, name: String, host_path: PathBuf) -> Result<ShareDef, CoreError> {
        let store = self.share_db()?;
        let name = name.trim().to_string();
        if name.is_empty() {
            return Err(CoreError::invalid("share.name_empty", vec![], "share name must not be empty"));
        }
        if self.share_name_taken(&name, None) {
            return Err(CoreError::invalid(
                "share.name_taken",
                vec![("name", name.clone())],
                format!("a share named '{name}' already exists"),
            ));
        }
        validate_host_path(&host_path)?;
        if let Some(other) = self.overlapping_share(&host_path, None) {
            return Err(CoreError::invalid(
                "share.path_overlaps",
                vec![("other", other.clone())],
                format!("overlaps existing share '{other}'"),
            ));
        }

        let conn = store.conn()?;
        conn.execute(
            "INSERT INTO share_ (name, host_path, created_ns) VALUES (?1, ?2, ?3)",
            rusqlite::params![name, host_path.to_string_lossy(), ns_to_i64(now_ns())],
        )
        .map_err(db)?;
        let rowid = conn.last_insert_rowid();
        drop(conn);

        let id = ShareId::new(DYNAMIC_SHARE_ID_BASE + rowid as u32);
        let def = ShareDef {
            id,
            name,
            host_path,
            policy: self.apply_trash_override(id, SharePolicy::default()),
            shared_externally: false,
        };
        if let Err(e) = self.register_share(def.clone()) {
            // Not one of the four pre-checked failure modes (most likely a
            // rejected filesystem type) — roll back so it doesn't linger as
            // a row with no live share behind it.
            let _ = store.conn().and_then(|c| c.execute("DELETE FROM share_ WHERE id = ?1", [rowid]).map_err(db));
            return Err(CoreError::Internal(format!("share rejected: {e}")));
        }
        Ok(def)
    }

    /// Rename, repoint and/or toggle trash on an existing share —
    /// `PATCH /api/admin/shares/{id}`. Works on a `config.toml` share too:
    /// a share below [`DYNAMIC_SHARE_ID_BASE`] has no `share_` row of its
    /// own, so its new name/path are written as an override that
    /// `register_shares` reapplies at startup (see the module doc). This is
    /// what the refusal that used to live here was protecting against — an
    /// edit the next restart would silently undo — and it is now handled
    /// rather than forbidden.
    pub fn update_share(
        &self,
        id: ShareId,
        name: Option<String>,
        host_path: Option<PathBuf>,
        trash_enabled: Option<bool>,
    ) -> Result<ShareDef, CoreError> {
        let editing_identity = name.is_some() || host_path.is_some();
        let store = self.share_db()?;
        let existing = self.share_defs().into_iter().find(|d| d.id == id).ok_or(CoreError::NotFound)?;

        let new_name = match name {
            Some(n) => {
                let n = n.trim().to_string();
                if n.is_empty() {
                    return Err(CoreError::invalid("share.name_empty", vec![], "share name must not be empty"));
                }
                if self.share_name_taken(&n, Some(id)) {
                    return Err(CoreError::invalid(
                        "share.name_taken",
                        vec![("name", n.clone())],
                        format!("a share named '{n}' already exists"),
                    ));
                }
                n
            }
            None => existing.name.clone(),
        };
        let new_host_path = match host_path {
            Some(p) => {
                validate_host_path(&p)?;
                if let Some(other) = self.overlapping_share(&p, Some(id)) {
                    return Err(CoreError::invalid(
                        "share.path_overlaps",
                        vec![("other", other.clone())],
                        format!("overlaps existing share '{other}'"),
                    ));
                }
                p
            }
            None => existing.host_path.clone(),
        };

        // A dynamic share owns a `share_` row; a config-file share does not,
        // and is edited through the override table `register_shares` reads.
        if editing_identity {
            if id.get() >= DYNAMIC_SHARE_ID_BASE {
                let rowid = (id.get() - DYNAMIC_SHARE_ID_BASE) as i64;
                store
                    .conn()?
                    .execute(
                        "UPDATE share_ SET name = ?1, host_path = ?2 WHERE id = ?3",
                        rusqlite::params![new_name, new_host_path.to_string_lossy(), rowid],
                    )
                    .map_err(db)?;
            } else {
                store.set_identity_override(id.get(), &new_name, &new_host_path)?;
            }
        }

        let mut policy = existing.policy;
        if let Some(enabled) = trash_enabled {
            store.set_trash_override(id.get(), enabled)?;
            policy.trash = if enabled { TrashMode::ShareLocal } else { TrashMode::Off };
        }

        let def = ShareDef {
            id,
            name: new_name,
            host_path: new_host_path,
            policy,
            shared_externally: existing.shared_externally,
        };
        // Re-insert under the same id: the live `ShareRoot` this replaces in
        // `self.shares` may still be referenced by an in-flight request
        // holding its own `Arc` clone, which is fine — it finishes against
        // the old root, and every request after this point sees the new one.
        self.register_share(def.clone()).map_err(|e| CoreError::Internal(format!("share rejected: {e}")))?;
        Ok(def)
    }

    /// Unregister and forget an admin-created share —
    /// `DELETE /api/admin/shares/{id}`. Unlike `update_share`, this still
    /// refuses a `config.toml` share: an edited one can be overridden because
    /// the config entry keeps declaring it, but nothing here can stop that
    /// entry from declaring it — the next restart would bring it back, and
    /// there would be nothing to override.
    ///
    /// Cascades to every grant that named this share: a
    /// grant on a share that no longer exists is not a security hole
    /// (default-deny already covers it), but leaving it behind would make
    /// the admin grant list show entries for a share picker that no longer
    /// offers that share.
    pub fn delete_share(&self, id: ShareId) -> Result<(), CoreError> {
        if id.get() < DYNAMIC_SHARE_ID_BASE {
            return Err(CoreError::invalid(
                "share.config_defined_not_deletable",
                vec![],
                "this share is defined in the config file and cannot be deleted here; edit the config and restart",
            ));
        }
        let store = self.share_db()?;
        if !self.shares.contains_key(&id) {
            return Err(CoreError::NotFound);
        }
        if let Ok(grants) = self.list_grants(&crate::GrantFilter { principal: None, share: Some(id) }) {
            for rec in grants {
                let _ = self.delete_grant(rec.grant.id);
            }
        }
        let rowid = (id.get() - DYNAMIC_SHARE_ID_BASE) as i64;
        store.conn()?.execute("DELETE FROM share_ WHERE id = ?1", [rowid]).map_err(db)?;
        self.shares.remove(&id);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::mode_predicts_writable;

    #[test]
    fn root_can_write_regardless_of_mode_bits() {
        assert!(mode_predicts_writable(0, 0, &[], 1000, 1000, 0o000));
    }

    #[test]
    fn owner_match_follows_the_owner_write_bit() {
        assert!(mode_predicts_writable(1000, 1000, &[], 1000, 1000, 0o600));
        assert!(!mode_predicts_writable(1000, 1000, &[], 1000, 1000, 0o400));
    }

    #[test]
    fn primary_group_match_follows_the_group_write_bit() {
        assert!(mode_predicts_writable(1000, 2000, &[], 999, 2000, 0o060));
        assert!(!mode_predicts_writable(1000, 2000, &[], 999, 2000, 0o040));
    }

    #[test]
    fn supplementary_group_membership_also_counts() {
        // egid is 2000, but the file's group (3000) is one of this
        // process's *supplementary* groups — must still follow the group
        // bit, not fall through to "other".
        assert!(mode_predicts_writable(1000, 2000, &[3000], 999, 3000, 0o060));
        assert!(!mode_predicts_writable(1000, 2000, &[4000], 999, 3000, 0o060));
    }

    #[test]
    fn neither_owner_nor_group_follows_the_other_write_bit() {
        assert!(mode_predicts_writable(1000, 2000, &[], 999, 3000, 0o006));
        assert!(!mode_predicts_writable(1000, 2000, &[], 999, 3000, 0o004));
    }
}
