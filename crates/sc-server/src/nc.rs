//! Wiring for the compat layer.
//!
//! Everything in this module is behind `feature = "compat-nc"`, including the
//! `sc-compat-nc` dependency itself, so `--no-default-features` produces a
//! binary in which none of this — and none of that crate's route strings —
//! exists at all.
//!
//! The dependency arrow points one way by contract: `sc-compat-nc` consumes
//! the core crates through the port traits it declares, and no core crate
//! knows this module exists. The adapters below are the only place the two
//! vocabularies meet.

use std::sync::Arc;

use axum::extract::{Request, State};
use axum::response::{IntoResponse, Response};
use axum::routing::any;
use axum::Router;
use sc_compat_nc::ports::{self, PortError, PortResult};
use sc_core::{SharePath, Vpath};
use sc_vfs::{FileId, ShareId, UserId};
use http::StatusCode;

use crate::app::App;

/// The name this layer contributes to `capabilities.features.extensions`.
/// The core owns the *list*; the string is ours.
pub const EXTENSION_NAME: &str = "compat-nc";

fn port_io<E: std::fmt::Display>(e: E) -> PortError {
    PortError::Backend(e.to_string())
}

fn core_port_err(e: sc_core::CoreError) -> PortError {
    match e {
        sc_core::CoreError::NotFound => PortError::NotFound,
        sc_core::CoreError::Denied { .. } => PortError::Forbidden,
        sc_core::CoreError::Conflict => PortError::Conflict("already exists".into()),
        sc_core::CoreError::InvalidPath(m) => PortError::Invalid(m),
        other => PortError::Backend(other.to_string()),
    }
}

// ---------------------------------------------------------------- CorePort --

pub struct NcCore {
    core: Arc<sc_core::Core>,
    meta: Arc<sc_meta::MetaStore>,
    auth: Arc<sc_auth::AuthService>,
}

impl NcCore {
    /// Best-effort mtime stamp applied *after* a write has already
    /// succeeded — used to honour the upload-time header the compat layer
    /// reads on a plain (non-chunked) `PUT` (`h_put_files`, below).
    ///
    /// `vpath` here is already in `sc-core`'s `/{label}/…` form (it comes
    /// straight from `dav_paths::DavTarget::Files`, whose `path` is exactly
    /// what `sc-dav`'s own `vpath_of` would have produced from the same
    /// URL), so this re-resolves the same target the write just landed on
    /// rather than threading a `ShareRoot`/`SafePath` pair through the
    /// caller.
    ///
    /// Returns whether the timestamp was actually applied, so the caller can
    /// decide whether to echo the reference server's confirmation header.
    /// Failure here (permission re-check, or the file having vanished
    /// between the write and this call) is logged and swallowed: the PUT
    /// already succeeded and returned, and retroactively failing it over a
    /// cosmetic timestamp would be worse than leaving the mtime alone.
    fn set_upload_mtime(&self, user: UserId, vpath: &Vpath, mtime_ns: i128) -> bool {
        match self.core.resolve(user, vpath) {
            Ok(r) => match r.root.set_times(&r.path, mtime_ns) {
                Ok(()) => true,
                Err(e) => {
                    tracing::warn!(error = %e, vpath = %vpath, "failed to apply upload mtime after a successful PUT");
                    false
                }
            },
            Err(e) => {
                tracing::warn!(error = %e, vpath = %vpath, "failed to resolve path for upload mtime after a successful PUT");
                false
            }
        }
    }

    /// Every grant-projected root `user` can see, each resolved to a real
    /// `sc_core::Entry` (kind, size, mtime, etag, id, perms) via its own
    /// label — what `h_files_root`'s synthetic PROPFIND lists as the
    /// caller's files root's children (`DavTarget::Files`'s doc comment:
    /// "the reference clients expect the files root to *contain* the
    /// user's folders").
    ///
    /// `roots()` already filters to grants that carry `READ` (`sc-acl`'s own
    /// doc comment on `AclEngine::roots`), so every label here should
    /// resolve; a root that does not (a race against an in-flight ACL edit,
    /// say) is skipped rather than surfacing a broken row — a listing must
    /// never crash on a race it can quietly outlive.
    /// Carries the share and the grant's subpath alongside the entry, because
    /// the files-root response needs them to ask for a recursive size: a
    /// grant's subpath *is* the share path of that grant's root, so no
    /// conversion is involved and the aggregate can be looked up directly.
    fn root_entries(&self, user: UserId) -> Vec<RootRow> {
        self.core
            .roots(user)
            .into_iter()
            .filter_map(|r| {
                let mut e = self.core.stat_entry(user, &r.label).ok()?;
                if e.id.is_none() {
                    // Every row here is, by construction, the *root* of
                    // whatever `r.subpath` names within `r.share` — see
                    // `share_root_pseudo_id`'s doc comment for why a grant
                    // rooted at the share's own physical root
                    // (`r.subpath.is_empty()`) needs the synthetic id rather
                    // than `ensure_fileid`, which would just hand back the
                    // same ambiguous `FileId(0)` every such root shares.
                    e.id = if r.subpath.is_empty() {
                        Some(share_root_pseudo_id(r.share))
                    } else {
                        self.core.ensure_fileid(r.share, &r.subpath).ok()
                    };
                }
                Some(RootRow {
                    label: r.label,
                    share: r.share,
                    subpath: SharePath::new(r.subpath),
                    entry: e,
                })
            })
            .collect()
    }
}

/// One grant-projected root as the synthetic files-root response needs it.
struct RootRow {
    label: String,
    share: ShareId,
    /// Where the grant is rooted inside its share, which is also the share
    /// path whose recursive size the row reports.
    subpath: SharePath,
    entry: sc_core::Entry,
}

/// Reserved-range marker for [`share_root_pseudo_id`]: bit 62 set, bits
/// 0-31 free for a `ShareId` (`u32`) to occupy without ever touching this
/// bit. `oc:fileid` is a positive DB rowid on the wire everywhere real
/// clients are concerned (the reference server's own `fileid` is a plain SQLite
/// `AUTOINCREMENT` id, always positive, and `oc:id`'s `nc_id()` formatting —
/// `props.rs`'s doc comment — treats it as a zero-padded positive integer,
/// not a signed one), so the reserved range has to be positive too: a
/// negative pseudo-id is technically disjoint from a real rowid, but it is
/// a value no reference client's parser was ever written to expect, and
/// `i64::MIN` specifically is actively dangerous — negating it overflows
/// under two's-complement, `abs()` on it is undefined/panics in most
/// languages, and a client that stores it and later diffs two ids gets
/// nonsense. `1u64 << 62` is a value only a database with over four
/// quintillion rows could ever reach through ordinary `rowid` allocation —
/// not a hard guarantee the way `sc-meta`'s own uniqueness constraints are,
/// but disjoint in every practical sense, while staying a plain positive
/// integer a client can zero-pad, store, and diff without surprises.
const SHARE_ROOT_PSEUDO_MARKER: i64 = 1 << 62;

/// A deterministic, real-row-disjoint stand-in for a share's own root
/// directory's `oc:fileid`/`oc:id` — used everywhere a compat response
/// needs identity for an entry that sits exactly at `sc-meta`'s idea of a
/// share's physical root.
///
/// `sc-meta`'s `node` table names every row under a parent id
/// (`crates/sc-core/src/aggregate.rs`'s `ensure_fileid_chain`), and the
/// share root has none — so `ensure_fileid`/`ensure_fileid_chain` return the
/// sentinel `FileId(0)` for it *by design*, not as an error. Left as-is,
/// every share's root would report the identical id (zero): exactly the
/// collision this whole fix exists to close, just moved from "never
/// allocated" to "structurally un-allocatable". `SHARE_ROOT_PSEUDO_MARKER`
/// OR'd with the share id: disjoint from a real row (see that constant's
/// doc comment), disjoint from every *other* share's own pseudo-id (the low
/// 32 bits are the share id verbatim, and no two shares share one), and a
/// pure function of `ShareId`, so it is both stable across requests and —
/// correctly — identical for two different grants that both expose the
/// very same physical share root.
fn share_root_pseudo_id(share: sc_vfs::ShareId) -> sc_vfs::FileId {
    sc_vfs::FileId::new(SHARE_ROOT_PSEUDO_MARKER | share.get() as i64)
}

impl ports::CorePort for NcCore {
    fn home_root(&self, user: UserId) -> PortResult<ShareId> {
        // Clients see one files root. With `homes.enabled = false` (the
        // default) there is no per-user home share, so
        // the first grant-projected root is the honest answer — the same one
        // the web UI shows first.
        self.core
            .roots(user)
            .first()
            .map(|r| r.share)
            .ok_or(PortError::NotFound)
    }

    fn resolve(&self, user: UserId, path: &Vpath) -> PortResult<ports::Entry> {
        self.stat_entry(user, path)
    }

    fn list(&self, user: UserId, path: &Vpath) -> PortResult<Vec<ports::Entry>> {
        self.core
            .list(user, path.as_str(), sc_core::Sort::Name, sc_core::Order::Asc)
            .map(|l| l.entries)
            .map_err(core_port_err)
    }

    fn stat_entry(&self, user: UserId, path: &Vpath) -> PortResult<ports::Entry> {
        self.core
            .stat_entry(user, path.as_str())
            .map_err(core_port_err)
    }

    fn aggregate(&self, share: ShareId, path: &SharePath) -> PortResult<ports::Aggregate> {
        // Straight through. There used to be an id-to-path lookup here,
        // because the caller had only a file id: it could not serve a share's
        // own root, which has no `node` row at all, so every grant root
        // reported its directory inode's size instead of its contents'.
        // `Core::aggregate` takes the path and allocates the id itself.
        self.core.aggregate(share, path).map_err(port_io)
    }

    fn user_info(&self, user: UserId) -> PortResult<ports::UserInfo> {
        let row = self
            .auth
            .find_user_by_id(user)
            .map_err(port_io)?
            .ok_or(PortError::NotFound)?;
        Ok(ports::UserInfo {
            id: row.id,
            login_name: row.name.clone(),
            display_name: row.display.unwrap_or(row.name),
            // `sc-auth` models accounts, not directories: there is no email
            // column and no group table. Reporting empty is truthful;
            // fabricating a value here would surface in client UIs.
            email: None,
            enabled: !row.disabled,
            groups: Vec::new(),
            language: "en".into(),
            locale: "en".into(),
        })
    }

    fn user_info_by_login(
        &self,
        _caller: UserId,
        login: &str,
        scope: ports::GranteeScope,
    ) -> PortResult<Option<ports::UserInfo>> {
        // Same gate as the sharee search, for the same reason: an unscoped
        // lookup turns a login name into an existence oracle.
        //
        // `SameGroup` cannot be honoured because `sc-auth` has no group table,
        // and widening it to `All` here would reopen exactly what the setting
        // closes. Both it and `Off` therefore answer nothing.
        if scope != ports::GranteeScope::All {
            return Ok(None);
        }
        let row = self
            .auth
            .list_users()
            .map_err(port_io)?
            .into_iter()
            .find(|u| u.name.eq_ignore_ascii_case(login) && !u.disabled);
        Ok(row.map(|row| ports::UserInfo {
            id: row.id,
            login_name: row.name.clone(),
            display_name: row.display.unwrap_or(row.name),
            email: None,
            enabled: !row.disabled,
            groups: Vec::new(),
            language: "en".into(),
            locale: "en".into(),
        }))
    }

