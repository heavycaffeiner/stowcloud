//! Where the protocol crates get bound to the real core.
//!
//! `sc-dav` and `sc-http` each declare the storage API they need as an
//! object-safe trait, so neither of them depends on `sc-core` and both stay
//! testable with an in-memory backend. `sc-server` is the only crate that
//! depends on both sides, so this is where the two are joined.
//!
//! It is one newtype rather than `impl sc_dav::CoreApi for sc_core::Core`,
//! because Rust's orphan rule forbids implementing a foreign trait for a
//! foreign type — [`CoreBridge`] is the local type that makes both impls
//! legal. It holds nothing but an `Arc<Core>`.
//!
//! ## Two translations, not one
//!
//! The two traits are *not* the same API with different names, and collapsing
//! them would be wrong:
//!
//! * `sc-dav` needs the `403`-vs-`404` distinction spelled out — a path the
//!   caller may not list must be indistinguishable from a missing one, or the
//!   status itself confirms the path exists — which `sc-core` does not model:
//!   its `Denied` covers both. [`CoreBridge::dav_err`] recovers it by asking
//!   whether the caller can read the path at all.
//! * `sc-http` needs JSON-shaped DTOs with nanosecond times as strings
//!   (`DESIGN-API.md` §1), which `sc-core` deliberately doesn't know about.

use std::collections::{HashMap, HashSet};
use std::path::PathBuf;
use std::sync::Arc;

use sc_vfs::{GroupId, SafePath, ShareId, TrashMode, UserId};

/// Upper bound on a whole-file read buffered in memory.
///
/// Both `sc-dav`'s `GET` and `sc-http`'s `/api/fs/read` read a file into a
/// `Vec` — neither has a streaming path yet — so the bound is what stands
/// between one request for a 40 GB file and the OOM killer. Larger files are
/// refused rather than truncated: a silently short body is corruption.
const MAX_INLINE_READ: usize = 256 * 1024 * 1024;

#[derive(Clone)]
pub struct CoreBridge {
    pub core: Arc<sc_core::Core>,
    /// Deployment-wide `[smb] enabled` (`sc_smb::SmbConfig.enabled`) — the
    /// half of `DESIGN-AUTH.md` §2.4's "publishing" gate this crate can see.
    /// `sc-http`'s `features.smb` capability is this value verbatim
    /// (`hapi::CoreApi::smb_enabled`); the per-account half
    /// (`user.smb_enabled`) lives in `sc_auth::UserRow` and is unrelated.
    smb_enabled: bool,
    /// `None` when the watcher failed to start (`app.rs::start_watcher_and_ws_hub`)
    /// — every call site below treats that the same as "not hot right now",
    /// never as an error, since every read still re-stats.
    watcher: Option<Arc<sc_watch::Watcher>>,
    /// Admin override for `[index] name_enabled` (`FEATURES.md` #116/#117) —
    /// see `sc_search::settings` module doc for why this lives in its own DB
    /// rather than a `config.toml` rewrite.
    index_settings: Arc<sc_search::IndexSettingsStore>,
    /// Set for the duration of an admin-triggered `/api/admin/index/build`
    /// job; `run_idle_merge_pass` checks it so the idle scheduler never
    /// contends with a build the admin explicitly asked for right now.
    index_build_running: Arc<std::sync::atomic::AtomicBool>,
}

impl CoreBridge {
    pub fn new(
        core: Arc<sc_core::Core>,
        smb_enabled: bool,
        watcher: Option<Arc<sc_watch::Watcher>>,
        index_settings: Arc<sc_search::IndexSettingsStore>,
    ) -> Self {
        Self {
            core,
            smb_enabled,
            watcher,
            index_settings,
            index_build_running: Arc::new(std::sync::atomic::AtomicBool::new(false)),
        }
    }

    /// LRU-bump the watcher's hot set for the directory `vpath` resolves to
    /// — "a directory someone is currently looking at" is the seam
    /// `fs_list`/`fs_stat` sit on. A no-op if there is no watcher, or `vpath`
    /// doesn't resolve (the caller's own read already succeeded or failed on
    /// its own terms; this is best-effort freshness, never a correctness
    /// gate — `sc-watch`'s module doc).
    fn touch_watch(&self, user: UserId, vpath: &str) {
        let Some(w) = &self.watcher else { return };
        if let Some((share, path)) = self.index_path(user, vpath) {
            w.touch(share, &path);
        }
    }

    /// `sc-core` answers "denied" for both "you may not do *this*" and "you
    /// may not see this at all"; DAV must answer 403 only for the first and
    /// 404 for the second, since a 403 on the second would confirm the path
    /// exists. So re-ask the cheap (ACL-cached) question of whether a plain
    /// read resolves.
    fn dav_err(&self, user: UserId, vpath: &str, e: sc_core::CoreError) -> sc_dav::CoreError {
        use sc_core::CoreError as C;
        use sc_dav::CoreError as D;
        match e {
            C::NotFound => D::NotFound,
            C::InvalidPath(m) => D::Invalid(m),
            // A DAV client has no locale to render a key in, so it gets the
            // English `message` — the key is for the browser only.
            C::Invalid { message, .. } => D::Invalid(message),
            C::Conflict => D::Exists,
            // The DAV mapping turns `Exists` into 412, which is exactly what
            // a failed `If-Match` means.
            C::Precondition { .. } => D::Exists,
            C::CrossDevice => D::Io("cross-device".into()),
            // WebDAV has no share-link concept of its own; both collapse to
            // the closest DAV-native meaning rather than inventing a new
            // `sc_dav::CoreError` variant nothing else produces.
            C::Gone => D::NotFound,
            C::NotSupported(m) => D::Io(m),
            // A per-user quota is a storage limit, not an ACL decision, so it
            // maps to the same 507 as a full filesystem rather than a 403.
            C::QuotaExceeded => D::NoSpace,
            C::Io(m) => D::Io(m),
            C::Internal(m) => D::Io(m),
            C::Denied { .. } => {
                if self.core.resolve(user, vpath).is_ok() {
                    D::Denied
                } else {
                    D::NotListable
                }
            }
        }
    }

    /// Resolve `vpath` to `(share, path)` purely for name-index bookkeeping —
    /// never for the ACL decision the file operation itself already made.
    /// `Core::resolve` does no filesystem I/O (`sc-core/src/resolve.rs`: pure
    /// label lookup + ACL evaluation over the virtual path), so calling it a
    /// second time right after a write already succeeded is cheap, and safe
    /// to call even for a path that no longer resolves to anything on disk
    /// (a delete's source, or a rename's old name) — it never `stat`s the
    /// target. `None` (ACL denied, unknown label) means "nothing to record",
    /// which is the fail-safe direction: the index just falls a little
    /// further behind, never wrongly.
    fn index_path(&self, user: UserId, vpath: &str) -> Option<(sc_vfs::ShareId, sc_vfs::SafePath)> {
        self.core
            .resolve(user, vpath)
            .ok()
            .map(|r| (r.share, r.path))
    }

    /// Batch operations report per-item outcomes; the single-target DAV and
    /// REST methods need the first (only) one hoisted back into a `Result`.
    fn first_result(results: Vec<sc_core::OpResult>, path: &str) -> Result<(), sc_core::CoreError> {
        match results.into_iter().next() {
            Some(r) if r.ok => Ok(()),
            Some(r) => Err(r
                .error
                .unwrap_or_else(|| sc_core::CoreError::Internal(format!("{path}: failed")))),
            None => Err(sc_core::CoreError::NotFound),
        }
    }
}

// ---------------------------------------------------------------- sc-dav --

fn dav_entry(e: sc_core::Entry) -> sc_dav::Entry {
    sc_dav::Entry {
        name: e.name,
        kind: e.kind,
        size: e.size,
        mtime_ns: e.mtime_ns,
        etag: e.etag,
        perms: e.perms,
        id: e.id,
        is_symlink_denied: e.is_symlink_denied,
        confusable: e.confusable,
        btime_ns: e.btime_ns,
    }
}

fn core_sort(s: sc_dav::Sort) -> sc_core::Sort {
    match s {
        sc_dav::Sort::Name => sc_core::Sort::Name,
        sc_dav::Sort::Size => sc_core::Sort::Size,
        sc_dav::Sort::Mtime => sc_core::Sort::Mtime,
    }
}

fn core_order(o: sc_dav::Order) -> sc_core::Order {
    match o {
        sc_dav::Order::Asc => sc_core::Order::Asc,
        sc_dav::Order::Desc => sc_core::Order::Desc,
    }
}

/// `sc-core` keys a listing session by an opaque string; `sc-dav` types the
/// same field as `u64`. Fold the string rather than inventing a parallel id:
/// the value is only ever compared for equality.
fn fold_listing_id(s: &str) -> u64 {
    let mut h: u64 = 0xcbf2_9ce4_8422_2325;
    for b in s.as_bytes() {
        h ^= *b as u64;
        h = h.wrapping_mul(0x100_0000_01b3);
    }
    h
}

impl sc_dav::CoreApi for CoreBridge {
    fn resolve(&self, user: UserId, vpath: &str) -> sc_dav::backend::CoreResult<sc_dav::Resolved> {
        let r = self
            .core
            .resolve(user, vpath)
            .map_err(|e| self.dav_err(user, vpath, e))?;
        Ok(sc_dav::Resolved {
            share: r.share,
            path: r.path,
            perms: r.perms,
        })
    }

    fn list(
        &self,
        user: UserId,
        vpath: &str,
        sort: sc_dav::Sort,
        order: sc_dav::Order,
    ) -> sc_dav::backend::CoreResult<sc_dav::Listing> {
        let l = self
            .core
            .list(user, vpath, core_sort(sort), core_order(order))
            .map_err(|e| self.dav_err(user, vpath, e))?;
        Ok(sc_dav::Listing {
            entries: l.entries.into_iter().map(dav_entry).collect(),
            total: l.total as u64,
            dir_etag: l.dir_etag,
            listing_id: fold_listing_id(&l.listing_id),
            cursor: l.cursor,
        })
    }

    fn stat_entry(&self, user: UserId, vpath: &str) -> sc_dav::backend::CoreResult<sc_dav::Entry> {
        self.core
            .stat_entry(user, vpath)
            .map(dav_entry)
            .map_err(|e| self.dav_err(user, vpath, e))
    }

    fn mkdir(&self, user: UserId, vpath: &str) -> sc_dav::backend::CoreResult<()> {
        self.core
            .mkdir(user, vpath)
            .map(|_| ())
            .map_err(|e| self.dav_err(user, vpath, e))?;
        if let Some(p) = self.index_path(user, vpath) {
            note_index_change(&self.core, &[p], &[]);
        }
        Ok(())
    }

    fn rename(&self, user: UserId, from: &str, to: &str) -> sc_dav::backend::CoreResult<()> {
        // DAV MOVE names its destination, so this is `move_to`, not
        // `move_entries` (which keeps the source name) and not `rename`
        // (which cannot leave the source directory).
        self.core
            .move_to(user, from, to, true)
            .map(|_| ())
            .map_err(|e| self.dav_err(user, from, e))?;
        // "our own writes": record the move against
        // whichever share(s) have an index, the moment we know it succeeded.
        let removed = self.index_path(user, from).into_iter().collect::<Vec<_>>();
        let added = self.index_path(user, to).into_iter().collect::<Vec<_>>();
        note_index_change(&self.core, &added, &removed);
        Ok(())
    }

    fn move_entries(
        &self,
        user: UserId,
        from: &[String],
        to_dir: &str,
    ) -> sc_dav::backend::CoreResult<()> {
        let removed: Vec<_> = from
            .iter()
            .filter_map(|p| self.index_path(user, p))
            .collect();
        let results = self
            .core
            .move_entries(
                user,
                from,
                to_dir,
                sc_core::OnConflict::Overwrite,
                &HashMap::new(),
            )
            .map_err(|e| self.dav_err(user, to_dir, e))?;
        for r in results {
            if !r.ok {
                let path = r.path.clone();
                return Err(self.dav_err(
                    user,
                    &path,
                    r.error.unwrap_or(sc_core::CoreError::Conflict),
                ));
            }
        }
        // Reached only when every item above reported `ok`, so every source
        // in `from` really landed at `to_dir/<its old basename>` — this mode
        // is hardcoded `Overwrite`, never `Rename`, so the destination name
        // never differs from the source's own.
        let added: Vec<_> = from
            .iter()
            .filter_map(|p| dest_under(to_dir, p))
            .filter_map(|to_vpath| self.index_path(user, &to_vpath))
            .collect();
        note_index_change(&self.core, &added, &removed);
        Ok(())
    }

    fn copy_entries(
        &self,
        user: UserId,
        from: &[String],
        to_dir: &str,
    ) -> sc_dav::backend::CoreResult<()> {
        let results = self
            .core
            .copy_entries(user, from, to_dir, sc_core::OnConflict::Overwrite)
            .map_err(|e| self.dav_err(user, to_dir, e))?;
        for r in results {
            if !r.ok {
                let path = r.path.clone();
                return Err(self.dav_err(
                    user,
                    &path,
                    r.error.unwrap_or(sc_core::CoreError::Conflict),
                ));
            }
        }
        // Same "reached ⇒ every item ok" reasoning as `move_entries`; a copy
        // never removes the source, so only `added` applies.
        let added: Vec<_> = from
            .iter()
            .filter_map(|p| dest_under(to_dir, p))
            .filter_map(|to_vpath| self.index_path(user, &to_vpath))
            .collect();
        note_index_change(&self.core, &added, &[]);
        Ok(())
    }

    fn copy_to(&self, user: UserId, from: &str, to: &str) -> sc_dav::backend::CoreResult<()> {
        // `Core::copy_to` is a primitive, not `copy_entries` + `rename`:
        // faking it that way deletes the source on a same-directory copy
        // (Finder's "Duplicate"). See `DESIGN-API.md` §5.1.
        self.core
            .copy_to(user, from, to, true)
            .map(|_| ())
            .map_err(|e| self.dav_err(user, from, e))?;
        if let Some(p) = self.index_path(user, to) {
            note_index_change(&self.core, &[p], &[]);
        }
        Ok(())
    }

    fn delete(&self, user: UserId, vpath: &str) -> sc_dav::backend::CoreResult<()> {
        let removed = self.index_path(user, vpath);
        let results = self
            .core
            .delete(user, &[vpath.to_string()], false)
            .map_err(|e| self.dav_err(user, vpath, e))?;
        Self::first_result(results, vpath).map_err(|e| self.dav_err(user, vpath, e))?;
        note_index_change(&self.core, &[], &removed.into_iter().collect::<Vec<_>>());
        Ok(())
    }

    fn read_text(&self, user: UserId, vpath: &str) -> sc_dav::backend::CoreResult<String> {
        self.core
            .read_text(user, vpath, MAX_INLINE_READ)
            .map(|(t, _)| t)
            .map_err(|e| self.dav_err(user, vpath, e))
    }

    fn write_text(&self, user: UserId, vpath: &str, data: &str) -> sc_dav::backend::CoreResult<()> {
        self.write_bytes(user, vpath, data.as_bytes())
    }

    fn read_bytes(&self, user: UserId, vpath: &str) -> sc_dav::backend::CoreResult<Vec<u8>> {
        self.core
            .read_bytes(user, vpath, MAX_INLINE_READ)
            .map(|(b, _)| b)
            .map_err(|e| self.dav_err(user, vpath, e))
    }

    fn write_bytes(
        &self,
        user: UserId,
        vpath: &str,
        data: &[u8],
    ) -> sc_dav::backend::CoreResult<()> {
        // DAV `PUT` without `If-Match` is an unconditional overwrite, which is
        // what the protocol says it is. `Core::write_text` demands the current
        // etag when the target exists, so supply it.
        let if_match = self.core.stat_entry(user, vpath).ok().map(|e| e.etag);
        self.core
            .write_text(user, vpath, data, if_match.as_deref())
            .map(|_| ())
            .map_err(|e| self.dav_err(user, vpath, e))?;
        // A create *or* an overwrite both append: an overwrite's append is a
        // harmless duplicate live entry (query-time dedup absorbs it, §4.2),
        // and telling the two apart would need an extra `stat` this path
        // doesn't otherwise need.
        if let Some(p) = self.index_path(user, vpath) {
            note_index_change(&self.core, &[p], &[]);
        }
        Ok(())
    }

    fn aggregate(&self, share: ShareId, path: &SafePath) -> anyhow::Result<sc_dav::Aggregate> {
        let a = self.core.aggregate(share, path)?;
        Ok(sc_dav::Aggregate {
            etag: a.etag,
            rsize: a.rsize,
            rcount: a.rcount,
        })
    }

    fn quota(&self, user: UserId, vpath: &str) -> sc_dav::backend::CoreResult<sc_dav::Quota> {
        let q = self
            .core
            .quota(user, vpath)
            .map_err(|e| self.dav_err(user, vpath, e))?;
        Ok(sc_dav::Quota {
            used: q.used,
            available: q.available,
        })
    }

    fn create_empty(&self, user: UserId, vpath: &str) -> sc_dav::backend::CoreResult<()> {
        self.core
            .write_text(user, vpath, b"", None)
            .map(|_| ())
            .map_err(|e| self.dav_err(user, vpath, e))?;
        if let Some(p) = self.index_path(user, vpath) {
            note_index_change(&self.core, &[p], &[]);
        }
        Ok(())
    }
}

/// `to_dir/<basename of from>` — the destination a `move_entries`/
/// `copy_entries` item lands at under `OnConflict::Overwrite` (the only mode
/// either DAV bridge method above ever passes): the name never changes, only
/// the parent directory does, unlike `OnConflict::Rename` which can append a
/// disambiguating suffix this helper has no way to predict. `None` for a
/// `from` with no basename component (should not happen for a valid vpath,
/// but skipping is the safe direction — worst case the index misses one
/// entry until the next rebuild, never records a wrong one).
fn dest_under(to_dir: &str, from: &str) -> Option<String> {
    let name = from.rsplit('/').next().filter(|s| !s.is_empty())?;
    Some(format!("{}/{name}", to_dir.trim_end_matches('/')))
}

/// `vpath`'s own parent plus `new_name` — what `hapi::CoreApi::rename`
/// produces (it changes only the last path component, unlike DAV `MOVE`
/// which can also change the parent directory; see `dest_under` for that
/// case).
fn dest_under_vpath(vpath: &str, new_name: &str) -> Option<String> {
    let (parent, _old_name) = vpath.rsplit_once('/')?;
    Some(format!("{parent}/{new_name}"))
}

pub struct MetaBridge {
    pub meta: Arc<sc_meta::MetaStore>,
}

impl sc_dav::MetaApi for MetaBridge {
    fn get_props(&self, id: sc_vfs::FileId) -> anyhow::Result<Vec<sc_dav::DavProp>> {
        Ok(self
            .meta
            .get_props(id)?
            .into_iter()
            .map(|p| sc_dav::DavProp {
                ns: p.ns,
                name: p.name,
                value: p.value,
            })
            .collect())
    }

    fn set_prop(
        &self,
        id: sc_vfs::FileId,
        ns: &str,
        name: &str,
        value: &str,
    ) -> anyhow::Result<()> {
        self.meta.set_prop(id, ns, name, value)
    }

