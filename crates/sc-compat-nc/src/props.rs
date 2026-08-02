//! WebDAV property decoration.
//!
//! Registered with `sc-dav` through `DavService::add_prop_source`. Core DAV
//! emits the standard live properties; this source appends the `oc:`/`nc:`
//! vocabulary that compat clients require.
//!
//! Reference implementation:
//! - `apps/dav/lib/Connector/Sabre/FilesPlugin.php` (property constants and
//!   value formats)
//! - `apps/dav/lib/Connector/Sabre/SharesPlugin.php` (`oc:share-types`)
//! - `lib/public/Files/DavUtil.php` (`getDavPermissions`, `getDavFileId`)

use std::sync::Arc;

use crate::ports::{
    Entry, FileId, GranteeKind, Perms, PreviewPort, PropCtx, PropReq, PropSource, PropWriter,
    SharePort, ShareId,
};
use crate::store::NcStore;

pub const NS_OC: &str = "http://owncloud.org/ns";
pub const NS_NC: &str = "http://nextcloud.org/ns";
pub const NS_DAV: &str = "DAV:";

/// `(prefix, uri)` pairs, matching the prefixes the reference server registers in
/// `FilesPlugin::initialize` (`$server->xml->namespaceMap`).
pub const NC_NAMESPACES: &[(&str, &str)] = &[("oc", NS_OC), ("nc", NS_NC)];

/// `oc:id` — zero-padded file id concatenated with the instance id, no
/// separator.
///
/// ```text
/// lib/public/Files/DavUtil.php::getDavFileId()
///     $id = sprintf('%08d', $id);
///     return $id . $instanceId;
/// ```
///
/// `sprintf('%08d')` zero-pads to *at least* 8 digits and does not truncate,
/// which is exactly what `{:08}` does in Rust.
#[inline]
pub fn nc_id(file: FileId, instance_id: &str) -> String {
    format!("{:08}{}", file.0, instance_id)
}

/// # THE highest-risk function in this crate.
///
/// A wrong letter here makes desktop clients refuse to sync **without
/// reporting an error**, which is the single hardest failure mode to debug in
/// the whole compatibility surface.
///
/// ## Letter order
///
/// The order is not alphabetical and is not free to choose — some clients
/// string-compare the value. Taken verbatim from
/// `lib/public/Files/DavUtil.php::getDavPermissions()`, which appends in this
/// sequence:
///
/// ```text
///   S  isShared()
///   R  PERMISSION_SHARE
///   M  isMounted()
///   G  PERMISSION_READ
///   D  PERMISSION_DELETE
///   N  canRename()
///   V  PERMISSION_UPDATE
///   then, exclusively:
///     file:      W   if writable
///     directory: CK  if PERMISSION_CREATE
/// ```
///
/// So the maximal strings are `SRMGDNVW` for a file and `SRMGDNVCK` for a
/// directory.
///
/// ## Where we deliberately differ from the reference
///
/// * `M` (mounted / external storage) is **never emitted**. We have no external
///   storage concept ( non-goals), and claiming a
///   mount makes clients apply mount-specific move restrictions.
/// * The reference server derives `N` from `canRename()` (updateable, or deletable with a
///   creatable parent) and `V` from `PERMISSION_UPDATE`. We have distinct
///   `Perms::RENAME` and `Perms::MOVE` bits, so we map them directly per
/// This is strictly more expressive than the
///   reference; the letter *positions* are unchanged, which is what matters on
///   the wire.
/// * The reference server folds `WRITE` into `PERMISSION_UPDATE`, so a file with update
///   rights gets both `V` and `W`. We separate them: `Perms::MOVE` gives `V`,
///   `Perms::WRITE` gives `W`.
///
/// ## Consequences of getting a letter wrong (see §7.2 notes)
///
/// * missing `W` on a file  -> client treats it read-only and never uploads
///   local edits.
/// * missing `N`/`V`        -> client implements rename as delete + re-upload;
///   renaming a 1 GB directory re-uploads 1 GB.
/// * missing `C`/`K` on dir -> nothing can be created inside it.
/// * empty string           -> client ignores the entry entirely. A read-only
///   share root **must** still carry `G`.
pub fn oc_permissions(perms: Perms, is_dir: bool, shared: bool) -> String {
    // Longest possible output is 9 bytes ("SRGDNVCK" plus the unused 'M' slot).
    let mut s = String::with_capacity(9);
    if shared {
        s.push('S');
    }
    if perms.contains(Perms::SHARE) {
        s.push('R');
    }
    // 'M' intentionally never emitted — see doc comment.
    if perms.contains(Perms::READ) {
        s.push('G');
    }
    if perms.contains(Perms::DELETE) {
        s.push('D');
    }
    if perms.contains(Perms::RENAME) {
        s.push('N');
    }
    if perms.contains(Perms::MOVE) {
        s.push('V');
    }
    if is_dir {
        if perms.contains(Perms::CREATE) {
            s.push('C');
            s.push('K');
        }
    } else if perms.contains(Perms::WRITE) {
        s.push('W');
    }
    s
}

