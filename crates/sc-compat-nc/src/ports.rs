//! # The seam
//!
//! `sc-compat-nc` is only allowed to consume the *public* APIs of `sc-core`,
//! `sc-dav`, `sc-auth`, `sc-meta`, `sc-acl` and `sc-upload` (see
//! `docs/), and no core crate may depend on this one.
//!
//! Every **data type** in this module is now the real upstream type,
//! re-exported. What remains defined here is exactly two things:
//!
//! * the **port traits** (`CorePort`, `AuthPort`, `SharePort`, `PreviewPort`,
//!   `UploadEngine`) — these are not mirrors of anything, they are the
//!   dependency-inversion boundary itself, implemented by `sc-server`;
//! * the handful of **request/response shapes** those traits take, which have
//!   no upstream counterpart because nothing in the core needs them.
//!
//! Nothing in here introduces compat vocabulary. That is deliberate: this
//! is the *core-facing* side of the boundary, and the CI isolation gate
//! exists precisely to keep NC words out of it.

use std::sync::Arc;

// ---------------------------------------------------------------------------
// ids, permissions, entries — the real types
// ---------------------------------------------------------------------------

pub use sc_vfs::{FileId, GroupId, Kind, ShareId, UserId};

// There is deliberately no `ShareRoot` re-export. The real one
// (`sc_vfs::ShareRoot`) is a live directory handle, and this crate never
// dereferences it -- it only ever hands it straight back to a port. Taking
// a `ShareId` says exactly that, and keeps the compatibility layer from
// carrying an open descriptor it has no use for; `sc-server` maps the id to
// the handle at the one place that actually opens a file.

pub use sc_acl::Perms;

/// Number of distinct `Perms` values. Used by the golden permission test to
/// enumerate the whole space.
pub const PERMS_CARDINALITY: u16 = 1 << 8;

pub use sc_core::Entry;

/// The two path vocabularies, straight from the crate that owns the
/// distinction. A `Vpath` is what a client names a file by (`{label}/{rest}`);
/// a `SharePath` is what the core hands back (relative to the share root, the
/// grant's subpath already on the front). Prefixing a label onto the second
/// without stripping that subpath is the mistake these types exist to stop, so
/// nothing in this crate does the conversion itself: `Core::vpath_for` does.
pub use sc_core::{SharePath, Vpath};

/// Recursive rollup of a directory subtree.
///
/// `sc-meta` keeps one recursive *count*, not a file/directory split, because
/// the aggregate exists to answer "did anything under here change, and how
/// big is it" — `oc:size` needs exactly `rsize` and nothing else.
pub use sc_meta::Aggregate;

pub use sc_upload::SessionId;

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

#[derive(Debug, thiserror::Error)]
pub enum PortError {
    #[error("not found")]
    NotFound,
    #[error("forbidden")]
    Forbidden,
    #[error("invalid argument: {0}")]
    Invalid(String),
    #[error("conflict: {0}")]
    Conflict(String),
    #[error("backend failure: {0}")]
    Backend(String),
}

pub type PortResult<T> = Result<T, PortError>;

// ---------------------------------------------------------------------------
// sc-dav decorator hook
// ---------------------------------------------------------------------------

/// Everything a `PropSource` may know about the request that is not the entry
/// itself. Owned rather than borrowed so the trait signature can stay
/// `&PropCtx` with no lifetime parameter, exactly as `sc-dav` publishes it.
#[derive(Clone, Debug)]
pub struct PropCtx {
    pub user: UserId,
    /// Login name of the requesting principal.
    pub user_name: String,
    pub share: ShareId,
    /// The entry's path relative to its share root, when the host adapter
    /// could resolve one. `None` means the size property has to be omitted
    /// rather than guessed at: the field used to be a `String` documented as
    /// share-relative and filled with a vpath, which is how `oc:size` ended up
    /// asking the aggregate cache about a path that does not exist.
    pub share_path: Option<SharePath>,
    /// Login name of the owner of the share this entry lives in.
    pub owner_name: String,
    pub owner_display_name: String,
}

