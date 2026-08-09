//! Test-only helpers for building a minimal but real [`AppState`].

use std::sync::Arc;

use sc_auth::{AuthConfig, AuthService};
use sc_vfs::ids::UserId;
use parking_lot::Mutex;

use crate::config::HttpConfig;
use crate::content::SignedUrlKeys;
use crate::content_api::UnimplementedContent;
use crate::core_api::{CoreApi, UnimplementedCore};
use crate::listing::ListingCache;
use crate::rate_limit::{IpTokenBucket, KeyedTokenBucket};
use crate::search_api::UnimplementedSearch;
use crate::setup_api::{SetupApi, SetupClosed, SetupError, SetupOutcome};
use crate::state::AppState;
use crate::ws::{ReadPermCheck, WsHub};

struct AllowAll;
impl ReadPermCheck for AllowAll {
    fn can_read(&self, _user: UserId, _vpath: &str) -> bool {
        true
    }
}

/// A brand-new [`AppState`] backed by a temp-file SQLite auth DB and
/// [`UnimplementedCore`]. `_tempdir` must be kept alive by the caller for as
/// long as `AppState` is in use (the DB file lives under it).
pub fn test_state() -> (AppState, tempfile::TempDir) {
    test_state_with_core(Arc::new(UnimplementedCore))
}

/// A `CoreApi` whose `list()` returns a fixed entry set and a `dir_etag`
/// that only changes when [`MockCore::bump_etag`] is called — used to
/// exercise `/api/fs/list` pagination and the `Sc-Listing-Stale` path
/// end-to-end without a real backend.
pub struct MockCore {
    entries: Vec<crate::core_api::Entry>,
    etag: Mutex<String>,
}

impl MockCore {
    pub fn new(entries: Vec<crate::core_api::Entry>) -> Self {
        Self { entries, etag: Mutex::new("etag-1".to_string()) }
    }

    pub fn bump_etag(&self) {
        *self.etag.lock() = "etag-2".to_string();
    }
}

impl CoreApi for MockCore {
    fn list(
        &self,
        _user: UserId,
        _vpath: &str,
        _sort: crate::core_api::SortKey,
        _order: crate::core_api::Order,
    ) -> Result<crate::core_api::Listing, crate::core_api::CoreError> {
        Ok(crate::core_api::Listing {
            listing: String::new(),
            total: self.entries.len() as u64,
            dirs: self.entries.iter().filter(|e| e.kind == crate::core_api::Kind::Dir).count() as u64,
            cursor: None,
            entries: self.entries.clone(),
            dir_etag: self.etag.lock().clone(),
        })
    }
}

/// A `CoreApi` whose only real behaviour is `resolve`, mapping each virtual
/// path's label to a fixed `ShareId` from a small fixed table — just enough
/// to exercise `middleware::scope_gate`'s `Scope::shares` half (and anything
/// else built on `resolve`/`resolve_share`) without a real `sc-core` backend.
pub struct ShareMockCore {
    shares: std::collections::HashMap<&'static str, sc_vfs::ShareId>,
}