    fn del_prop(&self, id: sc_vfs::FileId, ns: &str, name: &str) -> anyhow::Result<()> {
        self.meta.del_prop(id, ns, name)
    }
}

// --------------------------------------------------------------- sc-http --

use sc_http::core_api as hapi;

fn http_entry(e: sc_core::Entry) -> hapi::Entry {
    hapi::Entry {
        name: e.name,
        kind: e.kind.into(),
        size: e.size,
        mtime_ns: e.mtime_ns.to_string(),
        etag: e.etag,
        perms: e.perms,
        id: e.id,
        preview: None,
        link: e
            .is_symlink_denied
            .then_some(hapi::SymlinkInfo { blocked: true }),
        confusable: e.confusable,
    }
}

fn http_principal(p: hapi::GrantPrincipal) -> sc_acl::Principal {
    match p {
        hapi::GrantPrincipal::User(id) => sc_acl::Principal::User(UserId::new(id)),
        hapi::GrantPrincipal::Group(id) => sc_acl::Principal::Group(GroupId::new(id)),
    }
}

fn core_principal(p: sc_acl::Principal) -> hapi::GrantPrincipal {
    match p {
        sc_acl::Principal::User(id) => hapi::GrantPrincipal::User(id.get()),
        sc_acl::Principal::Group(id) => hapi::GrantPrincipal::Group(id.get()),
    }
}

fn http_grant(rec: sc_core::GrantRecord) -> hapi::GrantInfo {
    let g = rec.grant;
    hapi::GrantInfo {
        id: g.id,
        principal: core_principal(g.principal),
        share: g.share.get(),
        subpath: g.subpath.to_display_string(),
        allow: g.allow,
        deny: g.deny,
        inherit: g.inherit,
        label: g.label,
        created_ns: rec.created_ns.to_string(),
    }
}

fn http_err(e: sc_core::CoreError) -> hapi::CoreError {
    use sc_core::CoreError as C;
    match e {
        C::NotFound => hapi::CoreError::NotFound,
        C::Denied { by } => hapi::CoreError::Denied { by },
        C::Conflict => hapi::CoreError::Conflict {
            path: String::new(),
            etag: None,
        },
        C::Precondition { current_etag } => hapi::CoreError::Precondition { current_etag },
        C::InvalidPath(m) => hapi::CoreError::InvalidName(m),
        C::CrossDevice => hapi::CoreError::CrossDevice { total_bytes: 0 },
        C::Gone => hapi::CoreError::Gone,
        C::NotSupported(_) => hapi::CoreError::NotSupported,
        C::Io(m) | C::Internal(m) => hapi::CoreError::Internal(m),
        C::QuotaExceeded => hapi::CoreError::QuotaExceeded,
        C::Invalid { key, params, message } => hapi::CoreError::Invalid {
            key: key.to_string(),
            params: params_json(&params),
            message,
        },
    }
}