/// The set of properties a PROPFIND asked for.
#[derive(Clone, Debug, Default)]
pub struct PropReq {
    /// `<d:allprop/>` was used. Sources should emit their cheap properties.
    pub allprop: bool,
    /// Explicit `(namespace, local-name)` requests.
    pub names: Vec<(String, String)>,
}

impl PropReq {
    pub fn explicit<I, A, B>(it: I) -> Self
    where
        I: IntoIterator<Item = (A, B)>,
        A: Into<String>,
        B: Into<String>,
    {
        Self {
            allprop: false,
            names: it.into_iter().map(|(a, b)| (a.into(), b.into())).collect(),
        }
    }

    pub fn allprop() -> Self {
        Self { allprop: true, names: Vec::new() }
    }

    pub fn wants(&self, ns: &str, name: &str) -> bool {
        if self.allprop {
            return true;
        }
        self.names.iter().any(|(n, l)| n == ns && l == name)
    }
}

/// A property value as it will be serialised into the `<d:prop>` element.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum PropValue {
    /// Character data. Escaped at serialisation time.
    Text(String),
    /// Zero or more child elements, each `(namespace, local-name, text)`.
    /// Used for `oc:share-types` and `oc:checksums`.
    Children(Vec<(&'static str, &'static str, String)>),
    /// Present but empty, e.g. `<oc:checksums/>`.
    Empty,
}

/// Sink handed to a `PropSource`.
#[derive(Clone, Debug, Default)]
pub struct PropWriter {
    props: Vec<(&'static str, &'static str, PropValue)>,
}

impl PropWriter {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn text(&mut self, ns: &'static str, name: &'static str, v: impl Into<String>) {
        self.props.push((ns, name, PropValue::Text(v.into())));
    }

    pub fn num(&mut self, ns: &'static str, name: &'static str, v: impl std::fmt::Display) {
        self.props.push((ns, name, PropValue::Text(v.to_string())));
    }

    /// The reference server serialises booleans in the `nc:` namespace as the JSON
    /// literals `true`/`false`, not as `1`/`0`. See
    /// `FilesPlugin::handleGetProperties` for `nc:has-preview`, which is
    /// `json_encode($previewManager->isAvailable(...))`.
    pub fn json_bool(&mut self, ns: &'static str, name: &'static str, v: bool) {
        self.props
            .push((ns, name, PropValue::Text(if v { "true" } else { "false" }.into())));
    }

    pub fn children(
        &mut self,
        ns: &'static str,
        name: &'static str,
        v: Vec<(&'static str, &'static str, String)>,
    ) {
        self.props.push((ns, name, PropValue::Children(v)));
    }

    pub fn empty(&mut self, ns: &'static str, name: &'static str) {
        self.props.push((ns, name, PropValue::Empty));
    }

    pub fn as_slice(&self) -> &[(&'static str, &'static str, PropValue)] {
        &self.props
    }

    pub fn get(&self, ns: &str, name: &str) -> Option<&PropValue> {
        self.props
            .iter()
            .find(|(n, l, _)| *n == ns && *l == name)
            .map(|(_, _, v)| v)
    }

    pub fn len(&self) -> usize {
        self.props.len()
    }

    pub fn is_empty(&self) -> bool {
        self.props.is_empty()
    }
}

/// The decorator hook, in *structured* form.
///
/// This is not `sc_dav::PropSource`, and the difference is not accidental.
/// `sc-dav`'s writer emits XML directly, with a namespace prefix, into the
/// response buffer; this one accumulates `(namespace, name, value)` triples.
/// The structured form is what makes the golden tests in `props.rs` able to
/// assert on `oc:permissions` as a *value* — the letter order in that string
/// is the highest-risk thing in this crate — instead of on a substring of
/// serialised XML, and it is what lets a value be inspected after the fact.
///
/// `sc-server` adapts one to the other when it registers this source with
/// `DavService::add_prop_source`; that adapter is ~40 lines and lives with
/// the rest of the wiring. Neither side has to give up its shape.
///
/// `PropCtx` likewise carries owner login/display names, which `sc-dav`'s
/// context does not: DAV has no owner concept, and pushing one into it would
/// be putting this layer's needs into the protocol crate.
pub trait PropSource: Send + Sync {
    /// `(prefix, namespace-uri)` pairs this source needs declared on the
    /// multistatus root.
    fn namespaces(&self) -> &[(&'static str, &'static str)];

