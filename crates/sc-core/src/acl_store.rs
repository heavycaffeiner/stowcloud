//! Durable [`sc_acl::Grant`]s — `DESIGN-CORE.md` §3.5 describes the virtual
//! root as "the projection of the grant list"; this is where that list
//! actually lives once it stops being a startup-time fiction.
//!
//! ## Why its own database
//!
//! `sc-meta` is documented as a disposable cache (`ARCHITECTURE.md` §0.1:
//! "can be deleted at any time and the service keeps working") — it is
//! rebuilt from the filesystem on demand. A grant is not reconstructible
//! from the filesystem: nothing about `/srv/photos` on disk says "user 7
//! may read `/vacation` but not `/vacation/private`". Putting grants in
//! `meta.db` would quietly turn a documented-as-disposable file into one
//! that must be backed up, and
//! deleting it to "fix a corrupt cache" would silently lock every non-admin
//! user out of everything. Same reasoning `links.rs` already used for share
//! links; grants get the same treatment, in their own file
//! (`<data>/acl.db`), for the same reason.
//!
//! ## The migration
//!
//! Before this module existed, `sc-server::app::project_grants` gave *every
//! enabled account full permissions on every configured share*, recomputed
//! from nothing at every startup (`ARCHITECTURE.md`/`app.rs`'s own doc
//! comment called this "the honest interim"). That behavior must not
//! silently vanish out from under a deployment that depends on it — a
//! server with one admin account and no share access is a server nobody can
//! use.
//!
//! So the very first time this store is opened for a given data directory
//! (tracked by [`Self::migration_done`], never re-evaluated afterward), it
//! looks at what `sc-auth` already has on record. If there is at least one
//! account already, this is an *existing* deployment being upgraded onto
//! persisted grants for the first time: every account that exists at that
//! moment gets exactly what the old projection would have given it — a
//! full-access root grant on every registered share — written down as
//! ordinary, revocable rows. If there are zero accounts, this is a brand
//! new install with nothing to preserve, and nothing is seeded: the new
//! default is "no access" — no share is visible until a grant says so.
//!
//! The marker is written unconditionally on that first open, whether or not
//! anything was seeded, so the decision is made exactly once per data
//! directory. An admin who later revokes every grant on a migrated
//! deployment does not get them silently re-seeded on the next restart —
//! that would make "no access" impossible to actually configure.
//!
//! A brand-new install's first (bootstrap) administrator is *not* covered by
//! this migration (there are no prior accounts to preserve), but is not left
//! with an empty file list either: [`Core::seed_full_access`] is called for
//! that one account, once, right after `sc-server::setup::SetupGate` creates
//! it — the built-in-administrator convenience an operator expects on first
//! login, without reintroducing "everyone gets everything" for accounts
//! created afterward.

use std::collections::HashMap;
use std::path::Path;

use sc_acl::{Grant, Perms, Principal};
use sc_vfs::{GroupId, SafePath, ShareId, UserId};
use r2d2::Pool;
use r2d2_sqlite::SqliteConnectionManager;
use rusqlite::{Connection, OpenFlags, OptionalExtension};

use crate::error::CoreError;