    fn quota(&self, user: UserId) -> PortResult<ports::Quota> {
        let share = self.home_root(user)?;
        let root = self.core.share(share).ok_or(PortError::NotFound)?;
        let space = root.space(&sc_vfs::SafePath::root()).map_err(port_io)?;
        let used = space.used();
        let free = space.available;
        // `quota_bytes` (`user.quota_bytes`) is a
        // reporting gate, not a usage-tracking cap:
        // follows the reference server exactly — real numbers only when a
        // quota is actually configured (`Some(n)` with `n > 0`), otherwise
        // `None` renders the unlimited sentinel. `used` stays the real
        // statvfs-derived figure either way.
        let cap = self
            .auth
            .find_user_by_id(user)
            .map_err(port_io)?
            .and_then(|row| row.quota_bytes)
            .filter(|&n| n > 0);
        Ok(ports::Quota {
            used,
            free,
            total: cap,
        })
    }

    /// `_user` stays unused and is now visibly correct rather than
    /// suspicious: id-to-path is a metadata lookup, and the ACL check belongs
    /// where the resulting path is used, which is `Core::stat_entry`.
    fn locate(&self, _user: UserId, id: FileId) -> PortResult<(ShareId, SharePath)> {
        let (share, path) = self
            .meta
            .resolve_path(id)
            .map_err(port_io)?
            .ok_or(PortError::NotFound)?;
        let root = self.core.share(share).ok_or(PortError::NotFound)?;
        let sp = SharePath::parse(&path, root.policy().max_depth).map_err(port_io)?;
        Ok((share, sp))
    }

    fn vpath_for(&self, user: UserId, share: ShareId, path: &SharePath) -> Option<Vpath> {
        self.core.vpath_for(user, share, path)
    }
}

// ---------------------------------------------------------------- AuthPort --

/// `sc-auth` verifies credentials asynchronously (it bounds Argon2
/// concurrency with a semaphore) while this port is synchronous, because
/// every caller is inside a blocking OCS handler. Bridging needs a runtime,
/// and which one depends on where the server was assembled from: `serve` is
/// already inside the multi-thread reactor, while `gc`/`smb-sync` are plain
/// synchronous commands with no reactor at all.
enum Bridge {
    /// Inside a running reactor: hand the blocking wait to a worker thread
    /// so the reactor keeps making progress.
    Reactor(tokio::runtime::Handle),
    /// No reactor: own a single-threaded one. `block_on` is legal here
    /// precisely because we are not inside any other runtime.
    Standalone(tokio::runtime::Runtime),
}

pub struct NcAuth {
    auth: Arc<sc_auth::AuthService>,
    bridge: Bridge,
}

impl NcAuth {
    fn block_on<F: std::future::Future>(&self, fut: F) -> F::Output {
        match &self.bridge {
            Bridge::Reactor(h) => tokio::task::block_in_place(|| h.block_on(fut)),
            Bridge::Standalone(rt) => rt.block_on(fut),
        }
    }
}

fn principal_of(
    auth: &sc_auth::AuthService,
    user: UserId,
    credential_id: Option<u32>,
) -> ports::Principal {
    let row = auth.find_user_by_id(user).ok().flatten();
    let login = row.as_ref().map(|r| r.name.clone()).unwrap_or_default();
    let display = row
        .as_ref()
        .and_then(|r| r.display.clone())
        .unwrap_or_else(|| login.clone());
    ports::Principal {
        user,
        login_name: login,
        display_name: display,
        credential_id,
    }
}

/// The credential id a verified principal authenticated with, when it was an
/// app password. A session and an account password both answer `None`: neither
/// is revocable through the app-password API.
fn credential_of(p: &sc_auth::Principal) -> Option<u32> {
    match p.via {
        sc_auth::AuthVia::AppPassword(id) => Some(id),
        _ => None,
    }
}

impl ports::AuthPort for NcAuth {
    fn issue_app_password(
        &self,
        user: UserId,
        name: &str,
        scope: ports::Scope,
    ) -> PortResult<(u32, String)> {
        // `sc_auth::Scope::perms_mask: None` is what "unrestricted" means
        // everywhere it is enforced (;
        // `sc_http::middleware::scope_gate`'s `RouteScope::Requires` arm;
        // this crate's own `dav_authenticate`) — a route not explicitly
        // mapped for a *restricted* credential fails closed, an unrestricted
        // one is unaffected. `ports::Scope::full()` (the Login Flow v2
        // consent screen's "Full access" radio — `router.rs`'s
        // `h_login_grant`) sets every bit `sc-acl` currently defines and no
        // share restriction: that is not a narrowing, it is the verbose way
        // of saying "every capability, every share" — the same thing `None`
        // says. Translating it literally into `Some(all_bits)` instead
        // produced a token that behaved identically to an unrestricted one
        // on every *mapped* route, but was still, structurally, `Some(_)` —
        // and `dav_authenticate` refuses any `Some(_)` mask outright on the
        // compat surfaces that have no per-method `Perms` bit to check it
        // against (OCS, `status.php`, Login Flow v2 itself): no bit
        // combination proves a restricted scope should be allowed to read
        // `cloud/capabilities`, so it never got the chance to prove it was
        // not actually restricted at all. A real client walked the whole
        // enrolment flow, picked "Full access", and could not read its own
        // capabilities or account info afterward. A password that names an
        // actual subset of perms and/or a specific share keeps `Some(bits)`
        // and stays subject to that same fail-closed rule, unchanged.
        let unrestricted = scope.perms == ports::Perms::all() && scope.share.is_none();
        let scope = sc_auth::Scope {
            perms_mask: if unrestricted {
                None
            } else {
                Some(scope.perms.bits())
            },
            shares: scope.share.map(|s| vec![s]),
        };
        self.auth
            .issue_app_password(user, name, scope)
            .map_err(port_io)
    }

    fn verify_basic(
        &self,
        login: &str,
        secret: &str,
        from: ports::ClientAddr,
    ) -> PortResult<Option<ports::Principal>> {
        let secret = secrecy::SecretString::from(secret.to_string());
        // The address the trusted-proxy layer resolved, relabelled into this
        // crate's vocabulary by `relabel_client_addr` below. `sc-auth` keys
        // its per-IP brute-force gate and its audit rows on it, so a constant
        // here — this used to be a literal `127.0.0.1` — gives every
        // compatibility client in the world one shared bucket and one
        // meaningless audit column.
        let ip = from.0;
        let result = self.block_on(self.auth.verify_basic(login, &secret, ip));
        Ok(match result {
            sc_auth::BasicResult::Ok(p) => {
                Some(principal_of(&self.auth, p.user, credential_of(&p)))
            }
            _ => None,
        })
    }

    fn validate_session(&self, token: &str) -> PortResult<Option<ports::Principal>> {
        Ok(self
            .auth
            .validate_session(token)
            .map_err(port_io)?
            // A browser session carries no app password, so there is nothing
            // here for the revoke endpoint to act on.
            .map(|p| principal_of(&self.auth, p.user, None)))
    }

    fn revoke_app_password(&self, user: UserId, credential: u32) -> PortResult<()> {
        // Idempotent by design: a client retrying its logout must not see an
        // error it cannot act on, and "already gone" is the outcome it wanted.
        self.auth
            .revoke_app_password_owned(user, credential)
            .map(|_| ())
            .map_err(port_io)
    }

    fn wipe_requested(&self, credential: u32) -> PortResult<bool> {
        self.auth.wipe_requested(credential).map_err(port_io)
    }

    fn finish_wipe(&self, credential: u32) -> PortResult<()> {
        self.auth.finish_wipe(credential).map_err(port_io)
    }
}

// ------------------------------------------------------------ UploadEngine --

pub struct NcUpload {
    engine: Arc<sc_upload::UploadEngine>,
    core: Arc<sc_core::Core>,
    journal: Option<Arc<crate::journal::WriteJournal>>,
}

impl NcUpload {
    fn root(&self, share: ShareId) -> PortResult<Arc<sc_vfs::ShareRoot>> {
        self.core.share(share).ok_or(PortError::NotFound)
    }
}

impl ports::UploadEngine for NcUpload {
    // `spec.dest` is a full vpath (`{label}/{rest}`), not yet resolved to any
    // share — the caller cannot supply one because deciding which grant's
    // label `dest` names *is* the resolution. Doing that resolution here
    // (WRITE-checked, via `resolve_for_upload`) is also what fixed the 500 an
    // earlier version of this method produced: it used to take `share` as an
    // input and re-parse `spec.dest` (which still carried the label) as a
    // `SafePath` *underneath* that share's root, nesting the label inside
    // itself and pointing the session at a path that could never exist.
    fn create(&self, spec: ports::SessionSpec) -> PortResult<(ShareId, ports::SessionId)> {
        let resolved = self
            .core
            .resolve_for_upload(spec.owner, &Vpath::new(&spec.dest))
            .map_err(core_port_err)?;
        let engine_spec = sc_upload::SessionSpec {
            user: spec.owner,
            share: resolved.share,
            dest: resolved.path.into_safe(),
            total_len: spec.total_len,
            random_access: false,
            if_match: None,
            mode: spec.mode,
            meta: sc_upload::UploadMeta {
                filename: spec.dest.rsplit('/').next().unwrap_or("").to_string(),
                ..Default::default()
            },
        };
        let sid = self
            .engine
            .create(&resolved.root, engine_spec)
            .map_err(port_io)?;
        Ok((resolved.share, sid))
    }

    fn put_named(
        &self,
        share: ShareId,
        session: ports::SessionId,
        user: UserId,
        name: u32,
        data: &[u8],
    ) -> PortResult<()> {
        let root = self.root(share)?;
        self.engine
            .put_named(&root, session, user, name, data)
            .map_err(port_io)
    }

    fn assemble_and_finalize(
        &self,
        share: ShareId,
        session: ports::SessionId,
        user: UserId,
        total: u64,
        mtime_ns: Option<i128>,
    ) -> PortResult<()> {
        let root = self.root(share)?;
        // The journal is read and written here, and `()` still goes back up:
        // no vocabulary and no new value crosses the isolation boundary.
        let published = self
            .engine
            .assemble_and_finalize(&root, session, user, total, mtime_ns)
            .map_err(port_io)?;
        if let Some(j) = &self.journal {
            j.note(
                user,
                share,
                &published,
                crate::journal::WriteOp::Upload,
                crate::journal::now_ns(),
            );
        }
        Ok(())
    }

    fn list_chunks(&self, session: ports::SessionId) -> PortResult<Vec<u32>> {
        self.engine.list_chunks(session).map_err(port_io)
    }

    fn received_len(&self, session: ports::SessionId, user: UserId) -> PortResult<u64> {
        // Not `offset`: that is the contiguous prefix of `received`, which a
        // NameOrdered session leaves empty until assembly writes it. Reading
        // it here reported 0 for every chunked-v2 upload, so an Android MOVE
        // (which never sends OC-Total-Length) failed with "expected 0".
        self.engine
            .head(session, user)
            .map(|s| s.received_bytes)
            .map_err(port_io)
    }

    fn abort(&self, session: ports::SessionId, user: UserId) -> PortResult<()> {
        self.engine.abort(session, user).map_err(port_io)
    }

    /// The live default, not the one this process booted with:
    /// `PATCH /api/admin/upload-settings` moves it and the core
    /// `/api/capabilities` has always reported the new number immediately.
    fn chunk_size_advisory(&self) -> u64 {
        self.engine.chunk_settings().1
    }
}

// --------------------------------------------------------------- SharePort --

/// Public links are backed by `sc_core::LinkStore`.
///
/// **User and group grants are not.** `sc-acl` grants are administrator-owned
/// and have no per-user CRUD anywhere in the workspace, so `shareType` 0 and 1
/// are *refused*, not accepted-and-dropped: a client told a share was created
/// that then cannot be found is worse off than one told it cannot be created.
/// The refusal is deliberate and is the reason this gap stays findable. (The
/// OCS layer separately rejects every other `shareType` with `400` — see
/// `sc_compat_nc::shares::share_type_to_kind`.)
///
/// `token` is populated on list and get as well as create: `sc-core` keeps it
/// sealed under the server master key. It is `None` only for a link created
/// before that column existed, and such a link renders with a null `url`
/// rather than failing the whole listing.
pub struct NcShares {
    auth: Arc<sc_auth::AuthService>,
    core: Arc<sc_core::Core>,
}