    fn emit(&self, e: &Entry, ctx: &PropCtx, req: &PropReq, out: &mut PropWriter);
}

// ---------------------------------------------------------------------------
// sc-upload
// ---------------------------------------------------------------------------

/// `SpoolMode::NameOrdered` is the mode this crate's chunking protocol maps
/// onto. It lives in `sc-upload` rather than here because "assemble in
/// ascending name order" is a spool strategy, not a vendor quirk — the engine
/// would need it for any protocol that numbers its chunks instead of
/// addressing them by offset.
pub use sc_upload::SpoolMode;

/// What this layer knows when it opens a session. Deliberately *not*
/// `sc_upload::SessionSpec`: the engine's version takes a resolved
/// `SafePath` and a share, which is the shape you have after ACL resolution.
/// A compat handler has only a user-supplied string at that point, and
/// resolving it is the adapter's job, not this crate's.
#[derive(Clone, Debug)]
pub struct SessionSpec {
    pub mode: SpoolMode,
    /// Full vpath (`{label}/{rest}`) of the destination, exactly as the
    /// client's `Destination` header named it once the DAV prefix is
    /// stripped — **not** a path already relative to some share. There is no
    /// share to give the adapter in advance: resolving which grant-projected
    /// label this names *is* what decides the share, so `create` below takes
    /// no `ShareId` parameter and hands one back instead.
    pub dest: String,
    pub owner: UserId,
    /// Total length when the client volunteered it at session creation.
    pub total_len: Option<u64>,
}

pub trait UploadEngine: Send + Sync {
    /// Resolves `spec.dest` against `spec.owner`'s grants (WRITE-checked)
    /// and opens a session there, returning the `ShareId` that resolution
    /// landed on alongside the session id. There is deliberately no `share`
    /// input parameter — see `SessionSpec::dest`'s doc comment for why
    /// passing one in would have nothing correct to be checked against.
    fn create(&self, spec: SessionSpec) -> PortResult<(ShareId, SessionId)>;
    fn put_named(
        &self,
        share: ShareId,
        session: SessionId,
        user: UserId,
        name: u32,
        data: &[u8],
    ) -> PortResult<()>;
    fn assemble_and_finalize(
        &self,
        share: ShareId,
        session: SessionId,
        user: UserId,
        total: u64,
        mtime_ns: Option<i128>,
    ) -> PortResult<()>;
    fn list_chunks(&self, session: SessionId) -> PortResult<Vec<u32>>;
    /// Bytes durably received for this session — the contiguous prefix of the
    /// assembled file.
    ///
    /// Both mobile clients need this, for different reasons:
    ///
    /// * The Android client resumes by *summing the `d:getcontentlength` of
    ///   every chunk we list* and continuing from that byte
    ///   (`ChunkedFileUploadRemoteOperation.java:192`, `nextByte += …`). A
    ///   listing without lengths makes it restart at byte 0 while continuing
    ///   the chunk *numbering*, which assembles a corrupt file rather than
    ///   failing.
    /// * The Android client never sends `OC-Total-Length` on the final `MOVE`
    ///   (`ChunkedFileUploadRemoteOperation.java:216-225` sets only
    ///   `X-OC-Mtime`/`X-OC-Ctime`), so the assemble step has to be able to
    ///   determine the expected length itself.
    fn received_len(&self, session: SessionId, user: UserId) -> PortResult<u64>;
    fn abort(&self, session: SessionId, user: UserId) -> PortResult<()>;
}

// ---------------------------------------------------------------------------
// sc-auth
// ---------------------------------------------------------------------------

/// Upper bound on what an issued credential may do.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Scope {
    pub perms: Perms,
    /// `None` = every share the user can reach.
    pub share: Option<ShareId>,
}

impl Scope {
    pub fn full() -> Self {
        Self { perms: Perms::all(), share: None }
    }
}

#[derive(Clone, Debug)]
pub struct Principal {
    pub user: UserId,
    pub login_name: String,
    pub display_name: String,
    /// Identifies the credential this request authenticated with, so a handler
    /// can revoke exactly that one. `None` for a browser session, which is
    /// revoked through its own path and never through the app-password API.
    pub credential_id: Option<u32>,
}