impl ShareMockCore {
    /// `labels` is `(virtual-path label, ShareId)` pairs — a label not in
    /// this table resolves to `CoreError::NotFound`, the same as a real
    /// backend asked about a share the caller has no root over.
    pub fn new(labels: &[(&'static str, u32)]) -> Self {
        Self { shares: labels.iter().map(|&(l, id)| (l, sc_vfs::ShareId::new(id))).collect() }
    }
}

impl CoreApi for ShareMockCore {
    fn resolve(&self, _user: UserId, vpath: &str) -> Result<crate::core_api::Resolved, crate::core_api::CoreError> {
        let label = vpath.trim_start_matches('/').split('/').next().unwrap_or("");
        self.shares
            .get(label)
            .copied()
            .map(|share| crate::core_api::Resolved { share, subpath: sc_vfs::SafePath::root(), perms: sc_acl::Perms::all() })
            .ok_or(crate::core_api::CoreError::NotFound)
    }

    fn stat_entry(&self, user: UserId, vpath: &str) -> Result<crate::core_api::Entry, crate::core_api::CoreError> {
        self.resolve(user, vpath)?;
        Ok(crate::core_api::Entry {
            name: "x".into(),
            kind: crate::core_api::Kind::File,
            size: 0,
            mtime_ns: "0".into(),
            etag: "e".into(),
            perms: sc_acl::Perms::all(),
            id: None,
            preview: None,
            link: None,
            confusable: false,
        })
    }

    fn mkdir(&self, user: UserId, vpath: &str) -> Result<crate::core_api::Entry, crate::core_api::CoreError> {
        self.stat_entry(user, vpath)
    }
}

/// A `CoreApi` with just enough share-link behaviour to exercise the HTTP
/// surface: routing, auth gating, the link-session cookie, the per-token rate
/// limit, and the "wrong password looks exactly like no such link" rule.
///
/// The cryptography and the store itself are `sc-core`'s and are tested there
/// — duplicating them here would only test the duplicate.
pub struct LinkMockCore {
    /// `token -> id`.
    pub tokens: parking_lot::Mutex<std::collections::HashMap<String, i64>>,
    /// `id -> password`. Absent means "no password set".
    pub passwords: parking_lot::Mutex<std::collections::HashMap<i64, String>>,
    /// Ids whose target is dead (expired / replaced / cap spent).
    pub dead: parking_lot::Mutex<std::collections::HashSet<i64>>,
    pub downloads: std::sync::atomic::AtomicU32,
    pub dropped: parking_lot::Mutex<Vec<(String, Vec<u8>)>>,
    pub is_dir: bool,
    pub is_drop: bool,
}

impl LinkMockCore {
    pub fn with_link(token: &str, id: i64) -> Self {
        let mut tokens = std::collections::HashMap::new();
        tokens.insert(token.to_string(), id);
        Self {
            tokens: parking_lot::Mutex::new(tokens),
            passwords: parking_lot::Mutex::new(std::collections::HashMap::new()),
            dead: parking_lot::Mutex::new(std::collections::HashSet::new()),
            downloads: std::sync::atomic::AtomicU32::new(0),
            dropped: parking_lot::Mutex::new(Vec::new()),
            is_dir: false,
            is_drop: false,
        }
    }

    pub fn with_password(self, id: i64, pw: &str) -> Self {
        self.passwords.lock().insert(id, pw.to_string());
        self
    }

    pub fn kill(self, id: i64) -> Self {
        self.dead.lock().insert(id);
        self
    }

    pub fn as_drop(mut self) -> Self {
        self.is_dir = true;
        self.is_drop = true;
        self
    }
}

impl CoreApi for LinkMockCore {
    fn shares_enabled(&self) -> bool {
        true
    }

    fn share_link_lookup(&self, token: &str) -> Result<Option<i64>, crate::core_api::CoreError> {
        Ok(self.tokens.lock().get(token).copied())
    }

    fn share_link_public(&self, id: i64) -> Result<crate::core_api::PublicLink, crate::core_api::CoreError> {
        if self.dead.lock().contains(&id) {
            return Err(crate::core_api::CoreError::Gone);
        }
        Ok(crate::core_api::PublicLink {
            id,
            name: "shared.txt".into(),
            is_dir: self.is_dir,
            size: 3,
            mtime_ns: 0,
            has_password: self.passwords.lock().contains_key(&id),
            is_drop: self.is_drop,
            can_download: !self.is_drop,
            fid: Some(77),
            etag8: [1, 2, 3, 4, 5, 6, 7, 8],
            label: None,
        })
    }

    fn share_link_check_password(&self, id: i64, candidate: &str) -> Result<bool, crate::core_api::CoreError> {
        match self.passwords.lock().get(&id) {
            Some(p) => Ok(p == candidate),
            // Unknown id (including the `-1` sentinel the handler passes for
            // an unknown token) — refuse, same as the real core.
            None if id < 0 || !self.tokens.lock().values().any(|v| *v == id) => Ok(false),
            None => Ok(true),
        }
    }

    fn share_link_note_download(&self, id: i64) -> Result<(), crate::core_api::CoreError> {
        if self.dead.lock().contains(&id) {
            return Err(crate::core_api::CoreError::Gone);
        }
        self.downloads.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        Ok(())
    }

    fn share_link_entries(&self, _id: i64) -> Result<Vec<crate::core_api::Entry>, crate::core_api::CoreError> {
        Ok(Vec::new())
    }

    fn share_link_drop(&self, _id: i64, name: &str, body: &[u8]) -> Result<crate::core_api::Entry, crate::core_api::CoreError> {
        self.dropped.lock().push((name.to_string(), body.to_vec()));
        Ok(crate::core_api::Entry {
            name: name.to_string(),
            kind: crate::core_api::Kind::File,
            size: body.len() as u64,
            mtime_ns: "0".into(),
            etag: "e".into(),
            perms: sc_acl::Perms::CREATE,
            id: None,
            preview: None,
            link: None,
            confusable: false,
        })
    }
}

/// A `CoreApi` with just enough grant behaviour to exercise
/// `/api/admin/grants*`'s HTTP surface end to end — routing, admin gating,
/// request/response wire shapes, and the "at least one bit" refusal — without
/// a real `sc-core`/`sc-acl` backend. The evaluation algorithm itself (depth,
/// deny-beats-allow, the virtual-root projection) is `sc-acl`'s and
/// `sc-core::acl_store`'s to test; duplicating it here would only test the
/// duplicate. One fixed share (`id: 1, name: "docs"`) is enough for the
/// picker-population test.
pub struct GrantMockCore {
    next_id: std::sync::atomic::AtomicU32,
    grants: Mutex<Vec<crate::core_api::GrantInfo>>,
}

impl Default for GrantMockCore {
    fn default() -> Self {
        Self { next_id: std::sync::atomic::AtomicU32::new(1), grants: Mutex::new(Vec::new()) }
    }
}

impl CoreApi for GrantMockCore {
    fn admin_shares(&self) -> Vec<crate::core_api::AdminShareInfo> {
        vec![crate::core_api::AdminShareInfo {
            id: 1,
            name: "docs".into(),
            host_path: "/srv/docs".into(),
            config_defined: true,
            trash_enabled: false,
        }]
    }

    fn list_grants(&self, filter: crate::core_api::GrantFilter) -> Result<Vec<crate::core_api::GrantInfo>, crate::core_api::CoreError> {
        Ok(self
            .grants
            .lock()
            .iter()
            .filter(|g| filter.principal.is_none_or(|p| g.principal == p))
            .filter(|g| filter.share.is_none_or(|s| g.share == s))
            .cloned()
            .collect())
    }

    fn create_grant(&self, spec: crate::core_api::GrantSpec) -> Result<crate::core_api::GrantInfo, crate::core_api::CoreError> {
        if spec.allow.is_empty() && spec.deny.is_empty() {
            return Err(crate::core_api::CoreError::InvalidName("a grant must allow or deny at least one permission".into()));
        }
        let id = self.next_id.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        let info = crate::core_api::GrantInfo {
            id,
            principal: spec.principal,
            share: spec.share,
            subpath: spec.subpath,
            allow: spec.allow,
            deny: spec.deny,
            inherit: spec.inherit,
            label: spec.label,
            created_ns: "0".into(),
        };
        self.grants.lock().push(info.clone());
        Ok(info)
    }

    fn update_grant(&self, id: u32, patch: crate::core_api::GrantPatch) -> Result<crate::core_api::GrantInfo, crate::core_api::CoreError> {
        let mut grants = self.grants.lock();
        let g = grants.iter_mut().find(|g| g.id == id).ok_or(crate::core_api::CoreError::NotFound)?;
        let next_allow = patch.allow.unwrap_or(g.allow);
        let next_deny = patch.deny.unwrap_or(g.deny);
        if next_allow.is_empty() && next_deny.is_empty() {
            return Err(crate::core_api::CoreError::InvalidName("a grant must allow or deny at least one permission".into()));
        }
        g.allow = next_allow;
        g.deny = next_deny;
        if let Some(i) = patch.inherit {
            g.inherit = i;
        }
        if let Some(l) = patch.label {
            g.label = l;
        }
        Ok(g.clone())
    }

    fn delete_grant(&self, id: u32) -> Result<(), crate::core_api::CoreError> {
        let mut grants = self.grants.lock();
        let before = grants.len();
        grants.retain(|g| g.id != id);
        if grants.len() == before {
            return Err(crate::core_api::CoreError::NotFound);
        }
        Ok(())
    }
}

/// A `CoreApi` for exercising the job vertical (`routes::spawn_batch_job`,
/// `spawn_archive_job`, `estimate_entry_count`) without a real backend.
///
/// `aggregate` always answers the fixed value it was built with, and
/// `resolve` always succeeds (share 1, root subpath) — `aggregate`'s numbers
/// only feed an archive job's upfront `total` estimate now (every request is
/// a job regardless of size), but tests still get to choose a shape that
/// exercises `aggregate`'s directory path or `stat_entry`'s leaf-file
/// fallback.
///
/// `copy_entries`/`move_entries`/`delete`/`archive_walk` each count the call
/// in `calls` and, if built via [`Self::gated`], block on a channel until
/// the test releases it — enough to make "the item already running finishes
/// before a cancel takes effect" deterministic instead of a sleep-and-hope.
pub struct JobMockCore {
    pub agg: crate::core_api::Aggregate,
    /// When set, `aggregate` errors (as it does on a real plain file, which
    /// isn't a directory `read_dir` can walk) and `stat_entry` reports this
    /// size instead — lets a test pin `estimate_entry_count`'s leaf-file
    /// fallback.
    leaf_size: Option<u64>,
    calls: Arc<std::sync::atomic::AtomicU64>,
    gate: Option<Mutex<std::sync::mpsc::Receiver<()>>>,
}

impl JobMockCore {
    pub fn new(agg: crate::core_api::Aggregate) -> Self {
        Self { agg, leaf_size: None, calls: Arc::new(std::sync::atomic::AtomicU64::new(0)), gate: None }
    }

    /// A batch of plain files only (no directories) — `aggregate` errors for
    /// every path the way it really does on a leaf file, so this only
    /// exercises `estimate_entry_count`'s `stat_entry` fallback.
    pub fn new_leaf_files(size: u64) -> Self {
        Self {
            agg: crate::core_api::Aggregate { file_count: 0, dir_count: 0, total_bytes: 0 },
            leaf_size: Some(size),
            calls: Arc::new(std::sync::atomic::AtomicU64::new(0)),
            gate: None,
        }
    }

    /// Like [`Self::new`], but every per-item call blocks until the returned
    /// `Sender` is sent to once — one release per call. The returned `Arc`
    /// is the same counter `step` increments, readable from the test after
    /// the core has been moved into `Arc<dyn CoreApi>`.
    pub fn gated(agg: crate::core_api::Aggregate) -> (Self, std::sync::mpsc::Sender<()>, Arc<std::sync::atomic::AtomicU64>) {
        let (tx, rx) = std::sync::mpsc::channel();
        let calls = Arc::new(std::sync::atomic::AtomicU64::new(0));
        (Self { agg, leaf_size: None, calls: calls.clone(), gate: Some(Mutex::new(rx)) }, tx, calls)
    }

    fn step(&self) {
        self.calls.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
        if let Some(gate) = &self.gate {
            let _ = gate.lock().recv();
        }
    }
}

impl CoreApi for JobMockCore {
    fn resolve(&self, _user: UserId, _vpath: &str) -> Result<crate::core_api::Resolved, crate::core_api::CoreError> {
        Ok(crate::core_api::Resolved { share: sc_vfs::ids::ShareId::new(1), subpath: sc_vfs::SafePath::root(), perms: sc_acl::Perms::all() })
    }

    fn aggregate(&self, _share: sc_vfs::ids::ShareId, _subpath: &sc_vfs::SafePath) -> anyhow::Result<crate::core_api::Aggregate> {
        if self.leaf_size.is_some() {
            anyhow::bail!("not a directory");
        }
        Ok(self.agg.clone())
    }

    fn stat_entry(&self, _user: UserId, _vpath: &str) -> Result<crate::core_api::Entry, crate::core_api::CoreError> {
        let size = self.leaf_size.ok_or(crate::core_api::CoreError::NotFound)?;
        Ok(crate::core_api::Entry {
            name: "x".into(),
            kind: crate::core_api::Kind::File,
            size,
            mtime_ns: "0".into(),
            etag: "e".into(),
            perms: sc_acl::Perms::all(),
            id: None,
            preview: None,
            link: None,
            confusable: false,
        })
    }

    fn copy_entries(
        &self,
        _user: UserId,
        paths: &[String],
        _dest: &str,
        _on_conflict: crate::core_api::OnConflict,
        _if_match: &std::collections::HashMap<String, String>,
    ) -> Result<Vec<crate::core_api::OpResult>, crate::core_api::CoreError> {
        self.step();
        Ok(paths.iter().map(|p| crate::core_api::OpResult { path: p.clone(), ok: true, error: None, will_copy: false }).collect())
    }

    fn move_entries(
        &self,
        user: UserId,
        paths: &[String],
        dest: &str,
        on_conflict: crate::core_api::OnConflict,
        if_match: &std::collections::HashMap<String, String>,
    ) -> Result<Vec<crate::core_api::OpResult>, crate::core_api::CoreError> {
        self.copy_entries(user, paths, dest, on_conflict, if_match)
    }

    fn delete(&self, _user: UserId, paths: &[String], _permanent: bool) -> Result<Vec<crate::core_api::OpResult>, crate::core_api::CoreError> {
        self.step();
        Ok(paths.iter().map(|p| crate::core_api::OpResult { path: p.clone(), ok: true, error: None, will_copy: false }).collect())
    }

    fn archive_walk(
        &self,
        _user: UserId,
        _vpath: &str,
        visit: &mut dyn FnMut(&crate::core_api::WalkEntry, Option<&mut dyn std::io::Read>),
    ) -> Result<(), crate::core_api::CoreError> {
        self.step();
        let entry = crate::core_api::WalkEntry { rel_path: "file.txt".into(), is_dir: false, readable: true, size: Some(3), mtime_ns: Some(0) };
        let mut reader: &[u8] = b"abc";
        visit(&entry, Some(&mut reader));
        Ok(())
    }
}

pub fn test_state_with_core(core: Arc<dyn CoreApi>) -> (AppState, tempfile::TempDir) {
    let dir = tempfile::tempdir().expect("tempdir");
    let db_path = dir.path().join("auth.sqlite3");
    let auth = AuthService::new(&db_path, AuthConfig::default(), [1u8; 32]).expect("auth service");

    let state = AppState {
        cfg: Arc::new(HttpConfig::default()),
        auth: Arc::new(auth),
        core,
        uploads: Arc::new(crate::upload_api::UnimplementedUploads),
        content: Arc::new(UnimplementedContent),
        search: Arc::new(UnimplementedSearch),
        setup: Arc::new(SetupClosed),
        oidc: Arc::new(crate::oidc_api::OidcDisabled),
        oidc_rate: Arc::new(IpTokenBucket::new(1000, std::time::Duration::from_secs(1))),
        signed_url_keys: Arc::new(Mutex::new(SignedUrlKeys::generate())),
        listings: Arc::new(ListingCache::new()),
        ws: WsHub::new(Arc::new(AllowAll)),
        jobs: Arc::new(crate::state::JobStore::open_in_memory().expect("in-memory jobs db")),
        rate_limiter: Arc::new(IpTokenBucket::new(1000, std::time::Duration::from_secs(1))),
        link_rate: Arc::new(KeyedTokenBucket::new(10, std::time::Duration::from_secs(360))),
        search_rate: Arc::new(KeyedTokenBucket::new(1000, std::time::Duration::from_secs(60))),
        setup_rate: Arc::new(IpTokenBucket::new(1000, std::time::Duration::from_secs(1))),
        search_concurrency: Arc::new(crate::search_limits::SearchConcurrency::new(
            &crate::search_limits::SearchLimitsConfig::default(),
        )),
        archive_concurrency: Arc::new(crate::state::ResizableSemaphore::new(4)),
        csrf_key: [9u8; 32],
        boot_time: std::time::Instant::now(),
        settings: Arc::new(crate::settings_api::UnimplementedSettings),
        restart_signal: Arc::new(tokio::sync::Notify::new()),
    };
    (state, dir)
}

/// Same as [`test_state_with_core`], but with a caller-supplied `content`
/// backend — for tests exercising `GET /c/{token}` without a real
/// `sc-core`/`sc-preview`.
pub fn test_state_with_content(content: Arc<dyn crate::content_api::ContentApi>) -> (AppState, tempfile::TempDir) {
    let (mut state, dir) = test_state_with_core(Arc::new(UnimplementedCore));
    state.content = content;
    // Content tests model the *two-origin* deployment, which is the one the
    // design actually wants — so say so explicitly rather than relying on a
    // default. `HttpConfig::content_hosts` defaults to empty (single-origin),
    // and a test that silently depended on a default content host broke the
    // moment that default changed.
    let mut cfg = (*state.cfg).clone();
    cfg.content_hosts = vec!["content.example.com".into()];
    state.cfg = Arc::new(cfg);
    (state, dir)
}

/// A state configured for the **single-origin fallback**: no dedicated
/// content host, so user content is served from the app origin.
/// Supported, but it gives up the XSS isolation
/// the split exists for, which is why startup warns about it.
pub fn test_state_single_origin(content: Arc<dyn crate::content_api::ContentApi>) -> (AppState, tempfile::TempDir) {
    let (mut state, dir) = test_state_with_core(Arc::new(UnimplementedCore));
    state.content = content;
    let mut cfg = (*state.cfg).clone();
    cfg.content_hosts.clear();
    state.cfg = Arc::new(cfg);
    (state, dir)
}

/// Same as [`test_state_with_core`], but with a caller-supplied `search`
/// backend — for tests exercising `/api/search[/stream]` without a real
/// `sc-search` walker.
pub fn test_state_with_search(search: Arc<dyn crate::search_api::SearchApi>) -> (AppState, tempfile::TempDir) {
    let (mut state, dir) = test_state_with_core(Arc::new(UnimplementedCore));
    state.search = search;
    (state, dir)
}

/// A `SetupApi` that reports every `SetupError` variant on demand, so the
/// HTTP layer's status-code and error-code mapping can be checked without a
/// real gate. The *behaviour* being mapped — the timing-safe comparison,
/// single use, expiry, the "an account exists" gate — belongs to `sc-server`
/// and is tested there against the real implementation; duplicating it in a
/// mock here would only test the duplicate.
pub struct ScriptedSetup {
    pub required: bool,
    result: parking_lot::Mutex<Option<Result<SetupOutcome, SetupError>>>,
    repeat: bool,
}

impl ScriptedSetup {
    /// Scripted for exactly one call. A second call reports `Completed`,
    /// which is what a handler that asked twice would (correctly) see from
    /// the real gate — and what makes an accidental double-call visible.
    pub fn required_returning(result: Result<SetupOutcome, SetupError>) -> Self {
        Self { required: true, result: parking_lot::Mutex::new(Some(result)), repeat: false }
    }

    /// Same answer every time — for tests about the layer *in front of* the
    /// gate, like the rate limit, where the gate's own answer is incidental.
    pub fn required_always(result: Result<SetupOutcome, SetupError>) -> Self {
        Self { required: true, result: parking_lot::Mutex::new(Some(result)), repeat: true }
    }
}

impl SetupApi for ScriptedSetup {
    fn is_required(&self) -> bool {
        self.required
    }

    fn complete(
        &self,
        _token: &str,
        _username: &str,
        _password: &secrecy::SecretString,
        _ip: std::net::IpAddr,
    ) -> Result<SetupOutcome, SetupError> {
        let mut slot = self.result.lock();
        if self.repeat {
            return slot.clone().unwrap_or(Err(SetupError::Completed));
        }
        slot.take().unwrap_or(Err(SetupError::Completed))
    }
}

/// [`test_state_with_core`] with a caller-supplied setup gate.
pub fn test_state_with_setup(setup: Arc<dyn SetupApi>) -> (AppState, tempfile::TempDir) {
    let (mut state, dir) = test_state_with_core(Arc::new(UnimplementedCore));
    state.setup = setup;
    (state, dir)
}

/// A scripted [`OidcApi`](crate::oidc_api::OidcApi), for the same reason
/// [`ScriptedSetup`] exists: the HTTP layer's own contract -- status codes,
/// error codes, cookie handling, and above all the callback's *ordering* --
/// is what these tests are about, and none of it needs a real identity
/// provider to be about it.
///
/// What is deliberately not simulated here: discovery, JWKS, the signature,
/// and the eleven claim checks. Those are `sc-oidc`'s, tested there against
/// an in-process fake IdP with committed key material. Re-simulating them
/// here would only test the simulation.
///
/// Every secret is derived from one `seed` so a test can predict both halves
/// of a flow: `{seed}-state` is what the IdP hands back as `?state=`, and
/// `{seed}-binding` is what the browser sends in `__Host-sc_oidc`.
pub struct ScriptedOidc {
    pub enabled: bool,
    pub issuer: String,
    pub display_name: String,
    pub seed: String,
    /// What a successful [`redeem`](crate::oidc_api::OidcApi::redeem)
    /// resolves to.
    pub identity: crate::oidc_api::VerifiedIdentity,
    pub begin_error: Option<crate::oidc_api::OidcError>,
    pub redeem_error: Option<crate::oidc_api::OidcError>,
    /// Every `code` this was asked to redeem, in order. A test asserting that
    /// a rejected callback never reached the token endpoint reads this.
    pub redeemed: Mutex<Vec<String>>,
}

impl ScriptedOidc {
    pub const TEST_ISSUER: &'static str = "https://idp.example.test/realms/sc";

    /// Enabled, answering with `subject` for whatever code it is given.
    pub fn linked_as(subject: &str) -> Self {
        Self {
            enabled: true,
            issuer: Self::TEST_ISSUER.to_string(),
            display_name: "Example SSO".to_string(),
            seed: "flow".to_string(),
            identity: crate::oidc_api::VerifiedIdentity {
                issuer: Self::TEST_ISSUER.to_string(),
                subject: subject.to_string(),
            },
            begin_error: None,
            redeem_error: None,
            redeemed: Mutex::new(Vec::new()),
        }
    }

    pub fn disabled() -> Self {
        Self { enabled: false, ..Self::linked_as("unused") }
    }

    /// The `?state=` value a callback for this fake's flow carries.
    pub fn state_param(&self) -> String {
        format!("{}-state", self.seed)
    }

    /// The `__Host-sc_oidc` value that flow's browser holds.
    pub fn binding(&self) -> String {
        format!("{}-binding", self.seed)
    }

    pub fn nonce(&self) -> String {
        format!("{}-nonce", self.seed)
    }

    pub fn code_verifier(&self) -> String {
        format!("{}-verifier", self.seed)
    }
}

#[async_trait::async_trait]
impl crate::oidc_api::OidcApi for ScriptedOidc {
    fn display(&self) -> crate::oidc_api::OidcDisplay {
        crate::oidc_api::OidcDisplay {
            enabled: self.enabled,
            display_name: self.display_name.clone(),
        }
    }

    fn issuer(&self) -> Option<String> {
        self.enabled.then(|| self.issuer.clone())
    }

    async fn begin(&self) -> Result<crate::oidc_api::StartedFlow, crate::oidc_api::OidcError> {
        if let Some(e) = &self.begin_error {
            return Err(e.clone());
        }
        let digest = |s: &str| -> [u8; 32] {
            use sha2::Digest;
            sha2::Sha256::digest(s.as_bytes()).into()
        };
        Ok(crate::oidc_api::StartedFlow {
            authorize_url: format!(
                "https://idp.example.test/authorize?client_id=sc&state={}",
                self.state_param()
            ),
            state_hash: digest(&self.state_param()),
            binding: secrecy::SecretString::from(self.binding()),
            binding_hash: digest(&self.binding()),
            nonce_hash: digest(&self.nonce()),
            code_verifier: secrecy::SecretString::from(self.code_verifier()),
        })
    }

    async fn redeem(
        &self,
        code: &str,
        _code_verifier: &secrecy::SecretString,
        _nonce_hash: &[u8; 32],
    ) -> Result<crate::oidc_api::VerifiedIdentity, crate::oidc_api::OidcError> {
        self.redeemed.lock().push(code.to_string());
        if let Some(e) = &self.redeem_error {
            return Err(e.clone());
        }
        Ok(self.identity.clone())
    }
}

/// [`test_state_with_core`] with a caller-supplied relying party.
pub fn test_state_with_oidc(
    oidc: Arc<dyn crate::oidc_api::OidcApi>,
) -> (AppState, tempfile::TempDir) {
    let (mut state, dir) = test_state_with_core(Arc::new(UnimplementedCore));
    state.oidc = oidc;
    (state, dir)
}