/// `sc-core` has no serde, so it carries placeholders as pairs and the
/// translation to JSON happens here.
fn params_json(params: &[(&'static str, String)]) -> serde_json::Value {
    serde_json::Value::Object(params.iter().map(|(k, v)| ((*k).to_string(), serde_json::Value::String(v.clone()))).collect())
}

fn http_op_result(r: sc_core::OpResult) -> hapi::OpResult {
    hapi::OpResult {
        path: r.path,
        ok: r.ok,
        // The same `{code, message, detail}` object the top-level envelope
        // carries (`AppError::to_wire`), so a per-item failure and a whole-
        // request failure speak one vocabulary. It used to be `{message}`
        // alone, which is why `+page.svelte`'s `r.error?.code === 'fs.conflict'`
        // only ever matched against the mock. `code` is also what the job tray
        // translates — it cannot translate a sentence.
        error: r.error.map(|e| sc_http::error::AppError::from(http_err(e)).to_wire()),
        will_copy: r.will_copy,
    }
}

fn core_on_conflict(c: hapi::OnConflict) -> sc_core::OnConflict {
    match c {
        hapi::OnConflict::Fail => sc_core::OnConflict::Fail,
        hapi::OnConflict::Rename => sc_core::OnConflict::Rename,
        hapi::OnConflict::Overwrite => sc_core::OnConflict::Overwrite,
        hapi::OnConflict::Skip => sc_core::OnConflict::Skip,
    }
}

fn core_sort_key(s: hapi::SortKey) -> sc_core::Sort {
    match s {
        hapi::SortKey::Name => sc_core::Sort::Name,
        hapi::SortKey::Size => sc_core::Sort::Size,
        hapi::SortKey::Mtime => sc_core::Sort::Mtime,
        hapi::SortKey::Kind => sc_core::Sort::Kind,
    }
}

fn core_http_order(o: hapi::Order) -> sc_core::Order {
    match o {
        hapi::Order::Asc => sc_core::Order::Asc,
        hapi::Order::Desc => sc_core::Order::Desc,
    }
}

/// Trash ids are `"{share}:{handle}"`.
///
/// `sc-core` scopes a trash handle to one share (`<share>/.sctrash/…`), so a
/// bare handle is ambiguous the moment a user can reach two shares. The
/// composite keeps the id opaque to the client while staying resolvable.
fn split_trash_id(s: &str) -> Option<(ShareId, &str)> {
    let (share, handle) = s.split_once(':')?;
    Some((ShareId::new(share.parse().ok()?), handle))
}

impl hapi::CoreApi for CoreBridge {
    fn resolve(&self, user: UserId, vpath: &str) -> Result<hapi::Resolved, hapi::CoreError> {
        let r = self.core.resolve(user, vpath).map_err(http_err)?;
        Ok(hapi::Resolved {
            share: r.share,
            subpath: r.path,
            perms: r.perms,
        })
    }

    fn roots(&self, user: UserId) -> Vec<sc_acl::RootEntry> {
        self.core.roots(user)
    }

    fn list(
        &self,
        user: UserId,
        vpath: &str,
        sort: hapi::SortKey,
        order: hapi::Order,
    ) -> Result<hapi::Listing, hapi::CoreError> {
        let l = self
            .core
            .list(user, vpath, core_sort_key(sort), core_http_order(order))
            .map_err(http_err)?;
        // A directory someone just listed is "currently being looked at" —
        // the seam that decides which of a large tree's directories get a
        // live OS watch at all (`sc-watch`'s hot-set bound).
        self.touch_watch(user, vpath);
        Ok(hapi::Listing {
            listing: l.listing_id,
            total: l.total as u64,
            cursor: l.cursor,
            entries: l.entries.into_iter().map(http_entry).collect(),
            dir_etag: l.dir_etag,
        })
    }

    fn stat_entry(&self, user: UserId, vpath: &str) -> Result<hapi::Entry, hapi::CoreError> {
        let entry = self
            .core
            .stat_entry(user, vpath)
            .map(http_entry)
            .map_err(http_err)?;
        // The watcher observes a directory's *children* changing, so a
        // `stat` of a directory itself hot-touches that directory, while a
        // `stat` of a file hot-touches its parent — there is no useful
        // single-file watch here, only "is anything in the containing
        // directory changing".
        if let Some(w) = &self.watcher {
            if let Some((share, path)) = self.index_path(user, vpath) {
                let key = if entry.kind == hapi::Kind::Dir {
                    path
                } else {
                    path.parent()
                };
                w.touch(share, &key);
            }
        }
        Ok(entry)
    }

    fn mkdir(&self, user: UserId, vpath: &str) -> Result<hapi::Entry, hapi::CoreError> {
        let entry = self
            .core
            .mkdir(user, vpath)
            .map(http_entry)
            .map_err(http_err)?;
        if let Some(p) = self.index_path(user, vpath) {
            note_index_change(&self.core, &[p], &[]);
        }
        Ok(entry)
    }

    fn rename(
        &self,
        user: UserId,
        vpath: &str,
        new_name: &str,
    ) -> Result<hapi::Entry, hapi::CoreError> {
        let removed = self.index_path(user, vpath);
        let entry = self
            .core
            .rename(user, vpath, new_name, None)
            .map(http_entry)
            .map_err(http_err)?;
        // `rename` (unlike DAV `MOVE`) only ever changes the last component,
        // never the parent directory, so the destination vpath is `vpath`'s
        // own parent plus the new name.
        let added = dest_under_vpath(vpath, new_name).and_then(|to| self.index_path(user, &to));
        note_index_change(
            &self.core,
            &added.into_iter().collect::<Vec<_>>(),
            &removed.into_iter().collect::<Vec<_>>(),
        );
        Ok(entry)
    }

    fn move_entries(
        &self,
        user: UserId,
        paths: &[String],
        dest: &str,
        on_conflict: hapi::OnConflict,
        if_match: &HashMap<String, hapi::Etag>,
    ) -> Result<Vec<hapi::OpResult>, hapi::CoreError> {
        self.core
            .move_entries(user, paths, dest, core_on_conflict(on_conflict), if_match)
            .map(|v| v.into_iter().map(http_op_result).collect())
            .map_err(http_err)
    }

    fn move_entries_dry_run(
        &self,
        user: UserId,
        paths: &[String],
        dest: &str,
        on_conflict: hapi::OnConflict,
        if_match: &HashMap<String, hapi::Etag>,
    ) -> Result<Vec<hapi::OpResult>, hapi::CoreError> {
        self.core
            .move_entries_dry_run(user, paths, dest, core_on_conflict(on_conflict), if_match)
            .map(|v| v.into_iter().map(http_op_result).collect())
            .map_err(http_err)
    }

    fn copy_entries(
        &self,
        user: UserId,
        paths: &[String],
        dest: &str,
        on_conflict: hapi::OnConflict,
        _if_match: &HashMap<String, hapi::Etag>,
    ) -> Result<Vec<hapi::OpResult>, hapi::CoreError> {
        self.core
            .copy_entries(user, paths, dest, core_on_conflict(on_conflict))
            .map(|v| v.into_iter().map(http_op_result).collect())
            .map_err(http_err)
    }

    fn delete(
        &self,
        user: UserId,
        paths: &[String],
        permanent: bool,
    ) -> Result<Vec<hapi::OpResult>, hapi::CoreError> {
        // Resolved *before* the delete for the same reason `CoreBridge::delete`
        // (DAV) does it up front — `Core::resolve` is pure ACL arithmetic, so
        // there is no ordering hazard either way, but this keeps the intent
        // obvious rather than relying on that fact continuing to hold.
        let resolved: Vec<_> = paths.iter().map(|p| self.index_path(user, p)).collect();
        let results: Vec<hapi::OpResult> = self
            .core
            .delete(user, paths, permanent)
            .map(|v| v.into_iter().map(http_op_result).collect())
            .map_err(http_err)?;
        let removed: Vec<_> = results
            .iter()
            .zip(resolved)
            .filter(|(r, _)| r.ok)
            .filter_map(|(_, p)| p)
            .collect();
        note_index_change(&self.core, &[], &removed);
        Ok(results)
    }

    fn read_text(&self, user: UserId, vpath: &str) -> Result<String, hapi::CoreError> {
        self.core
            .read_text(user, vpath, MAX_INLINE_READ)
            .map(|(t, _)| t)
            .map_err(http_err)
    }

    fn write_text(
        &self,
        user: UserId,
        vpath: &str,
        content: &str,
        if_match: Option<&hapi::Etag>,
    ) -> Result<hapi::Entry, hapi::CoreError> {
        let entry = self
            .core
            .write_text(
                user,
                vpath,
                content.as_bytes(),
                if_match.map(|s| s.as_str()),
            )
            .map(http_entry)
            .map_err(http_err)?;
        // Create-or-overwrite, same reasoning as the DAV `write_bytes` hook.
        if let Some(p) = self.index_path(user, vpath) {
            note_index_change(&self.core, &[p], &[]);
        }
        Ok(entry)
    }

    fn trash_list(&self, user: UserId) -> Result<Vec<hapi::TrashEntry>, hapi::CoreError> {
        // Every share the caller can see contributes; `Core::trash_list` is
        // per-share because the trash lives inside the share root.
        let mut out = Vec::new();
        let mut seen: Vec<u32> = Vec::new();
        for root in self.core.roots(user) {
            if seen.contains(&root.share.get()) {
                continue;
            }
            seen.push(root.share.get());
            let Ok(entries) = self.core.trash_list(user, root.share) else {
                continue;
            };
            for e in entries {
                out.push(hapi::TrashEntry {
                    id: format!("{}:{}", root.share.get(), e.id),
                    name: e.name,
                    size: e.size,
                    deleted_mtime_ns: e.deleted_mtime_ns.to_string(),
                });
            }
        }
        Ok(out)
    }

    fn trash_restore(
        &self,
        user: UserId,
        ids: &[String],
    ) -> Result<Vec<hapi::OpResult>, hapi::CoreError> {
        Ok(ids
            .iter()
            .map(|id| match split_trash_id(id) {
                Some((share, handle)) => match self.core.trash_restore(user, share, handle) {
                    Ok(()) => hapi::OpResult {
                        path: id.clone(),
                        ok: true,
                        error: None,
                        will_copy: false,
                    },
                    Err(e) => hapi::OpResult {
                        path: id.clone(),
                        ok: false,
                        error: Some(serde_json::json!({ "message": e.to_string() })),
                        will_copy: false,
                    },
                },
                None => hapi::OpResult {
                    path: id.clone(),
                    ok: false,
                    error: Some(serde_json::json!({ "message": "malformed trash id" })),
                    will_copy: false,
                },
            })
            .collect())
    }

    fn trash_purge(
        &self,
        user: UserId,
        ids: &[String],
    ) -> Result<Vec<hapi::OpResult>, hapi::CoreError> {
        Ok(ids
            .iter()
            .map(|id| match split_trash_id(id) {
                Some((share, handle)) => match self.core.trash_purge(user, share, Some(handle)) {
                    Ok(()) => hapi::OpResult {
                        path: id.clone(),
                        ok: true,
                        error: None,
                        will_copy: false,
                    },
                    Err(e) => hapi::OpResult {
                        path: id.clone(),
                        ok: false,
                        error: Some(serde_json::json!({ "message": e.to_string() })),
                        will_copy: false,
                    },
                },
                None => hapi::OpResult {
                    path: id.clone(),
                    ok: false,
                    error: Some(serde_json::json!({ "message": "malformed trash id" })),
                    will_copy: false,
                },
            })
            .collect())
    }

    fn aggregate(&self, share: ShareId, subpath: &SafePath) -> anyhow::Result<hapi::Aggregate> {
        let a = self.core.aggregate(share, subpath)?;
        Ok(hapi::Aggregate {
            // `sc-meta` rolls files and directories into one recursive count;
            // it does not keep them apart, so reporting a fabricated split
            // would be worse than reporting zero directories.
            file_count: a.rcount,
            dir_count: 0,
            total_bytes: a.rsize,
        })
    }

    fn storage_report(&self) -> Result<hapi::StorageReport, hapi::CoreError> {
        let mut shares = Vec::new();
        for def in self.core.share_defs() {
            let (free, total) = self
                .core
                .share(def.id)
                .and_then(|r| r.statfs_free().ok())
                .unwrap_or((0, 0));
            shares.push(hapi::ShareStorage {
                label: def.name,
                free_bytes: free,
                total_bytes: total,
            });
        }
        Ok(hapi::StorageReport {
            db_bytes: self.db_bytes(),
            shares,
        })
    }

    fn index_estimate(&self) -> Result<hapi::IndexEstimate, hapi::CoreError> {
        // Every coefficient is *measured* from a
        // sample of the real corpus rather than assumed, because a CJK
        // corpus and a Latin one of the same file count cost very different
        // amounts — the distinct-trigram term is what makes the difference,
        // and it can only be counted.
        const BLOCK_SIZE: u32 = 32;
        const MAX_SAMPLES: usize = 256;
        const MAX_ENTRIES: u64 = 2_000_000;

        let mut scanner = sc_search::CorpusScanner::new(BLOCK_SIZE, MAX_SAMPLES);
        let mut seen = 0u64;
        let mut truncated = false;
        for def in self.core.share_defs() {
            let Some(root) = self.core.share(def.id) else {
                continue;
            };
            if walk_for_estimate(
                &root,
                &SafePath::root(),
                &mut scanner,
                &mut seen,
                MAX_ENTRIES,
            )
            .is_err()
            {
                truncated = true;
            }
            if seen >= MAX_ENTRIES {
                truncated = true;
                break;
            }
        }
        // A truncated scan must say so: the estimate is then a lower bound,
        // and presenting it as a measurement would understate the cost of
        // the very thing the admin is deciding about.
        scanner.set_truncated(truncated);

        let files = scanner.files();
        let stats = scanner.finish();
        let est = sc_search::estimate_name_index(&stats, BLOCK_SIZE);
        // The derivation goes to the log, not to the admin screen. An operator
        // who distrusts the number needs to see which term produced it; every
        // other reader needed a size and a duration, and got a wall of
        // trigram arithmetic instead.
        tracing::info!(
            files,
            index_bytes = est.index_bytes,
            build_secs = est.build_secs,
            confidence = ?est.confidence,
            "search index estimate:\n{}",
            est.formula
        );
        Ok(hapi::IndexEstimate {
            files,
            index_bytes: est.index_bytes,
            build_secs: est.build_secs,
            confidence: format!("{:?}", est.confidence).to_lowercase(),
        })
    }

    // ------------------------------------------------------------ grants --

    fn admin_shares(&self) -> Vec<hapi::AdminShareInfo> {
        self.core
            .share_defs()
            .into_iter()
            .map(|d| hapi::AdminShareInfo {
                id: d.id.get(),
                name: d.name,
                host_path: d.host_path.to_string_lossy().into_owned(),
                config_defined: d.id.get() < sc_core::DYNAMIC_SHARE_ID_BASE,
                trash_enabled: d.policy.trash != TrashMode::Off,
            })
            .collect()
    }

    fn create_share(
        &self,
        req: hapi::ShareCreateReq,
    ) -> Result<hapi::AdminShareInfo, hapi::CoreError> {
        let def = self
            .core
            .create_share(req.name, PathBuf::from(req.host_path))
            .map_err(http_err)?;
        Ok(hapi::AdminShareInfo {
            id: def.id.get(),
            name: def.name,
            host_path: def.host_path.to_string_lossy().into_owned(),
            config_defined: false,
            trash_enabled: def.policy.trash != TrashMode::Off,
        })
    }

    fn update_share(
        &self,
        id: u32,
        patch: hapi::SharePatchReq,
    ) -> Result<hapi::AdminShareInfo, hapi::CoreError> {
        let def = self
            .core
            .update_share(
                ShareId::new(id),
                patch.name,
                patch.host_path.map(PathBuf::from),
                patch.trash_enabled,
            )
            .map_err(http_err)?;
        Ok(hapi::AdminShareInfo {
            id: def.id.get(),
            name: def.name,
            host_path: def.host_path.to_string_lossy().into_owned(),
            config_defined: def.id.get() < sc_core::DYNAMIC_SHARE_ID_BASE,
            trash_enabled: def.policy.trash != TrashMode::Off,
        })
    }

    fn delete_share(&self, id: u32) -> Result<(), hapi::CoreError> {
        self.core.delete_share(ShareId::new(id)).map_err(http_err)
    }

    fn list_grants(
        &self,
        filter: hapi::GrantFilter,
    ) -> Result<Vec<hapi::GrantInfo>, hapi::CoreError> {
        let f = sc_core::GrantFilter {
            principal: filter.principal.map(http_principal),
            share: filter.share.map(ShareId::new),
        };
        let recs = self.core.list_grants(&f).map_err(http_err)?;
        Ok(recs.into_iter().map(http_grant).collect())
    }

    fn create_grant(&self, spec: hapi::GrantSpec) -> Result<hapi::GrantInfo, hapi::CoreError> {
        let s = sc_core::GrantSpec {
            principal: http_principal(spec.principal),
            share: ShareId::new(spec.share),
            subpath: spec.subpath,
            allow: spec.allow,
            deny: spec.deny,
            inherit: spec.inherit,
            label: spec.label,
        };
        self.core.create_grant(&s).map(http_grant).map_err(http_err)
    }

    fn update_grant(
        &self,
        id: u32,
        patch: hapi::GrantPatch,
    ) -> Result<hapi::GrantInfo, hapi::CoreError> {
        let p = sc_core::GrantPatch {
            allow: patch.allow,
            deny: patch.deny,
            inherit: patch.inherit,
            label: patch.label,
        };
        self.core
            .update_grant(id, &p)
            .map(http_grant)
            .map_err(http_err)
    }

    fn delete_grant(&self, id: u32) -> Result<(), hapi::CoreError> {
        self.core.delete_grant(id).map_err(http_err)
    }

    fn refresh_group_memberships(&self, m: HashMap<UserId, Vec<sc_vfs::GroupId>>) {
        self.core.set_group_memberships(m);
    }

    // --------------------------------------------------------------- smb --

    fn smb_enabled(&self) -> bool {
        self.smb_enabled
    }

    // ------------------------------------------------------------ shares --

    fn shares_enabled(&self) -> bool {
        self.core.links_enabled()
    }

    fn share_link_list(
        &self,
        user: UserId,
        path: Option<&str>,
    ) -> Result<Vec<hapi::ShareLinkInfo>, hapi::CoreError> {
        let links = self.core.list_links(user, path).map_err(http_err)?;
        Ok(links.into_iter().map(|l| self.http_link(user, l)).collect())
    }

    fn share_link_get(
        &self,
        user: UserId,
        id: i64,
    ) -> Result<hapi::ShareLinkInfo, hapi::CoreError> {
        let link = self.core.get_link(user, id).map_err(http_err)?;
        Ok(self.http_link(user, link))
    }

    fn share_link_create(
        &self,
        user: UserId,
        req: &hapi::ShareLinkCreate,
    ) -> Result<(hapi::ShareLinkInfo, String), hapi::CoreError> {
        let spec = sc_core::LinkSpec {
            perms: req
                .perms
                .map(|p| p.to_perms())
                .unwrap_or(sc_core::Perms::READ | sc_core::Perms::DOWNLOAD),
            password: req.password.clone(),
            expires_ns: parse_ns_opt(req.expires_ns.as_deref())?,
            max_downloads: req.max_downloads,
            label: req.label.clone(),
        };
        let (link, token) = self
            .core
            .create_link(user, &req.path, &spec)
            .map_err(http_err)?;
        Ok((self.http_link(user, link), token))
    }

    fn share_link_update(
        &self,
        user: UserId,
        id: i64,
        patch: &hapi::ShareLinkPatch,
    ) -> Result<hapi::ShareLinkInfo, hapi::CoreError> {
        let expires = match &patch.expires_ns {
            None => None,
            Some(None) => Some(None),
            Some(Some(s)) => Some(parse_ns_opt(Some(s))?),
        };
        let core_patch = sc_core::LinkPatch {
            perms: patch.perms.map(|p| p.to_perms()),
            password: patch.password.clone(),
            expires_ns: expires,
            max_downloads: patch.max_downloads,
            label: patch.label.clone(),
        };
        let link = self
            .core
            .update_link(user, id, &core_patch)
            .map_err(http_err)?;
        Ok(self.http_link(user, link))
    }

    fn share_link_delete(&self, user: UserId, id: i64) -> Result<(), hapi::CoreError> {
        self.core.delete_link(user, id).map_err(http_err)
    }

    fn share_link_lookup(&self, token: &str) -> Result<Option<i64>, hapi::CoreError> {
        Ok(self
            .core
            .resolve_link(token)
            .map_err(http_err)?
            .map(|l| l.id))
    }

    fn share_link_public(&self, id: i64) -> Result<hapi::PublicLink, hapi::CoreError> {
        let link = self
            .core
            .link_by_id(id)
            .map_err(http_err)?
            .ok_or(hapi::CoreError::NotFound)?;
        // `link_target` is where expiry, the download cap and the
        // path+fileid cross-check all land; a failure here is `Gone`.
        let entry = self.core.link_target(&link).map_err(http_err)?;
        Ok(hapi::PublicLink {
            id: link.id,
            name: entry.name,
            is_dir: entry.kind == sc_vfs::Kind::Dir,
            size: entry.size,
            mtime_ns: entry.mtime_ns,
            has_password: link.has_password,
            is_drop: link.is_drop(),
            can_download: link
                .perms
                .intersects(sc_core::Perms::READ | sc_core::Perms::DOWNLOAD),
            fid: entry.id.map(|f| f.get()),
            etag8: etag8_of(&entry.etag),
            label: link.label,
        })
    }

    fn share_link_entries(&self, id: i64) -> Result<Vec<hapi::Entry>, hapi::CoreError> {
        let link = self
            .core
            .link_by_id(id)
            .map_err(http_err)?
            .ok_or(hapi::CoreError::NotFound)?;
        Ok(self
            .core
            .link_list(&link)
            .map_err(http_err)?
            .into_iter()
            .map(http_entry)
            .collect())
    }

    fn share_link_check_password(&self, id: i64, candidate: &str) -> Result<bool, hapi::CoreError> {
        self.core
            .check_link_password(id, candidate)
            .map_err(http_err)
    }

    fn share_link_note_download(&self, id: i64) -> Result<(), hapi::CoreError> {
        self.core.note_link_download(id).map_err(http_err)
    }

    fn share_link_drop(
        &self,
        id: i64,
        name: &str,
        body: &[u8],
    ) -> Result<hapi::Entry, hapi::CoreError> {
        let link = self
            .core
            .link_by_id(id)
            .map_err(http_err)?
            .ok_or(hapi::CoreError::NotFound)?;
        Ok(http_entry(
            self.core.link_drop(&link, name, body).map_err(http_err)?,
        ))
    }

    // ----------------------------------------------------------- archive --

    fn archive_walk(
        &self,
        user: UserId,
        vpath: &str,
        visit: &mut dyn FnMut(&hapi::WalkEntry, Option<&mut dyn std::io::Read>),
    ) -> Result<(), hapi::CoreError> {
        self.core
            .archive_walk(user, vpath, &mut |entry, stream| {
                let we = hapi::WalkEntry {
                    rel_path: entry.rel_path.clone(),
                    is_dir: entry.is_dir,
                    readable: entry.readable,
                    size: entry.size,
                    mtime_ns: entry.mtime_ns,
                };
                match stream {
                    Some(s) => visit(&we, Some(s as &mut dyn std::io::Read)),
                    None => visit(&we, None),
                }
            })
            .map_err(http_err)
    }

    fn index_settings(&self) -> Result<hapi::IndexSettings, hapi::CoreError> {
        Ok(hapi::IndexSettings {
            name_enabled: self.index_settings.name_enabled(),
        })
    }

    fn set_index_name_enabled(&self, enabled: bool) -> Result<hapi::IndexSettings, hapi::CoreError> {
        self.index_settings
            .set_name_enabled(enabled)
            .map_err(|e| hapi::CoreError::Internal(e.to_string()))?;
        Ok(hapi::IndexSettings { name_enabled: enabled })
    }

    /// `POST /api/admin/index/build` (`FEATURES.md` #116). Crawls every
    /// registered share through the same `CrawlThrottle`-paced walk
    /// `build_name_index` always used (`build_name_index_with_progress` is
    /// the same crawl, just also reporting progress and polling
    /// `should_cancel`) — an admin-triggered build must not bypass the
    /// pacing that keeps this from starving Jellyfin/Samba on a co-accessed
    /// share.
    fn build_name_indexes(
        &self,
        on_progress: &dyn Fn(u64, Option<String>),
        should_cancel: &dyn Fn() -> bool,
    ) -> Result<Vec<hapi::IndexBuildResult>, hapi::CoreError> {
        if !self.index_settings.name_enabled() {
            return Err(hapi::CoreError::NotSupported);
        }
        self.index_build_running
            .store(true, std::sync::atomic::Ordering::Relaxed);
        let _guard = BuildRunningGuard(&self.index_build_running);

        let mut results = Vec::new();
        for def in self.core.share_defs() {
            if should_cancel() {
                results.push(hapi::IndexBuildResult {
                    share: def.name.clone(),
                    ok: false,
                    error: Some("cancelled".to_string()),
                });
                break;
            }
            let label = def.name.clone();
            let report = |visited: usize| on_progress(visited as u64, Some(label.clone()));
            match build_name_index_with_progress(&self.core, def.id, &report, should_cancel) {
                Ok(CrawlOutcome::Built(_)) => results.push(hapi::IndexBuildResult {
                    share: def.name,
                    ok: true,
                    error: None,
                }),
                Ok(CrawlOutcome::Cancelled) => {
                    results.push(hapi::IndexBuildResult {
                        share: def.name,
                        ok: false,
                        error: Some("cancelled".to_string()),
                    });
                    break;
                }
                Err(e) => results.push(hapi::IndexBuildResult {
                    share: def.name,
                    ok: false,
                    error: Some(e.to_string()),
                }),
            }
        }
        Ok(results)
    }
}

/// First 8 bytes of the ETag string, matching how every other signed-URL
/// caller in this binary derives `Claim::etag`.
fn etag8_of(etag: &str) -> [u8; 8] {
    let mut out = [0u8; 8];
    let raw = etag.as_bytes();
    let n = raw.len().min(8);
    out[..n].copy_from_slice(&raw[..n]);
    out
}

fn parse_ns_opt(s: Option<&str>) -> Result<Option<i128>, hapi::CoreError> {
    match s {
        None => Ok(None),
        Some(v) => v.parse::<i128>().map(Some).map_err(|_| {
            hapi::CoreError::InvalidName("expires_ns must be an integer nanosecond string".into())
        }),
    }
}

impl CoreBridge {
    /// `(share, share-relative path)` back to the owner's `/{label}/…` view.
    ///
    /// The store keeps the share-relative path — labels are an ACL projection
    /// that can be renamed — so the virtual path has to be rebuilt per caller.
    fn vpath_for(&self, user: UserId, share: ShareId, path: &sc_vfs::SafePath) -> String {
        for r in self.core.roots(user) {
            if r.share != share || !r.subpath.is_prefix_of(path) {
                continue;
            }
            let skip = r.subpath.components().len();
            let rest: Vec<&str> = path.components()[skip..]
                .iter()
                .map(|c| c.as_str())
                .collect();
            return if rest.is_empty() {
                format!("/{}", r.label)
            } else {
                format!("/{}/{}", r.label, rest.join("/"))
            };
        }
        String::new()
    }

    fn http_link(&self, user: UserId, l: sc_core::ShareLink) -> hapi::ShareLinkInfo {
        hapi::ShareLinkInfo {
            id: l.id,
            path: self.vpath_for(user, l.share, &l.path),
            perms: l.perms,
            expires_ns: l.expires_ns.map(|v| v.to_string()),
            max_downloads: l.max_downloads,
            downloads: l.downloads,
            label: l.label,
            has_password: l.has_password,
            created_ns: l.created_ns.to_string(),
            // Never recoverable after creation — the plaintext was returned
            // once and only `sha256(token)` was kept.
            token: None,
            url: None,
        }
    }
}

/// Depth-first name walk feeding the estimator. Stops at `budget` entries and
/// reports that as an error so the caller can flag the estimate as truncated.
fn walk_for_estimate(
    root: &sc_vfs::ShareRoot,
    path: &SafePath,
    scanner: &mut sc_search::CorpusScanner,
    seen: &mut u64,
    budget: u64,
) -> Result<(), ()> {
    if *seen >= budget {
        return Err(());
    }
    let max_depth = root.policy().max_depth;
    let entries = root.read_dir(path).map_err(|_| ())?;
    for e in entries {
        let Ok(p) = path.join(&e.name, max_depth) else {
            continue;
        };
        let is_dir = e.kind == sc_vfs::Kind::Dir;
        scanner.observe(&p.to_display_string(), is_dir);
        *seen += 1;
        if is_dir {
            walk_for_estimate(root, &p, scanner, seen, budget)?;
        }
    }
    Ok(())
}

impl CoreBridge {
    /// Injected separately from `Core` because the metadata DB's size is a
    /// deployment fact, not a domain one.
    fn db_bytes(&self) -> u64 {
        DB_BYTES.load(std::sync::atomic::Ordering::Relaxed)
    }
}

/// Last observed size of `meta.db`, refreshed at startup and after `gc`.
/// A global rather than a field so `CoreBridge` stays a single `Arc<Core>`
/// and can be cloned into both trait objects without duplicating state.
pub static DB_BYTES: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

// --------------------------------------------------------------- quota --

/// Backs `sc_core::QuotaSink` with `sc-auth`'s `user.usage_bytes`/
/// `user.quota_bytes` columns (`FEATURES.md` #49) — the one seam `Core`
/// needs to enforce a cap without either crate depending on the other.
pub struct AuthQuotaSink {
    pub auth: Arc<sc_auth::AuthService>,
}

impl sc_core::QuotaSink for AuthQuotaSink {
    fn check(&self, user: UserId, additional: u64) -> Result<(), sc_core::CoreError> {
        let Ok(Some(status)) = self.auth.quota_status(user) else {
            // Unknown user or a storage error: fail open, same as a `Core`
            // with no sink attached at all — a quota check must never be
            // the reason an otherwise-valid write 500s.
            return Ok(());
        };
        match status.limit {
            Some(limit) if status.used.saturating_add(additional) > limit => {
                Err(sc_core::CoreError::QuotaExceeded)
            }
            _ => Ok(()),
        }
    }

    fn charge(&self, user: UserId, delta: i64) {
        self.auth.add_usage(user, delta);
    }
}

// ------------------------------------------------------------- sc-upload --

/// Binds `sc_http::upload_api::UploadApi` (the TUS wire contract) to
/// `sc_upload::UploadEngine` (the spool/interval engine).
pub struct UploadBridge {
    pub engine: Arc<sc_upload::UploadEngine>,
    pub core: Arc<sc_core::Core>,
}

impl UploadBridge {
    fn parse_id(id: &str) -> Result<sc_upload::SessionId, hapi::CoreError> {
        sc_upload::SessionId::parse_b64(id)
            .ok_or_else(|| hapi::CoreError::InvalidName("malformed upload id".into()))
    }

    /// Maps every `sc_upload::UploadError` variant to the distinct HTTP
    /// status `docs/DESIGN-UPLOAD.md` §2.2 specifies, rather than collapsing
    /// all of them to `500`. A TUS client acts on this distinction — `409`
    /// (offset conflict) means "re-sync via `HEAD` and resend from the real
    /// offset", `410` (session gone) means "start a new upload", `412`
    /// (precondition) means "the file changed underneath you" — and every
    /// one of those is a client-side recovery, not the server retry-forever
    /// loop a blanket `500` invites.
    ///
    /// Two variants only get an approximate status because
    /// `sc_http::core_api::CoreError` has no dedicated code for them yet
    /// (adding one means editing `core_api.rs`, which is out of this
    /// change's scope):
    /// - `ChecksumMismatch` wants TUS's `460`; it gets `412` (Precondition)
    ///   instead — still "an integrity check failed, don't blindly retry
    ///   the same bytes", just not the exact code.
    /// - `RateLimited` wants `429`; it gets `409` (Conflict) instead — still
    ///   "retry, but not immediately". This one is currently unreachable:
    ///   nothing in `sc_upload::engine` constructs it yet (`upload.create_rate`
    ///   from §6 isn't wired), so the approximation has no live effect today.
    fn upload_err(e: sc_upload::UploadError) -> hapi::CoreError {
        use sc_upload::UploadError as U;
        match e {
            U::NotFound => hapi::CoreError::NotFound,
            U::Gone => hapi::CoreError::Gone,
            U::Conflict { expected, got } => hapi::CoreError::Conflict {
                path: format!("offset conflict: expected {expected}, got {got}"),
                etag: None,
            },
            U::PreconditionFailed => hapi::CoreError::Precondition {
                current_etag: String::new(),
            },
            // See doc comment: nearest available code, not the TUS-spec 460.
            U::ChecksumMismatch => hapi::CoreError::Precondition {
                current_etag: String::new(),
            },
            U::ResourceExhausted(msg) => {
                tracing::warn!(reason = %msg, "upload rejected: resource limit");
                hapi::CoreError::QuotaExceeded
            }
            U::BadRequest(msg) => hapi::CoreError::InvalidName(msg),
            U::ChunkTooSmall { min } => {
                hapi::CoreError::InvalidName(format!("chunk below minimum size ({min} bytes)"))
            }
            U::Unprocessable => hapi::CoreError::InvalidName(
                "offset + length exceeds declared upload length".into(),
            ),
            U::Incomplete => {
                hapi::CoreError::InvalidName("upload incomplete: cannot finalize".into())
            }
            U::Fragmented => hapi::CoreError::InvalidName("too many disjoint received runs".into()),
            // See doc comment: nearest available code, not the TUS-spec 429;
            // dead branch today (nothing constructs `RateLimited` yet).
            U::RateLimited => hapi::CoreError::Conflict {
                path: "session creation rate limit exceeded".into(),
                etag: None,
            },
            // A session/part-file integrity failure is a genuine server-side
            // fault, unlike the client-facing conditions above.
            U::Corrupt => {
                tracing::error!("upload session corrupt");
                hapi::CoreError::Internal("corrupt session state".into())
            }
            U::Sqlite(err) => {
                tracing::error!(error = %err, "upload engine db error");
                hapi::CoreError::Internal(err.to_string())
            }
            // Reuse `sc-core`'s own `VfsError`/`io::Error` -> `CoreError`
            // mapping (`sc_core::CoreError::from`) instead of duplicating it:
            // it already turns "not found"/"already exists"/"cross-device"/
            // "permission denied" into the right shapes, and `http_err`
            // already turns *that* into the right `hapi::CoreError`.
            U::Vfs(err) => http_err(sc_core::CoreError::from(err)),
            U::Io(err) => http_err(sc_core::CoreError::from(err)),
        }
    }
}

impl UploadBridge {
    /// Shared by `create` and `create_with_upload`: resolve the destination,
    /// open its session, and hand back both the id and the `ShareRoot` a
    /// caller that also has bytes to spool (creation-with-upload) needs —
    /// avoids re-resolving the destination a second time just to get the
    /// same root `create` already looked up.
    fn create_session(
        &self,
        user: UserId,
        dest_vpath: &str,
        total_len: Option<u64>,
        random_access: bool,
    ) -> Result<(sc_upload::SessionId, Arc<sc_vfs::ShareRoot>), hapi::CoreError> {
        // Resolving here (rather than at finalisation) is what makes the ACL
        // check happen before a single byte is spooled. `resolve_for_upload`
        // (not `resolve`) is load-bearing: `resolve` checks READ, so a
        // read-only grant used to pass this line and every chunk PATCH'd
        // afterward landed on disk with zero permission check ever run.
        let r = self
            .core
            .resolve_for_upload(user, dest_vpath)
            .map_err(http_err)?;
        // Quota pre-check (`FEATURES.md` #49): the declared length is the
        // only size known this early — an overwrite's replaced bytes aren't
        // credited back, same simplification `ops::copy_to` accepts (errs
        // toward the cap sooner, never under-counts). Skipped entirely when
        // the client didn't declare a length (random-access/streaming
        // upload) — the actual charge at finalize still applies.
        if let Some(total) = total_len {
            self.core.check_quota(user, total).map_err(http_err)?;
        }
        let root = self
            .core
            .share(r.share)
            .ok_or_else(|| hapi::CoreError::Internal("share vanished".into()))?;
        let spec = sc_upload::SessionSpec {
            user,
            share: r.share,
            dest: r.path,
            total_len,
            random_access,
            if_match: None,
            mode: sc_upload::SpoolMode::OffsetAddressed,
            meta: sc_upload::UploadMeta {
                filename: dest_vpath
                    .rsplit('/')
                    .next()
                    .unwrap_or(dest_vpath)
                    .to_string(),
                ..Default::default()
            },
        };
        let id = self.engine.create(&root, spec).map_err(Self::upload_err)?;
        Ok((id, root))
    }

    /// Shared by `patch` and `patch_checked`: write `data` at `offset`,
    /// optionally enforcing `checksum`, and finalise if that completes the
    /// declared length. TUS has no explicit "commit" — the upload is
    /// finished the moment the declared length has arrived — so finalising
    /// here is the only place it can happen; the protocol never sends a
    /// separate call for it.
    ///
    /// No ACL re-check here, and none in `sc_upload::UploadEngine::finalize`
    /// either — deliberately. A grant revoked between session creation and
    /// this call still lets the upload complete. Two ways to close that
    /// window were considered and rejected:
    /// - Per-chunk re-check: an `AclEngine::evaluate` call is in-memory, not
    ///   I/O, but paying it on every `PATCH` (hundreds, for a multi-GB file
    ///   at the ~10 MiB default chunk size) to guard a race no other
    ///   check-then-write path in this codebase re-validates mid-flight
    ///   (`ops.rs::write_text` does not re-check between its own ACL check
    ///   and the `pwrite`s that follow) would be a new standard applied only
    ///   here, for a narrow benefit.
    /// - Finalize-time re-check: cheaper (once per upload), but needs the
    ///   destination `(ShareId, SafePath)` at finalize time, and neither
    ///   `sc_upload::SessionStatus` (`head()`'s return type) nor this
    ///   struct's own fields carry it. Adding it to `SessionStatus` means
    ///   editing `sc-upload`; adding a field to `UploadBridge` breaks
    ///   `app.rs`'s fixed-field struct literal that constructs it — both
    ///   files are off-limits for this change. A process-wide `static`
    ///   cache inside this file would dodge both edits, but its lifetime
    ///   wouldn't be tied to a `UploadBridge`/server instance the way a
    ///   struct field's is — the wrong tool to reach for just because the
    ///   right one is off-limits.
    ///
    /// So the gap is accepted, not silently unhandled: the create-time
    /// `WRITE` check above is the one durable, unconditional gate, and it is
    /// enough to fix the reported defect (no check ever ran). See
    /// `revoking_the_grant_mid_upload_does_not_stop_an_in_flight_session` in
    /// `upload_bridge_tests` for the behavior this pins.
    fn patch_impl(
        &self,
        user: UserId,
        sid: sc_upload::SessionId,
        offset: u64,
        data: &[u8],
        checksum: Option<sc_upload::Checksum>,
    ) -> Result<u64, hapi::CoreError> {
        let st = self.engine.head(sid, user).map_err(Self::upload_err)?;
        let root = self
            .core
            .share(st.share)
            .ok_or_else(|| hapi::CoreError::Internal("share vanished".into()))?;
        let new_offset = self
            .engine
            .patch(&root, sid, user, offset, data, checksum)
            .map_err(Self::upload_err)?;
        if st.total_len == Some(new_offset) {
            self.engine
                .finalize(&root, sid, user)
                .map_err(Self::upload_err)?;
            // Charge on the actual finalized size, not the declared one —
            // `create_session`'s check above only pre-checked, it never
            // charged (`FEATURES.md` #49, `quota.rs`: check and charge are
            // separate calls).
            self.core.charge_quota(user, new_offset as i64);
        }
        Ok(new_offset)
    }

    /// TUS wire checksum -> engine checksum. Independent enums on either
    /// side of `sc_http::upload_api` (that crate does not depend on
    /// `sc-upload` — see `upload_api.rs`'s module doc), so the digest is
    /// re-validated here rather than trusted as already the right shape.
    /// Big-endian is the byte order every crc32c-over-HTTP implementation
    /// this was checked against uses for the 4-byte digest; the whole point
    /// of naming it here is that whoever wires the `Upload-Checksum` header
    /// parse in `sc-http::routes` (this bridge cannot reach that file) must
    /// match it on the decode side.
    fn tus_checksum(
        c: sc_http::upload_api::TusChecksum,
    ) -> Result<sc_upload::Checksum, hapi::CoreError> {
        use sc_http::upload_api::TusChecksumAlgo as A;
        match c.algo {
            A::Crc32c => {
                let bytes: [u8; 4] = c.digest.as_slice().try_into().map_err(|_| {
                    hapi::CoreError::InvalidName("crc32c checksum must be 4 bytes".into())
                })?;
                Ok(sc_upload::Checksum::Crc32c(u32::from_be_bytes(bytes)))
            }
            A::Blake3 => {
                let bytes: [u8; 32] = c.digest.as_slice().try_into().map_err(|_| {
                    hapi::CoreError::InvalidName("blake3 checksum must be 32 bytes".into())
                })?;
                Ok(sc_upload::Checksum::Blake3(bytes))
            }
        }
    }
}

impl sc_http::upload_api::UploadApi for UploadBridge {
    fn create(
        &self,
        user: UserId,
        dest_vpath: &str,
        total_len: Option<u64>,
        random_access: bool,
    ) -> Result<String, hapi::CoreError> {
        let (id, _root) = self.create_session(user, dest_vpath, total_len, random_access)?;
        Ok(id.to_b64())
    }

    /// TUS `creation-with-upload`: unlike the default trait method (which
    /// creates the session and drops the body), this actually spools
    /// `initial_body` at offset 0 before answering — the whole reason to
    /// advertise the extension is that the client's `POST` body is not
    /// wasted.
    fn create_with_upload(
        &self,
        user: UserId,
        dest_vpath: &str,
        total_len: Option<u64>,
        random_access: bool,
        initial_body: &[u8],
    ) -> Result<sc_http::upload_api::CreatedWithUpload, hapi::CoreError> {
        let (id, root) = self.create_session(user, dest_vpath, total_len, random_access)?;
        let offset = if initial_body.is_empty() {
            0
        } else {
            let new_offset = self
                .engine
                .patch(&root, id, user, 0, initial_body, None)
                .map_err(Self::upload_err)?;
            if Some(new_offset) == total_len {
                self.engine
                    .finalize(&root, id, user)
                    .map_err(Self::upload_err)?;
                self.core.charge_quota(user, new_offset as i64);
            }
            new_offset
        };
        Ok(sc_http::upload_api::CreatedWithUpload {
            id: id.to_b64(),
            offset,
        })
    }

    fn status(
        &self,
        user: UserId,
        id: &str,
    ) -> Result<sc_http::upload_api::UploadStatus, hapi::CoreError> {
        let sid = Self::parse_id(id)?;
        let st = self.engine.head(sid, user).map_err(Self::upload_err)?;
        // `UploadEngine::head` deliberately keeps answering after `abort()`
        // (the DB row survives until GC finds it — see
        // `sc_upload::engine`'s `abort_then_gc_removes_part_file` test,
        // whose own comment says HEAD reports the terminal state "so the
        // HTTP layer can map it to 404/410 as it sees fit"). This is that
        // mapping. Before it existed, `GET`/`HEAD /api/uploads/{id}` on a
        // just-cancelled session answered `200` with its last-known offset
        // — a client resuming from IndexedDB after a cancel-then-reload
        // would read that as "still live, keep going", exactly backwards
        // from what `DELETE` (cancel) is supposed to mean. `patch()` already
        // refuses writes here via `ensure_receiving` -> `UploadError::Gone`;
        // this makes `status()` agree with it instead of contradicting it.
        if matches!(
            st.state,
            sc_upload::SessionState::Aborted | sc_upload::SessionState::Expired
        ) {
            return Err(Self::upload_err(sc_upload::UploadError::Gone));
        }
        Ok(sc_http::upload_api::UploadStatus {
            offset: st.offset,
            length: st.total_len,
            complete: st.state == sc_upload::SessionState::Done,
            chunk_size: st.chunk_size,
        })
    }

    fn patch(
        &self,
        user: UserId,
        id: &str,
        offset: u64,
        data: &[u8],
    ) -> Result<u64, hapi::CoreError> {
        let sid = Self::parse_id(id)?;
        self.patch_impl(user, sid, offset, data, None)
    }

    /// TUS `checksum` extension: `data` must match `checksum` (when the
    /// client sent one) before the range is committed — see
    /// `sc_upload::UploadEngine::patch`'s ordering-rule doc for why a
    /// mismatch leaves the bytes on disk but *uncommitted*, so a resend
    /// overwrites cleanly rather than corrupting the file.
    fn patch_checked(
        &self,
        user: UserId,
        id: &str,
        offset: u64,
        data: &[u8],
        checksum: Option<sc_http::upload_api::TusChecksum>,
    ) -> Result<u64, hapi::CoreError> {
        let sid = Self::parse_id(id)?;
        let checksum = checksum.map(Self::tus_checksum).transpose()?;
        self.patch_impl(user, sid, offset, data, checksum)
    }

    fn terminate(&self, user: UserId, id: &str) -> Result<(), hapi::CoreError> {
        let sid = Self::parse_id(id)?;
        self.engine.abort(sid, user).map_err(Self::upload_err)
    }

    fn drain(&self) -> usize {
        // Sessions are durable in the engine's own SQLite state and resumable
        // by `Upload-Offset`, so "draining" is a checkpoint, not a flush of
        // volatile data: sweep whatever is finishable and report the count.
        let core = self.core.clone();
        match self.engine.gc(&move |share| core.share(share)) {
            Ok(n) => n,
            Err(e) => {
                tracing::warn!(error = %e, "upload drain failed");
                0
            }
        }
    }

    fn active_count(&self) -> usize {
        self.engine.active_session_count() as usize
    }

    /// Overrides the trait default (which just restates it, `upload_api.rs`
    /// doc comment) with the actually-configured value, so a config file
    /// changing `body_idle_timeout` has an effect instead of being read into
    /// `sc_upload::UploadConfig` and then ignored by the HTTP layer.
    fn body_idle_timeout(&self) -> std::time::Duration {
        self.engine.config().body_idle_timeout
    }

    fn chunk_limits(&self) -> (u64, u64) {
        self.engine.chunk_settings()
    }

    fn set_chunk_limits(&self, min: u64, default: u64) -> Result<(), hapi::CoreError> {
        self.engine
            .set_chunk_settings(min, default)
            .map_err(Self::upload_err)
    }
}

// ------------------------------------------------------------- content --
// Binds `sc_http::content_api::ContentApi` (signed content-URL byte serving,
// `DESIGN-PREVIEW.md` §2) to `sc_core::Core`'s `FileId`-keyed streaming
// primitives plus `sc_preview::PreviewService` for `InlineThumb`. Kept as its
// own bridge type (not folded into `CoreBridge`) because it needs the
// preview service and `CoreBridge` deliberately doesn't.

pub struct ContentBridge {
    pub core: Arc<sc_core::Core>,
    pub preview: Arc<sc_preview::PreviewService>,
}

impl sc_http::content_api::ContentApi for ContentBridge {
    fn stat_by_fid(
        &self,
        fid: sc_vfs::ids::FileId,
    ) -> Result<sc_http::content_api::ContentStat, hapi::CoreError> {
        let e = self.core.stat_by_fid(fid).map_err(http_err)?;
        Ok(sc_http::content_api::ContentStat {
            name: e.name,
            size: e.size,
            etag: e.etag,
        })
    }

    fn check_read(&self, user: UserId, fid: sc_vfs::ids::FileId) -> Result<(), hapi::CoreError> {
        self.core.check_read_by_fid(user, fid).map_err(http_err)
    }

    fn open_stream(
        &self,
        fid: sc_vfs::ids::FileId,
        range: Option<(u64, u64)>,
    ) -> Result<Box<dyn std::io::Read + Send>, hapi::CoreError> {
        let (_meta, stream) = self.core.open_stream_by_fid(fid, range).map_err(http_err)?;
        Ok(Box::new(stream))
    }

    fn thumbnail(
        &self,
        fid: sc_vfs::ids::FileId,
        w: u16,
        h: u16,
        etag8: [u8; 8],
    ) -> sc_http::content_api::BoxFuture<'static, Result<Vec<u8>, hapi::CoreError>> {
        let core = self.core.clone();
        let preview = self.preview.clone();
        Box::pin(async move {
            // Opening the original bytes is a filesystem op; run it off the
            // reactor the same way every other blocking `Core` call in this
            // binary does when it's reached from an async context.
            let (_meta, stream) =
                tokio::task::spawn_blocking(move || core.open_stream_by_fid(fid, None))
                    .await
                    .map_err(|e| hapi::CoreError::Internal(e.to_string()))?
                    .map_err(http_err)?;
            preview
                .get_or_generate(fid, w as u32, h as u32, etag8, stream)
                .await
                .map(|bytes| bytes.to_vec())
                .map_err(|e| hapi::CoreError::Internal(e.to_string()))
        })
    }
}

// -------------------------------------------------------------- search --
// Binds `sc_http::search_api::SearchApi` (`GET /api/search[/stream]`,
//) to `sc-search`'s `Walker` (T2) over the caller's
// readable `ShareRoot`s, consulting a per-share `sc_search::NameIndex` (T3)
// first when one already exists on disk.
//
// An index is opened if (and only if) `<share host path>/.scindex/names/meta`
// is already present (`open_name_index` below), and every share without one
// falls back to a T2 walk — which remains every share until an operator
// turns `[index] name_enabled` on and runs `sc-server index build` (`lib.rs`
// `cmd_index`), since **both indexes are off by default**
// (`DESIGN-FOOTPRINT.md` §2) and nothing here plants one speculatively.
//
// Three things keep an index current once one exists, in ascending order of
// "how sure are we this path changed":
//
// * **Self-writes** (`note_index_change`, called from `CoreBridge`'s
//   `sc_dav`/`hapi` `mkdir`/`delete`/`rename`/`move_entries`/`copy_entries`/
//   `copy_to`/`write_*` impls below): the moment this process itself creates,
//   removes or moves a path, the affected share's index (if it has one) gets
//   an `append`/`tombstone` for exactly that path — no ambiguity, no
//   filesystem re-scan. Not hooked: `hapi`'s `move_entries`/`copy_entries`
//   (caller-chosen `OnConflict` — `Rename`/`Skip` can produce a destination
//   name this bridge cannot predict without re-deriving `sc-core`'s conflict
//   logic) and `UploadBridge`'s TUS finalize path (no destination vpath
//   available at that layer — see `UploadBridge::patch_impl`). Both fall
//   through to the mechanisms below instead of updating nothing.
// * **Watcher-driven reconciliation** (`reconcile_watch_event`, free
//   function below): answers one `sc_watch::InvalEvent` — a directory that
//   changed for a reason this process didn't cause (another process writing
//   the same tree, a client mounting the share directly) — by diffing a
//   fresh single-level `read_dir` against `sc_search::NameIndex::children_of`.
//   Called from `app.rs::start_watcher_and_ws_hub`'s forwarding thread, once
//   per `InvalEvent`.
// * **Full rebuild** (`build_name_index` / `sc-server index build`): an
//   operator-triggered full recrawl, for anything the two mechanisms above
//   miss (a subtree that changed before the server ever started watching it,
//   an index built for the first time).
//
// A corrupt or missing index is never load-bearing for correctness: every
// hit `consult_name_indexes` gets from the index is ACL-rechecked and
// re-`stat`ed before being trusted (`stale: gone since indexed` below), and
// `open_name_index`/`NameIndex::open` failing at all just means the caller
// falls back to the T2 walk it would have used anyway.
//
// T4 (content indexing) has no implementation anywhere in `sc-search`
// (its own module doc says so) and is out of scope for this binding.

/// One walk (or index-query) starting point: a share root plus the
/// share-relative path within it to start from. Named so
/// `consult_name_indexes`'s return tuple doesn't read as an unreadable wall
/// of generics (clippy's `type_complexity`).
type SearchRoot = (Arc<sc_vfs::ShareRoot>, sc_vfs::SafePath);

pub struct SearchBridge {
    pub core: Arc<sc_core::Core>,
    /// 's storage-class-aware cap/deadline needs to
    /// know each share's storage class; detection touches sysfs on Linux
    /// (`crate::storage_class`), so results are cached rather than re-read
    /// on every search.
    pub storage_cache: crate::storage_class::StorageClassCache,
    /// The *same* object as `sc_http::AppState::search_concurrency` (shared
    /// `Arc`, constructed once in `app.rs`) — not just an equal config
    /// value. That's what guarantees the walk's own deadline
    /// (`sc_search::WalkBudget`, chosen here) and the HTTP layer's
    /// concurrency-permit tier (chosen in `sc_http::routes` from
    /// `search_tier` below) always agree on which tier's numbers apply,
    /// with nothing that could let them drift apart.
    pub limits: Arc<sc_http::search_limits::SearchConcurrency>,
}

impl SearchBridge {
    /// Every `(ShareRoot, SafePath)` the caller can read, restricted to
    /// `scope` when given.: the ACL check that
    /// matters happens *inside* the walk (gating descent); this only decides
    /// which roots the walk starts from, and a root the caller cannot read
    /// at all is silently absent rather than an error — asking about paths
    /// you cannot see must not be distinguishable from asking about nothing.
    fn roots_for(&self, user: UserId, scope: Option<&str>) -> Vec<SearchRoot> {
        if let Some(scope) = scope {
            return match self.core.resolve(user, scope) {
                Ok(r) => vec![(r.root, r.path)],
                Err(_) => Vec::new(),
            };
        }
        self.core
            .roots(user)
            .into_iter()
            .filter(|r| r.perms.contains(sc_acl::Perms::READ))
            .filter_map(|r| self.core.share(r.share).map(|root| (root, r.subpath)))
            .collect()
    }

    fn matcher_for(q: &sc_http::search_api::SearchQuery) -> sc_search::Matcher {
        let mut m = sc_search::Matcher::new(&q.text);
        if let Some(kind) = &q.kind {
            m = m.exts(kind_extensions(kind));
        }
        if let (Some(lo), Some(hi)) = (q.size_min, q.size_max) {
            m = m.size_range(lo, hi);
        } else if let Some(lo) = q.size_min {
            m = m.size_range(lo, u64::MAX);
        } else if let Some(hi) = q.size_max {
            m = m.size_range(0, hi);
        }
        if let Some(after) = q.mtime_after_ns {
            m = m.mtime_range(after, i128::MAX);
        }
        m
    }

    /// Folds every root's real storage class (`crate::storage_class::detect`,
    /// cached per share) to the single tier that governs this search —
    /// "a search spanning shares of different
    /// classes takes the most conservative (slowest) one." Feeds both the
    /// concurrency budget (via `SearchApi::search_tier`) and, as a plain
    /// `bool`, `sc-search`'s own storage-aware knobs (`decide_threads`,
    /// `WalkBudget::for_storage`) below.
    fn effective_tier(&self, roots: &[SearchRoot]) -> sc_http::search_limits::SearchTier {
        sc_http::search_limits::fold_tier(
            roots
                .iter()
                .map(|(r, _)| self.storage_cache.get_or_detect(r)),
        )
    }

    // ------------------------------------------------------ T3 consultation --

    /// Consults each root's name index where one exists and returns
    /// `(hits already resolved, roots that still need a T2 walk, an optional
    /// completeness if the index side alone was truncated)`.
    ///
    /// A query with a `kind`/size/mtime filter bypasses the index for *every*
    /// root, not just the ones lacking one: the index stores bare paths only
    /// ( — no kind, no size, no mtime), so it has no
    /// way to evaluate such a filter, and silently answering a filtered query
    /// from a source that cannot apply the filter would be a wrong answer,
    /// not a fast one.
    ///
    /// For every hit that survives the index's own query, this re-checks ACL
    /// per hit exactly like the walk does ( — the
    /// index does not know permissions) and then stats the file, which does
    /// two jobs at once: it supplies `is_dir`/`size`/`mtime_ns` (the index
    /// doesn't carry them) and it revalidates the hit is not stale — a file
    /// deleted since the index was built must not resurrect as a ghost
    /// result (§4.2: "the index is a cache").
    fn consult_name_indexes(
        &self,
        user: UserId,
        roots: &[SearchRoot],
        q: &sc_http::search_api::SearchQuery,
        matcher: &sc_search::Matcher,
        budget: &sc_search::WalkBudget,
    ) -> (
        Vec<sc_search::Hit>,
        Vec<SearchRoot>,
        Option<sc_search::Completeness>,
    ) {
        if q.kind.is_some() || matcher.needs_stat() {
            return (Vec::new(), roots.to_vec(), None);
        }

        let want_total = budget.max_results as usize;
        // Overscan: an ACL-denied or since-deleted hit costs a query slot but
        // yields nothing, mirroring §6.2's `MAX_SCAN` reasoning — ask the
        // index for more than we need rather than under-filling a page for a
        // caller who turns out to be allowed to see everything they asked
        // for.
        let want_query = want_total.saturating_mul(8).max(64);

        let mut hits = Vec::new();
        let mut walker_roots = Vec::new();
        let mut truncated_by_cap = false;

        for (root, scope) in roots {
            if hits.len() >= want_total {
                // Already full: every remaining root still needs its own
                // chance to contribute once the index side stops, so it goes
                // to T2 rather than being silently dropped.
                walker_roots.push((root.clone(), scope.clone()));
                continue;
            }
            let share = root.id();
            let scope_display = scope.to_display_string();

            let Some(dir) = name_index_dir(&self.core, share) else {
                walker_roots.push((root.clone(), scope.clone()));
                continue;
            };
            let Some(idx) = open_name_index(&dir) else {
                walker_roots.push((root.clone(), scope.clone()));
                continue;
            };
            let Ok(result) = idx.query(q.text.as_bytes(), want_query) else {
                walker_roots.push((root.clone(), scope.clone()));
                continue;
            };
            if result.must_fall_back() {
                walker_roots.push((root.clone(), scope.clone()));
                continue;
            }

            for ih in result.hits {
                if hits.len() >= want_total {
                    truncated_by_cap = true;
                    break;
                }
                if !scope_display.is_empty()
                    && ih.path != scope_display
                    && !ih.path.starts_with(&format!("{scope_display}/"))
                {
                    continue;
                }
                let Ok(safe) = SafePath::parse(&ih.path, u16::MAX) else {
                    continue;
                };
                if !self.core.can_read(user, share, &safe) {
                    continue;
                }
                let Ok(st) = root.stat(&safe) else { continue }; // stale: gone since indexed
                hits.push(ih.into_hit(st.kind.is_dir(), Some(st.size), Some(st.mtime_ns)));
            }
        }

        let index_completeness = truncated_by_cap.then_some(sc_search::Completeness::Truncated {
            reason: sc_search::TruncReason::MaxResults,
            seen: hits.len() as u64,
            elapsed: std::time::Duration::ZERO,
        });
        (hits, walker_roots, index_completeness)
    }

    /// Builds (or fully rebuilds) `share`'s on-disk T3 name index by
    /// crawling it end to end ("initial activation") and
    /// handing the result to `sc_search::IndexBuilder`.
    ///
    /// Thin wrapper over the free function below so existing tests
    /// (`bridge.build_name_index(TEST_SHARE)`) keep working unchanged; the
    /// free function is what `sc-server index build` (`lib.rs`'s `cmd_index`)
    /// calls, since a CLI command has an `Arc<sc_core::Core>` but no reason to
    /// construct a whole `SearchBridge` (concurrency limits, storage-class
    /// cache) just to reach a crawler.
    pub fn build_name_index(&self, share: sc_vfs::ShareId) -> anyhow::Result<sc_search::NameIndex> {
        build_name_index(&self.core, share)
    }
}

/// RAII guard resetting [`CoreBridge::index_build_running`] on every exit
/// path (normal return, early `?`, or a panic) — a build that errors out
/// partway must not leave the flag stuck `true` and starve the idle merge
/// scheduler forever.
struct BuildRunningGuard<'a>(&'a std::sync::atomic::AtomicBool);
impl Drop for BuildRunningGuard<'_> {
    fn drop(&mut self) {
        self.0.store(false, std::sync::atomic::Ordering::Relaxed);
    }
}

