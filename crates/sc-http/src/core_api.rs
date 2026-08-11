//! The trait boundary between the HTTP routes and the domain layer.
//!
//! `CoreApi` names everything the routes need from `sc-core` (`resolve`,
//! `roots`, `list`, `stat_entry`, `mkdir`, `rename`, `move_entries`,
//! `copy_entries`, `delete`, `read_text`, `write_text`,
//! `trash_list/restore/purge`, `aggregate`, and the share/link/job surface),
//! plus the response DTOs (§5, §6) `sc-http`
//! serializes. `sc-server`'s `CoreBridge` is the real implementation; the
//! tests in this crate supply small in-memory ones.
//!
//! `sc-http` depends on the trait rather than on `sc-core` itself, so the
//! routes can be tested without a filesystem and `sc-core` can change shape
//! without dragging every route with it.
//!
//! `RootEntry`/`Perms` are **not** redefined — those come straight from
//! `sc_acl`, which is real and already implements them.

use sc_acl::{Perms, RootEntry};
use sc_vfs::ids::{FileId, ShareId, UserId};
use sc_vfs::SafePath;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

pub type Etag = String;

#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Kind {
    File,
    Dir,
    Symlink,
    Other,
}

impl From<sc_vfs::Kind> for Kind {
    fn from(k: sc_vfs::Kind) -> Self {
        match k {
            sc_vfs::Kind::File => Kind::File,
            sc_vfs::Kind::Dir => Kind::Dir,
            sc_vfs::Kind::Symlink => Kind::Symlink,
            sc_vfs::Kind::Other => Kind::Other,
        }
    }
}

/// `sc_acl::Perms` doesn't derive `Serialize` (it derives only
/// `Debug/Clone/Copy/PartialEq/Eq/Hash` — see `crates/sc-acl/src/lib.rs`), so
/// wire representation is handled here rather than upstream in `sc-acl`,
/// which has no reason to know about JSON shape.
pub fn perms_to_json(p: Perms) -> serde_json::Value {
    serde_json::json!({
        "read": p.contains(Perms::READ),
        "write": p.contains(Perms::WRITE),
        "create": p.contains(Perms::CREATE),
        "delete": p.contains(Perms::DELETE),
        "rename": p.contains(Perms::RENAME),
        "move": p.contains(Perms::MOVE),
        "share": p.contains(Perms::SHARE),
        "download": p.contains(Perms::DOWNLOAD),
    })
}

#[derive(Clone, Debug, Serialize)]
pub struct PreviewInfo {
    pub available: bool,
}

#[derive(Clone, Debug, Serialize)]
pub struct SymlinkInfo {
    /// `true` when policy blocks opening this symlink (`SymlinkPolicy::Deny`).
    pub blocked: bool,
}

/// One entry in a directory listing, as it goes on the wire.
#[derive(Clone, Debug, Serialize)]
pub struct Entry {
    pub name: String,
    pub kind: Kind,
    pub size: u64,
    /// `i128` nanoseconds serialized as a JSON string (§1: "timestamps are
    /// nanosecond integers (`i128` → JSON string); no floating-point seconds").
    pub mtime_ns: String,
    pub etag: Etag,
    #[serde(serialize_with = "serialize_perms")]
    pub perms: Perms,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<FileId>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub preview: Option<PreviewInfo>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub link: Option<SymlinkInfo>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub confusable: bool,
}

fn serialize_perms<S: serde::Serializer>(p: &Perms, s: S) -> Result<S::Ok, S::Error> {
    perms_to_json(*p).serialize(s)
}

#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum SortKey {
    Name,
    Size,
    Mtime,
    Kind,
}

#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Order {
    Asc,
    Desc,
}

/// One page of a directory listing.
#[derive(Clone, Debug, Serialize)]
pub struct Listing {
    pub listing: String,
    pub total: u64,
    /// Directories in this listing, which sort first, so also the index where
    /// files begin. The grid draws the two as different-sized cards and needs
    /// the boundary without having loaded the rows around it.
    pub dirs: u64,
    pub cursor: Option<String>,
    pub entries: Vec<Entry>,
    pub dir_etag: Etag,
}

#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum OnConflict {
    Fail,
    Rename,
    Overwrite,
    Skip,
}

/// Per-item batch result.
#[derive(Clone, Debug, Serialize)]
pub struct OpResult {
    pub path: String,
    pub ok: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<serde_json::Value>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub will_copy: bool,
}

/// Result of resolving a virtual path to a concrete `(ShareRoot, SafePath)` —
/// the single entry point `AclScope` calls.
#[derive(Clone, Debug)]
pub struct Resolved {
    pub share: ShareId,
    pub subpath: SafePath,
    pub perms: Perms,
}

