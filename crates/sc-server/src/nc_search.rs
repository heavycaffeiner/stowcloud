//! The compat layer's `SEARCH`, `REPORT` and settable-favourite wiring.
//!
//! Three `sc-dav` seams are filled here because this is the only crate that
//! sees both the protocol layer and the core: the search backend (`sc-search`
//! over the caller's readable roots), the favourites report (translated into
//! the same search), and the write side of the favourite property.
//!
//! Everything vendor-shaped goes through `sc_compat_nc::search`, so this file
//! carries wiring and no vocabulary.

use std::sync::Arc;

use sc_compat_nc::store::NcStore;
use sc_core::{SharePath, Vpath};
use sc_dav::{DavError, DavResult};
use sc_vfs::{FileId, ShareId, UserId};

use crate::journal::{WriteJournal, WriteRow};

/// Upper bound on rows a single `SEARCH` may return.
///
/// The protocol has no field for "there was more", so a truncated answer is
/// indistinguishable from a complete one to the client. Both apps page by
/// `d:nresults` and neither copes with an unbounded response, so the cap is
/// applied here and the truncation is logged rather than signalled.
const MAX_RESULTS: u32 = 500;

/// Body limit for a report. A favourites report is a property list and one
/// filter rule; nothing legitimate approaches this.
const MAX_REPORT_BODY: usize = 64 * 1024;

/// How many hits sit between the walk and the collector at once.
///
/// `Walker::emit` uses a blocking `send`, so a full channel applies
/// backpressure instead of allocating. An unbounded one would hold every
/// matching file at once now that `max_results` no longer stops the walk. This
/// bounds the channel, not the walk: the larger allocation is the walker's own
/// `pending` vector, which is unchanged.
const CHANNEL_DEPTH: usize = 4096;

/// Walk `roots` with `matcher` and return the newest `limit` hits it reached,
/// newest first, ties broken by path ascending, plus the walk's completeness.
///
/// `WalkBudget::max_results` bounds a walk by *stopping* it, so an ordered,
/// limited query used to end after the first `cap` matches the stat phase
/// happened to reach (inode order on rotational storage, readdir order
/// otherwise) and only then sort those. On any share with more than `cap` files
/// inside the window the answer was wrong, and wrong while reporting `Full`.
///
/// The caller must have set a size or mtime filter on `matcher`, because
/// `TopN` orders on `Hit::mtime_ns` and only those make the walk stat.
/// `budget` should carry `max_results(u32::MAX)`: the point of collecting is
/// that the walk is bounded by time and entries rather than by result count.
pub fn collect_newest(
    walker: &sc_search::Walker,
    roots: &[(Arc<sc_vfs::ShareRoot>, sc_vfs::SafePath)],
    matcher: &sc_search::Matcher,
    acl: &(dyn Fn(ShareId, &sc_vfs::SafePath) -> bool + Sync),
    budget: &sc_search::WalkBudget,
    limit: u32,
) -> (Vec<sc_search::Hit>, sc_search::Completeness) {
    let (tx, rx) = crossbeam_channel::bounded::<sc_search::Hit>(CHANNEL_DEPTH);
    // No deadlock risk: the consumer only receives and the walk only sends.
    let consumer = std::thread::spawn(move || {
        let mut top = sc_search::TopN::new(limit as usize);
        for hit in rx {
            top.offer(hit);
        }
        top.into_sorted_vec()
    });
    let completeness = walker.walk(roots, matcher, acl, budget, &tx);
    drop(tx);
    let hits = consumer.join().unwrap_or_default();
    (hits, completeness)
}

pub struct NcSearch {
    pub core: Arc<sc_core::Core>,
    pub meta: Arc<sc_meta::MetaStore>,
    pub auth: Arc<sc_auth::AuthService>,
    pub store: Arc<dyn NcStore>,
    /// The *same* object the native search reads its walk deadline from, so a
    /// `[search]` setting an operator changes applies to both surfaces rather
    /// than to one of them.
    pub limits: Arc<sc_http::search_limits::SearchConcurrency>,
    /// Storage-class detection touches sysfs on Linux, so results are cached
    /// rather than re-read per search. Shared with the native bridge for the
    /// same reason `limits` is.
    pub storage: Arc<crate::storage_class::StorageClassCache>,
    /// The same record `/api/recent` reads, so the phone's Recent screen and
    /// the web tab show the same list.
    pub journal: Option<Arc<WriteJournal>>,
}