/// Read by a person in a share dialog, not by an administrator in a log.
fn grants_unavailable() -> PortError {
    PortError::Invalid(
        "This server can only share by link. Create a link and send that instead.".into(),
    )
}

/// An expiry the caller asked for that has already passed.
///
/// `Core::create_link` refuses it too, as an invalid argument, and that is the
/// right shape for the native API: the browser renders the message. The OCS
/// surface answers `409` for it instead, which is what both apps' share sheets
/// branch on to re-open the date picker rather than showing a generic failure.
/// Checked here rather than translated out of the core's refusal, because
/// matching on an error message string is a test nobody can see break.
fn expiry_in_the_past(expires_s: Option<i64>) -> Option<PortError> {
    let expires_s = expires_s?;
    let now_s = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    (expires_s <= now_s).then(|| {
        PortError::Conflict("The expiry date has already passed; pick a later one.".into())
    })
}

impl NcShares {
    fn user_name(&self, user: UserId) -> (String, String) {
        match self.auth.list_users() {
            Ok(users) => users
                .into_iter()
                .find(|u| u.id == user)
                .map(|u| {
                    let display = u.display.clone().unwrap_or_else(|| u.name.clone());
                    (u.name, display)
                })
                .unwrap_or_else(|| (user.to_string(), user.to_string())),
            Err(_) => (user.to_string(), user.to_string()),
        }
    }

    fn to_core_share(&self, user: UserId, link: sc_core::ShareLink) -> ports::CoreShare {
        let (owner, owner_display) = self.user_name(link.owner);
        // The link's own share, not the caller's first root: a folder link in
        // any other share used to miss its stat entirely, so `item_type` came
        // back "file" and the file id degraded to 0.
        //
        // A link whose share the user can no longer reach has no label. It is
        // still reported, with its share-relative path, so it stays listable
        // and deletable rather than vanishing from a listing whose whole
        // purpose is to let the owner clean it up.
        let vpath = self.core.vpath_for(user, link.share, &link.path);
        let path = match &vpath {
            Some(v) => v.to_absolute_string(),
            None => format!("/{}", link.path.to_display_string()),
        };
        // `stat_entry` rather than `link_target`: a dead link must still be
        // listable and deletable by its owner, so its liveness is not allowed
        // to decide whether the row can be rendered.
        let entry = vpath
            .as_ref()
            .and_then(|v| self.core.stat_entry(user, v.as_str()).ok());
        ports::CoreShare {
            id: link.id.max(0) as u64,
            kind: ports::GranteeKind::Link,
            grantee: None,
            grantee_display: None,
            owner,
            owner_display,
            perms: link.perms,
            created_s: (link.created_ns / 1_000_000_000) as i64,
            expires_s: link.expires_ns.map(|v| (v / 1_000_000_000) as i64),
            token: link.token,
            has_password: link.has_password,
            label: link.label.unwrap_or_default(),
            note: link.note.unwrap_or_default(),
            path,
            kind_is_dir: entry
                .as_ref()
                .map(|e| e.kind == sc_vfs::Kind::Dir)
                .unwrap_or(false),
            file_id: link
                .fileid_at_creation
                .or(entry.as_ref().and_then(|e| e.id))
                .unwrap_or(FileId::new(0)),
            parent_file_id: None,
        }
    }
}

impl ports::SharePort for NcShares {
    fn list(&self, user: UserId, filter: &ports::ShareFilter) -> PortResult<Vec<ports::CoreShare>> {
        // `shared_with_me` asks for grants *other people* made to this user.
        // Those live in the admin-owned grant model, not here, so the honest
        // answer is an empty list rather than this user's own links.
        if filter.shared_with_me {
            return Ok(Vec::new());
        }
        // The client's `path` is already a vpath; the compat layer normalised
        // its separators and this passes it through unchanged.
        let scope = filter
            .path
            .as_deref()
            .filter(|p| !p.is_empty())
            .map(Vpath::new);

        // `subfiles=true` asks which entries *inside* the folder are shared,
        // which is a different question from "is this folder shared" and
        // cannot be answered by narrowing the core query to one node. So the
        // core is asked for every link the caller owns and the prefix test
        // runs here, over vpaths, comparing whole components.
        let core_filter = if filter.subfiles { None } else { scope.as_ref() };
        let links = self
            .core
            .list_links(user, core_filter)
            .map_err(core_port_err)?;
        let mut out = Vec::new();
        for l in links {
            if filter.subfiles {
                let Some(folder) = &scope else { continue };
                let Some(v) = self.core.vpath_for(user, l.share, &l.path) else {
                    continue;
                };
                if v == *folder || !v.is_inside(folder) {
                    continue;
                }
            }
            out.push(self.to_core_share(user, l));
        }
        Ok(out)
    }

    fn get(&self, user: UserId, id: u64) -> PortResult<ports::CoreShare> {
        let link = self.core.get_link(user, id as i64).map_err(core_port_err)?;
        Ok(self.to_core_share(user, link))
    }

    fn create(&self, user: UserId, spec: &ports::ShareSpec) -> PortResult<ports::CoreShare> {
        if spec.kind != ports::GranteeKind::Link {
            return Err(grants_unavailable());
        }
        if let Some(e) = expiry_in_the_past(spec.expires_s) {
            return Err(e);
        }
        let vpath = Vpath::new(&spec.path);
        let core_spec = sc_core::LinkSpec {
            perms: spec.perms,
            password: spec.password.clone(),
            expires_ns: spec.expires_s.map(|s| s as i128 * 1_000_000_000),
            max_downloads: None,
            label: spec.label.clone(),
            note: spec.note.clone(),
        };
        let (link, _token) = self
            .core
            .create_link(user, &vpath, &core_spec)
            .map_err(core_port_err)?;
        Ok(self.to_core_share(user, link))
    }

    fn update(
        &self,
        user: UserId,
        id: u64,
        spec: &ports::ShareSpec,
    ) -> PortResult<ports::CoreShare> {
        if spec.kind != ports::GranteeKind::Link {
            return Err(grants_unavailable());
        }
        if let Some(e) = expiry_in_the_past(spec.expires_s) {
            return Err(e);
        }
        let patch = sc_core::LinkPatch {
            perms: Some(spec.perms),
            // `None` here means "the request carried no password field", not
            // "clear it" — the caller already collapsed an empty string to
            // `None`, and silently removing a link's password because an
            // unrelated field was edited would be a downgrade nobody asked
            // for. Clearing a link password is therefore not expressible
            // through this API; it is through `/api/shares/:id`.
            password: spec.password.clone().map(Some),
            expires_ns: Some(spec.expires_s.map(|s| s as i128 * 1_000_000_000)),
            max_downloads: None,
            label: Some(spec.label.clone()),
            note: Some(spec.note.clone()),
        };
        let link = self
            .core
            .update_link(user, id as i64, &patch)
            .map_err(core_port_err)?;
        Ok(self.to_core_share(user, link))
    }

    fn delete(&self, user: UserId, id: u64) -> PortResult<()> {
        self.core
            .delete_link(user, id as i64)
            .map_err(core_port_err)
    }

    fn kinds_for(&self, share: ShareId, id: FileId) -> PortResult<Vec<ports::GranteeKind>> {
        let links = self.core.links_for_node(share, id).map_err(core_port_err)?;
        Ok(if links.is_empty() {
            Vec::new()
        } else {
            vec![ports::GranteeKind::Link]
        })
    }

    fn find_grantees(
        &self,
        user: UserId,
        query: &str,
        scope: ports::GranteeScope,
    ) -> PortResult<Vec<ports::GranteeCandidate>> {
        if scope == ports::GranteeScope::Off {
            return Ok(Vec::new());
        }
        // `SameGroup` cannot be honoured — `sc-auth` has no group table — and
        // silently widening it to `All` would turn this into the account
        // enumeration oracle the setting exists to prevent. Refuse instead.
        if scope == ports::GranteeScope::SameGroup {
            return Ok(Vec::new());
        }
        let needle = query.to_lowercase();
        Ok(self
            .auth
            .list_users()
            .map_err(port_io)?
            .into_iter()
            .filter(|u| !u.disabled && u.id != user && u.name.to_lowercase().contains(&needle))
            .map(|u| ports::GranteeCandidate {
                kind: ports::GranteeKind::User,
                exact: u.name.eq_ignore_ascii_case(query),
                display: u.display.clone().unwrap_or_else(|| u.name.clone()),
                id: u.name,
                subline: None,
            })
            .collect())
    }

    fn link_url(&self, origin: &str, token: &str) -> String {
        format!("{}/s/{token}", origin.trim_end_matches('/'))
    }
}

// ------------------------------------------------------------- PreviewPort --

pub struct NcPreview {
    content_host: String,
    keys: Arc<parking_lot::Mutex<sc_http::content::SignedUrlKeys>>,
    core: Arc<sc_core::Core>,
}

impl ports::PreviewPort for NcPreview {
    fn can_preview(&self, e: &ports::Entry) -> bool {
        // Contract says no I/O, so this is an extension test rather than a
        // magic-byte sniff. Being wrong is cheap in one direction only: a
        // false positive costs a 404 on the thumbnail, a false negative
        // hides a preview that exists.
        if e.kind.is_dir() {
            return false;
        }
        matches!(
            e.name
                .rsplit('.')
                .next()
                .map(str::to_ascii_lowercase)
                .as_deref(),
            Some("jpg" | "jpeg" | "png" | "gif" | "webp" | "bmp" | "tif" | "tiff")
        )
    }

    fn signed_thumb_url(
        &self,
        user: UserId,
        path: &Vpath,
        w: u32,
        h: u32,
        _fit: ports::FitMode,
    ) -> PortResult<Option<String>> {
        self.sign(
            user,
            path,
            sc_http::content::Disposition::InlineThumb,
            Some((w.min(u16::MAX as u32) as u16, h.min(u16::MAX as u32) as u16)),
        )
    }

    fn signed_download_url(&self, user: UserId, path: &Vpath) -> PortResult<Option<String>> {
        // `Stream`, because the whole point is that a player can seek: that
        // disposition is the one the content origin serves `Range` requests
        // for. Its own default lifetime is twelve hours, which is right for a
        // share link somebody bookmarks and wrong here — the client hands this
        // URL straight to a player process, so it needs minutes.
        self.sign_with_ttl(
            user,
            path,
            sc_http::content::Disposition::Stream,
            None,
            Some(DIRECT_URL_TTL),
        )
    }
}

/// How long a media-streaming URL stays valid.
///
/// Long enough to start playback and survive a retry, short enough that a URL
/// that leaks out of a player's log or a screenshot is dead by the time anyone
/// reads it. The URL carries no `Authorization` header, so its lifetime is the
/// whole of its containment.
const DIRECT_URL_TTL: std::time::Duration = std::time::Duration::from_secs(10 * 60);

impl NcPreview {
    /// One signed claim over `(fileid, etag prefix)`, so a stale URL stops
    /// working the moment the file changes rather than serving an old copy of
    /// something that has since been replaced.
    ///
    /// ACL-checked here, at issue time, under the requesting principal: the
    /// URL that comes out carries no `Authorization` header.
    fn sign(
        &self,
        user: UserId,
        vpath: &Vpath,
        disposition: sc_http::content::Disposition,
        box_size: Option<(u16, u16)>,
    ) -> PortResult<Option<String>> {
        self.sign_with_ttl(user, vpath, disposition, box_size, None)
    }