const SCHEMA_SQL: &str = "
CREATE TABLE IF NOT EXISTS grant_ (
  id             INTEGER PRIMARY KEY,
  principal_kind INTEGER NOT NULL, -- 0 = user, 1 = group
  principal_id   INTEGER NOT NULL,
  share          INTEGER NOT NULL,
  subpath        TEXT    NOT NULL, -- SafePath::to_display_string(); '' = share root
  allow          INTEGER NOT NULL,
  deny           INTEGER NOT NULL,
  inherit        INTEGER NOT NULL,
  label          TEXT,
  created_ns     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS grant_principal ON grant_(principal_kind, principal_id);
CREATE INDEX IF NOT EXISTS grant_share ON grant_(share);

CREATE TABLE IF NOT EXISTS acl_migration (
  key     TEXT PRIMARY KEY,
  done_ns INTEGER NOT NULL
);
";

const LEGACY_PROJECTION_KEY: &str = "legacy_projection_seeded";

/// The eight permission bits' wire names, in the same fixed order
/// `sc_acl::ALL_PERM_BITS` iterates them — lowercase, English, one word
/// each. Not compat-layer vocabulary and not HTTP-specific, so this lives here
/// rather than in whichever crate ends up serving the admin grant API: any
/// caller that needs to render or parse a `Perms` set as a list of names
/// (an admin UI checkbox group, an HTTP request body) needs exactly this
/// mapping, and duplicating it at the call site risks the list silently
/// drifting from `sc_acl::Perms`'s actual bit definitions.
pub const PERM_NAMES: [(Perms, &str); 8] = [
    (Perms::READ, "read"),
    (Perms::WRITE, "write"),
    (Perms::CREATE, "create"),
    (Perms::DELETE, "delete"),
    (Perms::RENAME, "rename"),
    (Perms::MOVE, "move"),
    (Perms::SHARE, "share"),
    (Perms::DOWNLOAD, "download"),
];

/// `Perms` -> its set bits' names, in `PERM_NAMES` order.
pub fn perms_to_names(p: Perms) -> Vec<&'static str> {
    PERM_NAMES.iter().filter(|(bit, _)| p.contains(*bit)).map(|(_, name)| *name).collect()
}

/// Names -> `Perms`. Unrecognized names are ignored rather than rejected —
/// callers that need "unknown permission name" to be a hard error (an HTTP
/// handler validating a request body, say) should check the round trip
/// themselves (`perms_to_names(perms_from_names(names)).len() ==
/// names.len()`) rather than this function silently widening what counts as
/// an error.
pub fn perms_from_names<S: AsRef<str>>(names: &[S]) -> Perms {
    let mut out = Perms::empty();
    for n in names {
        if let Some((bit, _)) = PERM_NAMES.iter().find(|(_, name)| *name == n.as_ref()) {
            out |= *bit;
        }
    }
    out
}

/// One persisted grant, as every caller above this module sees it — the
/// `sc_acl::Grant` the ACL engine actually evaluates, plus the bookkeeping
/// that only makes sense once it's a database row.
#[derive(Clone, Debug)]
pub struct GrantRecord {
    pub grant: Grant,
    pub created_ns: i128,
}

/// What [`Core::create_grant`] is asked to mint. All five fields together —
/// there is no sensible default for "which user, which share, how much
/// access" the way [`crate::links::LinkSpec`] can default to "read-only, no
/// password": a grant with no permission bits set at all would be a
/// silent no-op row, so [`Core::create_grant`] refuses that case explicitly
/// rather than accept a spec that means nothing.
#[derive(Clone, Debug)]
pub struct GrantSpec {
    pub principal: Principal,
    pub share: ShareId,
    /// Share-relative, not yet parsed — validated against the target
    /// share's own `SharePolicy::max_depth` inside `create_grant`, the same
    /// way `LinkSpec`'s caller-supplied path is validated inside
    /// `create_link`.
    pub subpath: String,
    pub allow: Perms,
    pub deny: Perms,
    pub inherit: bool,
    pub label: Option<String>,
}

/// A partial update. Principal/share/subpath are deliberately not
/// patchable — those are what *identifies* a grant; changing any of them is
/// modeled as delete-and-recreate so a client can't accidentally repoint an
/// existing grant at a different user by omitting a field. The doubled
/// `Option` on `label` distinguishes "leave the label alone" from
/// "explicitly clear it back to the subpath-basename fallback", exactly as
/// `LinkPatch` already does for its own optional fields.
#[derive(Clone, Debug, Default)]
pub struct GrantPatch {
    pub allow: Option<Perms>,
    pub deny: Option<Perms>,
    pub inherit: Option<bool>,
    pub label: Option<Option<String>>,
}