#[derive(Debug, thiserror::Error)]
pub enum CoreError {
    #[error("not found")]
    NotFound,
    /// `by` is the grant id responsible for the denial, if any (`None` for
    /// implicit default-deny) — mirrors `sc_core::CoreError::Denied`, which
    /// is where this actually comes from (`bridge.rs::http_err`). Carrying it
    /// through is what lets `AppError::acl_denied` report the real grant
    /// instead of the placeholder `"acl"` it used to hardcode.
    #[error("permission denied")]
    Denied { by: Option<u32> },
    /// The resource existed and is permanently unavailable — a share link
    /// whose target was replaced, whose expiry passed, or whose download cap
    /// is spent.
    #[error("gone")]
    Gone,
    /// The deployment does not provide this operation. Rendered as `501`, not
    /// as a success with an empty result: a client told a share was created
    /// and then unable to find it is worse off than one told it cannot be.
    #[error("not supported")]
    NotSupported,
    #[error("conflict")]
    Conflict { path: String, etag: Option<String> },
    #[error("precondition failed")]
    Precondition { current_etag: String },
    #[error("invalid name: {0}")]
    InvalidName(String),
    /// A validation refusal carrying a stable catalogue key instead of prose.
    /// `message` is the English rendering for logs and non-browser callers;
    /// the browser renders `key` + `params` in its own locale, so the wording
    /// here never reaches a screen. Mirrors `sc_core::CoreError::Invalid`.
    #[error("{message}")]
    Invalid { key: String, params: serde_json::Value, message: String },
    #[error("cross device")]
    CrossDevice { total_bytes: u64 },
    #[error("quota exceeded")]
    QuotaExceeded,
    #[error("internal: {0}")]
    Internal(String),
}

impl From<CoreError> for crate::error::AppError {
    fn from(e: CoreError) -> Self {
        use crate::error::{AppError, ErrorCode};
        match e {
            CoreError::NotFound => AppError::not_found(),
            // A real grant id renders as its number; the implicit
            // default-deny case (no grant matched at all) falls back to the
            // same `"acl"` label as before — there is no id to report.
            CoreError::Denied { by } => AppError::acl_denied(by.map(|id| id.to_string()).unwrap_or_else(|| "acl".to_string())),
            CoreError::Gone => AppError::gone(),
            CoreError::NotSupported => AppError::not_implemented(),
            CoreError::Conflict { path, etag } => AppError::conflict(path, etag.as_deref()),
            CoreError::Precondition { current_etag } => AppError::precondition(current_etag),
            CoreError::InvalidName(reason) => AppError::invalid_name(reason),
            CoreError::Invalid { key, params, message } => AppError::invalid_keyed(&key, params, &message),
            CoreError::CrossDevice { total_bytes } => AppError::new(ErrorCode::FsInvalidName, "cross device")
                .with_status(axum::http::StatusCode::OK)
                .with_detail(serde_json::json!({ "will_copy": true, "total_bytes": total_bytes, "reason": "cross_device" })),
            CoreError::QuotaExceeded => AppError::new(ErrorCode::QuotaExceeded, "quota exceeded"),
            CoreError::Internal(msg) => {
                tracing::error!(error = %msg, "CoreError::Internal");
                AppError::internal()
            }
        }
    }
}

/// One descendant discovered while walking an archive root
/// (`CoreApi::archive_walk`). Mirrors
/// `sc_core::WalkEntry` — kept as a local type so this crate's trait
/// boundary never names `sc-core`.
#[derive(Clone, Debug)]
pub struct WalkEntry {
    /// Path relative to the archive root, `/`-joined — the zip entry name.
    pub rel_path: String,
    pub is_dir: bool,
    /// `false` means: exists, but the caller may not read it (or it raced
    /// out from under us). Recorded as skipped, never a hard failure.
    pub readable: bool,
    pub size: Option<u64>,
    pub mtime_ns: Option<i128>,
}

#[derive(Clone, Debug)]
pub struct Aggregate {
    pub file_count: u64,
    pub dir_count: u64,
    pub total_bytes: u64,
}

/// One entry of an archive listing, as the wire carries it. Nothing here is
/// clickable on the screen: opening an entry means extraction, which this
/// server does not do.
///
#[derive(Clone, Debug, Serialize)]
pub struct ArchiveEntryWire {
    pub name: String,
    pub size: u64,
    /// `"file"` or `"dir"`.
    pub kind: &'static str,
}

/// One archive's listing.
///
/// `skipped` is what the listing left out: an entry whose name cannot be
/// handed out safely (a path escape, a raw Windows separator) or whose type
/// is not a file or a directory. Reported rather than hidden, and counted
/// rather than fatal — the rule guards whoever turns a name into a path, and
/// one such entry is no reason to refuse the other five thousand.
///
/// An archive that breaks a *bound* (entry count, central directory size) is
/// still refused whole, as a `422` with the reason.
#[derive(Clone, Debug, Serialize)]
pub struct ArchiveListingWire {
    pub entries: Vec<ArchiveEntryWire>,
    pub skipped: u32,
}

/// One item in the trash.
///
/// The id is an **opaque string**, not a `FileId`: a trashed entry has no
/// live fileid to be addressed by — the core names trash items by their own
/// handle — so typing this as an integer would make restore/purge
/// unimplementable against the real backend.
#[derive(Clone, Debug, Serialize)]
pub struct TrashEntry {
    pub id: String,
    pub name: String,
    pub size: u64,
    /// When the entry was moved into the trash. Nanoseconds, serialized as a
    /// string (§1, same rule as `Entry`). Was `deleted_mtime_ns` and carried
    /// the file's mtime, which is not when it was deleted.
    pub deleted_at_ns: String,
}

/// One share's contribution to `/api/admin/storage`.
#[derive(Clone, Debug, Serialize)]
pub struct ShareStorage {
    pub label: String,
    pub free_bytes: u64,
    pub total_bytes: u64,
}