impl CoreBridge {
    /// Periodic idle-triggered segment merge (`FEATURES.md` #117). Called
    /// from `app.rs`'s `spawn_idle_merge` thread on `IDLE_MERGE_INTERVAL`. A
    /// no-op whenever a `/api/admin/index/build` job holds
    /// `index_build_running`, and for every share whose index (if it has
    /// one) reports `needs_merge() == false`.
    pub fn run_idle_merge_pass(&self) {
        use std::sync::atomic::Ordering;
        if self.index_build_running.load(Ordering::Relaxed) {
            return;
        }
        for def in self.core.share_defs() {
            let Some(idx) = open_existing_name_index(&self.core, def.id) else {
                continue;
            };
            if !idx.needs_merge() {
                continue;
            }
            let flag = &self.index_build_running;
            tracing::info!(share = def.id.get(), "idle-triggered name index merge starting");
            // Re-checked as `merge` walks the base index (every 64 blocks,
            // per its own doc) so a build that starts mid-merge still gets
            // to interrupt it, not just block the next check.
            if let Err(e) = idx.merge(&|| !flag.load(Ordering::Relaxed)) {
                tracing::warn!(error = %e, share = def.id.get(), "idle-triggered name index merge failed");
            }
        }
    }
}

/// Where `share`'s name index would live, if it has one. A free function
/// (not a `SearchBridge` method) so `SearchBridge` (query side),
/// `CoreBridge` (self-write hooks), `reconcile_watch_event` (watcher-driven
/// updates) and `lib.rs`'s CLI can all reach it without any of them
/// depending on `SearchBridge` just for this.
///
/// `sc_vfs::ShareRoot` deliberately never exposes a real host path
/// (nothing above `sc-vfs` may see one) —
/// `Core::share_host_path` is documented as the one sanctioned escape hatch
/// for exactly this kind of trusted server-side infra. One index per share
/// (not per mount,'s finer split) — every share here
/// already has its own `ShareRoot`, so this is the simpler placement and
/// still satisfies "delete it and T2 still works" (§4.2).
fn name_index_dir(core: &sc_core::Core, share: sc_vfs::ShareId) -> Option<PathBuf> {
    core.share_host_path(share)
        .map(|p| p.join(".scindex").join("names"))
}