    /// `ttl` of `None` takes the disposition's own default.
    fn sign_with_ttl(
        &self,
        user: UserId,
        vpath: &Vpath,
        disposition: sc_http::content::Disposition,
        box_size: Option<(u16, u16)>,
        ttl: Option<std::time::Duration>,
    ) -> PortResult<Option<String>> {
        if self.content_host.is_empty() {
            // No content origin configured. Returning `None` yields a 404,
            // which is right: serving user content from the *app* origin would
            // put attacker-influenced bytes on the origin that holds the
            // session cookie.
            return Ok(None);
        }
        let entry = self
            .core
            .stat_entry(user, vpath.as_str())
            .map_err(core_port_err)?;
        let Some(fid) = entry.id else { return Ok(None) };

        let mut etag8 = [0u8; 8];
        let raw = entry.etag.as_bytes();
        let n = raw.len().min(8);
        etag8[..n].copy_from_slice(&raw[..n]);

        let claim =
            sc_http::content::make_claim(fid.0, etag8, disposition, box_size, user.get(), ttl);
        let token = sc_http::content::sign(&self.keys.lock(), claim);
        Ok(Some(format!("https://{}/c/{token}", self.content_host)))
    }
}

// --------------------------------------------------------------- DirSize ---

pub struct NcDirSize {
    core: Arc<NcCore>,
}

impl sc_compat_nc::props::DirSize for NcDirSize {
    fn recursive_size(&self, share: ShareId, path: &SharePath) -> Option<u64> {
        use sc_compat_nc::ports::CorePort;
        self.core.aggregate(share, path).ok().map(|a| a.rsize)
    }
}

// ------------------------------------------------------- PropSource bridge --

/// Adapts the structured property source in `sc-compat-nc` to the XML-emitting
/// hook in `sc-dav`.
///
/// The two writers have different shapes on purpose (see
/// `sc_compat_nc::ports::PropSource`): compat accumulates values so its golden
/// tests can assert on `oc:permissions` as a string, DAV streams XML with a
/// declared prefix. Translating is ~30 lines and belongs here, in the crate
/// that already owns every other adapter, rather than forcing either side to
/// give up the shape it needs.
struct NcPropBridge {
    inner: Arc<sc_compat_nc::NcPropSource>,
    auth: Arc<sc_auth::AuthService>,
    /// Used only to materialize a missing `oc:id`/`oc:fileid` on demand
    /// (`emit`'s doc comment below) — nothing else here touches `sc-core`
    /// directly.
    core: Arc<sc_core::Core>,
    namespaces: Vec<(&'static str, &'static str)>,
}

fn prefix_of(ns: &str) -> Option<&'static str> {
    match ns {
        sc_compat_nc::NS_OC => Some("oc"),
        sc_compat_nc::NS_NC => Some("nc"),
        _ => None,
    }
}

impl sc_dav::PropSource for NcPropBridge {
    fn namespaces(&self) -> &[(&'static str, &'static str)] {
        &self.namespaces
    }

    fn emit(
        &self,
        e: &sc_dav::Entry,
        ctx: &sc_dav::PropCtx,
        req: &sc_dav::PropReq,
        out: &mut sc_dav::PropWriter,
    ) {
        let name = self
            .auth
            .find_user_by_id(ctx.user)
            .ok()
            .flatten()
            .map(|r| (r.display.clone().unwrap_or_else(|| r.name.clone()), r.name))
            .unwrap_or_default();

        // `ctx.path` is `sc-dav`'s own vpath, parsed straight off the URL this
        // mount was given, label included. It is not the share-relative path
        // the aggregate cache and `ensure_fileid` want, and treating it as one
        // is what put `oc:size` on the wrong node.
        let vpath = Vpath::new(&ctx.path.to_display_string());

        let mut nc_ctx = sc_compat_nc::ports::PropCtx {
            user: ctx.user,
            user_name: name.1.clone(),
            share: ctx.share,
            share_path: None,
            // Shares have no separate owner principal in this server: a share
            // is a configured directory, not a user's property. Reporting the
            // requesting user keeps the `oc:owner-*` properties well-formed
            // (clients skip entries whose owner is blank) without inventing an
            // ownership model the core does not have.
            owner_name: name.1,
            owner_display_name: name.0,
        };

        // `allprop` in DAV means "the cheap live set"; this source's own
        // notion of `allprop` is the same, so the flag maps straight across.
        let nc_req = if req.all || req.names_only {
            sc_compat_nc::ports::PropReq::allprop()
        } else {
            sc_compat_nc::ports::PropReq::explicit(
                req.requested.iter().map(|p| (p.ns.clone(), p.name.clone())),
            )
        };

        // `sc_core::Entry::id` is populated by a pure lookup (`ops.rs`'s
        // `build_entry`) that never allocates — right for the native API and
        // the web UI, which have no protocol reason to need one just to
        // display a listing. A compat client is different: `oc:id`/
        // `oc:fileid` is the key its local sync journal is built on, and
        // "no id yet" degrading to a shared placeholder (`FileId(0)`, inside
        // `sc_compat_nc::NcPropSource::emit`) meant every entry that had
        // never separately been written to, aggregated, or otherwise
        // touched reported the *same* id — observed as 11 of 12 grant roots
        // all answering `oc:fileid=0` in one listing. Materializing one here,
        // on demand, only when this specific request actually asked for
        // identity, keeps the lazy-allocation default intact for every
        // caller that never asks (`Core::ensure_fileid`'s doc comment).
        // Failure (the path having vanished between resolution and this
        // call, a filesystem error) is logged and left as `None` — the rest
        // of this response, and the rest of the multistatus, must not be
        // sacrificed to one entry's identity.
        //
        // `Core::resolve` is the same label lookup plus ACL evaluation every
        // vpath goes through and does no filesystem I/O, so one call recovers
        // both things this response needs from the share's own vocabulary: the
        // share path for `oc:size`'s aggregate, and the parent chain
        // `ensure_fileid` allocates along. An empty share path means the entry
        // sits exactly at its share's physical root, which has no `sc-meta`
        // row to allocate at all — see `share_root_pseudo_id`.
        let wants_identity = nc_req.wants(sc_compat_nc::NS_OC, "id")
            || nc_req.wants(sc_compat_nc::NS_OC, "fileid");
        let wants_dir_size = e.kind.is_dir() && nc_req.wants(sc_compat_nc::NS_OC, "size");
        let resolved = if (e.id.is_none() && wants_identity) || wants_dir_size {
            match self.core.resolve(ctx.user, &vpath) {
                Ok(r) => Some(r),
                Err(err) => {
                    tracing::warn!(error = %err, path = %vpath, "could not re-resolve a path while decorating a compat PROPFIND response");
                    None
                }
            }
        } else {
            None
        };
        nc_ctx.share_path = resolved.as_ref().map(|r| r.path.clone());

        let id = match (e.id, &resolved) {
            (Some(id), _) => Some(id),
            (None, Some(r)) if wants_identity && r.path.is_empty() => {
                Some(share_root_pseudo_id(r.share))
            }
            (None, Some(r)) if wants_identity => match self.core.ensure_fileid(r.share, &r.path) {
                Ok(id) => Some(id),
                Err(err) => {
                    tracing::warn!(error = %err, path = %vpath, "could not materialize a stable file id for a compat PROPFIND response");
                    None
                }
            },
            _ => None,
        };

        let entry = sc_core::Entry {
            name: e.name.clone(),
            kind: e.kind,
            size: e.size,
            mtime_ns: e.mtime_ns,
            etag: e.etag.clone(),
            perms: e.perms,
            id,
            is_symlink_denied: e.is_symlink_denied,
            confusable: e.confusable,
            btime_ns: e.btime_ns,
        };

        let mut sink = sc_compat_nc::ports::PropWriter::new();
        sc_compat_nc::ports::PropSource::emit(
            self.inner.as_ref(),
            &entry,
            &nc_ctx,
            &nc_req,
            &mut sink,
        );

        for (ns, name, value) in sink.as_slice() {
            let Some(prefix) = prefix_of(ns) else {
                continue;
            };
            match value {
                sc_compat_nc::ports::PropValue::Text(t) => out.text(prefix, name, t),
                sc_compat_nc::ports::PropValue::Empty => out.empty(prefix, name),
                sc_compat_nc::ports::PropValue::Children(children) => {
                    let mut inner_xml = String::new();
                    for (cns, cname, ctext) in children {
                        let Some(cprefix) = prefix_of(cns) else {
                            continue;
                        };
                        inner_xml.push_str(&format!("<{cprefix}:{cname}>"));
                        sc_dav::xml::escape_into(ctext, &mut inner_xml);
                        inner_xml.push_str(&format!("</{cprefix}:{cname}>"));
                    }
                    // Every byte of `inner_xml` was generated here from
                    // escaped text, never from a client-supplied string.
                    out.raw(prefix, name, &inner_xml);
                }
            }
        }
    }
}

// ------------------------------------------------------------------ router --

#[derive(Clone)]
struct RemoteDav {
    dav: Arc<sc_dav::DavService>,
    chunks: Arc<sc_compat_nc::chunking::ChunkedUploads>,
    core: Arc<NcCore>,
    /// For `oc:id` (`{fileid}{instance_id}`, `sc_compat_nc::nc_id`) on the
    /// synthetic files-root response (`h_files_root`) — the only place in
    /// this struct's own request path that renders `oc:id` itself rather
    /// than delegating to `NcPropBridge`, which already carries this same
    /// value by a different route (`self.cfg.instance_id` inside
    /// `Compat::prop_source`).
    instance_id: Arc<str>,
    journal: Option<Arc<crate::journal::WriteJournal>>,
}

/// Everything the compatibility layer needs, assembled once.
///
/// Built *before* the `DavService` is shared, because the property
/// decoration has to be registered on it (`add_prop_source` takes `&mut`)
/// and the DAV tree is not duplicated for the compatibility layout — the
/// `/remote.php/…` URLs are remapped onto the *same* service, with the same
/// backend and the same lock manager, under a per-request prefix.
pub struct Compat {
    cfg: Arc<sc_compat_nc::NcConfig>,
    store: Arc<dyn sc_compat_nc::NcStore>,
    core: Arc<NcCore>,
    upload: Arc<NcUpload>,
    shares: Arc<NcShares>,
    preview: Arc<NcPreview>,
    auth_port: Arc<NcAuth>,
    auth: Arc<sc_auth::AuthService>,
    /// Shared with the native search, so one `[search]` setting governs both.
    search_limits: Arc<sc_http::search_limits::SearchConcurrency>,
    storage: Arc<crate::storage_class::StorageClassCache>,
    journal: Option<Arc<crate::journal::WriteJournal>>,
}

/// Inputs to [`Compat::build`], grouped into a struct rather than passed
/// positionally: two fields (`canonical_url`, `content_host`) share the same
/// `String` type, so a plain parameter list lets them be swapped with no
/// compiler error — naming them at the call site removes that failure mode.
pub struct CompatBuildInputs<'a> {
    pub data_dir: &'a std::path::Path,
    pub canonical_url: String,
    /// Further origins this server answers on. Already validated by
    /// `Config::resolve_public_origins`.
    pub alt_canonical_urls: Vec<String>,
    pub core: Arc<sc_core::Core>,
    pub meta: Arc<sc_meta::MetaStore>,
    pub auth: Arc<sc_auth::AuthService>,
    pub uploads: Arc<sc_upload::UploadEngine>,
    pub content_host: String,
    pub keys: Arc<parking_lot::Mutex<sc_http::content::SignedUrlKeys>>,
    /// The same objects the native search bridge holds, not equal copies: the
    /// compat `SEARCH` and the native one must agree on the walk budget.
    pub search_limits: Arc<sc_http::search_limits::SearchConcurrency>,
    pub storage: Arc<crate::storage_class::StorageClassCache>,
    /// The same record the native surface writes, so a restore or an upload
    /// through a phone lands in the same list as one through the web tab.
    pub journal: Option<Arc<crate::journal::WriteJournal>>,
}