#[derive(Clone, Debug, Serialize)]
pub struct StorageReport {
    /// Size of the metadata cache database on disk.
    pub db_bytes: u64,
    pub shares: Vec<ShareStorage>,
}

/// Projected cost of turning the optional name index on.
#[derive(Clone, Debug, Serialize)]
pub struct IndexEstimate {
    pub files: u64,
    pub index_bytes: u64,
    /// Processor time, not wall clock: the crawler runs only while the server
    /// is otherwise idle, so a real build takes longer than this.
    pub build_secs: u64,
    /// How much of the corpus the estimate actually measured — `high`,
    /// `medium` or `low`. A code, not a sentence: the browser owns the
    /// wording and the reader's language.
    ///
    /// The term-by-term derivation is deliberately not on the wire. It is
    /// written to the server log instead, where an operator checking the
    /// arithmetic can find it, rather than onto a screen where it read as
    /// noise to everyone else.
    pub confidence: String,
}

/// Whether the T3 name index is switched on for this deployment
/// — the admin-persisted override
/// `sc_search::IndexSettingsStore` backs, so it survives a restart without a
/// `config.toml` rewrite.
#[derive(Clone, Copy, Debug, Serialize, Deserialize)]
pub struct IndexSettings {
    pub name_enabled: bool,
}

/// One share's outcome from `POST /api/admin/index/build` — mirrors `OpResult`'s per-item shape (`path`/`ok`/`error`) since
/// this is the same "one request, several independent outcomes" pattern.
#[derive(Clone, Debug, Serialize)]
pub struct IndexBuildResult {
    pub share: String,
    pub ok: bool,
    pub error: Option<String>,
}

// ---------------------------------------------------------------- grants --
// `/api/admin/grants*` — the admin surface over `sc_acl::Grant`. The store
// itself lives in `sc-core` (`sc_core::acl_store::AclStore`); this crate
// stays a thin translation and `sc-server::CoreBridge` does the real work,
// same split as everything else in this file.
//
// Unlike `Entry::perms` (an object, `{"read":true,...}`, via
// `perms_to_json`/`serialize_perms`), a grant's `allow`/`deny` travel as an
// **array of names** (`["read","download"]`) — the shape the admin UI's
// checkbox group and `sc_core::acl_store::PERM_NAMES` both already settled
// on before this HTTP surface existed (see that module's doc comment and
// `web/src/lib/api/types.ts::GrantPermName`). `GRANT_PERM_NAMES` below
// restates that table rather than importing it: this crate cannot depend on
// `sc-core` (module doc, top of file), so `sc-server::CoreBridge` is what
// keeps the two agreeing on what a bit *means* — both ultimately wrap the
// same `sc_acl::Perms`, and
// `crates/sc-core/src/tests_acl_store.rs::perm_names_round_trip_every_bit`
// plus this crate's own equivalent pin the exact spelling on each side.

/// Who a grant applies to. `sc_acl::Principal` isn't reused directly: that
/// type derives no `Serialize`/`Deserialize` (this crate is the only one
/// that needs to put it on the wire), and its two variants carry a
/// `sc_vfs::UserId`/`GroupId` rather than a bare `u32`. The adjacently-tagged
/// shape (`tag = "kind", content = "id"`) is chosen deliberately: it
/// serializes to exactly `{"kind":"user","id":7}`, the flat two-field object
/// `web/src/lib/api/types.ts::GrantPrincipal` already declares.
#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(tag = "kind", content = "id", rename_all = "snake_case")]
pub enum GrantPrincipal {
    User(u32),
    Group(u32),
}

/// A share this deployment has registered (`GET /api/admin/shares`). Serves
/// two consumers: the grant-creation screen's picker (which only reads
/// `id`/`name`), and the share management screen,
/// which is the reason `host_path` is here — an admin adding/editing a
/// folder share has to see and set where it points on the host, unlike every
/// other surface below `sc-vfs` ('s "never leak a host
/// path" rule is about *request-handling* responses/errors/logs to
/// non-admins, not this trusted admin-configuration screen).
#[derive(Clone, Debug, Serialize)]
pub struct AdminShareInfo {
    pub id: u32,
    pub name: String,
    pub host_path: String,
    /// `true` for a share declared in `config.toml`. It can still be renamed,
    /// repointed and trash-toggled here — those are kept as overrides in
    /// `shares.db` that `register_shares` reapplies at startup — but it
    /// cannot be *deleted*, since the config entry would re-declare it on the
    /// next restart (`sc_core::Core::delete_share`). The UI hides only the
    /// delete affordance, and labels the row so the operator knows the config
    /// file no longer describes it.
    pub config_defined: bool,
    /// Off by default for every share (`sc_vfs::SharePolicy::default`).
    pub trash_enabled: bool,
}

/// `POST /api/admin/shares` request body.
#[derive(Clone, Debug, Deserialize)]
pub struct ShareCreateReq {
    pub name: String,
    pub host_path: String,
}

/// `PATCH /api/admin/shares/{id}` request body — all fields optional, so a
/// rename need not resend the host path and vice versa, and either can be
/// sent together with or without a trash toggle.
#[derive(Clone, Debug, Default, Deserialize)]
pub struct SharePatchReq {
    pub name: Option<String>,
    pub host_path: Option<String>,
    pub trash_enabled: Option<bool>,
}