/// Opens `dir` as a [`sc_search::NameIndex`] only if one is already there.
///
/// `NameIndex::open` creates `dir` (via `create_dir_all`) as a side effect,
/// so calling it speculatively — the only way to "check" otherwise — would
/// plant a `.scindex/` under every share the moment anyone searched it (or
/// wrote to it, or a watch event fired for it). That would violate the one
/// invariant this whole feature has to respect: **both indexes are off by
/// default** (`DESIGN-FOOTPRINT.md` §2), meaning a share with no index gets
/// zero filesystem footprint from any of this. Checking for `meta` first (the
/// file `IndexBuilder::build` writes last, atomically) is what keeps that
/// true.
fn open_name_index(dir: &std::path::Path) -> Option<sc_search::NameIndex> {
    if !dir.join("meta").exists() {
        return None;
    }
    sc_search::NameIndex::open(dir).ok()
}

/// `name_index_dir` + `open_name_index` in one call — the common case for
/// every caller that just wants "the index, if `share` has one, or nothing".
pub(crate) fn open_existing_name_index(
    core: &sc_core::Core,
    share: sc_vfs::ShareId,
) -> Option<sc_search::NameIndex> {
    open_name_index(&name_index_dir(core, share)?)
}

/// Builds (or fully rebuilds) `share`'s on-disk T3 name index by crawling it
/// end to end ("initial activation") and handing the result
/// to `sc_search::IndexBuilder`. See `SearchBridge::build_name_index`'s doc
/// comment for why this is a free function rather than a method.
///
/// `FEATURES.md` #124 "self-throttling index crawler": the walk paces itself
/// against `share`'s detected storage class (`crate::storage_class`) so it
/// doesn't monopolize a disk Jellyfin/Samba are also reading from
/// (`ShareBootstrap::shared_externally`). See [`CrawlThrottle`]'s doc for
/// the mechanism and why it was chosen over `ionice`.
pub(crate) fn build_name_index(
    core: &sc_core::Core,
    share: sc_vfs::ShareId,
) -> anyhow::Result<sc_search::NameIndex> {
    match build_name_index_with_progress(core, share, &|_| {}, &|| false)? {
        CrawlOutcome::Built(idx) => Ok(*idx),
        // `should_cancel` is a constant `false` above, so the walk can never
        // observe a cancellation — this arm is unreachable but still has to
        // typecheck.
        CrawlOutcome::Cancelled => unreachable!("build_name_index never cancels"),
    }
}

/// Outcome of a callback-driven crawl (`build_name_index_with_progress`):
/// either a finished index, or an early stop that produced nothing —
/// deliberately not an error, since an admin-requested cancellation is not a
/// failure.
pub(crate) enum CrawlOutcome {
    /// Boxed because `NameIndex` dwarfs the other variant — `Cancelled`
    /// carries nothing, and every caller moves this straight into an
    /// `anyhow::Result` that would otherwise be sized by it
    /// (`clippy::large_enum_variant`).
    Built(Box<sc_search::NameIndex>),
    Cancelled,
}

/// Same crawl as [`build_name_index`], additionally reporting progress
/// (called at the same batch boundary [`CrawlThrottle`] paces on) and
/// polling `should_cancel` at that same boundary — the admin-triggered
/// `/api/admin/index/build` path (`FEATURES.md` #116) needs both, and must
/// go through the identical `CrawlThrottle` pacing `build_name_index` always
/// used rather than a separate, unthrottled walk.
pub(crate) fn build_name_index_with_progress(
    core: &sc_core::Core,
    share: sc_vfs::ShareId,
    on_progress: &dyn Fn(usize),
    should_cancel: &dyn Fn() -> bool,
) -> anyhow::Result<CrawlOutcome> {
    let root = core
        .share(share)
        .ok_or_else(|| anyhow::anyhow!("no such share"))?;
    let dir = name_index_dir(core, share).ok_or_else(|| anyhow::anyhow!("no such share"))?;
    let class = crate::storage_class::detect(&root);
    let throttle = CrawlThrottle::for_class(class);
    let started = std::time::Instant::now();
    let mut entries = Vec::new();
    let mut ctx = CrawlCtx {
        throttle: &throttle,
        visited: 0,
        on_progress,
        should_cancel,
    };
    let completed =
        collect_paths_for_index(&root, &SafePath::root(), share, &mut entries, &mut ctx)?;
    let visited = ctx.visited;
    let elapsed = started.elapsed();
    if !completed {
        tracing::info!(share = share.get(), visited, "name index crawl cancelled");
        return Ok(CrawlOutcome::Cancelled);
    }
    tracing::info!(
        share = share.get(),
        storage_class = ?class,
        entries = entries.len(),
        elapsed_ms = elapsed.as_millis() as u64,
        paced_sleep_ms = ((visited / throttle.batch_entries) as u128 * throttle.sleep.as_millis()) as u64,
        "name index crawl finished"
    );
    sc_search::IndexBuilder::new()
        .build(&dir, entries)
        .map(|idx| CrawlOutcome::Built(Box::new(idx)))
}

/// Crawl pacing for [`build_name_index`]'s walk (`FEATURES.md` #124).
///
/// Chosen over `ioprio_set` ("ionice"): idle/best-effort I/O priority
/// classes only change scheduling under the CFQ/BFQ I/O elevators. The
/// virtio-backed block devices this deployment's Linux VM guest runs on
/// (`LINUX-VM-RUNTIME` note: prod and dev both run inside it) commonly use
/// `mq-deadline` or `none` instead, where priority classes are not
/// implemented at all — the syscall would succeed and change nothing,
/// which is worse than not trying: an operator would believe the crawl was
/// throttled when it wasn't. A sleep inserted between batches of
/// `read_dir`/`join` calls slows this process's own filesystem-call rate
/// regardless of which I/O scheduler the host picked, so its effect is
/// guaranteed rather than scheduler-dependent, and it is directly
/// measurable (`crawl_throttle_actually_paces`, in the test module below).
#[derive(Clone, Copy, Debug)]
struct CrawlThrottle {
    /// Sleep once every this many visited entries.
    batch_entries: usize,
    /// How long to sleep at each batch boundary.
    sleep: std::time::Duration,
}

impl CrawlThrottle {
    /// Paced harder on spinning disks and network mounts — the two classes
    /// where a crawl's continuous `read_dir`/`stat` traffic can actually
    /// starve a co-accessed Jellyfin/Samba reader (seek contention on a
    /// platter; shared bandwidth on the wire). Flash has enough IOPS
    /// headroom that a short pause between larger batches is enough to
    /// leave room for others without meaningfully slowing the crawl itself.
    fn for_class(class: sc_http::search_limits::StorageClass) -> Self {
        use sc_http::search_limits::StorageClass::*;
        use std::time::Duration;
        match class {
            Rotational => Self {
                batch_entries: 256,
                sleep: Duration::from_millis(15),
            },
            Network => Self {
                batch_entries: 256,
                sleep: Duration::from_millis(10),
            },
            SataSsd => Self {
                batch_entries: 1024,
                sleep: Duration::from_millis(2),
            },
            Nvme => Self {
                batch_entries: 4096,
                sleep: Duration::from_millis(1),
            },
        }
    }
}

/// "our own writes": append/tombstone the paths this
/// process itself just created/removed, so a share's name index (if it has
/// one) never has to wait for a rescan to see a write this same server made.
/// A no-op for any share with no index — `open_existing_name_index` checks
/// for `meta` first, never planting one (see `open_name_index`'s doc), which
/// is what keeps "both indexes off by default" true for this path too.
///
/// Best-effort: the file operation this runs after has *already* succeeded,
/// so an index-write failure here is logged and swallowed rather than
/// surfaced — the index is a cache (§4.2 "the index is a cache"), never load-bearing
/// for correctness (`consult_name_indexes` re-`stat`s every hit it gets from
/// one).
fn note_index_change(
    core: &sc_core::Core,
    added: &[(sc_vfs::ShareId, sc_vfs::SafePath)],
    removed: &[(sc_vfs::ShareId, sc_vfs::SafePath)],
) {
    if added.is_empty() && removed.is_empty() {
        return;
    }
    let mut shares: HashSet<sc_vfs::ShareId> = HashSet::new();
    shares.extend(added.iter().map(|(s, _)| *s));
    shares.extend(removed.iter().map(|(s, _)| *s));

    for share in shares {
        let Some(idx) = open_existing_name_index(core, share) else {
            continue;
        };
        let add_entries: Vec<(sc_vfs::ShareId, String)> = added
            .iter()
            .filter(|(s, _)| *s == share)
            .map(|(s, p)| (*s, p.to_display_string()))
            .collect();
        let rm_entries: Vec<(sc_vfs::ShareId, String)> = removed
            .iter()
            .filter(|(s, _)| *s == share)
            .map(|(s, p)| (*s, p.to_display_string()))
            .collect();
        if !add_entries.is_empty() {
            if let Err(e) = idx.append(&add_entries) {
                tracing::warn!(error = %e, share = share.get(), "name index append failed for a self-write; T2 still finds the file until the index is rebuilt");
            }
        }
        if !rm_entries.is_empty() {
            if let Err(e) = idx.tombstone(&rm_entries) {
                tracing::warn!(error = %e, share = share.get(), "name index tombstone failed for a self-write");
            }
        }
    }
}

/// "watcher event": reconcile one directory's entries
/// in `share`'s name index against what is really there, for a directory an
/// `sc_watch::InvalEvent` reported dirty for a reason this process did not
/// itself cause (`note_index_change` already covers our own writes).
///
/// Called from `app.rs::start_watcher_and_ws_hub`'s forwarding thread, once
/// per `InvalEvent` (`dir` parsed from `InvalEvent::path`).
///
/// A no-op wherever `share` has no index (checked the same way
/// `note_index_change` does), and — same reasoning as
/// `sc_search::NameIndex::children_of`'s doc comment — best-effort in a way
/// that only ever fails toward staying stale, never toward a wrong answer:
/// a diff that misses a real deletion just leaves a dead entry in the index
/// until the next merge/rebuild (`consult_name_indexes` re-`stat`s every hit,
/// so a dead entry is filtered at query time, never resurrected).
pub fn reconcile_watch_event(core: &sc_core::Core, share: sc_vfs::ShareId, dir: &SafePath) {
    /// Generous cap on how many already-indexed entries `children_of` is
    /// asked to enumerate for a single directory — large enough for any
    /// directory an admin would reasonably keep flat, small enough that a
    /// pathological one cannot make a single watch event unbounded work.
    const MAX_RECONCILE_CHILDREN: usize = 20_000;

    let Some(root) = core.share(share) else {
        return;
    };
    let Some(idx) = open_existing_name_index(core, share) else {
        return; // no index for this share: nothing to keep in sync
    };
    let Ok(entries) = root.read_dir(dir) else {
        return;
    };

    let max_depth = root.policy().max_depth;
    let dir_display = dir.to_display_string();
    let actual: HashSet<String> = entries
        .iter()
        .filter_map(|e| dir.join(&e.name, max_depth).ok())
        .map(|p| p.to_display_string())
        .collect();
    let known: HashSet<String> = idx
        .children_of(share, &dir_display, MAX_RECONCILE_CHILDREN)
        .into_iter()
        .collect();

    let to_append: Vec<(sc_vfs::ShareId, String)> = actual
        .difference(&known)
        .map(|p| (share, p.clone()))
        .collect();
    let to_tomb: Vec<(sc_vfs::ShareId, String)> = known
        .difference(&actual)
        .map(|p| (share, p.clone()))
        .collect();

    if !to_append.is_empty() {
        if let Err(e) = idx.append(&to_append) {
            tracing::warn!(error = %e, share = share.get(), "watch-driven name index append failed");
        }
    }
    if !to_tomb.is_empty() {
        if let Err(e) = idx.tombstone(&to_tomb) {
            tracing::warn!(error = %e, share = share.get(), "watch-driven name index tombstone failed");
        }
    }
}