impl Compat {
    pub fn build(inputs: CompatBuildInputs<'_>) -> Self {
        let CompatBuildInputs {
            data_dir,
            canonical_url,
            alt_canonical_urls,
            core,
            meta,
            auth,
            uploads,
            content_host,
            keys,
            search_limits,
            storage,
            journal,
        } = inputs;
        let store: Arc<dyn sc_compat_nc::NcStore> = match sc_compat_nc::SqliteStore::open(
            &data_dir.join("compat-nc.db"),
        ) {
            Ok(s) => Arc::new(s),
            Err(e) => {
                // The instance id lives in this store, and losing it
                // forces a full resync on every connected client. Falling
                // back keeps the server up, but it must be loud.
                tracing::error!(error = %e, "compat store unavailable; falling back to memory (instance id will NOT persist)");
                Arc::new(sc_compat_nc::MemStore::new())
            }
        };
        let instance_id = store.instance_id().unwrap_or_default();

        let cfg = Arc::new(sc_compat_nc::NcConfig {
            canonical_url,
            alt_canonical_urls,
            instance_id,
            // Handed across from the crate that sets the cookie. `sc-compat-nc`
            // depends on no HTTP crate, so it cannot read the constant itself —
            // and when the name was written out on both sides instead, they
            // drifted: the compat layer looked for `sc_session` while the
            // server set `__Host-sc_sid`, so the login-flow consent screen
            // never saw a logged-in browser and bounced to `/login` forever.
            session_cookie: sc_http::SESSION_COOKIE.to_string(),
            ..Default::default()
        });

        let bridge = match tokio::runtime::Handle::try_current() {
            Ok(h) => Bridge::Reactor(h),
            Err(_) => Bridge::Standalone(
                tokio::runtime::Builder::new_current_thread()
                    .enable_all()
                    .build()
                    .expect("current-thread runtime"),
            ),
        };

        Compat {
            upload: Arc::new(NcUpload {
                engine: uploads,
                core: core.clone(),
                journal: journal.clone(),
            }),
            preview: Arc::new(NcPreview {
                content_host,
                keys,
                core: core.clone(),
            }),
            shares: Arc::new(NcShares {
                auth: auth.clone(),
                core: core.clone(),
            }),
            core: Arc::new(NcCore {
                core,
                meta,
                auth: auth.clone(),
            }),
            auth_port: Arc::new(NcAuth {
                auth: auth.clone(),
                bridge,
            }),
            cfg,
            store,
            auth,
            search_limits,
            storage,
            journal,
        }
    }

    /// The share adapter, so an integration test can prove the compat share
    /// API and `sc-core`'s link store are one and the same rows rather than
    /// two implementations that happen to agree.
    pub fn share_port(&self) -> Arc<dyn ports::SharePort> {
        self.shares.clone()
    }

    fn nc_search(&self) -> Arc<crate::nc_search::NcSearch> {
        Arc::new(crate::nc_search::NcSearch {
            core: self.core.core.clone(),
            meta: self.core.meta.clone(),
            auth: self.auth.clone(),
            store: self.store.clone(),
            limits: self.search_limits.clone(),
            storage: self.storage.clone(),
            journal: self.journal.clone(),
        })
    }

    /// The search backend, over the caller's readable roots.
    pub fn search_source(&self) -> Arc<dyn sc_dav::SearchSource> {
        self.nc_search()
    }

    /// The favourites report, answered through the same search.
    pub fn report_source(&self) -> Arc<dyn sc_dav::ReportSource> {
        Arc::new(crate::nc_search::NcFilterFilesReport)
    }

    /// The write side of the favourite property, so a `PROPPATCH` on it never
    /// reaches the dead-property store.
    pub fn favorite_writer(&self) -> Arc<dyn sc_dav::PropPatchSource> {
        Arc::new(crate::nc_search::NcFavoriteWriter {
            store: self.store.clone(),
        })
    }

    /// The `oc:`/`nc:` property decoration, ready to hand to
    /// `DavService::add_prop_source`.
    pub fn prop_source(&self) -> Arc<dyn sc_dav::PropSource> {
        Arc::new(NcPropBridge {
            inner: Arc::new(sc_compat_nc::NcPropSource::new(
                self.store.clone(),
                self.shares.clone(),
                self.preview.clone(),
                Arc::new(NcDirSize {
                    core: self.core.clone(),
                }),
                self.cfg.instance_id.clone(),
            )),
            auth: self.auth.clone(),
            // `NcCore`'s own field, not a separately-threaded input: `Compat`
            // already holds exactly one `sc_core::Core`, wrapped once in
            // `NcCore` (`Compat::build`) — this reaches straight into it
            // rather than plumbing a second clone of the same `Arc` through
            // `CompatBuildInputs` for what both crate-private fields are.
            core: self.core.core.clone(),
            namespaces: sc_compat_nc::props::NC_NAMESPACES
                .iter()
                .map(|(p, u)| (*p, *u))
                .collect(),
        })
    }

    pub fn router(&self, dav: Arc<sc_dav::DavService>) -> Router {
        let deps = sc_compat_nc::ports::Deps {
            core: self.core.clone(),
            auth: self.auth_port.clone(),
            upload: self.upload.clone(),
            shares: self.shares.clone(),
            preview: self.preview.clone(),
            search: self.nc_search(),
        };

        let login = Arc::new(sc_compat_nc::login_flow::LoginFlowService::new(
            self.store.clone(),
            deps.auth.clone(),
            self.cfg.clone(),
            Arc::new(sc_compat_nc::login_flow::SystemClock),
        ));

        let state = sc_compat_nc::NcState {
            cfg: self.cfg.clone(),
            store: self.store.clone(),
            deps,
            login,
            shares: Arc::new(sc_compat_nc::shares::SharesApi::new(self.shares.clone())),
            sharees: Arc::new(sc_compat_nc::shares::ShareesApi::new(
                self.shares.clone(),
                self.cfg.clone(),
            )),
            preview: Arc::new(sc_compat_nc::preview::PreviewApi::new(
                self.core.clone(),
                self.preview.clone(),
            )),
            unified: Arc::new(sc_compat_nc::unified_search::UnifiedSearchApi::new(
                self.nc_search(),
                self.preview.clone(),
            )),
        };

        let remote = RemoteDav {
            dav,
            chunks: Arc::new(sc_compat_nc::chunking::ChunkedUploads::new(
                self.store.clone(),
                self.upload.clone(),
                self.cfg.instance_id.clone(),
            )),
            core: self.core.clone(),
            instance_id: Arc::from(self.cfg.instance_id.as_str()),
            journal: self.journal.clone(),
        };

        sc_compat_nc::router(state)
            .merge(
                Router::new()
                    .route("/remote.php/{*rest}", any(h_remote))
                    .route("/index.php/remote.php/{*rest}", any(h_remote))
                    .with_state(remote),
            )
            .layer(axum::middleware::from_fn(relabel_client_addr))
    }

    /// Periodic sweep of expired Login Flow v2 rows
    /// (`login_flow.rs`'s own "20-minute expiry plus a sweep" security
    /// property). `flow_peek`/`flow_approve`/`flow_poll` already treat an
    /// expired row as gone, but none of them delete it — only `flow_sweep`
    /// does, and until this was wired up nothing ever called it, so
    /// `nc_login_flow` grew without bound on a server that sees repeated
    /// enrolment attempts. Mirrors `DavService::spawn_lock_sweeper`: a
    /// detached `tokio::spawn` loop, not stopped at shutdown, since a sweep
    /// pass touches nothing that shutdown also touches.
    pub fn spawn_login_flow_sweeper(&self) -> tokio::task::JoinHandle<()> {
        use sc_compat_nc::login_flow::Clock as _;

        let store = self.store.clone();
        let clock = sc_compat_nc::login_flow::SystemClock;
        tokio::spawn(async move {
            let mut tick = tokio::time::interval(std::time::Duration::from_secs(5 * 60));
            loop {
                tick.tick().await;
                match store.flow_sweep(clock.now_ns()) {
                    Ok(n) if n > 0 => tracing::debug!("swept {n} expired login flow v2 rows"),
                    Ok(_) => {}
                    Err(e) => tracing::warn!(error = %e, "login flow v2 sweep failed"),
                }
            }
        })
    }
}

/// Restate the resolved client address in the compatibility layer's own
/// vocabulary.
///
/// `sc-compat-nc` declares its port types itself and depends on no HTTP crate,
/// so it cannot read `sc_http`'s `ClientIp`
/// directly. This copies the value across — and only the value. The rule that
/// *produced* it (peer vs. `CF-Connecting-IP` vs. `X-Forwarded-For`, gated on
/// the trusted-proxy CIDRs) stays in its single home,
/// `sc_http::middleware::resolve_client_ip`, which `App::router` runs once in
/// front of this mount. If that layer ever stops running the extension is
/// absent and `ClientAddr::default()` reports an unknown client — never a
/// plausible-looking wrong answer.
async fn relabel_client_addr(mut req: Request, next: axum::middleware::Next) -> Response {
    if let Some(ip) = req
        .extensions()
        .get::<sc_http::state::ClientIp>()
        .map(|c| c.0)
    {
        req.extensions_mut().insert(ports::ClientAddr(ip));
    }
    next.run(req).await
}

/// Merge the compatibility routes into the application router.
pub fn router(app: &App) -> Router {
    match &app.compat {
        Some(c) => c.router(app.dav.clone()),
        None => Router::new(),
    }
}

async fn h_remote(State(s): State<RemoteDav>, req: Request) -> Response {
    use sc_compat_nc::dav_paths::DavTarget;

    let path = req.uri().path().to_string();
    let Some(target) = sc_compat_nc::dav_paths::parse(&path) else {
        return StatusCode::NOT_FOUND.into_response();
    };

    match target {
        // The root collection and the principals stub are Depth: 0 discovery
        // PROPFINDs. Serving them through the DAV tree at its own prefix is
        // correct: what a client wants back is a collection that exists.
        DavTarget::Root | DavTarget::PrincipalRoot | DavTarget::Principal { .. } => {
            s.dav.with_prefix("/remote.php/dav").handle(req).await
        }
        DavTarget::Files { user, path } => {
            // The prefix must include the user segment, or every `<D:href>`
            // in the multistatus comes back pointing outside the tree the
            // client asked about — which desktop clients treat as a moved
            // resource and re-download.
            let prefix = if user.is_empty() {
                "/remote.php/webdav".to_string()
            } else {
                format!("/remote.php/dav/files/{user}")
            };
            // `path.is_empty()` is the caller's files root *itself* —
            // `sc-core`'s vpath vocabulary has no root of its own (every
            // vpath names a grant-projected label as its first segment;
            // `Core::resolve_want` refuses an empty one, `resolve.rs`), so
            // handing this straight to `sc_dav::DavService` the way every
            // other request on this branch does would 404 unconditionally —
            // which is exactly what a real client's very first request
            // after Login Flow v2 finishes *is*. See `h_files_root`'s doc
            // comment for what answers instead.
            //
            // `HEAD` needs the same carve-out as `PROPFIND`: the Android
            // client's `ExistenceCheckRemoteOperation` (run right after
            // Login Flow v2 hands back credentials) sends `HEAD` on this
            // exact URL and only treats {200, 401, 403} as "the account is
            // usable" — a 404 here took the "authorization fail due to
            // client side" branch in `AuthenticatorActivity`, which
            // re-opens the web login and produces the second, uncollectable
            // Grant Access window the poller can no longer see.
            if path.is_empty() && matches!(req.method().as_str(), "PROPFIND" | "HEAD") {
                return h_files_root(s, prefix, req).await;
            }
            // The root above is real — `HEAD`/`PROPFIND` just answered 200/207
            // for it — but it is not a writable collection: every vpath's
            // first segment must be a grant-projected label
            // (`Core::resolve_want`'s `label.is_empty()` is always
            // `CoreError::NotFound`, `resolve.rs`), and the root has no label
            // of its own to write under or create children in. Falling
            // through to `s.dav`/`h_put_files` here used to answer `404`,
            // which wrongly claims the root itself does not exist; real
            // sabre-dav answers `405` for a `PUT`/`MKCOL` against an
            // existing, non-writable collection (`CorePlugin::httpPut`/
            // `httpMkcol`, both throw `MethodNotAllowed`), so we match that.
            if path.is_empty() && matches!(req.method().as_str(), "PUT" | "MKCOL") {
                return files_root_method_not_allowed();
            }
            // Only `PUT` carries an upload-time header worth reading
            // (`h_put_files`'s doc comment). Everything else — GET, the
            // PROPFIND family, MOVE, COPY, DELETE, MKCOL — is untouched, on
            // the same fast path as before.
            if req.method().as_str() == "PUT" {
                h_put_files(s, prefix, path, req).await
            } else {
                s.dav.with_prefix(prefix).handle(req).await
            }
        }
        DavTarget::UploadHome { .. }
        | DavTarget::UploadFolder { .. }
        | DavTarget::UploadChunk { .. } => h_chunked(s, target, req).await,
        DavTarget::TrashRoot { .. }
        | DavTarget::TrashEntry { .. }
        | DavTarget::TrashRestore { .. } => h_trash(s, target, req).await,
    }
}