/// Narrows [`Core::list_grants`] to one principal and/or one share. Both
/// `None` lists every grant in the deployment — the admin "all grants"
/// view.
#[derive(Clone, Debug, Default)]
pub struct GrantFilter {
    pub principal: Option<Principal>,
    pub share: Option<ShareId>,
}

// ---------------------------------------------------------------------------
// storage
// ---------------------------------------------------------------------------

/// SQLite-backed persistence for [`sc_acl::Grant`]s. See the module doc for
/// why this is a separate database from `sc-meta`'s cache.
pub struct AclStore {
    pool: Pool<SqliteConnectionManager>,
    _keepalive: parking_lot::Mutex<Option<Connection>>,
}

enum Target {
    File(std::path::PathBuf),
    Memory(String),
}

impl AclStore {
    pub fn open(path: &Path) -> anyhow::Result<Self> {
        Self::open_with(Target::File(path.to_path_buf()))
    }

    /// Shared-cache in-memory store, for tests. A plain `:memory:` would
    /// give every pooled connection its own empty database — see
    /// `LinkStore::open_in_memory`'s identical rationale.
    pub fn open_in_memory() -> anyhow::Result<Self> {
        static COUNTER: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let n = COUNTER.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let uri = format!("file:sc_acl_mem_{}_{n}?mode=memory&cache=shared", std::process::id());
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
        self.pool.get().map_err(|e| CoreError::Internal(format!("acl db: {e}")))
    }

    /// Has the one-time legacy-projection migration already run against
    /// this database? See the module doc — this is checked exactly once,
    /// at the moment `sc-server::App::build` wires the store up, and never
    /// again, so an admin revoking every grant afterward is not undone by
    /// the next restart.
    pub fn migration_done(&self) -> Result<bool, CoreError> {
        let conn = self.conn()?;
        conn.query_row(
            "SELECT 1 FROM acl_migration WHERE key = ?1",
            [LEGACY_PROJECTION_KEY],
            |_| Ok(()),
        )
        .optional()
        .map_err(db)
        .map(|r| r.is_some())
    }

    /// Record that the migration decision has been made, whichever way it
    /// went (seeded or not — see the module doc).
    pub fn mark_migration_done(&self) -> Result<(), CoreError> {
        self.conn()?
            .execute(
                "INSERT OR IGNORE INTO acl_migration (key, done_ns) VALUES (?1, ?2)",
                rusqlite::params![LEGACY_PROJECTION_KEY, ns_to_i64(now_ns())],
            )
            .map_err(db)?;
        Ok(())
    }
}

fn db(e: rusqlite::Error) -> CoreError {
    CoreError::Internal(format!("acl db: {e}"))
}

fn now_ns() -> i128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i128)
        .unwrap_or(0)
}

/// SQLite integers are 64-bit; nanosecond timestamps stay inside that range
/// until the year 2262 (`links.rs` makes the same call the same way).
fn ns_to_i64(v: i128) -> i64 {
    v.clamp(i64::MIN as i128, i64::MAX as i128) as i64
}

fn principal_to_cols(p: Principal) -> (i64, i64) {
    match p {
        Principal::User(u) => (0, u.get() as i64),
        Principal::Group(g) => (1, g.get() as i64),
    }
}

fn principal_from_cols(kind: i64, id: i64) -> Option<Principal> {
    match kind {
        0 => Some(Principal::User(UserId::new(id as u32))),
        1 => Some(Principal::Group(GroupId::new(id as u32))),
        _ => None,
    }
}

const SELECT_COLS: &str =
    "id, principal_kind, principal_id, share, subpath, allow, deny, inherit, label, created_ns";