/// Merges the T2 walk's completeness with the (optional) T3 side's own
/// truncation: the walker's reason reflects a real resource boundary
/// (deadline, entry cap, depth cap) so it wins whenever it fired; the
/// index's cap is only worth reporting when the walker itself has nothing to
/// say.
fn merge_completeness(
    walker: sc_search::Completeness,
    index_side: Option<sc_search::Completeness>,
) -> sc_search::Completeness {
    match (walker.is_full(), index_side) {
        (true, Some(t)) => t,
        _ => walker,
    }
}

/// Full recursive share walk collecting every path for `build_name_index`
/// (§4.2's initial crawl). Unlike `walk_for_estimate` below, this does not
/// sample or bound by entry count — an index that silently skipped part of
/// the tree would make "it's a cache; delete it and search still works"
/// (§4.2) a lie the day a real file is missing from it. `root.read_dir`
/// already hides `.sctrash`/`.scpart-`/`.scmeta`/`.scindex`
/// (`sc_vfs::RESERVED_PREFIXES`), so this never indexes its own index files.
/// A read failure partway aborts the whole crawl (via `?`) rather than
/// silently building a partial index and calling it done.
/// Walks `path` down, appending every entry to `out`. Returns `Ok(true)` if
/// the walk ran to completion, `Ok(false)` if `should_cancel` fired partway
/// (an admin-triggered build's cooperative cancellation, `FEATURES.md`
/// #116) — an in-progress `Vec` from a cancelled walk is discarded by the
/// caller rather than fed to `IndexBuilder`, so a cancelled build never
/// produces a half-crawled index.
///
/// The pacing/progress state travels as one `CrawlCtx` rather than as four
/// more positional parameters: this function is self-recursive, and a
/// recursive call reconstructing an eight-argument list by hand is where a
/// `&mut usize` gets swapped for the wrong one.
struct CrawlCtx<'a> {
    throttle: &'a CrawlThrottle,
    visited: usize,
    on_progress: &'a dyn Fn(usize),
    should_cancel: &'a dyn Fn() -> bool,
}

fn collect_paths_for_index(
    root: &sc_vfs::ShareRoot,
    path: &SafePath,
    share: sc_vfs::ShareId,
    out: &mut Vec<(sc_vfs::ShareId, String)>,
    ctx: &mut CrawlCtx<'_>,
) -> Result<bool, sc_vfs::VfsError> {
    let max_depth = root.policy().max_depth;
    for e in root.read_dir(path)? {
        let Ok(p) = path.join(&e.name, max_depth) else {
            continue;
        };
        out.push((share, p.to_display_string()));
        ctx.visited += 1;
        // Pace ourselves once per batch rather than once per entry — a
        // per-entry sleep would dominate wall time on a small share and a
        // per-directory sleep would let one huge directory run unthrottled,
        // so the boundary is a fixed entry count instead. Progress and
        // cancellation are checked at the same boundary: reporting/polling
        // more often than that would just be spending the pacing budget on
        // bookkeeping instead of on the yield itself.
        if ctx.visited.is_multiple_of(ctx.throttle.batch_entries) {
            tracing::debug!(
                share = share.get(),
                visited = ctx.visited,
                "index crawl pacing: yielding to avoid starving other disk consumers"
            );
            (ctx.on_progress)(ctx.visited);
            if (ctx.should_cancel)() {
                return Ok(false);
            }
            std::thread::sleep(ctx.throttle.sleep);
        }
        if e.kind == sc_vfs::Kind::Dir && !collect_paths_for_index(root, &p, share, out, ctx)? {
            return Ok(false);
        }
    }
    Ok(true)
}

/// Extension-group filter (`kind=image` etc., `DESIGN-API.md` §2/§4.3) —
/// never opens a file to decide, matching `sc-search::Matcher::exts`'s own
/// contract.
fn kind_extensions(kind: &str) -> &'static [&'static str] {
    match kind {
        "image" => &[
            "jpg", "jpeg", "png", "gif", "webp", "bmp", "tif", "tiff", "avif", "heic",
        ],
        "video" => &["mp4", "mkv", "mov", "avi", "webm", "m4v", "wmv"],
        "audio" => &["mp3", "flac", "wav", "ogg", "m4a", "aac", "opus"],
        "document" => &["pdf", "doc", "docx", "odt", "txt", "md", "rtf"],
        "archive" => &["zip", "tar", "gz", "7z", "rar", "xz", "zst"],
        _ => &[],
    }
}

fn to_search_hit(h: sc_search::Hit) -> sc_http::search_api::SearchHit {
    sc_http::search_api::SearchHit {
        path: h.path,
        name: h.name.to_string(),
        is_dir: h.is_dir,
        size: h.size,
        mtime_ns: h.mtime_ns,
        score: h.score,
    }
}

fn to_search_completeness(c: sc_search::Completeness) -> sc_http::search_api::SearchCompleteness {
    match c {
        sc_search::Completeness::Full => sc_http::search_api::SearchCompleteness::Full,
        sc_search::Completeness::Truncated {
            reason,
            seen,
            elapsed,
        } => sc_http::search_api::SearchCompleteness::Truncated {
            reason: format!("{reason:?}"),
            seen,
            elapsed_ms: elapsed.as_millis() as u64,
        },
    }
}

impl sc_http::search_api::SearchApi for SearchBridge {
    fn search_tier(
        &self,
        user: UserId,
        q: &sc_http::search_api::SearchQuery,
    ) -> sc_http::search_limits::SearchTier {
        let roots = self.roots_for(user, q.scope.as_deref());
        self.effective_tier(&roots)
    }

    fn search(
        &self,
        user: UserId,
        q: &sc_http::search_api::SearchQuery,
    ) -> Result<sc_http::search_api::SearchOutcome, hapi::CoreError> {
        let roots = self.roots_for(user, q.scope.as_deref());
        let tier = self.effective_tier(&roots);
        let rotational = tier == sc_http::search_limits::SearchTier::Slow;
        let matcher = Self::matcher_for(q);
        // Storage-tier walk deadline, config-reachable via `[search]` — see
        // `SearchBridge::limits` doc comment for why this is the same
        // object the HTTP layer's concurrency cap reads.
        let budget = sc_search::WalkBudget::new(self.limits.walk_deadline(tier));

        // T3 first (§4): every root with a usable index is answered from it;
        // everything else — including every root, today, since nothing
        // builds one yet — falls through to the unchanged T2 walk below.
        let (index_hits, walker_roots, index_completeness) =
            self.consult_name_indexes(user, &roots, q, &matcher, &budget);

        let walker = sc_search::Walker::new(sc_search::Walker::decide_threads(rotational, None))
            .with_rotational(rotational);
        let (tx, rx) = crossbeam_channel::unbounded::<sc_search::Hit>();
        let acl =
            |share: sc_vfs::ShareId, path: &sc_vfs::SafePath| self.core.can_read(user, share, path);
        let walker_completeness = walker.walk(&walker_roots, &matcher, &acl, &budget, &tx);
        drop(tx);

        let mut hits: Vec<sc_http::search_api::SearchHit> = index_hits
            .into_iter()
            .map(to_search_hit)
            .chain(rx.try_iter().map(to_search_hit))
            .collect();
        // Ranking is meaningful once the full (or
        // budget-exhausted) result set is in hand, which is exactly the case
        // for the non-streaming endpoint — the streaming one leaves this to
        // the client on purpose (§3.5).
        hits.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });

        let completeness = merge_completeness(walker_completeness, index_completeness);
        Ok(sc_http::search_api::SearchOutcome {
            hits,
            completeness: to_search_completeness(completeness),
        })
    }

    fn search_stream(
        &self,
        user: UserId,
        q: &sc_http::search_api::SearchQuery,
        on_hit: &mut dyn FnMut(sc_http::search_api::SearchHit) -> bool,
    ) -> sc_http::search_api::SearchCompleteness {
        let roots = self.roots_for(user, q.scope.as_deref());
        let tier = self.effective_tier(&roots);
        let rotational = tier == sc_http::search_limits::SearchTier::Slow;
        let matcher = Self::matcher_for(q);
        let budget = sc_search::WalkBudget::new(self.limits.walk_deadline(tier));

        // Same T3-first split as the batch path. The index side is not a
        // walk, so it is drained synchronously, before the walker even
        // starts — that also makes the "client disconnected" semantics below
        // line up with the walker's own (§3.5's `emit` never sets a
        // truncation reason for a closed receiver, only for a real budget
        // hit — mirrored here by reporting the index's own cap, not `Full`,
        // when disconnect happens after the index side already filled it).
        let (index_hits, walker_roots, index_completeness) =
            self.consult_name_indexes(user, &roots, q, &matcher, &budget);

        let mut client_stopped = false;
        for hit in index_hits {
            if !on_hit(to_search_hit(hit)) {
                client_stopped = true;
                break;
            }
        }
        if client_stopped {
            return to_search_completeness(
                index_completeness.unwrap_or(sc_search::Completeness::Full),
            );
        }

        let walker = sc_search::Walker::new(sc_search::Walker::decide_threads(rotational, None))
            .with_rotational(rotational);

        // A bounded channel here would let a slow SSE client throttle the
        // walker's worker threads via backpressure; unbounded matches T2's
        // own "never block a worker on I/O it doesn't own" stance and keeps
        // this consistent with the batch path above.
        let (tx, rx) = crossbeam_channel::unbounded::<sc_search::Hit>();
        let acl =
            |share: sc_vfs::ShareId, path: &sc_vfs::SafePath| self.core.can_read(user, share, path);

        // The walk runs on a spawned scoped thread and `on_hit` is drained
        // *here*, on the calling thread — deliberately the other way around
        // from "spawn the consumer". `on_hit` is an arbitrary caller-supplied
        // `&mut dyn FnMut` with no `Send` bound (it closes over an SSE
        // channel sender in `sc-http`, which has no reason to be `Sync`), so
        // it can never cross into a spawned thread; everything that *is*
        // `Send`/`Sync` (the walker, the roots, the ACL closure, the
        // channel) does the crossing instead.
        let walker_completeness = std::thread::scope(|scope| {
            // `move` is load-bearing, not stylistic: `tx` must be *owned* by
            // this closure so it drops (closing the channel) the instant the
            // walk finishes, which is what lets `rx.recv()` below return
            // `Err` and the drain loop terminate instead of blocking
            // forever on a sender that nothing would otherwise drop.
            let walk =
                scope.spawn(move || walker.walk(&walker_roots, &matcher, &acl, &budget, &tx));
            while let Ok(hit) = rx.recv() {
                if !on_hit(to_search_hit(hit)) {
                    break;
                }
            }
            walk.join().unwrap_or(sc_search::Completeness::Full)
        });

        to_search_completeness(merge_completeness(walker_completeness, index_completeness))
    }
}

#[cfg(test)]
mod upload_err_tests {
    //! `UploadBridge::upload_err` used to map every `sc_upload::UploadError`
    //! to `hapi::CoreError::Internal`, i.e. HTTP `500`, no matter what went
    //! wrong. A TUS client cannot tell "resend the same bytes" (`409`) apart
    //! from "give up and start over" (`410`) apart from "you are done,
    //! nothing to retry" if every one of them looks like a server fault. This
    //! proves the distinctions `docs/DESIGN-UPLOAD.md` §2.2 calls for
    //! actually reach the wire.
    use super::*;
    use axum::http::StatusCode;
    use sc_http::error::AppError;

    fn status_of(e: sc_upload::UploadError) -> StatusCode {
        let mapped = UploadBridge::upload_err(e);
        let app: AppError = mapped.into();
        app.status_override.unwrap_or_else(|| app.code.status())
    }

    #[test]
    fn upload_errors_map_to_the_status_docs_design_upload_specifies() {
        use sc_upload::UploadError as U;
        assert_eq!(
            status_of(U::Conflict {
                expected: 10,
                got: 5
            }),
            StatusCode::CONFLICT,
            "§2.2: offset conflict is 409"
        );
        assert_eq!(
            status_of(U::Gone),
            StatusCode::GONE,
            "§2.2: expired-but-not-yet-GC'd session is 410"
        );
        assert_eq!(
            status_of(U::NotFound),
            StatusCode::NOT_FOUND,
            "§2.2: no such session is 404"
        );
        assert_eq!(
            status_of(U::PreconditionFailed),
            StatusCode::PRECONDITION_FAILED,
            "§2.2: ifMatch mismatch at finalize is 412"
        );
        assert_eq!(
            status_of(U::ResourceExhausted("max_sessions_per_user".into())),
            StatusCode::INSUFFICIENT_STORAGE,
            "§2.2: resource/quota exhaustion is 507"
        );
        assert_eq!(
            status_of(U::Unprocessable),
            StatusCode::UNPROCESSABLE_ENTITY,
            "§2.2: offset+len > Upload-Length is 422"
        );
    }

    #[test]
    fn a_client_actionable_upload_error_never_collapses_to_a_bare_500() {
        use sc_upload::UploadError as U;
        for e in [
            U::Conflict {
                expected: 0,
                got: 1,
            },
            U::Gone,
            U::NotFound,
            U::PreconditionFailed,
            U::Unprocessable,
            U::ChunkTooSmall {
                min: 5 * 1024 * 1024,
            },
            U::ResourceExhausted("disk".into()),
        ] {
            let status = status_of(e);
            assert_ne!(
                status,
                StatusCode::INTERNAL_SERVER_ERROR,
                "a client-recoverable upload error must not look like a server fault"
            );
        }
    }
}

#[cfg(test)]
mod upload_bridge_tests {
    //! `UploadBridge::create_with_upload`/`patch_checked` are the engine
    //! side of the fix for "checksum and creation-with-upload are
    //! advertised but inert": before these existed, `UploadBridge` only
    //! implemented the plain `create`/`patch`, whose bodies never touched a
    //! request body beyond what `PATCH` itself carries, and never passed a
    //! checksum into `sc_upload::UploadEngine::patch` (it hardcoded `None`).
    //! `sc-http::routes` — owned elsewhere at the time of this change — still
    //! only calls the plain methods, so these two aren't reachable over HTTP
    //! yet; this proves the plumbing they need is real, not just declared.
    use super::*;
    use axum::http::StatusCode;
    use sc_acl::{AclEngine, Grant, Perms as AclPerms, Principal};
    use sc_http::error::AppError;
    use sc_http::upload_api::{TusChecksum, TusChecksumAlgo, UploadApi};
    use sc_meta::MetaStore;
    use sc_upload::{Checksum, ChecksumAlgo};
    use sc_vfs::SharePolicy;

    const USER: UserId = UserId::new(1);
    const TEST_SHARE: sc_vfs::ShareId = sc_vfs::ShareId::new(1);

    fn setup() -> (UploadBridge, tempfile::TempDir) {
        let (bridge, _acl, dir) = setup_with_acl(vec![Grant {
            id: 1,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::root(),
            allow: AclPerms::all(),
            deny: AclPerms::empty(),
            inherit: true,
            label: Some("root".into()),
        }]);
        (bridge, dir)
    }

    /// As `setup()`, but with caller-chosen grants and the live `AclEngine`
    /// handed back too — needed by tests that change grants after the
    /// `UploadBridge` (and the `Core` that owns its own `Arc<AclEngine>`
    /// clone) already exist, e.g. to simulate a grant being revoked
    /// mid-upload.
    fn setup_with_acl(grants: Vec<Grant>) -> (UploadBridge, Arc<AclEngine>, tempfile::TempDir) {
        let dir = tempfile::tempdir().unwrap();
        let data_dir = dir.path().join("data");
        std::fs::create_dir_all(&data_dir).unwrap();

        let meta = Arc::new(MetaStore::open_in_memory().unwrap());
        let acl = Arc::new(AclEngine::new());
        acl.replace_grants(grants);
        let core = Arc::new(sc_core::Core::new(meta, acl.clone()));
        core.register_share(sc_core::ShareDef {
            id: TEST_SHARE,
            name: "root".into(),
            host_path: data_dir,
            policy: SharePolicy::default(),
            shared_externally: false,
        })
        .unwrap();

        let engine = Arc::new(
            sc_upload::UploadEngine::new(
                &dir.path().join("upload.db"),
                sc_upload::UploadConfig::default(),
            )
            .unwrap(),
        );
        (UploadBridge { engine, core }, acl, dir)
    }

    #[test]
    fn creation_with_upload_spools_the_body_instead_of_dropping_it() {
        let (bridge, dir) = setup();
        let body = b"hello from creation-with-upload";
        let created = bridge
            .create_with_upload(USER, "/root/a.txt", Some(body.len() as u64), false, body)
            .expect("creation-with-upload should succeed");
        assert_eq!(
            created.offset,
            body.len() as u64,
            "the whole POST body must be reported as received, not silently dropped"
        );

        // A body covering the whole declared length finalizes immediately
        // (same as `patch` reaching the declared length) — finalize deletes
        // the session row, so the session is gone rather than "complete".
        let err = bridge.status(USER, &created.id).unwrap_err();
        assert!(
            matches!(err, hapi::CoreError::NotFound),
            "a finalized session should no longer be found: {err:?}"
        );
        assert_eq!(
            std::fs::read(dir.path().join("data/a.txt")).unwrap(),
            body,
            "the spooled body must have been renamed into place at finalize"
        );
    }

    #[test]
    fn creation_with_upload_with_no_body_behaves_like_plain_create() {
        let (bridge, _dir) = setup();
        let created = bridge
            .create_with_upload(USER, "/root/empty.txt", Some(5), false, b"")
            .unwrap();
        assert_eq!(created.offset, 0);
        assert!(!bridge.status(USER, &created.id).unwrap().complete);
    }

    #[test]
    fn a_checksum_mismatch_is_rejected_and_does_not_advance_the_offset() {
        let (bridge, _dir) = setup();
        let id = bridge.create(USER, "/root/b.txt", Some(5), false).unwrap();
        let data = b"abcde";

        let wrong = TusChecksum {
            algo: TusChecksumAlgo::Crc32c,
            digest: 0xdead_beefu32.to_be_bytes().to_vec(),
        };
        bridge
            .patch_checked(USER, &id, 0, data, Some(wrong))
            .expect_err("a checksum that does not match the body must be rejected");
        assert_eq!(
            bridge.status(USER, &id).unwrap().offset,
            0,
            "a rejected chunk must not be counted as received"
        );
    }