/// The trashbin collection. Everything it needs is already public on `Core`;
/// what is new is the flat, per-user URL shape it is served under.
async fn h_trash(
    s: RemoteDav,
    target: sc_compat_nc::dav_paths::DavTarget,
    req: Request,
) -> Response {
    use sc_compat_nc::dav_paths::DavTarget;

    let Some(sc_dav::DavPrincipal(user)) = req.extensions().get::<sc_dav::DavPrincipal>().copied()
    else {
        return StatusCode::UNAUTHORIZED.into_response();
    };
    let api = crate::nc_trash::TrashApi {
        core: s.core.core.clone(),
        instance_id: s.instance_id.clone(),
        journal: s.journal.clone(),
    };
    let max_body = s.dav.config().max_request_body;
    let method = req.method().as_str().to_string();

    match (method.as_str(), target) {
        ("PROPFIND", DavTarget::TrashRoot { user: du }) => {
            let prefix = format!("/remote.php/dav/trashbin/{du}/trash");
            api.propfind(user, &prefix, None, max_body, req).await
        }
        ("PROPFIND", DavTarget::TrashEntry { user: du, entry }) => {
            let prefix = format!("/remote.php/dav/trashbin/{du}/trash/{entry}");
            api.propfind(user, &prefix, Some(&entry), max_body, req).await
        }
        ("DELETE", DavTarget::TrashRoot { .. }) => api.empty_all(user),
        ("DELETE", DavTarget::TrashEntry { entry, .. }) => api.purge_one(user, &entry),
        ("MOVE", DavTarget::TrashEntry { entry, .. }) => {
            let dest = req
                .headers()
                .get("destination")
                .and_then(|v| v.to_str().ok())
                .map(str::to_string);
            api.restore(user, &entry, dest.as_deref())
        }
        _ => {
            let mut resp = StatusCode::METHOD_NOT_ALLOWED.into_response();
            resp.headers_mut().insert(
                http::header::ALLOW,
                http::HeaderValue::from_static("OPTIONS, PROPFIND, DELETE, MOVE"),
            );
            resp
        }
    }
}

/// `405` for a `PUT`/`MKCOL` targeting the empty-path files root, with the
/// `Allow` header naming exactly the methods that root actually answers
/// (`OPTIONS` — handled generically, upstream of this dispatch — plus the
/// two `h_files_root` serves). See `h_remote`'s call site for why 405 and not
/// 404.
fn files_root_method_not_allowed() -> Response {
    let mut resp = StatusCode::METHOD_NOT_ALLOWED.into_response();
    resp.headers_mut().insert(
        http::header::ALLOW,
        http::HeaderValue::from_static("OPTIONS, PROPFIND, HEAD"),
    );
    resp
}

/// `PROPFIND /remote.php/dav/files/{user}/` and its `/remote.php/webdav/`
/// alias, addressing the empty relative path — the caller's files root
/// *itself*.
///
/// There is no single "home directory" in this server the way the
/// reference server's default install has one: a user can be granted
/// several roots, each a distinct `sc-core` label, and — before per-user
/// grants — the compat layer's only opinion about which one to treat as
/// "home" was `NcCore::home_root`'s "the first projected root", used
/// nowhere in the plain WebDAV path at all. A real client's very first
/// request after Login Flow v2 finishes enrolment is a PROPFIND of exactly
/// this URL, and it went straight to `sc_dav::DavService` with an empty
/// vpath, which `sc-core` refuses unconditionally (`resolve.rs`:
/// `label.is_empty()` is always `NotFound`, independent of how many shares
/// exist or who has grants on them) — so no client could ever browse past
/// the root, whether or not the grant projection changed underneath it.
///
/// The honest answer — and the one real desktop/mobile clients already
/// handle correctly, since it is exactly how the reference server's own
/// "external storage" / group-folder mounts appear on the wire — is a
/// collection whose children are the caller's grant-projected roots, named
/// by label: `oc:permissions`, `oc:fileid` and friends are real values
/// pulled from `sc-core`, not placeholders (`NcCore::root_entries`), so a
/// client that stats a child before descending into it sees a consistent
/// answer both here and when it PROPFINDs that label directly afterward.
async fn h_files_root(s: RemoteDav, prefix: String, req: Request) -> Response {
    let Some(sc_dav::DavPrincipal(user)) = req.extensions().get::<sc_dav::DavPrincipal>().copied()
    else {
        // `with_dav_auth` (`app.rs`) inserts this for every authenticated
        // request and lets an unauthenticated one through unchanged so
        // `sc-dav` can answer its own `401` with a `WWW-Authenticate`
        // realm; this branch never reaches that machinery, so it has to
        // answer the same shape of failure itself.
        return StatusCode::UNAUTHORIZED.into_response();
    };

    // `HEAD` only wants to know the collection exists — no PROPFIND body to
    // parse, no multistatus to render. The reference server answers 200 here
    // (sabre/dav's browser plugin serves the files root to `GET`/`HEAD`
    // alike), which is also the only status this handler needs to produce:
    // the caller has already been authenticated above.
    if req.method() == http::Method::HEAD {
        return StatusCode::OK.into_response();
    }

    // `Depth: 0` asks for the collection's own properties only; anything
    // else (`1`, `infinity`, or the header's absence — `sc_dav::parse_depth`
    // defaults an absent header to `Infinity`, and every reference client
    // that omits it here means "list my roots", not "just the root") lists
    // the immediate children. Nothing is recursed *into* a root itself:
    // that is exactly what the client's follow-up PROPFIND on that label
    // does, over the already-working per-label path.
    let list_children = req
        .headers()
        .get("depth")
        .and_then(|v| v.to_str().ok())
        .map(|v| v.trim() != "0")
        .unwrap_or(true);

    let max_body = s.dav.config().max_request_body;
    let body_bytes = match axum::body::to_bytes(req.into_body(), max_body).await {
        Ok(b) => b,
        Err(_) => return StatusCode::PAYLOAD_TOO_LARGE.into_response(),
    };
    // Same parser real PROPFIND dispatch uses (`sc_dav::xml::parse_propfind`,
    // hardened against XXE/billion-laughs/depth blowups — `xml.rs`'s module
    // doc), so a malformed body is refused the same way here as everywhere
    // else in this server, not waved through because this response happens
    // to be hand-assembled.
    let want = match sc_dav::xml::parse_propfind(&body_bytes, max_body) {
        Ok(w) => w,
        Err(_) => return StatusCode::BAD_REQUEST.into_response(),
    };
    let propreq = match want {
        sc_dav::xml::PropFindBody::AllProp => sc_dav::PropReq {
            all: true,
            names_only: false,
            requested: Vec::new(),
        },
        sc_dav::xml::PropFindBody::PropName => sc_dav::PropReq {
            all: false,
            names_only: true,
            requested: Vec::new(),
        },
        sc_dav::xml::PropFindBody::Prop(names) => sc_dav::PropReq {
            all: false,
            names_only: false,
            requested: names,
        },
    };

    let roots = s.core.root_entries(user);
    // One aggregate per grant, computed here rather than inside the XML
    // builder so that function stays a pure renderer and can be unit-tested.
    // A grant's `subpath` is already the share path of that grant's root, so
    // no conversion is involved.
    let sizes: Vec<Option<u64>> = roots
        .iter()
        .map(|r| {
            s.core
                .core
                .aggregate(r.share, &r.subpath)
                .ok()
                .map(|a| a.rsize)
        })
        .collect();
    let body = files_root_propfind_xml(
        &prefix,
        &roots,
        &sizes,
        list_children,
        &propreq,
        &s.instance_id,
    );
    (
        StatusCode::MULTI_STATUS,
        [(http::header::CONTENT_TYPE, "application/xml; charset=utf-8")],
        body,
    )
        .into_response()
}

/// A stand-in `oc:fileid`/`oc:id` for the files-root collection itself —
/// not any one share, so [`share_root_pseudo_id`] does not apply. Bit 61,
/// not bit 62 (`SHARE_ROOT_PSEUDO_MARKER`): a distinct reserved bit rather
/// than reusing that one with a share id of `0`, since nothing in this
/// codebase actually guarantees `ShareId` is never `0` (every *configured*
/// share happens to be 1-indexed — `app.rs`'s `register_shares` — but nothing
/// enforces that as an invariant of the type itself), and a real "share 0"
/// would otherwise collide with this constant. Still a plain positive
/// integer, for the same reason `SHARE_ROOT_PSEUDO_MARKER` is — see that
/// constant's doc comment for why negative (`i64::MIN`, this value's
/// predecessor) is actively unsafe on the wire, not merely unconventional.
const FILES_ROOT_PSEUDO_ID: i64 = 1 << 61;

/// Namespace URI -> XML prefix for the three vocabularies this response
/// declares on `<d:multistatus>`. `None` means "not one of ours" — the
/// client named a property we have no declared prefix for at all, which
/// gets dropped from the 404 list rather than rendered with a made-up one
/// (mirrors `NcPropBridge`'s own `prefix_of`, which this can't reuse
/// directly: that one doesn't know about `DAV:` since `sc-dav` renders its
/// own `d:` properties itself; this function assembles the whole response
/// by hand and has to cover all three).
fn root_prefix_of(ns: &str) -> Option<&'static str> {
    match ns {
        sc_dav::xml::NS_DAV => Some("d"),
        sc_compat_nc::NS_OC => Some("oc"),
        sc_compat_nc::NS_NC => Some("nc"),
        _ => None,
    }
}

/// One `<d:response>` row: a `200 OK` propstat carrying every property in
/// `known` (each already rendered as its complete tag), plus — RFC 4918
/// §9.1, and only when the client enumerated properties explicitly rather
/// than sending `allprop`/`propname` (`sc_dav::PropReq::is_explicit`, the
/// same rule `sc-dav`'s own PROPFIND dispatch applies) — a second `404 Not
/// Found` propstat for any of the client's *own* named properties that
/// `known` did not answer. Before this, an unanswerable requested property
/// on a synthetic row was simply absent from the response with no propstat
/// at all, which several clients treat identically to "the server is
/// broken" rather than "not applicable here".
pub(crate) fn propfind_row(
    href: &str,
    known: &[(&'static str, &'static str, String)],
    req: &sc_dav::PropReq,
) -> String {
    let mut out = String::new();
    out.push_str("<d:response><d:href>");
    out.push_str(href);
    out.push_str("</d:href><d:propstat><d:prop>");
    for (_, _, xml) in known {
        out.push_str(xml);
    }
    out.push_str("</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>");

    if req.is_explicit() {
        let mut missing_prop = String::new();
        for p in &req.requested {
            if known
                .iter()
                .any(|(ns, name, _)| p.ns == *ns && p.name == *name)
            {
                continue;
            }
            if let Some(prefix) = root_prefix_of(&p.ns) {
                missing_prop.push('<');
                missing_prop.push_str(prefix);
                missing_prop.push(':');
                missing_prop.push_str(&p.name);
                missing_prop.push_str("/>");
            }
        }
        if !missing_prop.is_empty() {
            out.push_str("<d:propstat><d:prop>");
            out.push_str(&missing_prop);
            out.push_str("</d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>");
        }
    }
    out.push_str("</d:response>");
    out
}