/// The eight permission bits' wire names for the admin grant API — see the
/// section doc above for why this restates rather than shares
/// `sc_core::acl_store::PERM_NAMES`.
pub const GRANT_PERM_NAMES: [(Perms, &str); 8] = [
    (Perms::READ, "read"),
    (Perms::WRITE, "write"),
    (Perms::CREATE, "create"),
    (Perms::DELETE, "delete"),
    (Perms::RENAME, "rename"),
    (Perms::MOVE, "move"),
    (Perms::SHARE, "share"),
    (Perms::DOWNLOAD, "download"),
];

/// `Perms` -> its set bits' names, in `GRANT_PERM_NAMES` order.
pub fn grant_perm_names(p: Perms) -> Vec<&'static str> {
    GRANT_PERM_NAMES.iter().filter(|(bit, _)| p.contains(*bit)).map(|(_, name)| *name).collect()
}

/// Names -> `Perms`. Unrecognized names are ignored rather than rejected —
/// same contract as `sc_core::acl_store::perms_from_names`; a caller that
/// needs "unknown name" to be a hard error checks the round trip itself
/// (`grant_perm_names(grant_perms_from_names(names)).len() == names.len()`).
pub fn grant_perms_from_names<S: AsRef<str>>(names: &[S]) -> Perms {
    let mut out = Perms::empty();
    for n in names {
        if let Some((bit, _)) = GRANT_PERM_NAMES.iter().find(|(_, name)| *name == n.as_ref()) {
            out |= *bit;
        }
    }
    out
}

fn serialize_grant_perms<S: serde::Serializer>(p: &Perms, s: S) -> Result<S::Ok, S::Error> {
    grant_perm_names(*p).serialize(s)
}

/// One persisted grant, as `CoreApi`'s caller sees it — already wire-shaped
/// (`created_ns` baked to a JSON-string nanosecond timestamp by whichever
/// backend builds this, same convention as `ShareLinkInfo::created_ns`
/// below), unlike most of this file's *request*-side types, which stay as
/// rich Rust values until a handler needs to answer.
#[derive(Clone, Debug, Serialize)]
pub struct GrantInfo {
    pub id: u32,
    pub principal: GrantPrincipal,
    pub share: u32,
    /// Share-relative, `''` = the share's own root.
    pub subpath: String,
    #[serde(serialize_with = "serialize_grant_perms")]
    pub allow: Perms,
    #[serde(serialize_with = "serialize_grant_perms")]
    pub deny: Perms,
    pub inherit: bool,
    pub label: Option<String>,
    pub created_ns: String,
}

/// Narrows [`CoreApi::list_grants`] to one principal and/or one share. Both
/// `None` lists every grant in the deployment — the admin "all grants" view.
/// Not itself deserialized from JSON: `routes.rs` builds this from query
/// parameters, because "user id" and "group id" arrive as two different
/// query keys, not as one JSON-shaped `GrantPrincipal`.
#[derive(Clone, Copy, Debug, Default)]
pub struct GrantFilter {
    pub principal: Option<GrantPrincipal>,
    pub share: Option<u32>,
}

/// `POST /api/admin/grants` request body. Mirrors
/// `sc_core::acl_store::GrantSpec`, except `allow`/`deny` are still the
/// wire's name arrays here — [`Self::into_spec`] is where they become
/// `Perms`, once, rather than every call site re-deriving the mapping.
#[derive(Clone, Debug, Deserialize)]
pub struct GrantCreateReq {
    pub principal: GrantPrincipal,
    pub share: u32,
    pub subpath: String,
    #[serde(default)]
    pub allow: Vec<String>,
    #[serde(default)]
    pub deny: Vec<String>,
    #[serde(default)]
    pub inherit: bool,
    pub label: Option<String>,
}

/// What [`CoreApi::create_grant`] is asked to mint — [`GrantCreateReq`] after
/// its permission names have become `Perms`.
#[derive(Clone, Debug)]
pub struct GrantSpec {
    pub principal: GrantPrincipal,
    pub share: u32,
    pub subpath: String,
    pub allow: Perms,
    pub deny: Perms,
    pub inherit: bool,
    pub label: Option<String>,
}

impl GrantCreateReq {
    pub fn into_spec(self) -> GrantSpec {
        GrantSpec {
            principal: self.principal,
            share: self.share,
            subpath: self.subpath,
            allow: grant_perms_from_names(&self.allow),
            deny: grant_perms_from_names(&self.deny),
            inherit: self.inherit,
            label: self.label,
        }
    }
}

/// `PATCH /api/admin/grants/{id}` request body. `label` is a *double*
/// option so "absent" and "explicitly null" stay distinguishable — same
/// reason `ShareLinkPatch::label` above needs one: the difference between
/// leaving a label alone and clearing it back to the subpath-basename
/// fallback.
#[derive(Clone, Debug, Default, Deserialize)]
pub struct GrantPatchReq {
    #[serde(default)]
    pub allow: Option<Vec<String>>,
    #[serde(default)]
    pub deny: Option<Vec<String>>,
    #[serde(default)]
    pub inherit: Option<bool>,
    #[serde(default, deserialize_with = "double_option")]
    pub label: Option<Option<String>>,
}