    #[test]
    fn a_matching_checksum_is_accepted_and_completes_the_upload() {
        let (bridge, _dir) = setup();
        let id = bridge.create(USER, "/root/c.txt", Some(5), false).unwrap();
        let data = b"abcde";

        let digest = match Checksum::compute(ChecksumAlgo::Crc32c, data) {
            Checksum::Crc32c(v) => v.to_be_bytes().to_vec(),
            _ => unreachable!(),
        };
        let correct = TusChecksum {
            algo: TusChecksumAlgo::Crc32c,
            digest,
        };
        let new_offset = bridge
            .patch_checked(USER, &id, 0, data, Some(correct))
            .expect("a correct checksum must be accepted");
        assert_eq!(new_offset, 5);
        // The declared length was reached, so `patch_impl` finalized and
        // deleted the session — same reasoning as the creation-with-upload
        // test above.
        assert!(matches!(
            bridge.status(USER, &id).unwrap_err(),
            hapi::CoreError::NotFound
        ));
    }

    #[test]
    fn patch_checked_with_no_checksum_behaves_like_plain_patch() {
        let (bridge, _dir) = setup();
        let id = bridge.create(USER, "/root/d.txt", Some(5), false).unwrap();
        let new_offset = bridge.patch_checked(USER, &id, 0, b"abcde", None).unwrap();
        assert_eq!(new_offset, 5);
    }

    /// `DELETE /api/uploads/{id}` (cancel) must make the session look gone,
    /// not merely stop it from accepting more bytes. Before `status()`
    /// mapped `Aborted`/`Expired` to an error, `HEAD` on a just-cancelled
    /// session kept answering `200` with its last-known offset — a client
    /// that resumes from a stale IndexedDB record (`web/src/lib/upload/
    /// worker.ts`'s `addFile`) would read that as "still resumable" and try
    /// to continue an upload the user explicitly cancelled.
    #[test]
    fn status_after_terminate_reports_gone_not_the_stale_offset() {
        let (bridge, _dir) = setup();
        let id = bridge.create(USER, "/root/e.txt", Some(10), false).unwrap();
        bridge.patch_checked(USER, &id, 0, b"abcde", None).unwrap();
        assert_eq!(
            bridge.status(USER, &id).unwrap().offset,
            5,
            "sanity: offset is tracked before cancel"
        );

        bridge.terminate(USER, &id).unwrap();

        let err = bridge.status(USER, &id).unwrap_err();
        let mapped: AppError = err.into();
        assert_eq!(
            mapped.status_override.unwrap_or_else(|| mapped.code.status()),
            StatusCode::GONE,
            "a cancelled session must answer like an expired one (410), not like it is still receiving"
        );

        // Writes were already refused before this fix (`ensure_receiving`) —
        // this asserts `status()` now agrees with `patch()` instead of the
        // two halves of the same session disagreeing about whether it's alive.
        assert!(bridge.patch_checked(USER, &id, 5, b"fghij", None).is_err());
    }

    /// `create_session` used to resolve the destination with `Perms::READ`
    /// (`Core::resolve`), so a grant that denies WRITE/CREATE but still
    /// allows READ passed straight through — the reported defect: a subpath
    /// grant with `deny: ["write","create"]` refused ordinary `fs` writes
    /// but let `POST /api/uploads` open a session anyway. Fails on pre-fix
    /// code: both `create()` calls below returned `Ok`.
    #[test]
    fn a_read_only_grant_is_refused_at_session_creation_without_leaking_path_existence() {
        let (bridge, _acl, dir) = setup_with_acl(vec![Grant {
            id: 1,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::root(),
            allow: AclPerms::READ,
            deny: AclPerms::WRITE | AclPerms::CREATE,
            inherit: true,
            label: Some("root".into()),
        }]);
        std::fs::write(dir.path().join("data/exists.txt"), b"pre-existing").unwrap();

        let err_existing = bridge
            .create(USER, "/root/exists.txt", Some(5), false)
            .unwrap_err();
        let err_missing = bridge
            .create(USER, "/root/missing.txt", Some(5), false)
            .unwrap_err();
        assert!(
            matches!(err_existing, hapi::CoreError::Denied { .. }),
            "read-only grant must refuse upload creation: {err_existing:?}"
        );
        assert!(
            matches!(err_missing, hapi::CoreError::Denied { .. }),
            "same refusal whether or not the destination already exists on disk"
        );

        // A vpath under no root the user has at all answers NotFound, not
        // Denied (`DESIGN-AUTH.md` §12: "a share the caller has no grant on
        // answers 404, not 403") — proving the Denied above comes from an
        // actual WRITE check, not a blanket catch-all that would also hide
        // this distinction.
        let err_unknown = bridge
            .create(USER, "/no-such-share/x.txt", Some(5), false)
            .unwrap_err();
        assert!(
            matches!(err_unknown, hapi::CoreError::NotFound),
            "unknown share must be 404, distinct from a denied one: {err_unknown:?}"
        );
    }

    /// A read-only-grant user must not be able to write via someone else's
    /// session either. `sc_upload::UploadEngine` already refuses cross-owner
    /// access at every stage via its own `row.user != user` check
    /// (`sc-upload/src/engine.rs`), so — unlike the other three tests in
    /// this group — this one already passes on pre-fix code; it is included
    /// to document that the create-time WRITE fix neither depends on nor
    /// weakens that separate ownership boundary for the "stole/guessed a
    /// session id" angle the task also asked to cover.
    #[test]
    fn a_stolen_session_id_is_refused_at_patch_regardless_of_the_thiefs_own_grants() {
        const OWNER: UserId = UserId::new(1);
        const ATTACKER: UserId = UserId::new(2);
        let (bridge, _acl, _dir) = setup_with_acl(vec![
            Grant {
                id: 1,
                principal: Principal::User(OWNER),
                share: TEST_SHARE,
                subpath: SafePath::root(),
                allow: AclPerms::all(),
                deny: AclPerms::empty(),
                inherit: true,
                label: Some("root".into()),
            },
            Grant {
                id: 2,
                principal: Principal::User(ATTACKER),
                share: TEST_SHARE,
                subpath: SafePath::root(),
                allow: AclPerms::READ,
                deny: AclPerms::WRITE | AclPerms::CREATE,
                inherit: true,
                label: Some("root".into()),
            },
        ]);
        let id = bridge
            .create(OWNER, "/root/owner.txt", Some(5), false)
            .unwrap();

        let err = bridge
            .patch_checked(ATTACKER, &id, 0, b"abcde", None)
            .unwrap_err();
        let mapped: AppError = err.into();
        assert_eq!(
            mapped.status_override.unwrap_or_else(|| mapped.code.status()),
            StatusCode::NOT_FOUND,
            "a session id belonging to another user must look like it does not exist, not merely forbidden"
        );
    }

    /// A grant with `READ | WRITE` — an ordinary "can browse and edit"
    /// grant, not `Perms::all()` like `setup()`'s — must be enough to
    /// upload, whole in one `PATCH` and split across several, with the
    /// finished bytes matching exactly. `READ` has to be present:
    /// `sc_acl::AclEngine::roots` (`resolve_want`'s first step, matching a
    /// vpath label to a share) only ever projects grants that include
    /// `READ` — a `WRITE`-only grant is not merely denied by this fix, it
    /// was already unresolvable as a vpath before it, so it is not a useful
    /// shape for this test. This is therefore a non-regression/happy-path
    /// test, not a defect reproduction: pre-fix code (which only checked
    /// `READ`) would have let this same grant through too, just for the
    /// wrong reason — see the read-only-grant test above for the case that
    /// actually distinguishes pre- from post-fix.
    #[test]
    fn a_write_grant_uploads_end_to_end_single_and_multi_chunk() {
        let (bridge, _acl, dir) = setup_with_acl(vec![Grant {
            id: 1,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::root(),
            allow: AclPerms::READ | AclPerms::WRITE,
            deny: AclPerms::empty(),
            inherit: true,
            label: Some("root".into()),
        }]);

        let body = b"single chunk payload";
        let id = bridge
            .create(USER, "/root/single.txt", Some(body.len() as u64), false)
            .unwrap();
        let off = bridge.patch_checked(USER, &id, 0, body, None).unwrap();
        assert_eq!(off, body.len() as u64);
        assert_eq!(
            std::fs::read(dir.path().join("data/single.txt")).unwrap(),
            body
        );

        let part_a = b"first half:";
        let part_b = b":second half";
        let mut whole = Vec::new();
        whole.extend_from_slice(part_a);
        whole.extend_from_slice(part_b);
        let id2 = bridge
            .create(USER, "/root/multi.txt", Some(whole.len() as u64), false)
            .unwrap();
        let off_a = bridge.patch_checked(USER, &id2, 0, part_a, None).unwrap();
        assert_eq!(off_a, part_a.len() as u64);
        let off_b = bridge
            .patch_checked(USER, &id2, off_a, part_b, None)
            .unwrap();
        assert_eq!(off_b, whole.len() as u64);
        assert_eq!(
            std::fs::read(dir.path().join("data/multi.txt")).unwrap(),
            whole
        );
    }

    /// Pins the decision documented on `patch_impl`: neither `PATCH` nor
    /// `finalize` re-checks the ACL, so a grant revoked after a session is
    /// created does not stop that session from completing — a deliberately
    /// accepted gap, not an oversight (see `patch_impl`'s doc comment for
    /// why both a per-chunk and a finalize-time re-check were rejected).
    /// This test does not fail on pre-fix code — nothing about mid-upload
    /// re-checking changed — it exists to pin the current, intended
    /// behavior so a future edit to `patch_impl` cannot silently start (or
    /// stop) enforcing this without a test noticing.
    #[test]
    fn revoking_the_grant_mid_upload_does_not_stop_an_in_flight_session() {
        let (bridge, acl, dir) = setup_with_acl(vec![Grant {
            id: 1,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::root(),
            allow: AclPerms::all(),
            deny: AclPerms::empty(),
            inherit: true,
            label: Some("root".into()),
        }]);
        let id = bridge
            .create(USER, "/root/revoked.txt", Some(10), false)
            .unwrap();
        let off = bridge.patch_checked(USER, &id, 0, b"abcde", None).unwrap();
        assert_eq!(off, 5);

        // Revoke: same shape as the real-world repro (`deny: ["write", "create"]`).
        acl.replace_grants(vec![Grant {
            id: 1,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::root(),
            allow: AclPerms::READ,
            deny: AclPerms::WRITE | AclPerms::CREATE,
            inherit: true,
            label: Some("root".into()),
        }]);

        // The in-flight session still finishes — the accepted gap.
        let off2 = bridge.patch_checked(USER, &id, 5, b"fghij", None).unwrap();
        assert_eq!(off2, 10);
        assert_eq!(
            std::fs::read(dir.path().join("data/revoked.txt")).unwrap(),
            b"abcdefghij"
        );
    }
}

#[cfg(test)]
mod search_bridge_tests {
    //! `SearchBridge` (`sc_http::search_api::SearchApi`) had **zero** test
    //! coverage anywhere in the workspace before this module — a `grep -rl
    //! SearchBridge` across the repo turns up only `app.rs` (construction)
    //! and this file. These tests are therefore both a T2 regression net
    //! (the first one below is the "unchanged behaviour" baseline the audit
    //! required) and the proof for the T3 wiring fix: `sc_search::NameIndex`
    //! (T3) was a fully-implemented, fully-tested library with no caller in
    //! this binary — `consult_name_indexes`/`build_name_index` above are
    //! what closes that gap, and `search_uses_the_name_index_...` is the
    //! test that would fail without them (before this change, `search` only
    //! ever built a `Walker` and never touched `sc_search::NameIndex`).
    use super::*;
    use sc_acl::{AclEngine, Grant, Perms as AclPerms, Principal};
    use sc_http::search_api::{SearchApi, SearchQuery};
    use sc_http::search_limits::{SearchConcurrency, SearchLimitsConfig};
    use sc_meta::MetaStore;
    use sc_vfs::SharePolicy;

    const USER: UserId = UserId::new(1);
    const TEST_SHARE: sc_vfs::ShareId = sc_vfs::ShareId::new(1);

    fn root_grant() -> Grant {
        Grant {
            id: 1,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::root(),
            allow: AclPerms::READ,
            deny: AclPerms::empty(),
            inherit: true,
            label: Some("root".into()),
        }
    }

    /// A grant that denies `READ` (and so, blocking descent, `LIST`) at
    /// `path` and everything beneath it — unless a deeper grant re-allows it
    /// (`allow_grant`, `deeper ALLOW beats shallower DENY`, `sc-acl`'s own
    /// module doc). Deliberately does **not** set `allow.contains(READ)`, so
    /// (unlike `root_grant`) it never becomes its own entry in `Core::roots`
    /// — see `sc_acl::AclEngine::roots`'s "one entry per READ-granted rule".
    fn deny_grant(id: u32, path: &str) -> Grant {
        Grant {
            id,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::parse(path, u16::MAX).unwrap(),
            allow: AclPerms::empty(),
            deny: AclPerms::READ,
            inherit: true,
            label: None,
        }
    }

    /// Re-allows `READ` at an exact deeper path underneath a `deny_grant`.
    fn allow_grant(id: u32, path: &str) -> Grant {
        Grant {
            id,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::parse(path, u16::MAX).unwrap(),
            allow: AclPerms::READ,
            deny: AclPerms::empty(),
            inherit: true,
            label: None,
        }
    }

    /// Same as `allow_grant`, but also re-allows `WRITE`/`CREATE` — for tests
    /// that write through `CoreBridge` at a path denied above (`root_grant`
    /// alone only ever grants `READ`).
    fn allow_grant_rw(id: u32, path: &str) -> Grant {
        Grant {
            id,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::parse(path, u16::MAX).unwrap(),
            allow: AclPerms::READ | AclPerms::WRITE | AclPerms::CREATE,
            deny: AclPerms::empty(),
            inherit: true,
            label: None,
        }
    }

    /// Full access at the share root, for tests that write through
    /// `CoreBridge` (`root_grant` alone only ever grants `READ`, which is
    /// enough for every query-side test but not for `mkdir`/`write`/
    /// `delete`/`rename`).
    fn full_access_grant() -> Grant {
        Grant {
            id: 1,
            principal: Principal::User(USER),
            share: TEST_SHARE,
            subpath: SafePath::root(),
            allow: AclPerms::all(),
            deny: AclPerms::empty(),
            inherit: true,
            label: Some("root".into()),
        }
    }

    fn setup(grants: Vec<Grant>) -> (SearchBridge, tempfile::TempDir) {
        let dir = tempfile::tempdir().unwrap();
        let data_dir = dir.path().join("data");
        std::fs::create_dir_all(&data_dir).unwrap();

        let meta = Arc::new(MetaStore::open_in_memory().unwrap());
        let acl = Arc::new(AclEngine::new());
        acl.replace_grants(grants);
        let core = Arc::new(sc_core::Core::new(meta, acl));
        core.register_share(sc_core::ShareDef {
            id: TEST_SHARE,
            name: "root".into(),
            host_path: data_dir,
            policy: SharePolicy::default(),
            shared_externally: false,
        })
        .unwrap();

        let bridge = SearchBridge {
            core,
            storage_cache: crate::storage_class::StorageClassCache::default(),
            limits: Arc::new(SearchConcurrency::new(&SearchLimitsConfig::default())),
        };
        (bridge, dir)
    }

    /// Scoping every query to `/root` (rather than leaving `scope: None`)
    /// keeps each test to exactly the one root the test cares about — with
    /// an unscoped query, a grant like `allow_grant` (which necessarily also
    /// satisfies `Core::roots`'s "any `allow`-READ grant is its own root")
    /// would surface as a *second*, independent root pointing straight at
    /// the deep file, doubling up hits for reasons that have nothing to do
    /// with what each test is actually checking.
    fn q(text: &str) -> SearchQuery {
        SearchQuery {
            text: text.into(),
            scope: Some("/root".into()),
            ..Default::default()
        }
    }

    #[test]
    fn search_without_an_index_walks_the_filesystem_exactly_as_before() {
        // The baseline: with no `.scindex/` anywhere, `search` must behave
        // exactly as it did before this change (pure T2). This is the test
        // that protects "when it does not exist, behaviour is exactly what
        // it is today."
        let (bridge, dir) = setup(vec![root_grant()]);
        std::fs::write(dir.path().join("data/hello.txt"), b"hi").unwrap();

        let out = bridge.search(USER, &q("hello")).unwrap();
        assert_eq!(out.hits.len(), 1, "{:?}", out.hits);
        assert_eq!(out.hits[0].path, "hello.txt");
        assert!(!out.hits[0].is_dir);
        // A plain name query never forces a stat (
        // "zero statx calls for name matching") — `size`/`mtime_ns` are `None` here by
        // design, not a bug; this is what "exactly as before" means.
        assert_eq!(out.hits[0].size, None);
        assert_eq!(out.hits[0].mtime_ns, None);
        assert_eq!(
            out.completeness,
            sc_http::search_api::SearchCompleteness::Full
        );
    }

    #[test]
    fn search_uses_the_name_index_and_still_enforces_acl_per_hit() {
        // `secret/` denies READ (so T2 cannot even list it, let alone
        // descend into it), but `secret/target.txt` re-allows READ at that
        // exact deeper path — the classic "share a single file out of a
        // locked-down folder" grant shape.
        let (bridge, dir) = setup(vec![
            root_grant(),
            deny_grant(2, "secret"),
            allow_grant(3, "secret/target.txt"),
        ]);
        std::fs::create_dir_all(dir.path().join("data/secret")).unwrap();
        std::fs::write(dir.path().join("data/secret/target.txt"), b"treasure").unwrap();

        // Before an index exists, T2 is the only path there is, and it is
        // structurally unable to reach the file: it would have to list
        // `secret/` to find `target.txt`, and listing `secret/` is denied.
        let before = bridge.search(USER, &q("target")).unwrap();
        assert!(
            before.hits.is_empty(),
            "T2 must not find a file behind a denied directory: {:?}",
            before.hits
        );

        // Build the index — the crawler nothing in this binary calls yet
        // (see `SearchBridge::build_name_index`'s doc comment) — and run the
        // identical query over the identical ACL again. The only thing that
        // changed is that an index now exists, so a result appearing here is
        // proof `consult_name_indexes` is genuinely consulting it, not just
        // falling through to the same (still-blocked) T2 walk.
        bridge.build_name_index(TEST_SHARE).unwrap();
        let after = bridge.search(USER, &q("target")).unwrap();
        assert_eq!(
            after.hits.len(),
            1,
            "the index must surface the file once it exists: {:?}",
            after.hits
        );
        assert_eq!(after.hits[0].path, "secret/target.txt");
        assert!(!after.hits[0].is_dir);
        assert_eq!(after.hits[0].size, Some(8));
    }

    #[test]
    fn search_stream_also_uses_the_index() {
        let (bridge, dir) = setup(vec![
            root_grant(),
            deny_grant(2, "secret"),
            allow_grant(3, "secret/target.txt"),
        ]);
        std::fs::create_dir_all(dir.path().join("data/secret")).unwrap();
        std::fs::write(dir.path().join("data/secret/target.txt"), b"treasure").unwrap();
        bridge.build_name_index(TEST_SHARE).unwrap();

        let mut got = Vec::new();
        let completeness = bridge.search_stream(USER, &q("target"), &mut |hit| {
            got.push(hit);
            true
        });
        assert_eq!(got.len(), 1, "{got:?}");
        assert_eq!(got[0].path, "secret/target.txt");
        assert_eq!(completeness, sc_http::search_api::SearchCompleteness::Full);
    }