/// The address the *host application* decided this request came from.
///
/// This crate deliberately does not derive it. Working out which of the peer
/// address, `CF-Connecting-IP` and `X-Forwarded-For` to believe is a single
/// security rule that belongs to whatever terminates the connection, and a
/// second copy of it living here would be a second copy to get wrong. So the
/// host resolves it once, in front of every mount, and leaves the answer in
/// request extensions; this extractor only reads it.
///
/// Absent (a bare test request, or a host that forgot to wire it) resolves to
/// `0.0.0.0` — `Ipv4Addr::UNSPECIFIED`, which is nobody's source address and
/// therefore its own rate-limit bucket, shared with no real client.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ClientAddr(pub std::net::IpAddr);

impl Default for ClientAddr {
    fn default() -> Self {
        ClientAddr(std::net::IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED))
    }
}

impl<S: Send + Sync> axum::extract::FromRequestParts<S> for ClientAddr {
    // Infallible on purpose: a missing extension must degrade to "unknown
    // client", never to a 500 on an endpoint that would otherwise work.
    type Rejection = std::convert::Infallible;

    async fn from_request_parts(
        parts: &mut axum::http::request::Parts,
        _state: &S,
    ) -> Result<Self, Self::Rejection> {
        Ok(parts.extensions.get::<ClientAddr>().copied().unwrap_or_default())
    }
}

pub trait AuthPort: Send + Sync {
    /// Returns `(credential id, plaintext secret)`. The plaintext is the only
    /// time we ever see it.
    fn issue_app_password(
        &self,
        user: UserId,
        name: &str,
        scope: Scope,
    ) -> PortResult<(u32, String)>;

    /// `from` is the resolved client address ([`ClientAddr`]). It is not
    /// advisory: the implementation feeds it to the per-IP brute-force gate
    /// and the audit log, so a wrong value here disables both.
    fn verify_basic(
        &self,
        login: &str,
        secret: &str,
        from: ClientAddr,
    ) -> PortResult<Option<Principal>>;

    fn validate_session(&self, token: &str) -> PortResult<Option<Principal>>;

    /// Revoke one app password belonging to `user`. Idempotent: revoking an
    /// already-revoked or non-existent credential is `Ok(())`, so a client that
    /// retries its logout does not see an error it cannot act on.
    fn revoke_app_password(&self, user: UserId, credential: u32) -> PortResult<()>;

    /// Whether the device holding `credential` has been marked as lost.
    ///
    /// Answering `false` is always safe: the client treats anything other than
    /// an explicit yes as "no wipe".
    fn wipe_requested(&self, credential: u32) -> PortResult<bool>;

    /// The device reported that it has erased its local copies. Retires the
    /// credential.
    fn finish_wipe(&self, credential: u32) -> PortResult<()>;
}

// ---------------------------------------------------------------------------
// sc-core
// ---------------------------------------------------------------------------

#[derive(Clone, Debug)]
pub struct UserInfo {
    pub id: UserId,
    pub login_name: String,
    pub display_name: String,
    pub email: Option<String>,
    pub enabled: bool,
    pub groups: Vec<String>,
    pub language: String,
    pub locale: String,
}

/// Storage accounting for one share, as reported by `statvfs` plus any
/// configured per-user cap.
#[derive(Clone, Copy, Debug)]
pub struct Quota {
    pub used: u64,
    pub free: u64,
    /// `None` = unlimited. Compat translates this to the NC sentinel.
    pub total: Option<u64>,
}

pub trait CorePort: Send + Sync {
    /// The share a user's DAV home maps to, plus its root.
    fn home_root(&self, user: UserId) -> PortResult<ShareId>;

    // None of the three below takes a `ShareId`. The share is not an input:
    // it is what resolving the vpath's label decides. Passing one in gave the
    // host adapter a share it then had to reconcile with the one the path
    // named, and it reconciled them by prefixing that share's label onto a
    // path that already carried its own.

    fn resolve(&self, user: UserId, path: &Vpath) -> PortResult<Entry>;

    fn list(&self, user: UserId, path: &Vpath) -> PortResult<Vec<Entry>>;