/// A partial update — mirrors `sc_core::acl_store::GrantPatch`.
/// Principal/share/subpath aren't here for the same reason they aren't
/// there: those identify the grant, not describe it.
#[derive(Clone, Debug, Default)]
pub struct GrantPatch {
    pub allow: Option<Perms>,
    pub deny: Option<Perms>,
    pub inherit: Option<bool>,
    pub label: Option<Option<String>>,
}

impl GrantPatchReq {
    pub fn into_patch(self) -> GrantPatch {
        GrantPatch {
            allow: self.allow.map(|v| grant_perms_from_names(&v)),
            deny: self.deny.map(|v| grant_perms_from_names(&v)),
            inherit: self.inherit,
            label: self.label,
        }
    }
}

// ---------------------------------------------------------------- shares --
// / `/api/shares[/:id]`. The store
// itself lives in `sc-core` (`sc_core::LinkStore`) because both this crate and
// the compatibility layer are translations of the same rows; what is defined
// here is only the wire shape.

/// Permission set as it appears in a request body — the mirror image of
/// [`perms_to_json`]. Absent fields are `false`, so a client that sends
/// `{"read":true}` gets exactly `READ` and nothing implicit.
#[derive(Clone, Copy, Debug, Default, Deserialize)]
pub struct PermsReq {
    #[serde(default)]
    pub read: bool,
    #[serde(default)]
    pub write: bool,
    #[serde(default)]
    pub create: bool,
    #[serde(default)]
    pub delete: bool,
    #[serde(default)]
    pub rename: bool,
    #[serde(default, rename = "move")]
    pub move_: bool,
    #[serde(default)]
    pub share: bool,
    #[serde(default)]
    pub download: bool,
}

impl PermsReq {
    pub fn to_perms(self) -> Perms {
        let mut p = Perms::empty();
        p.set(Perms::READ, self.read);
        p.set(Perms::WRITE, self.write);
        p.set(Perms::CREATE, self.create);
        p.set(Perms::DELETE, self.delete);
        p.set(Perms::RENAME, self.rename);
        p.set(Perms::MOVE, self.move_);
        p.set(Perms::SHARE, self.share);
        p.set(Perms::DOWNLOAD, self.download);
        p
    }
}

/// One share link, as its **owner** sees it.
///
/// `token`/`url` are absent only when the row predates the sealed-token
/// column: the token is stored encrypted under the server master key, so a
/// later read can reproduce it as long as that key is available.
#[derive(Clone, Debug, Serialize)]
pub struct ShareLinkInfo {
    pub id: i64,
    pub path: String,
    #[serde(serialize_with = "serialize_perms")]
    pub perms: Perms,
    /// Nanoseconds as a JSON string — §1's time rule, same as `Entry`.
    pub expires_ns: Option<String>,
    pub max_downloads: Option<u32>,
    pub downloads: u32,
    pub label: Option<String>,
    pub has_password: bool,
    pub created_ns: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub token: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
}

#[derive(Clone, Debug, Deserialize)]
pub struct ShareLinkCreate {
    pub path: String,
    pub perms: Option<PermsReq>,
    pub password: Option<String>,
    /// Nanoseconds, as a string.
    pub expires_ns: Option<String>,
    pub max_downloads: Option<u32>,
    pub label: Option<String>,
}

/// A `PATCH` body. Each field is a *double* option so "absent" and
/// "explicitly null" stay distinguishable — that difference is the whole
/// difference between leaving a password alone and removing it.
#[derive(Clone, Debug, Default, Deserialize)]
pub struct ShareLinkPatch {
    #[serde(default)]
    pub perms: Option<PermsReq>,
    #[serde(default, deserialize_with = "double_option")]
    pub password: Option<Option<String>>,
    #[serde(default, deserialize_with = "double_option")]
    pub expires_ns: Option<Option<String>>,
    #[serde(default, deserialize_with = "double_option")]
    pub max_downloads: Option<Option<u32>>,
    #[serde(default, deserialize_with = "double_option")]
    pub label: Option<Option<String>>,
}

fn double_option<'de, D, T>(d: D) -> Result<Option<Option<T>>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de>,
{
    Option::deserialize(d).map(Some)
}

/// What an **anonymous visitor** is allowed to learn about a link.
///
/// Deliberately does not carry the host path, the owner, the virtual path, or
/// the existence of any other link on the same file ("must not leak the real path, owner, or the existence of other shares").
#[derive(Clone, Debug)]
pub struct PublicLink {
    pub id: i64,
    /// Basename of the shared item only.
    pub name: String,
    pub is_dir: bool,
    pub size: u64,
    pub mtime_ns: i128,
    pub has_password: bool,
    /// Upload-only link: no listing, no reading, no overwrite.
    pub is_drop: bool,
    pub can_download: bool,
    /// Needed to mint a signed content URL. `None` if no stable id exists.
    pub fid: Option<i64>,
    pub etag8: [u8; 8],
    pub label: Option<String>,
}

/// One node under a share link, as the public page needs it.
///
/// Mirrors the subset of [`PublicLink`] that describes the *resolved node*
/// rather than the link: `label`, `has_password`, `is_drop` and `can_download`
/// are properties of the link and stay on that document.
#[derive(Clone, Debug)]
pub struct PublicNode {
    pub name: String,
    pub is_dir: bool,
    pub size: u64,
    pub mtime_ns: i128,
    /// Needed to mint a signed content URL. `None` if no stable id exists.
    pub fid: Option<i64>,
    pub etag8: [u8; 8],
}