/// Whether this request asks the recency question rather than a content one.
///
/// It orders by `d:getlastmodified` descending, bounds
/// `d:getlastmodified`, and constrains nothing else: no name substring, no
/// media type, no folders-only filter. The favourites flag and the file id are
/// answered by their own branches before this is consulted.
///
/// The cost, stated rather than buried: such a request receives what this
/// account wrote through this server in that window, which is a subset of the
/// resources matching the filter it wrote. The only thing that sends this query
/// is a screen labelled Recent, and this is the answer that screen is asking
/// for; a truthful answer to the literal query is dominated by SMB and backup
/// writers the person reading it has no relationship with. Every query naming
/// any content at all still gets the filesystem.
fn is_recency_request(req: &sc_dav::SearchRequest) -> bool {
    req.newest_first
        && (req.mtime_from_ns.is_some() || req.mtime_to_ns.is_some())
        && req.name_contains.is_none()
        && req.content_type_prefixes.is_empty()
        && req.is_collection != Some(true)
}

impl NcSearch {
    fn login_of(&self, user: UserId) -> Option<String> {
        self.auth.find_user_by_id(user).ok().flatten().map(|r| r.name)
    }

    /// One row, if `vpath` resolves and the caller may read it.
    ///
    /// Every id- and favourite-derived hit goes through here rather than being
    /// trusted from the table it came out of: a favourite row outlives the file
    /// it points at, and a file id is a global handle whose reachability is the
    /// ACL's decision, not the table's.
    fn row(&self, user: UserId, vpath: &Vpath) -> Option<(String, sc_dav::Entry)> {
        let e = self.core.stat_entry(user, vpath.as_str()).ok()?;
        if !e.perms.contains(sc_acl::Perms::READ) {
            return None;
        }
        Some((vpath.as_str().to_string(), crate::bridge::dav_entry(e)))
    }

    fn row_for_id(&self, user: UserId, id: FileId) -> Option<(String, sc_dav::Entry)> {
        let (share, path) = self.meta.resolve_path(id).ok().flatten()?;
        let root = self.core.share(share)?;
        let sp = SharePath::parse(&path, root.policy().max_depth).ok()?;
        let vpath = self.core.vpath_for(user, share, &sp)?;
        self.row(user, &vpath)
    }