    fn stat_entry(&self, user: UserId, path: &Vpath) -> PortResult<Entry>;

    /// Takes the path rather than a file id because `Core::aggregate` takes a
    /// path and allocates the id itself, and because a share's own root has no
    /// `node` row to have an id at all.
    fn aggregate(&self, share: ShareId, path: &SharePath) -> PortResult<Aggregate>;

    fn user_info(&self, user: UserId) -> PortResult<UserInfo>;

    /// Another account by login name, or `None` when it is outside `scope` or
    /// does not exist.
    ///
    /// The two answers are deliberately the same one: this is an account-name
    /// oracle otherwise, and it is gated exactly the way the sharee search is
    /// for exactly that reason.
    fn user_info_by_login(
        &self,
        caller: UserId,
        login: &str,
        scope: GranteeScope,
    ) -> PortResult<Option<UserInfo>>;

    /// Which share's `statvfs` to report. See: the
    /// choice is genuinely ambiguous with multiple shares, so it is a
    /// configuration decision made on the core side, not here.
    fn quota(&self, user: UserId) -> PortResult<Quota>;

    /// Reverse lookup for the preview endpoint, which addresses by file id.
    /// Id-to-path is a metadata lookup, so it answers a share path; turning
    /// that into something addressable is [`CorePort::vpath_for`]'s job, and
    /// the ACL check happens where the resulting path is used.
    fn locate(&self, user: UserId, id: FileId) -> PortResult<(ShareId, SharePath)>;

    /// The vpath a share path has in `user`'s own tree. `None` when no grant
    /// the user holds projects it, which callers addressing a file must treat
    /// as not-found.
    fn vpath_for(&self, user: UserId, share: ShareId, path: &SharePath) -> Option<Vpath>;
}

// ---------------------------------------------------------------------------
// share links / grants  (sc-core LinkStore)
// ---------------------------------------------------------------------------

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum GranteeKind {
    User,
    Group,
    /// Anonymous public link.
    Link,
}

#[derive(Clone, Debug)]
pub struct CoreShare {
    pub id: u64,
    pub kind: GranteeKind,
    /// Login/group name for User/Group grants, `None` for links.
    pub grantee: Option<String>,
    pub grantee_display: Option<String>,
    pub owner: String,
    pub owner_display: String,
    pub perms: Perms,
    /// Seconds since epoch.
    pub created_s: i64,
    /// Seconds since epoch.
    pub expires_s: Option<i64>,
    /// Public link token; `None` for user/group grants.
    pub token: Option<String>,
    pub has_password: bool,
    pub label: String,
    pub note: String,
    /// Vpath of the shared node with a single leading separator, resolved
    /// against the share this link actually belongs to. This is the value the
    /// client sees as `path` and `file_target`, and it must name the same node
    /// in the client's own file tree.
    pub path: String,
    pub kind_is_dir: bool,
    pub file_id: FileId,
    pub parent_file_id: Option<FileId>,
}

#[derive(Clone, Debug)]
pub struct ShareSpec {
    /// Path of the node being shared, as a **vpath**: `{label}/{rest}`, no
    /// leading and no trailing separator. Already normalised by
    /// `shares::normalise_client_path`, which is where client spelling quirks
    /// are handled. The host adapter passes it to `sc-core` unchanged and must
    /// not re-prefix it.
    pub path: String,
    pub kind: GranteeKind,
    pub grantee: Option<String>,
    pub perms: Perms,
    pub password: Option<String>,
    pub expires_s: Option<i64>,
    pub label: Option<String>,
    pub note: Option<String>,
}