    #[test]
    fn a_query_too_short_for_a_trigram_still_falls_back_to_the_walker_even_with_an_index() {
        // `sc_search::index::MIN_TRIGRAM_QUERY` is 3 bytes; a 2-byte query
        // cannot be answered by the index at all (`QueryResult::fallback =
        // Some(QueryTooShort)`), index or no index, so this must still find
        // the file exactly as T2 alone would.
        let (bridge, dir) = setup(vec![root_grant()]);
        std::fs::write(dir.path().join("data/ab.txt"), b"x").unwrap();
        bridge.build_name_index(TEST_SHARE).unwrap();

        let out = bridge.search(USER, &q("ab")).unwrap();
        assert_eq!(out.hits.len(), 1, "{:?}", out.hits);
        assert_eq!(out.hits[0].path, "ab.txt");
    }

    #[test]
    fn a_size_filter_bypasses_the_index_and_is_still_correctly_enforced() {
        // The index stores bare paths only — no size, no mtime — so it has
        // no way to answer a size-filtered query at all. If the bypass in
        // `consult_name_indexes` (triggered by `Matcher::needs_stat`) were
        // missing, the index path would return *both* files (it cannot see
        // size to exclude the small one), which is exactly what this test
        // would catch.
        let (bridge, dir) = setup(vec![root_grant()]);
        std::fs::write(dir.path().join("data/photo_small.jpg"), vec![0u8; 10]).unwrap();
        std::fs::write(dir.path().join("data/photo_big.jpg"), vec![0u8; 1000]).unwrap();
        bridge.build_name_index(TEST_SHARE).unwrap();

        let mut query = q("photo");
        query.size_min = Some(500);
        let out = bridge.search(USER, &query).unwrap();
        assert_eq!(
            out.hits.len(),
            1,
            "the size filter must exclude the small file even though an index exists: {:?}",
            out.hits
        );
        assert_eq!(out.hits[0].path, "photo_big.jpg");
    }

    #[test]
    fn a_hit_deleted_since_indexing_does_not_resurrect_as_a_ghost_result() {
        let (bridge, dir) = setup(vec![root_grant()]);
        std::fs::write(dir.path().join("data/gone.txt"), b"bye").unwrap();
        bridge.build_name_index(TEST_SHARE).unwrap();
        std::fs::remove_file(dir.path().join("data/gone.txt")).unwrap();

        let out = bridge.search(USER, &q("gone")).unwrap();
        assert!(
            out.hits.is_empty(),
            "a deleted-since-indexed file must not appear: {:?}",
            out.hits
        );
    }

    #[test]
    fn build_name_index_is_idempotent_and_reflects_a_rebuild() {
        let (bridge, dir) = setup(vec![root_grant()]);
        std::fs::write(dir.path().join("data/one.txt"), b"1").unwrap();
        bridge.build_name_index(TEST_SHARE).unwrap();
        assert_eq!(bridge.search(USER, &q("one")).unwrap().hits.len(), 1);

        // A second file added after the first build; rebuilding must pick it
        // up (§4.2's "initial activation" builder discards anything already there
        // and starts fresh, rather than only ever appending).
        std::fs::write(dir.path().join("data/two.txt"), b"2").unwrap();
        bridge.build_name_index(TEST_SHARE).unwrap();
        assert_eq!(bridge.search(USER, &q("two")).unwrap().hits.len(), 1);
        assert_eq!(bridge.search(USER, &q("one")).unwrap().hits.len(), 1);
    }

    // ------------------------------------------------- keeping it current --
    // `note_index_change` (self-writes through `CoreBridge`) and
    // `reconcile_watch_event` (watcher-driven, exercised directly since
    // nothing wires it to a live `sc_watch::Watcher` yet — see that
    // function's doc comment).

    #[test]
    fn a_self_write_is_found_through_the_index_even_where_t2_cannot_see_it() {
        // Same ACL shape as `search_uses_the_name_index_and_still_enforces_acl_per_hit`:
        // `secret/` denies READ so T2 is structurally blind to anything
        // under it, but `secret/target.txt` re-allows READ at that exact
        // deeper path. The index is built *before* the file exists (so it
        // starts out with no knowledge of it), and the file is then created
        // *through `CoreBridge`*, not by writing into the temp dir directly.
        // This isolates exactly what `note_index_change` is responsible for:
        // without it wired into `hapi::CoreApi::write_text`, the index would
        // stay exactly as it was at build time and this search would find
        // nothing, because T2 can never see this path by construction. This
        // is the test that fails without the self-write hook.
        let (search, dir) = setup(vec![
            root_grant(),
            deny_grant(2, "secret"),
            allow_grant_rw(3, "secret/target.txt"),
        ]);
        std::fs::create_dir_all(dir.path().join("data/secret")).unwrap();
        search.build_name_index(TEST_SHARE).unwrap();
        assert!(search.search(USER, &q("target")).unwrap().hits.is_empty());

        let core_bridge = CoreBridge::new(
            search.core.clone(),
            false,
            None,
            Arc::new(sc_search::IndexSettingsStore::open_in_memory(false).unwrap()),
        );
        <CoreBridge as hapi::CoreApi>::write_text(
            &core_bridge,
            USER,
            "/root/secret/target.txt",
            "treasure",
            None,
        )
        .expect("write through CoreBridge");

        let out = search.search(USER, &q("target")).unwrap();
        assert_eq!(
            out.hits.len(),
            1,
            "a self-write must land in the index immediately, not wait for a rebuild: {:?}",
            out.hits
        );
        assert_eq!(out.hits[0].path, "secret/target.txt");
    }

    #[test]
    fn a_self_write_delete_tombstones_the_index_immediately() {
        let (search, dir) = setup(vec![full_access_grant()]);
        std::fs::write(dir.path().join("data/bye.txt"), b"x").unwrap();
        search.build_name_index(TEST_SHARE).unwrap();
        assert_eq!(search.search(USER, &q("bye")).unwrap().hits.len(), 1);

        let core_bridge = CoreBridge::new(
            search.core.clone(),
            false,
            None,
            Arc::new(sc_search::IndexSettingsStore::open_in_memory(false).unwrap()),
        );
        <CoreBridge as hapi::CoreApi>::delete(
            &core_bridge,
            USER,
            &["/root/bye.txt".to_string()],
            true,
        )
        .expect("delete through CoreBridge");

        // Deletion through `CoreBridge` also removes the file from disk, so
        // even without the tombstone this would come back empty via the
        // existing stat-revalidation fallback — check the index's own
        // bookkeeping directly (`children_of`) so this test is actually
        // about the tombstone, not about the fallback covering for its
        // absence.
        let idx = sc_search::NameIndex::open(&dir.path().join("data/.scindex/names")).unwrap();
        assert_eq!(
            idx.stats().tombstones,
            1,
            "the delete must record a tombstone, not just rely on the file being gone"
        );
    }

    #[test]
    fn a_self_write_rename_moves_the_index_entry() {
        let (search, dir) = setup(vec![full_access_grant()]);
        std::fs::write(dir.path().join("data/old.txt"), b"x").unwrap();
        search.build_name_index(TEST_SHARE).unwrap();

        let core_bridge = CoreBridge::new(
            search.core.clone(),
            false,
            None,
            Arc::new(sc_search::IndexSettingsStore::open_in_memory(false).unwrap()),
        );
        hapi::CoreApi::rename(&core_bridge, USER, "/root/old.txt", "new.txt")
            .expect("rename through CoreBridge");

        let idx = sc_search::NameIndex::open(&dir.path().join("data/.scindex/names")).unwrap();
        assert_eq!(idx.stats().tombstones, 1);
        let out = search.search(USER, &q("new")).unwrap();
        assert_eq!(out.hits.len(), 1, "{:?}", out.hits);
        assert_eq!(out.hits[0].path, "new.txt");
    }

    #[test]
    fn self_writes_create_zero_footprint_when_no_index_exists() {
        // `DESIGN-FOOTPRINT.md` §2: off by default means *zero* filesystem
        // footprint until an operator opts in. `note_index_change` runs on
        // every write regardless of whether an index exists, so this proves
        // it never plants one speculatively, and that ordinary writes behave
        // exactly as the pre-existing "no index" baseline
        // (`search_without_an_index_walks_the_filesystem_exactly_as_before`)
        // says they must.
        let (search, dir) = setup(vec![full_access_grant()]);
        let core_bridge = CoreBridge::new(
            search.core.clone(),
            false,
            None,
            Arc::new(sc_search::IndexSettingsStore::open_in_memory(false).unwrap()),
        );
        hapi::CoreApi::mkdir(&core_bridge, USER, "/root/newdir").unwrap();
        <CoreBridge as hapi::CoreApi>::write_text(
            &core_bridge,
            USER,
            "/root/newdir/note.txt",
            "hi",
            None,
        )
        .unwrap();

        assert!(
            !dir.path().join("data/.scindex").exists(),
            "no index was ever built; a self-write must not create one"
        );

        let out = search.search(USER, &q("note")).unwrap();
        assert_eq!(out.hits.len(), 1);
        assert_eq!(out.hits[0].path, "newdir/note.txt");
        assert_eq!(
            out.hits[0].size, None,
            "still pure T2 — no index means no stat forced by this path either"
        );
    }

    #[test]
    fn the_toggle_gates_the_build_and_takes_effect_without_a_restart() {
        // `FEATURES.md` #116/#117 and `DESIGN-FOOTPRINT.md` §2. Two claims in
        // one test because they are the same claim from both sides:
        //
        //   1. A fresh install (no `config.toml`, so `IndexConfig::default()`
        //      → `name_enabled: false`) neither consults nor builds an index,
        //      and leaves no `.scindex/` behind for having been searched.
        //   2. Flipping the toggle on is enough by itself — the *same live*
        //      `CoreBridge` (never reconstructed, never re-read from disk)
        //      goes from refusing the build to running it, which is what "no
        //      restart" means in code rather than in UI copy.
        let (search, dir) = setup(vec![
            root_grant(),
            deny_grant(2, "secret"),
            allow_grant(3, "secret/target.txt"),
        ]);
        std::fs::create_dir_all(dir.path().join("data/secret")).unwrap();
        std::fs::write(dir.path().join("data/secret/target.txt"), b"treasure").unwrap();

        // `false` here is `IndexConfig::default().name_enabled` verbatim: the
        // value `app.rs` passes when a deployment has no `[index]` section.
        let core_bridge = CoreBridge::new(
            search.core.clone(),
            false,
            None,
            Arc::new(sc_search::IndexSettingsStore::open_in_memory(false).unwrap()),
        );
        let fresh = hapi::CoreApi::index_settings(&core_bridge).unwrap();
        assert!(!fresh.name_enabled);

        // Searching a fresh install must not be what plants an index —
        // `open_name_index`'s whole reason for checking `meta` before calling
        // `NameIndex::open` (which would `create_dir_all` the path).
        assert!(
            search.search(USER, &q("target")).unwrap().hits.is_empty(),
            "T2 cannot see behind the denied directory, and no index exists to help it"
        );
        assert!(
            !dir.path().join("data/.scindex").exists(),
            "a search on a share that never opted in must leave zero filesystem footprint"
        );

        assert!(
            matches!(
                hapi::CoreApi::build_name_indexes(&core_bridge, &|_, _| {}, &|| false),
                Err(hapi::CoreError::NotSupported)
            ),
            "with the toggle off, a build must be refused rather than silently planting .scindex/"
        );
        assert!(
            !dir.path().join("data/.scindex").exists(),
            "the refused build must not have created anything either"
        );

        hapi::CoreApi::set_index_name_enabled(&core_bridge, true).unwrap();
        let results = hapi::CoreApi::build_name_indexes(&core_bridge, &|_, _| {}, &|| false)
            .expect("the same bridge instance must accept the build once the toggle is on");
        assert!(results.iter().all(|r| r.ok), "{results:?}");
        assert!(dir.path().join("data/.scindex/names/meta").exists());

        // The index is genuinely consulted now: T2 still cannot list
        // `secret/`, so a hit here can only have come from T3.
        let out = search.search(USER, &q("target")).unwrap();
        assert_eq!(out.hits.len(), 1, "{:?}", out.hits);
        assert_eq!(out.hits[0].path, "secret/target.txt");
    }

    #[test]
    fn reconcile_watch_event_appends_new_files_and_tombstones_removed_ones() {
        let (search, dir) = setup(vec![root_grant()]);
        std::fs::create_dir_all(dir.path().join("data/photos")).unwrap();
        std::fs::write(dir.path().join("data/photos/a.jpg"), b"1").unwrap();
        search.build_name_index(TEST_SHARE).unwrap();
        assert_eq!(search.search(USER, &q("photos/a")).unwrap().hits.len(), 1);

        // An external change the watcher (not this process) would have
        // noticed: one file removed, one added, with no `CoreBridge` call at
        // all.
        std::fs::remove_file(dir.path().join("data/photos/a.jpg")).unwrap();
        std::fs::write(dir.path().join("data/photos/b.jpg"), b"2").unwrap();

        reconcile_watch_event(
            &search.core,
            TEST_SHARE,
            &SafePath::parse("photos", u16::MAX).unwrap(),
        );

        let out = search.search(USER, &q("photos/b")).unwrap();
        assert_eq!(out.hits.len(), 1, "{:?}", out.hits);
        assert_eq!(out.hits[0].path, "photos/b.jpg");

        // Checked via the index's own bookkeeping, not just the search
        // result, so this is really testing the tombstone rather than the
        // pre-existing stat-revalidation fallback masking its absence.
        let idx = sc_search::NameIndex::open(&dir.path().join("data/.scindex/names")).unwrap();
        assert_eq!(
            idx.children_of(TEST_SHARE, "photos", 100),
            vec!["photos/b.jpg".to_string()]
        );
    }

    #[test]
    fn reconcile_watch_event_is_a_no_op_when_the_share_has_no_index() {
        let (search, dir) = setup(vec![root_grant()]);
        std::fs::create_dir_all(dir.path().join("data/photos")).unwrap();
        std::fs::write(dir.path().join("data/photos/a.jpg"), b"1").unwrap();

        reconcile_watch_event(
            &search.core,
            TEST_SHARE,
            &SafePath::parse("photos", u16::MAX).unwrap(),
        );

        assert!(
            !dir.path().join("data/.scindex").exists(),
            "must not plant an index just to reconcile a directory that never opted in"
        );
    }

    #[test]
    fn built_index_size_agrees_with_the_admin_estimator() {
        // The crawler this test exercises (`build_name_index`, called from
        // `sc-server index build`) and `GET /api/admin/index/estimate`
        // (`hapi::CoreApi::index_estimate`, below) are two independent
        // callers of the same `sc_search` machinery, so they had better
        // agree: if they don't, either the crawler is producing something
        // other than what an operator was told to expect, or the estimator
        // is lying about what turning the index on costs — either way it's a
        // real bug, which is exactly why this compares them directly instead
        // of trusting each in isolation.
        let (search, dir) = setup(vec![root_grant()]);
        // Photo-library shape: calls this the shape
        // block compression is strongest on, and it's what the estimator's
        // own accuracy tests (`sc-search/tests/estimate.rs`) use too.
        let mut n = 0u32;
        for month in 0..12u32 {
            let month_dir = dir.path().join(format!("data/photos/2026/{month:02}"));
            std::fs::create_dir_all(&month_dir).unwrap();
            for i in 0..300u32 {
                std::fs::write(month_dir.join(format!("IMG_{i:05}.jpg")), b"x").unwrap();
                n += 1;
            }
        }

        let core_bridge = CoreBridge::new(
            search.core.clone(),
            false,
            None,
            Arc::new(sc_search::IndexSettingsStore::open_in_memory(false).unwrap()),
        );
        let est = hapi::CoreApi::index_estimate(&core_bridge).expect("estimate");
        assert_eq!(
            est.files, n as u64,
            "estimator must see every file the crawler will"
        );

        let idx = search.build_name_index(TEST_SHARE).expect("build");
        let actual = idx.size_bytes();

        let err = (est.index_bytes as f64 - actual as f64) / actual as f64;
        eprintln!(
            "{n} files: estimator predicted {} bytes, crawler built {actual} bytes ({:+.1}%)",
            est.index_bytes,
            err * 100.0,
        );
        // Term-by-term derivation when this fails: `sc-search`'s own
        // `tests/estimate.rs` prints it, and the server logs it per request.
        assert!(
            err.abs() <= 0.40,
            "estimator predicted {} bytes, the real crawler built {} bytes ({:+.1}%)",
            est.index_bytes,
            actual,
            err * 100.0,
        );
    }

    #[test]
    fn crawl_throttle_is_more_conservative_on_slower_storage() {
        // Locks in `FEATURES.md` #124's actual design intent: rotational and
        // network storage — the classes where continuous crawl traffic can
        // starve a co-accessed Jellyfin/Samba reader — get a smaller batch
        // and a longer pause than flash does.
        use sc_http::search_limits::StorageClass;
        let rotational = CrawlThrottle::for_class(StorageClass::Rotational);
        let nvme = CrawlThrottle::for_class(StorageClass::Nvme);
        assert!(rotational.batch_entries < nvme.batch_entries);
        assert!(rotational.sleep > nvme.sleep);
    }

    #[test]
    fn crawl_throttle_actually_paces() {
        // Direct proof for FEATURES.md #124: with a small batch/sleep pair, a
        // crawl that crosses several batch boundaries must take at least as
        // long as the sleeps `CrawlThrottle` should have inserted. Log lines
        // can be missed or filtered; wall-clock time cannot lie about
        // whether the pacing actually ran.
        let (bridge, dir) = setup(vec![root_grant()]);
        let n = 47u32; // crosses 9 batch boundaries at batch_entries = 5
        for i in 0..n {
            std::fs::write(dir.path().join(format!("data/f{i:03}.txt")), b"x").unwrap();
        }
        let root = bridge.core.share(TEST_SHARE).expect("share exists");
        let throttle = CrawlThrottle {
            batch_entries: 5,
            sleep: std::time::Duration::from_millis(20),
        };
        let mut entries = Vec::new();
        let mut ctx = CrawlCtx {
            throttle: &throttle,
            visited: 0,
            on_progress: &|_| {},
            should_cancel: &|| false,
        };
        let started = std::time::Instant::now();
        collect_paths_for_index(&root, &SafePath::root(), TEST_SHARE, &mut entries, &mut ctx)
            .unwrap();
        let elapsed = started.elapsed();
        let visited = ctx.visited;

        assert_eq!(entries.len(), n as usize);
        let expected_pauses = visited / throttle.batch_entries;
        let expected_min = throttle.sleep * expected_pauses as u32;
        assert!(
            elapsed >= expected_min,
            "crawl of {visited} entries at batch={} sleep={:?} should have paused \
             {expected_pauses} time(s) (>= {expected_min:?}), but only took {elapsed:?} — \
             throttling did not run",
            throttle.batch_entries,
            throttle.sleep,
        );
        eprintln!(
            "crawl_throttle_actually_paces: {visited} entries, {expected_pauses} pause(s), \
             elapsed {elapsed:?} (floor {expected_min:?})"
        );
    }
}