    /// Every `(root, start path)` the walk may begin from, honouring the
    /// resolved scope. An empty scope is every readable root.
    fn roots_for(
        &self,
        user: UserId,
        scope: Option<&str>,
    ) -> Vec<(Arc<sc_vfs::ShareRoot>, sc_vfs::SafePath)> {
        if let Some(scope) = scope {
            return match self.core.resolve(user, &Vpath::new(scope)) {
                Ok(r) => vec![(r.root, r.path.into_safe())],
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

    /// The recency answer, from the record of what this account wrote here.
    ///
    /// The journal is read with no lower bound of its own. Passing the
    /// request's `d:getlastmodified` bound to it would filter the *recorded*
    /// time by a bound the client wrote about the file's *modification* time:
    /// two different quantities, and a wrong answer whenever they differ,
    /// which is exactly the restore case. The bounds are applied below to the
    /// mtime each row's `stat` already produces, because that is the property
    /// the client named, and the rows come back ordered by it for the same
    /// reason. The per-account row cap is what bounds the read.
    fn recency(
        &self,
        user: UserId,
        req: &sc_dav::SearchRequest,
        scope: Option<&str>,
    ) -> DavResult<Vec<(String, sc_dav::Entry)>> {
        let Some(journal) = &self.journal else {
            return Ok(Vec::new());
        };
        let rows = journal.newest(user, i64::MIN);
        let mut out: Vec<(String, sc_dav::Entry)> = Vec::new();
        // Only a row whose file the read proved gone. A revoked grant and an
        // unmounted share both hide rows without deleting them, because both
        // reverse and this record cannot be rebuilt.
        let mut dead: Vec<WriteRow> = Vec::new();

        for row in rows {
            let Ok(sp) = SharePath::parse(&row.path, u16::MAX) else {
                continue;
            };
            let Some(vpath) = self.core.vpath_for(user, row.share, &sp) else {
                continue;
            };
            if let Some(s) = scope {
                if !vpath.is_inside(&Vpath::new(s)) {
                    continue;
                }
            }
            let entry = match self.core.stat_entry(user, vpath.as_str()) {
                Ok(e) => e,
                Err(sc_core::CoreError::NotFound) => {
                    dead.push(row);
                    continue;
                }
                Err(_) => continue,
            };
            if entry.kind != sc_vfs::Kind::File || !entry.perms.contains(sc_acl::Perms::READ) {
                continue;
            }
            if req.mtime_from_ns.is_some_and(|lo| entry.mtime_ns < lo)
                || req.mtime_to_ns.is_some_and(|hi| entry.mtime_ns > hi)
            {
                continue;
            }
            out.push((vpath.as_str().to_string(), crate::bridge::dav_entry(entry)));
        }

        journal.forget(user, &dead);
        sort_rows(&mut out, true);
        truncate(&mut out, req.limit);
        Ok(out)
    }
}

impl sc_dav::SearchSource for NcSearch {
    fn search(
        &self,
        user: UserId,
        req: &sc_dav::SearchRequest,
    ) -> DavResult<Vec<(String, sc_dav::Entry)>> {
        let login = self
            .login_of(user)
            .ok_or_else(|| DavError::Internal("the requesting account has no login name".into()))?;

        // Scope first, and refuse before anything is read: a scope naming
        // another account is never narrowed or reinterpreted.
        let scope = sc_compat_nc::search::scope_to_vpath(&req.scope_href, &login).ok_or_else(
            || DavError::BadRequest("the search scope is not a path in your own files".into()),
        )?;

        let (vendor, unread) = sc_compat_nc::search::vendor_filters(req.vendor.iter().map(|t| {
            (
                t.ns.as_str(),
                t.name.as_str(),
                t.literal.as_str(),
                t.in_disjunction,
            )
        }));
        // Same rule `sc-dav` applies to its own properties, decided in the
        // crate that knows this vocabulary: dropping a conjunct would answer a
        // wider query than the client asked for.
        if let Some(first) = unread.first() {
            return Err(DavError::BadRequest(format!(
                "this search constrains {first}, which this server cannot apply"
            )));
        }

        // A file id is a direct lookup, and a favourites query is a table read.
        // Neither walks: a marked file can be anywhere, and walking a tree to
        // find rows a table already names would be absurd.
        if let Some(id) = vendor.file_id {
            return Ok(self.row_for_id(user, FileId(id)).into_iter().collect());
        }
        if vendor.favourites_only {
            let ids = self.store.favorites(user).map_err(|e| {
                DavError::Internal(format!("could not read the favourites table: {e}"))
            })?;
            let mut rows: Vec<(String, sc_dav::Entry)> = ids
                .into_iter()
                .filter_map(|id| self.row_for_id(user, id))
                .filter(|(p, _)| match &scope {
                    Some(s) => Vpath::new(p).is_inside(&Vpath::new(s)),
                    None => true,
                })
                .collect();
            sort_rows(&mut rows, req.newest_first);
            truncate(&mut rows, req.limit);
            return Ok(rows);
        }

        // Selected by what the request constrains, after parsing, and never by
        // a client or a user agent. Every other shape keeps walking: a name
        // search, a gallery or media query carrying `image/%`, a folders-only
        // ordered query, and an ordered query with no date bound at all.
        if is_recency_request(req) {
            return self.recency(user, req, scope.as_deref());
        }

        let roots = self.roots_for(user, scope.as_deref());
        if roots.is_empty() {
            return Ok(Vec::new());
        }

        let mut matcher = match &req.name_contains {
            Some(n) => sc_search::Matcher::new(n),
            None => sc_search::Matcher::match_all(),
        };
        if !req.content_type_prefixes.is_empty() {
            let exts: Vec<&str> = req
                .content_type_prefixes
                .iter()
                .flat_map(|p| sc_compat_nc::search::mime_prefix_extensions(p).iter().copied())
                .collect();
            // An unrecognised media-type prefix yields no extensions at all.
            // Filtering on an empty set would match everything, which is the
            // opposite of what the client asked, so the search answers empty
            // instead.
            if exts.is_empty() {
                return Ok(Vec::new());
            }
            matcher = matcher.exts(exts);
        }
        match (req.mtime_from_ns, req.mtime_to_ns) {
            (Some(lo), Some(hi)) => matcher = matcher.mtime_range(lo, hi),
            (Some(lo), None) => matcher = matcher.mtime_range(lo, i128::MAX),
            (None, Some(hi)) => matcher = matcher.mtime_range(i128::MIN, hi),
            // An ordering implies the property it orders on. A client may send
            // `d:orderby` with no date bound at all, and the top-N collector
            // below orders on `Hit::mtime_ns`, which only exists when the stat
            // phase ran, which only an mtime or size filter makes happen.
            // `post_matches` accepts everything in this range, so nothing is
            // filtered out and every hit arrives with the mtime the ordering
            // is defined on.
            (None, None) if req.newest_first => matcher = matcher.mtime_range(i128::MIN, i128::MAX),
            (None, None) => {}
        }
        if let Some(dirs_only) = req.is_collection {
            matcher = matcher.kinds(sc_search::KindFilter {
                files: !dirs_only,
                dirs: dirs_only,
            });
        }

        let cap = if req.limit == 0 {
            MAX_RESULTS
        } else {
            req.limit.min(MAX_RESULTS)
        };
        // Storage-tier walk deadline, from the same configuration the native
        // search reads. A search spanning shares of different classes takes
        // the most conservative one.
        let tier = sc_http::search_limits::fold_tier(
            roots.iter().map(|(r, _)| self.storage.get_or_detect(r)),
        );
        let rotational = tier == sc_http::search_limits::SearchTier::Slow;
        // `max_results` has to be set explicitly in both branches:
        // `WalkBudget::new` defaults it to 1000, so simply dropping it for the
        // ordered case would swap a cap of `cap` for a cap of 1000 and keep
        // the defect.
        let budget = sc_search::WalkBudget::new(self.limits.walk_deadline(tier))
            .max_results(if req.newest_first { u32::MAX } else { cap })
            .max_depth(u16::MAX);
        let walker = sc_search::Walker::new(sc_search::Walker::decide_threads(rotational, None))
            .with_rotational(rotational);
        let acl = |share: ShareId, path: &sc_vfs::SafePath| self.core.can_read(user, share, path);
        // The only correct way to truncate an ordered query is to keep the top
        // of the order. Stopping the walk at the first `cap` matches ends it in
        // whatever order the stat phase happened to reach them, which is inode
        // order on rotational storage and readdir order otherwise, and neither
        // has anything to do with mtime.
        let (hits, completeness) = if req.newest_first {
            collect_newest(&walker, &roots, &matcher, &acl, &budget, cap)
        } else {
            let (tx, rx) = crossbeam_channel::unbounded::<sc_search::Hit>();
            let completeness = walker.walk(&roots, &matcher, &acl, &budget, &tx);
            drop(tx);
            (rx.try_iter().collect(), completeness)
        };
        if let sc_search::Completeness::Truncated { reason, seen, .. } = &completeness {
            // Logged, not signalled: there is nowhere on the wire to say it.
            tracing::info!(?reason, seen, "a compat search was truncated by its budget");
        }

        let mut rows: Vec<(String, sc_dav::Entry)> = Vec::new();
        for hit in hits {
            let sp = match SharePath::parse(&hit.path, u16::MAX) {
                Ok(p) => p,
                Err(_) => continue,
            };
            let Some(vpath) = self.core.vpath_for(user, hit.share, &sp) else {
                continue;
            };
            if let Some(row) = self.row(user, &vpath) {
                rows.push(row);
            }
        }
        sort_rows(&mut rows, req.newest_first);
        truncate(&mut rows, cap);
        Ok(rows)
    }
}

impl sc_compat_nc::ports::SearchPort for NcSearch {
    /// The unified-search screen's question, answered by the same engine the
    /// DAV `SEARCH` method uses: one implementation, two envelopes.
    fn by_name(
        &self,
        user: UserId,
        term: &str,
        limit: u32,
    ) -> sc_compat_nc::ports::PortResult<Vec<sc_compat_nc::ports::SearchHit>> {
        use sc_dav::SearchSource as _;
        let req = sc_dav::SearchRequest {
            scope_href: String::new(),
            depth_infinity: true,
            name_contains: Some(term.to_string()),
            content_type_prefixes: Vec::new(),
            mtime_from_ns: None,
            mtime_to_ns: None,
            is_collection: None,
            limit,
            newest_first: false,
            vendor: Vec::new(),
            props: sc_dav::PropReq { all: true, names_only: false, requested: Vec::new() },
        };
        let rows = self
            .search(user, &req)
            .map_err(|e| sc_compat_nc::ports::PortError::Backend(e.to_string()))?;
        Ok(rows
            .into_iter()
            .map(|(path, e)| sc_compat_nc::ports::SearchHit {
                path,
                entry: sc_core::Entry {
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
                },
            })
            .collect())
    }
}

/// Newest first when the client asked for it, and by path otherwise so that a
/// listing is at least stable between two identical requests.
fn sort_rows(rows: &mut [(String, sc_dav::Entry)], newest_first: bool) {
    if newest_first {
        rows.sort_by(|a, b| b.1.mtime_ns.cmp(&a.1.mtime_ns).then(a.0.cmp(&b.0)));
    } else {
        rows.sort_by(|a, b| a.0.cmp(&b.0));
    }
}

fn truncate(rows: &mut Vec<(String, sc_dav::Entry)>, limit: u32) {
    let cap = if limit == 0 {
        MAX_RESULTS as usize
    } else {
        (limit as usize).min(MAX_RESULTS as usize)
    };
    if rows.len() > cap {
        rows.truncate(cap);
    }
}

// -------------------------------------------------------------- the report --

/// The favourites report, translated into the favourites search.
///
/// The body carries a filter rule and a property set; only the filter matters
/// here, and the property set is answered by the same machinery a `SEARCH`
/// response uses. Reusing the search means the two entry points cannot drift.
pub struct NcFilterFilesReport;

impl sc_dav::ReportSource for NcFilterFilesReport {
    fn report_name(&self) -> (&'static str, &'static str) {
        sc_compat_nc::search::FILTER_FILES_REPORT
    }

    fn to_search(&self, _user: UserId, vpath: &str, body: &[u8]) -> DavResult<sc_dav::SearchRequest> {
        let parsed = sc_dav::xml::parse_report_body(body, MAX_REPORT_BODY)?;
        let favourites = parsed.leaves.iter().any(|(n, v)| {
            let (ns, name) = sc_compat_nc::search::FAVORITE_PROPERTY;
            n.ns == ns && n.name == name && matches!(v.trim(), "1" | "yes" | "true")
        });
        let props = if parsed.props.is_empty() {
            sc_dav::PropReq { all: true, names_only: false, requested: Vec::new() }
        } else {
            sc_dav::PropReq { all: false, names_only: false, requested: parsed.props }
        };
        if !favourites {
            // The only filter rule either client sends. Refusing is honest:
            // answering an unfiltered listing for a filter we did not
            // understand would look like a working feature.
            return Err(DavError::UnsupportedReport);
        }
        Ok(sc_dav::SearchRequest {
            scope_href: vpath.to_string(),
            depth_infinity: true,
            name_contains: None,
            content_type_prefixes: Vec::new(),
            mtime_from_ns: None,
            mtime_to_ns: None,
            is_collection: None,
            limit: 0,
            newest_first: false,
            vendor: vec![sc_dav::SearchTerm {
                ns: sc_compat_nc::NS_OC.to_string(),
                name: "favorite".to_string(),
                op: sc_dav::SearchOp::Eq,
                literal: "1".to_string(),
                in_disjunction: false,
            }],
            props,
        })
    }
}

// ------------------------------------------------- the settable favourite --

/// The write side of the favourite property.
///
/// Without this the write lands in the dead-property store, answers `200`, and
/// the next `PROPFIND` reports the old value from the favourites table. The
/// star fills in and then reverts.
pub struct NcFavoriteWriter {
    pub store: Arc<dyn NcStore>,
}

/// Both spellings of the property. Android and iOS agree on the name; the
/// namespace is the one this crate already reads it from.
const FAVORITE_CLAIM: &[(&str, &str)] = &[sc_compat_nc::search::FAVORITE_PROPERTY];

impl sc_dav::PropPatchSource for NcFavoriteWriter {
    fn claims(&self) -> &[(&'static str, &'static str)] {
        FAVORITE_CLAIM
    }

    fn set(
        &self,
        user: UserId,
        _share: ShareId,
        id: FileId,
        _ns: &str,
        _name: &str,
        value: Option<&str>,
    ) -> DavResult<()> {
        // A `d:remove` is how Android unstars, so absent means off.
        let on = match value {
            None => false,
            Some(v) => matches!(v.trim(), "1" | "true" | "yes"),
        };
        self.store
            .set_favorite(user, id, on)
            .map_err(|e| DavError::Internal(format!("could not record the favourite: {e}")))
    }
}