/// The OCS/`files_sharing` integer bitmask (`lib/public/Constants.php`).
pub mod share_perm_bits {
    pub const READ: u32 = 1;
    pub const UPDATE: u32 = 2;
    pub const CREATE: u32 = 4;
    pub const DELETE: u32 = 8;
    pub const SHARE: u32 = 16;
    pub const ALL: u32 = 31;
}

/// `Perms` -> compat share bitmask.
pub fn perms_to_nc_bits(p: Perms) -> u32 {
    let mut n = 0;
    if p.contains(Perms::READ) {
        n |= share_perm_bits::READ;
    }
    // NC's UPDATE covers "modify an existing thing", which for us is WRITE on a
    // file and RENAME/MOVE on any node. Any of them lights it up.
    if p.intersects(Perms::WRITE | Perms::RENAME | Perms::MOVE) {
        n |= share_perm_bits::UPDATE;
    }
    if p.contains(Perms::CREATE) {
        n |= share_perm_bits::CREATE;
    }
    if p.contains(Perms::DELETE) {
        n |= share_perm_bits::DELETE;
    }
    if p.contains(Perms::SHARE) {
        n |= share_perm_bits::SHARE;
    }
    n
}

/// Compat share bitmask -> `Perms`.
///
/// Returns `Err(bit)` for any bit outside `PERMISSION_ALL`. is explicit that unknown bits are rejected with 400 rather than silently
/// dropped: silently dropping them would grant less than the user asked for
/// while reporting success.
pub fn nc_bits_to_perms(n: u32) -> Result<Perms, u32> {
    let unknown = n & !share_perm_bits::ALL;
    if unknown != 0 {
        return Err(unknown);
    }
    let mut p = Perms::empty();
    if n & share_perm_bits::READ != 0 {
        p |= Perms::READ | Perms::DOWNLOAD;
    }
    if n & share_perm_bits::UPDATE != 0 {
        p |= Perms::WRITE | Perms::RENAME | Perms::MOVE;
    }
    if n & share_perm_bits::CREATE != 0 {
        p |= Perms::CREATE;
    }
    if n & share_perm_bits::DELETE != 0 {
        p |= Perms::DELETE;
    }
    if n & share_perm_bits::SHARE != 0 {
        p |= Perms::SHARE;
    }
    Ok(p)
}

/// `IShare::TYPE_*` (`lib/public/Share/IShare.php`). Only the three we support
/// are named; everything else is rejected at the API boundary.
pub mod share_type {
    pub const USER: i64 = 0;
    pub const GROUP: i64 = 1;
    pub const PUBLIC_LINK: i64 = 3;
}

pub fn grantee_kind_to_share_type(k: GranteeKind) -> i64 {
    match k {
        GranteeKind::User => share_type::USER,
        GranteeKind::Group => share_type::GROUP,
        GranteeKind::Link => share_type::PUBLIC_LINK,
    }
}

/// The `PropSource` we hand to `sc-dav`.
pub struct NcPropSource {
    store: Arc<dyn NcStore>,
    shares: Arc<dyn SharePort>,
    preview: Arc<dyn PreviewPort>,
    aggregate: Arc<dyn DirSize>,
    instance_id: String,
}

/// Recursive directory size lookup. Split out from `CorePort` so the property
/// source can be unit-tested without a whole core.
pub trait DirSize: Send + Sync {
    fn recursive_size(&self, share: ShareId, id: FileId) -> Option<u64>;
}