/// One row of a public listing.
///
/// A public document's shape is chosen rather than inherited. Serialising the
/// internal `Entry` shipped the file id, the ETag, the permission set, the
/// preview flag and the confusable-name flag to an anonymous caller, of which
/// the page reads three fields.
///
/// No `mtime_ns`, deliberately: the page has never shown a date per row, so
/// shipping the field would be the same mistake in smaller print. The link
/// target's own `mtime_ns` stays on the document, where it already is.
#[derive(Clone, Debug, Serialize)]
pub struct PublicEntry {
    pub name: String,
    pub kind: Kind,
    pub size: u64,
}

/// The `sc-core::Core` contract (see module docs). `async_trait`-free: every
/// method that needs to block on I/O is written as returning a boxed future
/// so the trait stays object-safe (`Arc<dyn CoreApi>` in `AppState`).
pub trait CoreApi: Send + Sync {
    fn resolve(&self, _user: UserId, _vpath: &str) -> Result<Resolved, CoreError> {
        Err(not_wired())
    }
    /// Which `ShareId` a virtual path's label maps to for `user` —
    /// `sc_http::middleware::scope_gate` needs this to enforce
    /// `sc_auth::Scope::shares` ('s other half; see
    /// that module's doc comment for why the check has to live at that one
    /// choke point rather than in every handler).
    ///
    /// The default delegates to `resolve`, which every real backend already
    /// implements — so this works today with no further wiring — but
    /// carries `resolve`'s `Perms::READ` requirement along with it: a
    /// share-scoped app password needs `READ` on a path just to learn which
    /// share it is in, even when the operation it is actually calling for
    /// needs a different bit. `sc_core::Core::resolve_share` answers the
    /// same question without checking any permission at all; a backend can
    /// override this method to call that directly and drop the caveat.
    fn resolve_share(&self, user: UserId, vpath: &str) -> Result<ShareId, CoreError> {
        self.resolve(user, vpath).map(|r| r.share)
    }
    fn roots(&self, _user: UserId) -> Vec<RootEntry> {
        Vec::new()
    }
    fn list(&self, _user: UserId, _vpath: &str, _sort: SortKey, _order: Order) -> Result<Listing, CoreError> {
        Err(not_wired())
    }
    fn stat_entry(&self, _user: UserId, _vpath: &str) -> Result<Entry, CoreError> {
        Err(CoreError::NotFound)
    }
    fn mkdir(&self, _user: UserId, _vpath: &str) -> Result<Entry, CoreError> {
        Err(not_wired())
    }
    fn rename(&self, _user: UserId, _vpath: &str, _new_name: &str) -> Result<Entry, CoreError> {
        Err(not_wired())
    }
    fn move_entries(
        &self,
        _user: UserId,
        _paths: &[String],
        _dest: &str,
        _on_conflict: OnConflict,
        _if_match: &HashMap<String, Etag>,
    ) -> Result<Vec<OpResult>, CoreError> {
        Err(not_wired())
    }
    /// inspect a move without mutating anything, so the
    /// caller can warn "this will copy, not move" (cross-device / cross-share)
    /// before the user commits. A default that answers `not_wired()` rather
    /// than a blanket "no copy needed" is deliberate: a caller that forgets to
    /// wire this up should see an error, not a false negative on the exact
    /// warning it exists to give.
    fn move_entries_dry_run(
        &self,
        _user: UserId,
        _paths: &[String],
        _dest: &str,
        _on_conflict: OnConflict,
        _if_match: &HashMap<String, Etag>,
    ) -> Result<Vec<OpResult>, CoreError> {
        Err(not_wired())
    }
    fn copy_entries(
        &self,
        _user: UserId,
        _paths: &[String],
        _dest: &str,
        _on_conflict: OnConflict,
        _if_match: &HashMap<String, Etag>,
    ) -> Result<Vec<OpResult>, CoreError> {
        Err(not_wired())
    }
    fn delete(&self, _user: UserId, _paths: &[String], _permanent: bool) -> Result<Vec<OpResult>, CoreError> {
        Err(not_wired())
    }
    fn read_text(&self, _user: UserId, _vpath: &str) -> Result<String, CoreError> {
        Err(CoreError::NotFound)
    }
    fn write_text(&self, _user: UserId, _vpath: &str, _content: &str, _if_match: Option<&Etag>) -> Result<Entry, CoreError> {
        Err(not_wired())
    }
    fn trash_list(&self, _user: UserId) -> Result<Vec<TrashEntry>, CoreError> {
        Ok(Vec::new())
    }
    fn trash_restore(&self, _user: UserId, _ids: &[String]) -> Result<Vec<OpResult>, CoreError> {
        Err(not_wired())
    }
    fn trash_purge(&self, _user: UserId, _ids: &[String]) -> Result<Vec<OpResult>, CoreError> {
        Err(not_wired())
    }
    fn aggregate(&self, _share: ShareId, _subpath: &SafePath) -> anyhow::Result<Aggregate> {
        Ok(Aggregate { file_count: 0, dir_count: 0, total_bytes: 0 })
    }
    /// Subpaths below `root` that deny `user` something `root` allows.
    ///
    /// Already on `sc_core::Core`, where it was built for SMB over-grant
    /// reporting: `smb.conf` has no per-path ACL, so a deny below a share root
    /// has to be reported rather than enforced. `GET /api/fs/size` asks the
    /// same question for a different reason, and gets to refuse rather than
    /// report.
    fn denies_below(&self, _user: UserId, _share: ShareId, _root: &SafePath) -> Vec<String> {
        Vec::new()
    }
    /// Every entry in a ZIP archive at `vpath`, `READ`-checked. `Ok(None)`
    /// means the file is not a zip, which the route answers identically to a
    /// path the caller cannot list.
    fn archive_list(&self, _user: UserId, _vpath: &str) -> Result<Option<ArchiveListingWire>, CoreError> {
        Err(not_wired())
    }
    /// `/api/admin/storage`. Lives on `CoreApi` rather than in a handler
    /// because only the backend knows which shares exist and where their
    /// `statvfs` comes from.
    fn storage_report(&self) -> Result<StorageReport, CoreError> {
        Err(not_wired())
    }
    /// `/api/admin/index/estimate`. Measured, not modelled: it samples the
    /// real corpus, so it has to run where the shares are.
    fn index_estimate(&self) -> Result<IndexEstimate, CoreError> {
        Err(not_wired())
    }