fn row_to_record(row: &rusqlite::Row<'_>, max_depth: u16) -> rusqlite::Result<GrantRecord> {
    let kind: i64 = row.get("principal_kind")?;
    let pid: i64 = row.get("principal_id")?;
    let principal = principal_from_cols(kind, pid).unwrap_or(Principal::User(UserId::new(0)));
    let subpath_str: String = row.get("subpath")?;
    let subpath = SafePath::parse(&subpath_str, max_depth).unwrap_or_else(|_| SafePath::root());
    Ok(GrantRecord {
        grant: Grant {
            id: row.get::<_, i64>("id")? as u32,
            principal,
            share: ShareId::new(row.get::<_, i64>("share")? as u32),
            subpath,
            allow: Perms::from_bits_truncate(row.get::<_, i64>("allow")? as u16),
            deny: Perms::from_bits_truncate(row.get::<_, i64>("deny")? as u16),
            inherit: row.get::<_, i64>("inherit")? != 0,
            label: row.get("label")?,
        },
        created_ns: row.get::<_, i64>("created_ns")? as i128,
    })
}

// ---------------------------------------------------------------------------
// Core API
// ---------------------------------------------------------------------------

impl crate::Core {
    /// Attach the grant store. Idempotent-by-refusal, same contract as
    /// [`Core::attach_links`]: a second call is a wiring bug, not a runtime
    /// condition.
    pub fn attach_acl_store(&self, store: AclStore) -> anyhow::Result<()> {
        self.acl_store
            .set(std::sync::Arc::new(store))
            .map_err(|_| anyhow::anyhow!("acl store already attached"))
    }

    pub fn acl_store_enabled(&self) -> bool {
        self.acl_store.get().is_some()
    }

    fn acl_db(&self) -> Result<&AclStore, CoreError> {
        self.acl_store
            .get()
            .map(|s| s.as_ref())
            .ok_or_else(|| CoreError::NotSupported("grants are not configured on this server".into()))
    }

    fn max_depth_of(&self, share: ShareId) -> u16 {
        self.shares.get(&share).map(|e| e.def.policy.max_depth).unwrap_or(64)
    }

    /// Persist a new grant and immediately push the change into the live
    /// `AclEngine` (see [`Core::reload_acl`]) — a grant an admin just
    /// created must take effect on the very next request, not the next
    /// restart.
    pub fn create_grant(&self, spec: &GrantSpec) -> Result<GrantRecord, CoreError> {
        let store = self.acl_db()?;
        if spec.allow.is_empty() && spec.deny.is_empty() {
            return Err(CoreError::InvalidPath("a grant must allow or deny at least one permission".into()));
        }
        let root = self.share(spec.share).ok_or(CoreError::NotFound)?;
        let max_depth = root.policy().max_depth;
        let subpath = SafePath::parse(&spec.subpath, max_depth)?;

        // `RootEntry`'s label fallback (`sc_acl::AclEngine::roots`) is
        // `label -> subpath's basename -> "share-{id}"`. The middle rung is
        // `None` for a root grant by construction (the root has no
        // basename), which would otherwise surface as the distinctly
        // unhelpful "share-3" the very first time an admin grants someone
        // the whole share. `seed_full_access`/the legacy migration always
        // passed the share's real name explicitly for exactly this reason;
        // this does the same for the general case rather than making every
        // caller remember to.
        let label = spec.label.clone().or_else(|| {
            if subpath.is_empty() {
                self.share_defs().into_iter().find(|d| d.id == spec.share).map(|d| d.name)
            } else {
                None
            }
        });

        let (kind, pid) = principal_to_cols(spec.principal);
        let created = now_ns();
        let conn = store.conn()?;
        conn.execute(
            "INSERT INTO grant_ (principal_kind, principal_id, share, subpath, allow, deny, inherit, label, created_ns) \
             VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9)",
            rusqlite::params![
                kind,
                pid,
                spec.share.get() as i64,
                subpath.to_display_string(),
                spec.allow.bits() as i64,
                spec.deny.bits() as i64,
                spec.inherit as i64,
                label,
                ns_to_i64(created),
            ],
        )
        .map_err(db)?;
        let id = conn.last_insert_rowid();
        drop(conn);

        self.reload_acl()?;
        Ok(GrantRecord {
            grant: Grant {
                id: id as u32,
                principal: spec.principal,
                share: spec.share,
                subpath,
                allow: spec.allow,
                deny: spec.deny,
                inherit: spec.inherit,
                label,
            },
            created_ns: created,
        })
    }