impl NcPropSource {
    pub fn new(
        store: Arc<dyn NcStore>,
        shares: Arc<dyn SharePort>,
        preview: Arc<dyn PreviewPort>,
        aggregate: Arc<dyn DirSize>,
        instance_id: String,
    ) -> Self {
        Self { store, shares, preview, aggregate, instance_id }
    }
}

impl PropSource for NcPropSource {
    fn namespaces(&self) -> &[(&'static str, &'static str)] {
        const NS: &[(&str, &str)] = &[("oc", NS_OC), ("nc", NS_NC)];
        NS
    }

    fn emit(&self, e: &Entry, ctx: &PropCtx, req: &PropReq, out: &mut PropWriter) {
        let is_dir = e.kind.is_dir();

        // The core allocates file ids lazily (`ARCHITECTURE.md` §4.1): a plain
        // listing never forces one into existence, so `Entry::id` is an
        // `Option`. Clients here cannot cope with that — `oc:id` is the key
        // of their entire local sync journal, and an entry without one is
        // skipped outright — so whoever assembles the PROPFIND response must
        // have materialised an id before we are called. If it did not, emit
        // the sentinel rather than dropping the whole property set: a wrong
        // id is visible and debuggable, a silently missing entry is not.
        let file_id = match e.id {
            Some(id) => id,
            None => {
                tracing::warn!(name = %e.name, "entry reached property emission without a file id");
                FileId(0)
            }
        };

        // Grantee kinds are needed both for oc:share-types and for the 'S' in
        // oc:permissions, so resolve once. On backend failure we deliberately
        // fall back to "not shared" rather than dropping the whole property
        // set — a missing oc:permissions is worse than a missing 'S'.
        let kinds = if req.wants(NS_OC, "share-types") || req.wants(NS_OC, "permissions") {
            self.shares.kinds_for(ctx.share, file_id).unwrap_or_default()
        } else {
            Vec::new()
        };
        let shared = !kinds.is_empty();

        if req.wants(NS_OC, "id") {
            out.text(NS_OC, "id", nc_id(file_id, &self.instance_id));
        }
        if req.wants(NS_OC, "fileid") {
            out.num(NS_OC, "fileid", file_id.0);
        }
        if req.wants(NS_OC, "permissions") {
            out.text(NS_OC, "permissions", oc_permissions(e.perms, is_dir, shared));
        }
        if req.wants(NS_OC, "size") {
            // For a directory this is the recursive rollup; for a file the
            // plain size. The reference server emits oc:size for both.
            let size = if is_dir {
                self.aggregate.recursive_size(ctx.share, file_id).unwrap_or(e.size)
            } else {
                e.size
            };
            out.num(NS_OC, "size", size);
        }
        if req.wants(NS_OC, "favorite") {
            let fav = self.store.is_favorite(ctx.user, file_id).unwrap_or(false);
            // oc:favorite is an integer 0/1, not a JSON bool.
            out.num(NS_OC, "favorite", u8::from(fav));
        }
        if req.wants(NS_OC, "owner-id") {
            out.text(NS_OC, "owner-id", ctx.owner_name.clone());
        }
        if req.wants(NS_OC, "owner-display-name") {
            out.text(NS_OC, "owner-display-name", ctx.owner_display_name.clone());
        }
        if req.wants(NS_OC, "share-types") {
            // Serialises to zero or more <oc:share-type>N</oc:share-type>
            // children (SharesPlugin/ShareTypeList.php). Deduplicated, because
            // the reference wraps the list in array_unique().
            let mut seen: Vec<i64> = Vec::new();
            for k in &kinds {
                let t = grantee_kind_to_share_type(*k);
                if !seen.contains(&t) {
                    seen.push(t);
                }
            }
            let children = seen
                .into_iter()
                .map(|t| (NS_OC, "share-type", t.to_string()))
                .collect::<Vec<_>>();
            out.children(NS_OC, "share-types", children);
        }
        if req.wants(NS_OC, "checksums") {
            // Empty element by default: computing a content hash would mean
            // reading every byte of every file. Clients skip verification when
            // it is absent.
            out.empty(NS_OC, "checksums");
        }
        if req.wants(NS_NC, "has-preview") {
            out.json_bool(NS_NC, "has-preview", self.preview.can_preview(e));
        }
        if req.wants(NS_NC, "mount-type") {
            // Always the empty string: no external storage, no group folders.
            // Emitting "external" or "shared" changes client move semantics.
            out.text(NS_NC, "mount-type", "");
        }
        if req.wants(NS_NC, "is-encrypted") {
            // Integer 0. Note: this property does not exist in the reference server
            // master at all — it is contributed by the separate
            // `end_to_end_encryption` app. Clients that ship E2EE support ask
            // for it anyway, and a 404 propstat entry makes some of them treat
            // the directory as unreadable, so we answer 0 ("not encrypted").
            out.num(NS_NC, "is-encrypted", 0);
        }

        // ---- properties only the mobile clients ask for -------------------
        //
        // The desktop client's discovery set (`LsColJob::defaultProperties`)
        // stops above. Android asks for a much larger set in
        // `WebdavUtils.getAllPropSet()` and iOS for a larger one still (all 47
        // cases of `NKProperties`, `NKProperties.swift:11-66`). Most of those
        // are for features we do not have, and both parsers tolerate a missing
        // property by falling back to a default — so the ones below are the
        // subset where answering is actually better than staying silent.

        if req.wants(NS_NC, "creation_time") {
            // Real data we already hold, and both clients parse it: Android
            // into `createTimestamp` (`WebdavEntry.kt:177-185`), iOS into
            // `creationDate` (`NKDataFileXML.swift:340`). The iOS photo grid
            // sorts on it, so a folder of camera-roll uploads is ordered by
            // capture time rather than upload time once this is present.
            //
            // MUST NOT be emitted as an empty element. Android does
            // `(prop.value as String).toLong()` guarded only against
            // NumberFormatException; a `<nc:creation_time/>` makes that an
            // uncaught Kotlin cast NPE, which propagates out of the
            // `WebdavEntry` constructor and fails the **entire folder
            // listing**, not just this property. Hence the `if let`, and hence
            // no `else` branch emitting a placeholder.
            if let Some(btime) = e.btime_ns {
                out.num(NS_NC, "creation_time", btime / 1_000_000_000);
            }
        }
        if req.wants(NS_NC, "upload_time") {
            // Same null-cast hazard as creation_time (`WebdavEntry.kt:188-196`).
            // We do not track a separate upload time, so mtime is the closest
            // true statement; both clients treat 0/absent as "unknown".
            out.num(NS_NC, "upload_time", e.mtime_ns / 1_000_000_000);
        }
        if req.wants(NS_OC, "comments-unread") {
            // Comments are a non-goal, so the honest answer is 0. Android
            // reads this into a badge count (`WebdavEntry.kt:317-323`).
            out.num(NS_OC, "comments-unread", 0);
        }
        if req.wants(NS_NC, "lock") {
            // Consistent with `files.locking = false` in capabilities. Android
            // compares the text to "1" (`WebdavEntry.kt:504-510`), so 0 is
            // "unlocked"; leaving it out would also read as unlocked, but a
            // client that sees `files.locking` false and then a 404 propstat
            // for nc:lock has been given two different kinds of "no".
            out.num(NS_NC, "lock", 0);
        }
        if req.wants(NS_NC, "hidden") {
            out.json_bool(NS_NC, "hidden", false);
        }

        // Deliberately NOT emitted, though both clients ask:
        //
        // * `nc:rich-workspace` — absence is meaningful. Android maps a missing
        //   property to `null` and a present-but-empty one to `""`
        //   (`WebdavEntry.kt:360-370`), and only `null` means "the feature is
        //   off for this user". Emitting `""` would make the app render an
        //   empty rich-workspace editor at the top of every folder.
        // * `nc:sharees`, `nc:system-tags`, `nc:share-download-limits` —
        //   list-valued properties for features we do not implement. Absence
        //   yields an empty collection in both parsers, which is correct.
        // * `nc:mount-type` is emitted above rather than here because Android
        //   maps *absence* to `MountType.INTERNAL` but a present empty string
        //   to the same value, and the desktop client wants it present.
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ports::{Kind, PortResult, PropValue, ShareId, ShareSpec, UserId};
    use crate::store::MemStore;

    struct S(Vec<GranteeKind>);
    impl SharePort for S {
        fn list(
            &self,
            _u: UserId,
            _f: &crate::ports::ShareFilter,
        ) -> PortResult<Vec<crate::ports::CoreShare>> {
            Ok(vec![])
        }
        fn get(&self, _u: UserId, _i: u64) -> PortResult<crate::ports::CoreShare> {
            Err(crate::ports::PortError::NotFound)
        }
        fn create(&self, _u: UserId, _s: &ShareSpec) -> PortResult<crate::ports::CoreShare> {
            Err(crate::ports::PortError::NotFound)
        }
        fn update(
            &self,
            _u: UserId,
            _i: u64,
            _s: &ShareSpec,
        ) -> PortResult<crate::ports::CoreShare> {
            Err(crate::ports::PortError::NotFound)
        }
        fn delete(&self, _u: UserId, _i: u64) -> PortResult<()> {
            Ok(())
        }
        fn kinds_for(&self, _s: ShareId, _i: FileId) -> PortResult<Vec<GranteeKind>> {
            Ok(self.0.clone())
        }
        fn find_grantees(
            &self,
            _u: UserId,
            _q: &str,
            _s: crate::ports::GranteeScope,
        ) -> PortResult<Vec<crate::ports::GranteeCandidate>> {
            Ok(vec![])
        }
        fn link_url(&self, t: &str) -> String {
            format!("https://h/s/{t}")
        }
    }

    struct P(bool);
    impl PreviewPort for P {
        fn can_preview(&self, _e: &Entry) -> bool {
            self.0
        }
        fn signed_thumb_url(
            &self,
            _u: UserId,
            _s: ShareId,
            _p: &str,
            _w: u32,
            _h: u32,
            _f: crate::ports::FitMode,
        ) -> PortResult<Option<String>> {
            Ok(None)
        }
    }

    struct A(u64);
    impl DirSize for A {
        fn recursive_size(&self, _s: ShareId, _i: FileId) -> Option<u64> {
            Some(self.0)
        }
    }

    fn ctx() -> PropCtx {
        PropCtx {
            user: UserId(1),
            user_name: "alice".into(),
            share: ShareId(1),
            path: "photos".into(),
            owner_name: "alice".into(),
            owner_display_name: "Alice".into(),
        }
    }

    fn entry(kind: Kind) -> Entry {
        Entry {
            name: "photos".into(),
            kind,
            size: 17,
            mtime_ns: 0,
            etag: "e1".into(),
            perms: Perms::all(),
            id: Some(FileId(123)),
            is_symlink_denied: false,
            confusable: false,
            btime_ns: None,
        }
    }

    fn source(kinds: Vec<GranteeKind>, preview: bool) -> NcPropSource {
        NcPropSource::new(
            Arc::new(MemStore::with_instance_id("ocINST0001")),
            Arc::new(S(kinds)),
            Arc::new(P(preview)),
            Arc::new(A(4096)),
            "ocINST0001".into(),
        )
    }

    fn text(w: &PropWriter, ns: &str, n: &str) -> String {
        match w.get(ns, n) {
            Some(PropValue::Text(s)) => s.clone(),
            other => panic!("{ns}:{n} was {other:?}, expected text"),
        }
    }

    #[test]
    fn namespaces_are_the_two_nc_client_vocabularies() {
        let s = source(vec![], false);
        let ns = s.namespaces();
        assert!(ns.contains(&("oc", NS_OC)));
        assert!(ns.contains(&("nc", NS_NC)));
        assert_eq!(NS_OC, "http://owncloud.org/ns");
        assert_eq!(NS_NC, "http://nextcloud.org/ns");
    }

    #[test]
    fn allprop_emits_the_full_nc_vocabulary_for_a_directory() {
        let s = source(vec![], true);
        let mut w = PropWriter::new();
        s.emit(&entry(Kind::Dir), &ctx(), &PropReq::allprop(), &mut w);

        assert_eq!(text(&w, NS_OC, "id"), "00000123ocINST0001");
        assert_eq!(text(&w, NS_OC, "fileid"), "123");
        assert_eq!(text(&w, NS_OC, "permissions"), "RGDNVCK");
        // Directories report the recursive rollup, not the entry size.
        assert_eq!(text(&w, NS_OC, "size"), "4096");
        assert_eq!(text(&w, NS_OC, "favorite"), "0");
        assert_eq!(text(&w, NS_OC, "owner-id"), "alice");
        assert_eq!(text(&w, NS_OC, "owner-display-name"), "Alice");
        // nc: booleans are the JSON literals, not 1/0.
        assert_eq!(text(&w, NS_NC, "has-preview"), "true");
        assert_eq!(text(&w, NS_NC, "mount-type"), "");
        // ...but nc:is-encrypted is an integer.
        assert_eq!(text(&w, NS_NC, "is-encrypted"), "0");
        // Empty by default: a content hash means reading every byte.
        assert_eq!(w.get(NS_OC, "checksums"), Some(&PropValue::Empty));
        assert_eq!(
            w.get(NS_OC, "share-types"),
            Some(&PropValue::Children(vec![]))
        );
    }

    #[test]
    fn a_file_reports_its_own_size_and_a_w_not_ck() {
        let s = source(vec![], false);
        let mut w = PropWriter::new();
        s.emit(&entry(Kind::File), &ctx(), &PropReq::allprop(), &mut w);
        assert_eq!(text(&w, NS_OC, "size"), "17");
        assert_eq!(text(&w, NS_OC, "permissions"), "RGDNVW");
        assert_eq!(text(&w, NS_NC, "has-preview"), "false");
    }

    #[test]
    fn share_types_become_deduplicated_integer_children_and_light_up_the_s_letter() {
        let s = source(
            vec![
                GranteeKind::User,
                GranteeKind::User,
                GranteeKind::Link,
                GranteeKind::Group,
            ],
            false,
        );
        let mut w = PropWriter::new();
        s.emit(&entry(Kind::Dir), &ctx(), &PropReq::allprop(), &mut w);

        let PropValue::Children(c) = w.get(NS_OC, "share-types").unwrap() else {
            panic!("share-types must serialise as child elements");
        };
        // <oc:share-type>N</oc:share-type>, deduplicated, in discovery order.
        assert_eq!(
            c,
            &vec![
                (NS_OC, "share-type", "0".to_string()),
                (NS_OC, "share-type", "3".to_string()),
                (NS_OC, "share-type", "1".to_string()),
            ]
        );
        // An actively shared node gains the leading S.
        assert_eq!(text(&w, NS_OC, "permissions"), "SRGDNVCK");
    }

    #[test]
    fn only_requested_properties_are_emitted() {
        let s = source(vec![], false);
        let mut w = PropWriter::new();
        let req = PropReq::explicit([(NS_OC, "id"), (NS_OC, "permissions")]);
        s.emit(&entry(Kind::File), &ctx(), &req, &mut w);
        assert_eq!(w.len(), 2);
        assert!(w.get(NS_OC, "size").is_none());
        assert!(w.get(NS_NC, "has-preview").is_none());
    }

    /// The desktop client's discovery PROPFIND asks for a fixed list
    /// (`LsColJob::defaultProperties`). Every `oc:`/`nc:` name on it that is
    /// ours to answer must actually be answered — a missing propstat entry for
    /// `oc:permissions` in particular makes the client skip the item.
    #[test]
    fn every_property_the_desktop_client_asks_for_is_answered() {
        let s = source(vec![], false);
        let mut w = PropWriter::new();
        let wanted: &[(&str, &str)] = &[
            (NS_OC, "size"),
            (NS_OC, "id"),
            (NS_OC, "fileid"),
            (NS_OC, "permissions"),
            (NS_OC, "checksums"),
            (NS_OC, "share-types"),
            (NS_NC, "is-encrypted"),
        ];
        s.emit(
            &entry(Kind::File),
            &ctx(),
            &PropReq::explicit(wanted.iter().copied()),
            &mut w,
        );
        for (ns, n) in wanted {
            assert!(
                w.get(ns, n).is_some(),
                "client requests {ns}:{n} on every discovery PROPFIND"
            );
        }
    }

    #[test]
    fn favourites_are_per_user_integers() {
        let store = Arc::new(MemStore::with_instance_id("ocINST0001"));
        store.set_favorite(UserId(1), FileId(123), true).unwrap();
        let s = NcPropSource::new(
            store,
            Arc::new(S(vec![])),
            Arc::new(P(false)),
            Arc::new(A(0)),
            "ocINST0001".into(),
        );
        let mut w = PropWriter::new();
        s.emit(
            &entry(Kind::File),
            &ctx(),
            &PropReq::explicit([(NS_OC, "favorite")]),
            &mut w,
        );
        assert_eq!(text(&w, NS_OC, "favorite"), "1");

        let mut other = ctx();
        other.user = UserId(2);
        let mut w2 = PropWriter::new();
        s.emit(
            &entry(Kind::File),
            &other,
            &PropReq::explicit([(NS_OC, "favorite")]),
            &mut w2,
        );
        assert_eq!(text(&w2, NS_OC, "favorite"), "0");
    }

    /// Android's `WebdavUtils.getAllPropSet()` — the property set behind every
    /// folder listing in the app (`ReadFolderRemoteOperation.java:60`).
    ///
    /// This is the *complete* `oc:`/`nc:` half of that set, transcribed from
    /// `WebdavUtils.java:96-128`. Standard `DAV:` properties are core's job.
    /// The list is here in full — including the entries we intentionally do
    /// not answer — so that the next person can see the whole contract rather
    /// than only the part we happened to implement.
    const ANDROID_ALL_PROP_SET: &[(&str, &str)] = &[
        (NS_OC, "permissions"),
        (NS_OC, "fileid"),
        (NS_OC, "id"),
        (NS_OC, "size"),
        (NS_OC, "favorite"),
        (NS_NC, "is-encrypted"),
        (NS_NC, "mount-type"),
        (NS_OC, "owner-id"),
        (NS_OC, "owner-display-name"),
        (NS_OC, "comments-unread"),
        (NS_NC, "has-preview"),
        (NS_NC, "note"),
        (NS_NC, "sharees"),
        (NS_NC, "rich-workspace"),
        (NS_NC, "creation_time"),
        (NS_NC, "upload_time"),
        (NS_NC, "lock"),
        (NS_NC, "lock-owner-type"),
        (NS_NC, "lock-owner"),
        (NS_NC, "lock-owner-displayname"),
        (NS_NC, "lock-owner-editor"),
        (NS_NC, "lock-time"),
        (NS_NC, "lock-timeout"),
        (NS_NC, "lock-token"),
        (NS_NC, "system-tags"),
        (NS_NC, "color"),
        (NS_NC, "file-metadata-size"),
        (NS_NC, "file-metadata-gps"),
        (NS_NC, "metadata-photos-size"),
        (NS_NC, "metadata-photos-gps"),
        (NS_NC, "metadata-files-live-photo"),
        (NS_NC, "hidden"),
        (NS_NC, "share-download-limits"),
    ];

    /// Everything Android asks for that we have decided to answer. The rest
    /// falls into the 404 propstat, which both parsers tolerate — but only
    /// because our 200 propstat is written first (see
    /// `the_two_hundred_propstat_must_come_first` in the dav tests).
    #[test]
    fn every_android_property_we_claim_to_answer_is_answered() {
        let s = source(vec![], false);
        let mut w = PropWriter::new();
        let mut e = entry(Kind::File);
        e.btime_ns = Some(1_700_000_000_000_000_000);
        s.emit(
            &e,
            &ctx(),
            &PropReq::explicit(ANDROID_ALL_PROP_SET.iter().copied()),
            &mut w,
        );

        for (ns, n) in [
            (NS_OC, "permissions"),
            (NS_OC, "fileid"),
            (NS_OC, "id"),
            (NS_OC, "size"),
            (NS_OC, "favorite"),
            (NS_OC, "owner-id"),
            (NS_OC, "owner-display-name"),
            (NS_OC, "comments-unread"),
            (NS_NC, "is-encrypted"),
            (NS_NC, "mount-type"),
            (NS_NC, "has-preview"),
            (NS_NC, "creation_time"),
            (NS_NC, "upload_time"),
            (NS_NC, "lock"),
            (NS_NC, "hidden"),
        ] {
            assert!(
                w.get(ns, n).is_some(),
                "Android's getAllPropSet() asks for {ns}:{n} on every folder listing"
            );
        }
    }

    /// `nc:rich-workspace` is the one property where staying silent is the
    /// *feature*. Android distinguishes absent (`null`, meaning the feature is
    /// disabled) from present-and-empty (`""`, meaning an empty workspace to
    /// render) at `WebdavEntry.kt:360-370`.
    #[test]
    fn rich_workspace_is_never_emitted() {
        let s = source(vec![], false);
        let mut w = PropWriter::new();
        s.emit(&entry(Kind::Dir), &ctx(), &PropReq::allprop(), &mut w);
        assert!(w.get(NS_NC, "rich-workspace").is_none());

        let mut w = PropWriter::new();
        s.emit(
            &entry(Kind::Dir),
            &ctx(),
            &PropReq::explicit([(NS_NC, "rich-workspace")]),
            &mut w,
        );
        assert!(
            w.get(NS_NC, "rich-workspace").is_none(),
            "even when explicitly requested: absent means 'feature off', \
             an empty string means 'render an empty editor'"
        );
    }

    /// Android parses `nc:creation_time` with `(prop.value as String).toLong()`
    /// (`WebdavEntry.kt:177-185`) inside a `catch (NumberFormatException)`.
    /// A null value is a *cast* exception, not a format one, so it escapes the
    /// constructor and fails the whole folder listing. Therefore: emit a real
    /// number or nothing at all — never an empty element.
    #[test]
    fn creation_time_is_omitted_rather_than_emitted_empty() {
        let s = source(vec![], false);

        let mut with = entry(Kind::File);
        with.btime_ns = Some(1_700_000_000_500_000_000);
        let mut w = PropWriter::new();
        s.emit(&with, &ctx(), &PropReq::explicit([(NS_NC, "creation_time")]), &mut w);
        // Seconds, not nanoseconds, and truncated.
        assert_eq!(text(&w, NS_NC, "creation_time"), "1700000000");

        let mut without = entry(Kind::File);
        without.btime_ns = None;
        let mut w = PropWriter::new();
        s.emit(&without, &ctx(), &PropReq::explicit([(NS_NC, "creation_time")]), &mut w);
        assert!(
            w.get(NS_NC, "creation_time").is_none(),
            "an empty <nc:creation_time/> is an uncaught NPE in the Android parser"
        );
    }

    /// No property we emit may ever be the empty element, for the null-cast
    /// reason above. `oc:checksums` is the sole documented exception: neither
    /// mobile parser reads it (iOS looks for it under the wrong prefix, at
    /// `NKDataFileXML.swift:380`), and the desktop client treats empty as
    /// "no checksum available".
    #[test]
    fn no_numeric_property_is_ever_emitted_empty() {
        let s = source(vec![], true);
        for kind in [Kind::File, Kind::Dir] {
            let mut e = entry(kind);
            e.btime_ns = None;
            let mut w = PropWriter::new();
            s.emit(&e, &ctx(), &PropReq::allprop(), &mut w);
            for (ns, name, v) in w.as_slice() {
                if matches!(v, PropValue::Empty) {
                    assert_eq!(
                        (*ns, *name),
                        (NS_OC, "checksums"),
                        "{ns}:{name} is emitted as an empty element; if any client \
                         casts its value to a string that is an uncaught NPE"
                    );
                }
            }
        }
    }

    /// iOS asks for these in the `oc:` namespace but *parses* them with a `d:`
    /// prefix (`NKDataFileXML.swift:356, 360, 380`), so it can never see our
    /// answer. Confirming we do not emit `oc:downloadURL`/`oc:data-fingerprint`
    /// keeps someone from "fixing" iOS by adding them.
    #[test]
    fn properties_ios_asks_for_but_cannot_parse_are_not_emitted() {
        let s = source(vec![], false);
        let mut w = PropWriter::new();
        s.emit(&entry(Kind::File), &ctx(), &PropReq::allprop(), &mut w);
        assert!(w.get(NS_OC, "downloadURL").is_none());
        assert!(w.get(NS_OC, "data-fingerprint").is_none());
    }

    #[test]
    fn oc_id_format() {
        assert_eq!(nc_id(FileId(123), "oc9k2m4x1p"), "00000123oc9k2m4x1p");
        assert_eq!(nc_id(FileId(0), "abc"), "00000000abc");
        assert_eq!(nc_id(FileId(1), ""), "00000001");
        // sprintf('%08d') widens, never truncates.
        assert_eq!(nc_id(FileId(1234567890), "xy"), "1234567890xy");
        // Negative ids should not silently produce a shorter string; '-' counts
        // toward the width just as it does in PHP.
        assert_eq!(nc_id(FileId(-1), "xy"), "-0000001xy");
    }

    #[test]
    fn nc_bits_roundtrip_rejects_unknown() {
        assert!(nc_bits_to_perms(31).is_ok());
        // 32 = the first bit outside PERMISSION_ALL.
        assert_eq!(nc_bits_to_perms(32).unwrap_err(), 32);
        assert_eq!(nc_bits_to_perms(31 | 64).unwrap_err(), 64);
    }
}