/// A DAV text-element tag, self-closing (no value) under `names_only`
/// (`PROPFIND` mode `propname`) exactly the way `sc_dav::PropWriter::text`
/// degrades in the same mode — a `propname` request wants to know *which*
/// properties exist, never their values.
pub(crate) fn prop_tag(prefix: &str, name: &str, value: &str, names_only: bool) -> String {
    if names_only {
        return format!("<{prefix}:{name}/>");
    }
    let mut escaped = String::new();
    sc_compat_nc::ocs::xml_escape_text(value, &mut escaped);
    format!("<{prefix}:{name}>{escaped}</{prefix}:{name}>")
}

/// Hand-rolled rather than routed through `sc_dav::DavService`'s own
/// PROPFIND machinery (`propfind.rs`): that machinery resolves one vpath
/// under one share and lists *its* children, which has no way to express
/// "list every root the caller has, each in a different share" — the whole
/// reason this response has to be synthesized here. Follows the same style
/// as this crate's other hand-rolled multistatus bodies
/// (`chunking::chunk_listing_xml`, `dav_paths::principal_propfind_xml`).
fn files_root_propfind_xml(
    prefix: &str,
    roots: &[RootRow],
    // One per root, positionally: the recursive size of that grant's subtree,
    // `None` when the aggregate could not be computed.
    sizes: &[Option<u64>],
    list_children: bool,
    req: &sc_dav::PropReq,
    instance_id: &str,
) -> String {
    let mut body = String::from(
        "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n\
         <d:multistatus xmlns:d=\"DAV:\" xmlns:oc=\"http://owncloud.org/ns\" xmlns:nc=\"http://nextcloud.org/ns\">",
    );

    // The collection's own row always comes first, matching every other
    // DAV response in this crate.
    //
    // `getetag`: hashed over every root's `(label, etag)` pair rather than
    // left absent. A client uses this to decide whether to re-list at all
    // — absent, that shortcut is gone and every sync pass re-walks the
    // whole account; a constant would be *worse* than absent, since it
    // would tell the client nothing ever changes. Hashing the actual root
    // set means it changes exactly when a grant is added/removed or a
    // root's own top-level content changes (its `etag` already reflects
    // that — `NcCore::root_entries`).
    //
    // `oc:permissions`: `G` only (read, no `S R M D N V C K W`) — a real
    // choice, not an omission. There is nothing to write "next to" the
    // caller's roots: this collection has no filesystem backing of its own
    // for `CREATE`/`WRITE`/`DELETE`/`RENAME`/`MOVE` to mean anything about,
    // and reporting a permissive string here invites a client to offer an
    // upload that can only ever fail.
    //
    // `quota-*`: the reference server's own root (`FilesHome` — a real,
    // single-mount folder there) reports real numbers via `IQuota` on
    // whatever storage backs it. This collection has no single storage —
    // its children can be different shares on different filesystems — so
    // there is no honest single figure to aggregate them into. The reference server's
    // own `FileInfo::SPACE_UNKNOWN` sentinel (`-2`) exists for exactly this
    // "not computable" case and is already part of the vocabulary real
    // clients parse, so it is used verbatim rather than inventing a new
    // one or fabricating a sum.
    let self_etag = {
        let mut hasher = blake3::Hasher::new();
        for r in roots {
            hasher.update(r.label.as_bytes());
            hasher.update(r.entry.etag.as_bytes());
        }
        data_encoding::HEXLOWER.encode(&hasher.finalize().as_bytes()[..16])
    };

    // The root's own total, summed over **distinct `(share, subpath)` pairs**
    // rather than over labels: two grants can project one subtree under two
    // names, and summing per label would count it twice. `None` from any
    // contributing grant makes the whole total unavailable, because a partial
    // sum is a wrong number rather than a missing one.
    //
    // Unlike `quota-*` below this is a well-defined figure. A collection with
    // no single storage behind it has no honest free-space number; a total of
    // recursive sizes has no such problem.
    let self_size: Option<u64> = {
        let mut seen: Vec<(ShareId, String)> = Vec::new();
        let mut total = Some(0u64);
        for (r, size) in roots.iter().zip(sizes) {
            let key = (r.share, r.subpath.to_display_string());
            if seen.contains(&key) {
                continue;
            }
            seen.push(key);
            total = match (total, size) {
                (Some(t), Some(s)) => Some(t.saturating_add(*s)),
                _ => None,
            };
        }
        total
    };
    // `oc:id` (`{fileid}{instance_id}`, `sc_compat_nc::nc_id`) alongside
    // `oc:fileid`, not declared 404: it is the value a client's local sync
    // journal is actually keyed on (`NcPropSource::emit`'s doc comment on
    // `oc:id`/`file_id`), and this is the *first* response a real client
    // ever sees after enrolment. Both use the exact same
    // `FILES_ROOT_PSEUDO_ID`/`share_root_pseudo_id` values the direct
    // per-label PROPFIND path computes (`NcPropBridge::emit`), so the two
    // views of the same resource agree byte-for-byte — proved in
    // `compat_fileid_uniqueness.rs`'s
    // `the_synthetic_root_and_a_direct_propfind_agree_on_the_same_resource`.
    let mut self_known: Vec<(&'static str, &'static str, String)> = vec![
        (
            sc_dav::xml::NS_DAV,
            "resourcetype",
            "<d:resourcetype><d:collection/></d:resourcetype>".to_string(),
        ),
        (
            sc_dav::xml::NS_DAV,
            "getetag",
            prop_tag("d", "getetag", &format!("\"{self_etag}\""), req.names_only),
        ),
        (
            sc_dav::xml::NS_DAV,
            "quota-available-bytes",
            prop_tag("d", "quota-available-bytes", "-2", req.names_only),
        ),
        (
            sc_dav::xml::NS_DAV,
            "quota-used-bytes",
            prop_tag("d", "quota-used-bytes", "-2", req.names_only),
        ),
        (
            sc_compat_nc::NS_OC,
            "permissions",
            prop_tag("oc", "permissions", "G", req.names_only),
        ),
        (
            sc_compat_nc::NS_OC,
            "fileid",
            prop_tag(
                "oc",
                "fileid",
                &FILES_ROOT_PSEUDO_ID.to_string(),
                req.names_only,
            ),
        ),
        (
            sc_compat_nc::NS_OC,
            "id",
            prop_tag(
                "oc",
                "id",
                &sc_compat_nc::nc_id(sc_vfs::FileId::new(FILES_ROOT_PSEUDO_ID), instance_id),
                req.names_only,
            ),
        ),
    ];
    // Omitted, never emitted empty and never substituted, when the total is
    // unavailable: an absent `oc:size` leaves the Android parser at its
    // initialised 0, while `<oc:size/>` is an unguarded cast that fails the
    // entire folder listing rather than one property.
    if let Some(total) = self_size {
        self_known.push((
            sc_compat_nc::NS_OC,
            "size",
            prop_tag("oc", "size", &total.to_string(), req.names_only),
        ));
    }
    body.push_str(&propfind_row(
        &path_escape(&format!("{prefix}/")),
        &self_known,
        req,
    ));

    if list_children {
        for (r, size) in roots.iter().zip(sizes) {
            let (label, e) = (&r.label, &r.entry);
            let is_dir = e.kind.is_dir();
            let href = path_escape(&format!("{prefix}/{label}/"));
            let permissions = sc_compat_nc::oc_permissions(e.perms, is_dir, false);
            let file_id = e.id.unwrap_or(sc_vfs::FileId::new(0));
            let resourcetype = if is_dir {
                "<d:resourcetype><d:collection/></d:resourcetype>".to_string()
            } else {
                "<d:resourcetype/>".to_string()
            };
            let mut child_known: Vec<(&'static str, &'static str, String)> = vec![
                (sc_dav::xml::NS_DAV, "resourcetype", resourcetype),
                (
                    sc_dav::xml::NS_DAV,
                    "displayname",
                    prop_tag("d", "displayname", label, req.names_only),
                ),
                (
                    sc_dav::xml::NS_DAV,
                    "getetag",
                    prop_tag("d", "getetag", &format!("\"{}\"", e.etag), req.names_only),
                ),
                (
                    sc_compat_nc::NS_OC,
                    "permissions",
                    prop_tag("oc", "permissions", &permissions, req.names_only),
                ),
                (
                    sc_compat_nc::NS_OC,
                    "id",
                    prop_tag(
                        "oc",
                        "id",
                        &sc_compat_nc::nc_id(file_id, instance_id),
                        req.names_only,
                    ),
                ),
                (
                    sc_compat_nc::NS_OC,
                    "fileid",
                    prop_tag("oc", "fileid", &file_id.0.to_string(), req.names_only),
                ),
            ];
            // The reason every top-level folder read 0 B: this row carried no
            // `oc:size` at all, and this listing is the first thing a client
            // performs after enrolment.
            if let Some(size) = size {
                child_known.push((
                    sc_compat_nc::NS_OC,
                    "size",
                    prop_tag("oc", "size", &size.to_string(), req.names_only),
                ));
            }
            body.push_str(&propfind_row(&href, &child_known, req));
        }
    }

    body.push_str("</d:multistatus>");
    body
}