    /// `GET /api/admin/index/settings`.
    fn index_settings(&self) -> Result<IndexSettings, CoreError> {
        Err(not_wired())
    }

    /// `PATCH /api/admin/index/settings` — flips the persisted
    /// `name_enabled` override. Takes effect immediately for the next
    /// build/query; no restart involved (that is the entire point of #116).
    fn set_index_name_enabled(&self, _enabled: bool) -> Result<IndexSettings, CoreError> {
        Err(not_wired())
    }

    /// `POST /api/admin/index/build` — crawl every registered share and
    /// (re)build its T3 name index, reporting progress through
    /// `on_progress(entries_visited, current_share_label)` and stopping
    /// early wherever `should_cancel()` turns true. Refuses with
    /// `CoreError::NotSupported` when the name index is switched off
    /// (`index_settings().name_enabled == false`) — starting a build for a
    /// feature the admin has not turned on would silently plant
    /// `.scindex/` directories the "off by default" invariant promises
    /// won't exist.
    fn build_name_indexes(
        &self,
        _on_progress: &dyn Fn(u64, Option<String>),
        _should_cancel: &dyn Fn() -> bool,
    ) -> Result<Vec<IndexBuildResult>, CoreError> {
        Err(not_wired())
    }

    /// `GET /api/admin/shares` — every share this deployment has registered,
    /// for the grant-creation screen's picker. Never `not_wired()` by
    /// default (unlike almost everything else here): an empty list is a
    /// perfectly honest answer for a backend with no shares configured yet,
    /// whereas an error would read as "the grant feature is broken".
    fn admin_shares(&self) -> Vec<AdminShareInfo> {
        Vec::new()
    }

    /// `POST /api/admin/shares` — register a new folder share
    /// A real backend validates `host_path`
    /// explicitly (nonexistent / not a directory / unreadable / overlapping
    /// an existing share) and reports which one via
    /// `CoreError::InvalidName`, rather than a generic failure.
    fn create_share(&self, _req: ShareCreateReq) -> Result<AdminShareInfo, CoreError> {
        Err(not_wired())
    }

    /// `PATCH /api/admin/shares/{id}` — rename and/or repoint a share this
    /// deployment created at runtime. Refuses a `config.toml`-defined share
    /// (`CoreError::InvalidName`): editing it here would be silently undone
    /// on the next restart.
    fn update_share(&self, _id: u32, _patch: SharePatchReq) -> Result<AdminShareInfo, CoreError> {
        Err(not_wired())
    }

    /// `DELETE /api/admin/shares/{id}` — unregister a share this deployment
    /// created at runtime. Same `config.toml` refusal as `update_share`.
    fn delete_share(&self, _id: u32) -> Result<(), CoreError> {
        Err(not_wired())
    }

    /// `GET /api/admin/grants[?...]` — every persisted grant matching
    /// `filter`, the admin "who can see what" surface
    /// (`sc_core::Core::list_grants`).
    fn list_grants(&self, _filter: GrantFilter) -> Result<Vec<GrantInfo>, CoreError> {
        Err(not_wired())
    }

    /// `POST /api/admin/grants`. Refuses a spec with neither `allow` nor
    /// `deny` set — `sc_core::Core::create_grant` is where that refusal
    /// actually happens; a real backend surfaces it as
    /// `CoreError::InvalidName`.
    fn create_grant(&self, _spec: GrantSpec) -> Result<GrantInfo, CoreError> {
        Err(not_wired())
    }

    /// `PATCH /api/admin/grants/{id}`.
    fn update_grant(&self, _id: u32, _patch: GrantPatch) -> Result<GrantInfo, CoreError> {
        Err(not_wired())
    }

    /// `DELETE /api/admin/grants/{id}`.
    fn delete_grant(&self, _id: u32) -> Result<(), CoreError> {
        Err(not_wired())
    }

    /// Push a freshly recomputed group-membership map into the live ACL
    /// engine (`sc_core::Core::set_group_memberships`) — called after every
    /// group/membership mutation the admin API makes,
    /// so the change is visible immediately rather than only after the next
    /// restart's `sc-server::app::project_grants`. Default no-op: only a
    /// real backend has an `AclEngine` to push into; test doubles have no
    /// groups to evaluate.
    fn refresh_group_memberships(&self, _m: HashMap<UserId, Vec<sc_vfs::GroupId>>) {}