#[derive(Clone, Debug, Default)]
pub struct ShareFilter {
    /// Vpath of the node to narrow to, normalised the same way
    /// [`ShareSpec::path`] is. `None` lists every link the caller owns.
    pub path: Option<String>,
    pub reshares: bool,
    /// Ask for links on the entries *inside* `path` rather than on `path`
    /// itself. Both apps use it to badge shared children in a listing.
    pub subfiles: bool,
    pub shared_with_me: bool,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum GranteeScope {
    /// Only principals that share a group with the searching user. Default:
    /// partial-match search over all accounts is an enumeration oracle.
    SameGroup,
    All,
    Off,
}

#[derive(Clone, Debug)]
pub struct GranteeCandidate {
    pub kind: GranteeKind,
    pub id: String,
    pub display: String,
    /// Secondary line, e.g. an email, when the display name is ambiguous.
    pub subline: Option<String>,
    /// The search term matched the identifier exactly.
    pub exact: bool,
}

pub trait SharePort: Send + Sync {
    fn list(&self, user: UserId, filter: &ShareFilter) -> PortResult<Vec<CoreShare>>;
    fn get(&self, user: UserId, id: u64) -> PortResult<CoreShare>;
    fn create(&self, user: UserId, spec: &ShareSpec) -> PortResult<CoreShare>;
    fn update(&self, user: UserId, id: u64, spec: &ShareSpec) -> PortResult<CoreShare>;
    fn delete(&self, user: UserId, id: u64) -> PortResult<()>;

    /// Grantee kinds attached to one node, for the `oc:share-types` property.
    fn kinds_for(&self, share: ShareId, id: FileId) -> PortResult<Vec<GranteeKind>>;

    fn find_grantees(
        &self,
        user: UserId,
        query: &str,
        scope: GranteeScope,
    ) -> PortResult<Vec<GranteeCandidate>>;

    /// Absolute URL of a public link on `origin`, which the caller picks with
    /// `NcConfig::canonical_for_host` so the link names the origin the client
    /// reached us on rather than a different one it may not be able to resolve.
    fn link_url(&self, origin: &str, token: &str) -> String;
}

// ---------------------------------------------------------------------------
// search
// ---------------------------------------------------------------------------

/// One search result, in the caller's own vocabulary.
#[derive(Clone, Debug)]
pub struct SearchHit {
    /// Vpath in the caller's own tree, no leading separator.
    pub path: String,
    pub entry: Entry,
}

/// Filename search, the same engine the DAV `SEARCH` method is answered from.
///
/// The unified-search screen and the DAV search box ask the same question, so
/// they go through one implementation; the difference is only the envelope.
pub trait SearchPort: Send + Sync {
    fn by_name(&self, user: UserId, term: &str, limit: u32) -> PortResult<Vec<SearchHit>>;
}

// ---------------------------------------------------------------------------
// previews
// ---------------------------------------------------------------------------

#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum FitMode {
    /// Fit inside the box, preserving aspect ratio.
    #[default]
    Contain,
    /// Fill the box, cropping the overflow.
    Cover,
}

pub trait PreviewPort: Send + Sync {
    /// Cheap "would a thumbnail exist for this?" test used by the
    /// `nc:has-preview` property. Must not do I/O.
    fn can_preview(&self, e: &Entry) -> bool;

    /// Mint a signed URL on the *content* origin. Returning `Ok(None)` means
    /// "no preview for this file" and results in a 404, never a placeholder
    /// icon served from the app origin.
    ///
    /// Takes a `Vpath` and no share, for the reason `CorePort::resolve` does:
    /// the share this addresses is whatever the vpath's label names, and
    /// accepting a separate one is what made every path-addressed thumbnail
    /// resolve a level too deep.
    fn signed_thumb_url(
        &self,
        user: UserId,
        path: &Vpath,
        w: u32,
        h: u32,
        fit: FitMode,
    ) -> PortResult<Option<String>>;

    /// A short-lived, read-only, `GET`-only signed URL for the whole file on
    /// the content origin. Used by the media-streaming endpoint, which hands
    /// the URL to an external player process that carries no credentials.
    fn signed_download_url(&self, user: UserId, path: &Vpath) -> PortResult<Option<String>>;
}

// ---------------------------------------------------------------------------
// bundle
// ---------------------------------------------------------------------------

/// Everything compat needs from the rest of the server, in one struct so the
/// axum state stays a single `Arc`.
#[derive(Clone)]
pub struct Deps {
    pub core: Arc<dyn CorePort>,
    pub auth: Arc<dyn AuthPort>,
    pub upload: Arc<dyn UploadEngine>,
    pub shares: Arc<dyn SharePort>,
    pub preview: Arc<dyn PreviewPort>,
    pub search: Arc<dyn SearchPort>,
}