/// `PUT /remote.php/dav/files/{user}/{path}` and its `/remote.php/webdav/`
/// alias: the plain, non-chunked upload.
///
/// This is the *common* case for mobile clients, not a fallback: Android
/// only switches to the chunked v2 flow (`h_chunked`) above a fixed
/// threshold —
///
/// ```text
/// ChunkedFileUploadRemoteOperation.java (class doc):
///     "This operation is used for chunking uploads that exceeds
///      CHUNK_SIZE_MOBILE"
/// UploadFileRemoteOperation.java is what runs below it,
/// ```
///
/// and that threshold is 10,240,000 bytes — bigger than most camera-roll
/// photos. Below it (and for every iOS/desktop plain upload) the client
/// sends `X-OC-Mtime` — and optionally `X-OC-Ctime` — on this exact
/// request:
///
/// ```text
/// Android UploadFileRemoteOperation.java:203-210
///     putMethod.addRequestHeader(OC_TOTAL_LENGTH_HEADER, String.valueOf(f.length()));
///     putMethod.addRequestHeader(OC_X_OC_MTIME_HEADER, String.valueOf(lastModificationTimestamp));
///     if (creationTimestamp != null && creationTimestamp > 0) {
///         putMethod.addRequestHeader(OC_X_OC_CTIME_HEADER, String.valueOf(creationTimestamp));
///     }
/// iOS client SDK upload path (fractional — Swift `Double`,
/// timeIntervalSince1970 — unlike Android's bare integer)
///     headers.update(name: "X-OC-CTime", value: "\(dateCreationFile.timeIntervalSince1970)")
///     headers.update(name: "X-OC-MTime", value: "\(dateModificationFile.timeIntervalSince1970)")
/// desktop (v1, non-chunked) propagateupload.cpp:787
///     headers[QByteArrayLiteral("X-OC-Mtime")] = QByteArray::number(qint64(_item->_modtime));
/// ```
///
/// Before this, that header was read only on the chunked assembly MOVE
/// (`chunking::oc_mtime_ns`) — so every photo under Android's chunking
/// threshold landed with the upload time as its mtime instead of the
/// capture time, and the gallery sorted wrong.
///
/// `X-OC-Mtime` is compat vocabulary and must not leak into `sc-dav`
/// (`scripts/verify.sh`'s `\boc[:_-]` gate), so `sc-dav` does the actual
/// write knowing nothing about it; this only reads the header beforehand
/// and applies the timestamp afterward through `sc_vfs::ShareRoot::set_times`
/// — the same generic, vendor-neutral primitive the chunked path's
/// `UploadEngine::assemble_and_finalize` already uses
/// (`sc-upload/src/engine.rs::finalize_locked`).
async fn h_put_files(s: RemoteDav, prefix: String, rel_path: String, req: Request) -> Response {
    // `oc_mtime_ns` truncates iOS's fractional seconds and rejects anything
    // else non-numeric; `unwrap_or(None)` folds that rejection into "no
    // header was usable" rather than a request error. That is deliberate:
    // the reference server's equivalent parse failure
    // (`MtimeSanitizer::sanitizeMtime`) throws an uncaught
    // `InvalidArgumentException` out of `File::put()` *after* the rename has
    // already happened (`apps/dav/lib/Connector/Sabre/File.php:322,339,361`),
    // which Sabre turns into a bare 500 for a file that is already correctly
    // on disk and that the client will not retry. Failing the same way here
    // would be reproducing an accident, not the contract; ignoring an
    // unusable header and keeping the upload's success is what every
    // sync/mobile client actually needs.
    let mtime_ns = sc_compat_nc::chunking::oc_mtime_ns(req.headers()).unwrap_or(None);
    let principal = req.extensions().get::<sc_dav::DavPrincipal>().copied();

    let mut resp = s.dav.with_prefix(prefix).handle(req).await;

    // Apply only on success, and only after `sc-dav` has actually finished
    // the write: a failed PUT (412 precondition, 409 conflict, 403, ...)
    // must not touch whatever timestamp the file already had.
    if resp.status().is_success() {
        if let (Some(ns), Some(sc_dav::DavPrincipal(user))) = (mtime_ns, principal) {
            if s.core.set_upload_mtime(user, &Vpath::new(&rel_path), ns) {
                // Mirrors the reference server's own confirmation header
                // (`File.php:363`, `X-OC-MTime: accepted`). The desktop
                // client's non-chunked (v1) propagator reads this and only
                // logs a warning when it's absent
                // (`propagateuploadv1.cpp:340-343`) — never fails the sync —
                // and neither Android's `UploadFileRemoteOperation` nor
                // iOS's upload path reads it back at all. So this
                // is strictly upside for the one client that looks, and a
                // no-op for the two that matter most here.
                //
                // `X-OC-CTime` is deliberately not echoed as accepted:
                // `sc_vfs::ShareRoot` has no creation-time setter, and can't
                // — Linux exposes `statx` birth time as read-only, with no
                // `utimensat`-equivalent to set it, so there is nothing to
                // apply it *to*. (The reference server doesn't really
                // honour it as filesystem btime either — `File.php:371`
                // only ever writes it into the `oc_filecache.creation_time`
                // database column — but we have no equivalent metadata slot
                // to fake that with, and won't claim one that doesn't
                // exist.)
                if let Ok(v) = http::HeaderValue::from_str("accepted") {
                    resp.headers_mut().insert("X-OC-MTime", v);
                }
            }
        }
    }
    resp
}

/// Chunked upload v2: `MKCOL` opens a session,
/// `PUT {n}` spools a numbered chunk, `MOVE …/.file` assembles.
async fn h_chunked(
    s: RemoteDav,
    target: sc_compat_nc::dav_paths::DavTarget,
    req: Request,
) -> Response {
    use sc_compat_nc::dav_paths::{DavTarget, FUTURE_FILE};
    use sc_compat_nc::ports::CorePort;

    let method = req.method().clone();
    let headers = req.headers().clone();
    let Some(sc_dav::DavPrincipal(user)) = req.extensions().get::<sc_dav::DavPrincipal>().copied()
    else {
        return StatusCode::UNAUTHORIZED.into_response();
    };
    // No `home_root`/share lookup here any more: `dest` (below) is a full
    // vpath naming whatever grant-projected label the client is uploading
    // into, and only `NcUpload::create` (via `Core::resolve_for_upload`) is
    // positioned to resolve that — see `chunking.rs`'s `mkcol`/`assemble`.
    let now_ns = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i64)
        .unwrap_or(0);

    match (method.as_str(), target) {
        (
            "MKCOL",
            DavTarget::UploadFolder {
                user: dav_user,
                tid,
            },
        ) => {
            // The `Destination` header is an absolute URL, and it has to be
            // reduced to a share-relative path before it can become a session
            // destination — `SafePath` would otherwise be handed something
            // like `https:/host/remote.php/dav/files/alice/a.jpg`.
            //
            // All three clients send it on MKCOL, so this is not optional:
            //   desktop  propagateuploadng.cpp:273 (+ destinationHeader():80-87)
            //   Android  ChunkedFileUploadRemoteOperation.java:159
            //   iOS      client SDK upload path (rides in customHeader)
            let dest = match sc_compat_nc::chunking::destination_path(&headers, &dav_user) {
                Ok(d) => d,
                Err(e) => return chunk_status(&e),
            };
            // Desktop and iOS also assert the final size here; Android does
            // not. When it is present the engine can reject a truncated
            // assembly, so pass it through rather than discarding it.
            let total = match sc_compat_nc::chunking::total_length(&headers) {
                Ok(t) => t,
                Err(e) => return chunk_status(&e),
            };
            match s.chunks.mkcol(user, &tid, dest, total, now_ns) {
                // Exactly 201: the desktop client aborts the whole transfer on
                // anything else (`propagateuploadng.cpp:302`,
                // `_httpErrorCode != 201`). Android ignores the status
                // entirely and iOS accepts any 2xx, so 201 satisfies all three.
                Ok(_) => StatusCode::CREATED.into_response(),
                Err(e) => chunk_status(&e),
            }
        }
        ("PUT", DavTarget::UploadChunk { tid, name, .. }) => {
            let body = match axum::body::to_bytes(req.into_body(), usize::MAX).await {
                Ok(b) => b,
                Err(_) => return StatusCode::BAD_REQUEST.into_response(),
            };
            match s.chunks.put_chunk(user, &tid, &name, &body) {
                Ok(()) => StatusCode::CREATED.into_response(),
                Err(e) => chunk_status(&e),
            }
        }
        ("MOVE", DavTarget::UploadChunk { tid, name, .. }) if name == FUTURE_FILE => {
            let core = s.core.clone();
            // `rest` is the share-relative path `chunking::assemble` derived
            // from the `dest` captured at MKCOL time (label stripped) — stat
            // the file that was actually assembled, not the share root.
            let finished = |dest: &Vpath| core.stat_entry(user, dest);
            match s.chunks.assemble(user, &tid, &headers, false, finished) {
                Ok(r) => {
                    let code = if r.created {
                        StatusCode::CREATED
                    } else {
                        StatusCode::NO_CONTENT
                    };
                    // Load-bearing (module doc, `chunking.rs` point 2): the
                    // desktop client hard-fails the item without these on the
                    // MOVE response itself — "Missing File ID from server" —
                    // even though the assembled file is already correctly on
                    // disk. `chunking::assemble` computes both; this was the
                    // one place they were being computed and then discarded.
                    let mut resp = code.into_response();
                    let hs = resp.headers_mut();
                    if let Ok(v) = http::HeaderValue::from_str(&r.oc_file_id) {
                        hs.insert("OC-FileId", v);
                    }
                    if let Ok(v) = http::HeaderValue::from_str(&r.etag) {
                        hs.insert(http::header::ETAG, v);
                    }
                    resp
                }
                Err(e) => chunk_status(&e),
            }
        }
        ("DELETE", DavTarget::UploadFolder { tid, .. }) => match s.chunks.abort(user, &tid) {
            Ok(()) => StatusCode::NO_CONTENT.into_response(),
            Err(e) => chunk_status(&e),
        },
        // PROPFIND on an upload folder is how a resuming client asks which
        // chunks we already have.
        // PROPFIND on an upload folder is not only a resume probe. Android
        // issues it on *every* chunked upload, immediately after MKCOL, and
        // derives its starting byte offset from the `d:getcontentlength`
        // values in the reply — so the body is built by
        // `chunking::chunk_listing_xml`, which is unit-tested against both
        // mobile parsers' quirks. iOS uses the same request purely as an
        // existence check and only treats 404 as "create the folder"
        // (the iOS client SDK's upload path), which is what a missing session
        // already returns.
        (
            "PROPFIND",
            DavTarget::UploadFolder {
                user: dav_user,
                tid,
            },
        ) => {
            match s.chunks.list_chunks_sized(user, &tid) {
                Ok(chunks) => {
                    // The href must echo the *client's own* user segment, not
                    // our internal numeric id: the Android parser splits each
                    // href on the uploads path it built and indexes `[1]`
                    // (`WebdavEntry.kt:118`), which throws — aborting the whole
                    // upload — if the prefix does not match.
                    let prefix = path_escape(&format!("/remote.php/dav/uploads/{dav_user}/{tid}"));
                    let body = sc_compat_nc::chunking::chunk_listing_xml(&prefix, &chunks);
                    (
                        StatusCode::MULTI_STATUS,
                        [(http::header::CONTENT_TYPE, "application/xml; charset=utf-8")],
                        body,
                    )
                        .into_response()
                }
                Err(e) => chunk_status(&e),
            }
        }
        _ => StatusCode::METHOD_NOT_ALLOWED.into_response(),
    }
}

// An empty-bodied rejection tells a client nothing beyond the status line —
// cost real debugging time distinguishing a genuine server defect from a
// request that was simply missing a required header (`Destination`,
// `OC-Total-Length`). The body is for a human/log, not for client parsing:
// no DAV client here branches on chunked-upload error text, only on status.
fn chunk_status(e: &sc_compat_nc::chunking::ChunkError) -> Response {
    let status = e.status();
    (
        status,
        [(http::header::CONTENT_TYPE, "text/plain; charset=utf-8")],
        e.to_string(),
    )
        .into_response()
}

/// Percent-encode a path for use inside a `<D:href>`.
pub(crate) fn path_escape(p: &str) -> String {
    const SET: &percent_encoding::AsciiSet = &percent_encoding::CONTROLS
        .add(b' ')
        .add(b'"')
        .add(b'<')
        .add(b'>')
        .add(b'%');
    percent_encoding::utf8_percent_encode(p, SET).to_string()
}

#[cfg(test)]
mod cookie_name_tests {
    /// The compat layer and the HTTP layer must agree on the session cookie's
    /// name, and nothing else can notice if they stop.
    ///
    /// They did stop: `sc-compat-nc` looked for `sc_session`, `sc-http` set
    /// `__Host-sc_sid`. Every test passed — the compat layer's own tests never
    /// send a real cookie, and `sc-http`'s never ask the compat layer to
    /// read one. What broke was Login Flow v2 on a real phone: the consent
    /// screen judged a logged-in browser anonymous, redirected to `/login`,
    /// which redirected back, and the app showed "wrong username and
    /// password" for credentials that were correct.
    ///
    /// A mismatch is now a compile-time-adjacent failure rather than a silent
    /// one at runtime on somebody's handset.
    #[test]
    fn compat_reads_the_cookie_the_http_layer_actually_sets() {
        let compat_default = sc_compat_nc::NcConfig::default().session_cookie;
        assert_eq!(
            compat_default,
            sc_http::SESSION_COOKIE,
            "the compat layer's default session cookie name has drifted from the one sc-http sets"
        );
    }
}