    /// Every persisted grant, optionally narrowed by principal and/or
    /// share — the admin "list grants" surface. Ordered by id so paging and
    /// display are stable.
    pub fn list_grants(&self, filter: &GrantFilter) -> Result<Vec<GrantRecord>, CoreError> {
        let store = self.acl_db()?;
        let conn = store.conn()?;
        let sql = format!("SELECT {SELECT_COLS} FROM grant_ ORDER BY id");
        let mut stmt = conn.prepare(&sql).map_err(db)?;
        let rows = stmt
            .query_map([], |row| {
                let share = ShareId::new(row.get::<_, i64>("share")? as u32);
                row_to_record(row, self.max_depth_of(share))
            })
            .map_err(db)?;
        let mut out = Vec::new();
        for r in rows {
            let rec = r.map_err(db)?;
            if let Some(p) = filter.principal {
                if rec.grant.principal != p {
                    continue;
                }
            }
            if let Some(s) = filter.share {
                if rec.grant.share != s {
                    continue;
                }
            }
            out.push(rec);
        }
        Ok(out)
    }

    pub fn get_grant(&self, id: u32) -> Result<GrantRecord, CoreError> {
        let store = self.acl_db()?;
        let conn = store.conn()?;
        let sql = format!("SELECT {SELECT_COLS} FROM grant_ WHERE id = ?1");
        let mut stmt = conn.prepare(&sql).map_err(db)?;
        let share_max = |row: &rusqlite::Row<'_>| -> rusqlite::Result<u16> {
            let share = ShareId::new(row.get::<_, i64>("share")? as u32);
            Ok(self.max_depth_of(share))
        };
        stmt.query_row([id], |row| {
            let max_depth = share_max(row)?;
            row_to_record(row, max_depth)
        })
        .optional()
        .map_err(db)?
        .ok_or(CoreError::NotFound)
    }

    /// Modify an existing grant in place and reload the live engine — see
    /// [`GrantPatch`] for why principal/share/subpath aren't here.
    pub fn update_grant(&self, id: u32, patch: &GrantPatch) -> Result<GrantRecord, CoreError> {
        let store = self.acl_db()?;
        let existing = self.get_grant(id)?;
        let next_allow = patch.allow.unwrap_or(existing.grant.allow);
        let next_deny = patch.deny.unwrap_or(existing.grant.deny);
        if next_allow.is_empty() && next_deny.is_empty() {
            return Err(CoreError::InvalidPath("a grant must allow or deny at least one permission".into()));
        }

        let conn = store.conn()?;
        if let Some(a) = patch.allow {
            conn.execute("UPDATE grant_ SET allow = ?1 WHERE id = ?2", rusqlite::params![a.bits() as i64, id])
                .map_err(db)?;
        }
        if let Some(d) = patch.deny {
            conn.execute("UPDATE grant_ SET deny = ?1 WHERE id = ?2", rusqlite::params![d.bits() as i64, id])
                .map_err(db)?;
        }
        if let Some(i) = patch.inherit {
            conn.execute("UPDATE grant_ SET inherit = ?1 WHERE id = ?2", rusqlite::params![i as i64, id])
                .map_err(db)?;
        }
        if let Some(l) = &patch.label {
            conn.execute("UPDATE grant_ SET label = ?1 WHERE id = ?2", rusqlite::params![l, id]).map_err(db)?;
        }
        drop(conn);

        self.reload_acl()?;
        self.get_grant(id)
    }

    pub fn delete_grant(&self, id: u32) -> Result<(), CoreError> {
        let store = self.acl_db()?;
        // Existence check first, so deleting an id that never existed is a
        // `NotFound` rather than a silent no-op success.
        let _ = self.get_grant(id)?;
        store.conn()?.execute("DELETE FROM grant_ WHERE id = ?1", [id]).map_err(db)?;
        self.reload_acl()
    }

    /// Reload the live `AclEngine` from every row currently in the store.
    /// Called after every mutation above, and once at startup
    /// (`sc-server::app::project_grants`). A store that was never attached
    /// reloads to an empty grant list rather than erroring — a deployment
    /// with grants turned off is "nobody has access to anything", which is
    /// the same fail-closed default as a normal empty grant table.
    pub fn reload_acl(&self) -> Result<(), CoreError> {
        let grants = match self.acl_store.get() {
            Some(_) => self.list_grants(&GrantFilter::default())?.into_iter().map(|r| r.grant).collect(),
            None => Vec::new(),
        };
        self.acl.replace_grants(grants);
        Ok(())
    }

    /// Wire `sc-auth`'s group memberships into the ACL engine. A thin
    /// passthrough rather than something this crate computes itself:
    /// `sc-auth` owns the `group_`/`membership` tables
    /// (`crates/sc-auth/src/db.rs`) and exposes them via
    /// `AuthService::list_memberships_all`. Called at startup
    /// (`sc-server::app::project_grants`) and again after every membership
    /// mutation through the admin API, so a group change takes effect
    /// immediately rather than waiting for a restart; `Principal::Group` and
    /// the depth-first algorithm already handle group grants correctly
    /// (`sc-acl/src/tests.rs`'s `group_vs_user_tie_*` tests).
    pub fn set_group_memberships(&self, m: HashMap<UserId, Vec<GroupId>>) {
        self.acl.set_memberships(m);
    }

    /// Grant `user` full access to the root of every currently registered
    /// share. Used exactly twice: by the one-time legacy migration (for
    /// every account an upgraded deployment already had) and by
    /// `sc-server::setup::SetupGate` (for the single bootstrap
    /// administrator a brand-new deployment creates) — see the module doc
    /// for why those are the only two cases that get this instead of
    /// starting from nothing.
    ///
    /// Idempotent: skips a share the user already holds an identical
    /// root grant on (matched on principal + share + empty subpath), so
    /// calling this twice for the same user never duplicates rows.
    pub fn seed_full_access(&self, user: UserId) -> Result<(), CoreError> {
        let store = self.acl_db()?;
        let existing = self.list_grants(&GrantFilter { principal: Some(Principal::User(user)), share: None })?;
        for def in self.share_defs() {
            // The homes share (`FEATURES.md` #47) is never blanket-granted:
            // a root grant here would hand whoever received it every other
            // user's home directory at once. `Core::ensure_home` is the
            // only thing that ever grants access to it, always scoped to
            // exactly one user's own subpath.
            if def.id == crate::homes::HOME_SHARE_ID {
                continue;
            }
            let already = existing
                .iter()
                .any(|r| r.grant.share == def.id && r.grant.subpath.is_empty() && r.grant.allow.contains(Perms::READ));
            if already {
                continue;
            }
            let conn = store.conn()?;
            conn.execute(
                "INSERT INTO grant_ (principal_kind, principal_id, share, subpath, allow, deny, inherit, label, created_ns) \
                 VALUES (0,?1,?2,'',?3,0,1,?4,?5)",
                rusqlite::params![
                    user.get() as i64,
                    def.id.get() as i64,
                    Perms::all().bits() as i64,
                    def.name,
                    ns_to_i64(now_ns()),
                ],
            )
            .map_err(db)?;
        }
        self.reload_acl()
    }

    /// The one-time legacy-projection migration — see the module doc for
    /// the full rationale. `existing_users` is every account `sc-auth`
    /// already had *before* this call (an upgrading deployment); an empty
    /// slice means there is nothing to preserve. Safe to call on every
    /// startup: after the first call the marker row makes every later call
    /// a no-op, whether or not anything was seeded the first time.
    pub fn migrate_legacy_grants(&self, existing_users: &[UserId]) -> Result<(), CoreError> {
        let store = self.acl_db()?;
        if store.migration_done()? {
            return Ok(());
        }
        for &u in existing_users {
            self.seed_full_access(u)?;
        }
        store.mark_migration_done()?;
        self.reload_acl()
    }
}