    /// `POST /api/fs/archive`: enumerate `vpath`
    /// (file or directory) top to bottom, calling `visit` once per
    /// descendant. ACL is re-checked per entry and gates *descent* — an
    /// unreadable subtree is reported once (`readable: false`) and never
    /// entered. For a readable file, `visit` also receives a reader open for
    /// exactly the duration of the call, so the archive handler can stream
    /// its bytes straight into the zip without a second round trip.
    ///
    /// Only the *root* failing to resolve is a hard `Err` — everything below
    /// it that turns out unreadable is reported through `visit`, never as an
    /// error for the whole archive.
    fn archive_walk(
        &self,
        _user: UserId,
        _vpath: &str,
        _visit: &mut dyn FnMut(&WalkEntry, Option<&mut dyn std::io::Read>),
    ) -> Result<(), CoreError> {
        Err(not_wired())
    }

    // --------------------------------------------------------------- smb --

    /// Whether this deployment has SMB switched on at all
    /// (`sc_smb::SmbConfig.enabled`). Drives the
    /// `features.smb` capability the same way `shares_enabled` below drives
    /// `features.shares` — so the settings screen's SMB section only renders
    /// when there is something behind it to enable, and does not draw a
    /// section that every toggle would silently no-op against.
    fn smb_enabled(&self) -> bool {
        false
    }

    // ------------------------------------------------------------ shares --

    /// Whether this deployment has a share-link store at all. Drives the
    /// `features.shares` capability so the UI does not draw a button that
    /// every click will 501 on.
    fn shares_enabled(&self) -> bool {
        false
    }
    fn share_link_list(&self, _user: UserId, _path: Option<&str>) -> Result<Vec<ShareLinkInfo>, CoreError> {
        Err(CoreError::NotSupported)
    }
    fn share_link_get(&self, _user: UserId, _id: i64) -> Result<ShareLinkInfo, CoreError> {
        Err(CoreError::NotSupported)
    }
    /// Returns the row **and the plaintext token**, which exists only here.
    fn share_link_create(&self, _user: UserId, _req: &ShareLinkCreate) -> Result<(ShareLinkInfo, String), CoreError> {
        Err(CoreError::NotSupported)
    }
    fn share_link_update(&self, _user: UserId, _id: i64, _patch: &ShareLinkPatch) -> Result<ShareLinkInfo, CoreError> {
        Err(CoreError::NotSupported)
    }
    fn share_link_delete(&self, _user: UserId, _id: i64) -> Result<(), CoreError> {
        Err(CoreError::NotSupported)
    }

    /// Token → link id, with **no filesystem check**.
    ///
    /// Split out from `share_link_public` on purpose: the password endpoint
    /// must behave identically for "no such token" and "wrong password", so it
    /// cannot be the thing that discovers a link is `Gone`.
    fn share_link_lookup(&self, _token: &str) -> Result<Option<i64>, CoreError> {
        Err(CoreError::NotSupported)
    }
    /// Public view of a live link. `Err(Gone)` once expiry, the download cap,
    /// or the path+fileid cross-check says the link is dead.
    fn share_link_public(&self, _id: i64) -> Result<PublicLink, CoreError> {
        Err(CoreError::NotSupported)
    }
    /// One node under a link, named by a path relative to the link's own
    /// target. An empty `path` is the target itself.
    fn share_link_node(&self, _id: i64, _path: &str) -> Result<PublicNode, CoreError> {
        Err(not_wired())
    }
    /// Children of one directory under a link, in the public projection.
    fn share_link_entries_at(&self, _id: i64, _path: &str) -> Result<Vec<PublicEntry>, CoreError> {
        Err(not_wired())
    }
    /// Stream a ZIP of one directory under a link into `out`.
    ///
    /// `Send` on the writer is load-bearing: the walk runs in
    /// `spawn_blocking` and the writer is the sending half of the response
    /// body's channel.
    fn share_link_archive(
        &self,
        _id: i64,
        _path: &str,
        _out: &mut (dyn std::io::Write + Send),
    ) -> Result<(), CoreError> {
        Err(not_wired())
    }
    /// CPU-bound (Argon2). Callers on the async path must `spawn_blocking`.
    /// An id that does not exist still performs the hash before answering.
    fn share_link_check_password(&self, _id: i64, _candidate: &str) -> Result<bool, CoreError> {
        Ok(false)
    }
    /// Atomic increment against the cap. `Err(Gone)` when it is spent.
    fn share_link_note_download(&self, _id: i64) -> Result<(), CoreError> {
        Err(CoreError::NotSupported)
    }
    fn share_link_drop(&self, _id: i64, _name: &str, _body: &[u8]) -> Result<Entry, CoreError> {
        Err(CoreError::NotSupported)
    }
}

fn not_wired() -> CoreError {
    CoreError::Internal("sc-core not wired yet".into())
}

/// A `CoreApi` that reports every operation as not-yet-wired (the trait's
/// default bodies — see above). Used as `AppState::core`'s default until
/// `sc-core` lands and a real implementation is plugged in; also handy for
/// exercising the HTTP layer (middleware, routing, error mapping) in tests
/// without needing a real backend.
pub struct UnimplementedCore;

impl CoreApi for UnimplementedCore {}
