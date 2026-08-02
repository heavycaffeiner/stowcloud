//! Read/write filesystem operations: listing, stat, mkdir, rename, move,
//! copy, delete, small-file read/write. Everything here goes through
//! `resolve_want` first (`resolve.rs`) and never parses a virtual path
//! itself.

use std::collections::HashMap;
use std::num::NonZeroUsize;
use std::sync::atomic::Ordering;

use sc_acl::Perms;
use sc_meta::MetaStore;
use sc_vfs::{FileId, Kind, SafePath, ShareId, ShareRoot, Stat, SymlinkPolicy, UserId, VfsError};

use crate::entry::{Entry, ListingSession, OnConflict, OpResult, Order, Sort};
use crate::error::CoreError;
use crate::resolve::Resolved;
use crate::Listing;

/// First page returned by `list()` when no cursor is in play. shows 200 as the example page size; there is no way to override it
/// through the contracted `list()` signature, so it's a fixed constant here.
pub(crate) const DEFAULT_PAGE_SIZE: usize = 200;
pub(crate) const LISTING_CACHE_CAP: usize = 256;
pub(crate) const LISTING_TTL: std::time::Duration = std::time::Duration::from_secs(60);

/// Name for a staging file this crate creates and then renames into place
/// (`copy_file_internal`, the cross-device half of `move_one`, `write_text`).
///
/// Literally `.scpart-{uuid}`, so `sc_vfs::is_reserved_name` — a `starts_with`
/// test over `RESERVED_PREFIXES` — actually matches it. These three sites used
/// to spell it `.{name}.scpart-{uuid}` to get past `validate_component`'s
/// reserved-prefix rejection, which defeated that match: a copy/move/write
/// killed mid-syscall left a partial file visible in listings, over WebDAV,
/// past SMB's `veto files = /.scpart-*/`, and indexed by the search walker —
/// a half-written file presenting itself as a real one. `sc-upload` hit and
/// documented the identical bug (`sc_upload::engine`'s naming note);
/// `SafePath::join_control` is the exemption that exists so the name does not
/// have to be disguised.
pub(crate) fn part_name() -> String {
    format!(".scpart-{}", uuid::Uuid::new_v4().simple())
}

/// How long an abandoned staging entry is left alone before the sweep below
/// will reclaim it.
///
/// Generous on purpose: the sweep runs while other requests are staging into
/// the same directory, and deleting a copy that is still being written would
/// be a far worse bug than leaving a dead temp file for another day. An entry
/// this old is not a concurrent operation — a running copy keeps touching the
/// file (or, for a staged tree, the directory) it is filling.
const PART_ORPHAN_TTL: std::time::Duration = std::time::Duration::from_secs(24 * 60 * 60);

pub(crate) fn path_exists(root: &ShareRoot, p: &SafePath) -> Result<bool, VfsError> {
    match root.stat(p) {
        Ok(_) => Ok(true),
        Err(VfsError::NotFound) => Ok(false),
        Err(e) => Err(e),
    }
}

impl crate::Core {
    fn stat_counted(&self, root: &ShareRoot, p: &SafePath) -> Result<Stat, VfsError> {
        self.stat_calls.fetch_add(1, Ordering::Relaxed);
        root.stat(p)
    }

    /// Reclaim `.scpart-` staging entries in `dir` that no operation is
    /// coming back for.
    ///
    /// Every staging site in this module unlinks its temp entry on the error
    /// path, so an orphan only exists when the process never got to run that
    /// code: `SIGKILL`, an OOM kill, a power cut. `sc_upload` has its own
    /// sweep for the temp files *it* creates, but it works from
    /// `upload_touched_dirs` matched against live session rows — this crate's
    /// staging files have neither, so nothing was ever reclaiming them.
    ///
    /// Called where this crate is about to stage into a directory, which is
    /// exactly the set of directories that can hold one of its orphans. That
    /// keeps it stateless — no touched-dir ledger to maintain, no periodic
    /// whole-share walk, and the cost (one `read_dir`) is paid right before a
    /// copy or write that is more expensive by orders of magnitude.
    ///
    /// Best-effort throughout: a failure to tidy up must never be the reason
    /// a valid copy fails.
    fn sweep_stale_parts(&self, root: &ShareRoot, dir: &SafePath) {
        // `read_dir_control`, not `read_dir`: `.scpart-` is a reserved prefix
        // and the plain listing hides it — which is the whole reason these
        // survive unnoticed.
        let Ok(entries) = root.read_dir_control(dir) else { return };
        let max_depth = root.policy().max_depth;
        let now = crate::links::now_ns();
        let ttl_ns = PART_ORPHAN_TTL.as_nanos() as i128;
        for entry in entries {
            if !entry.name.starts_with(".scpart-") {
                continue;
            }
            let Ok(child) = dir.join_control(entry.name.as_str(), max_depth) else { continue };
            let Ok(st) = root.stat(&child) else { continue };
            if now - st.mtime_ns < ttl_ns {
                continue;
            }
            let removed = if st.kind == Kind::Dir {
                Self::delete_recursive(root, &child).and_then(|()| Ok(root.rmdir(&child)?))
            } else {
                root.unlink(&child).map_err(CoreError::from)
            };
            match removed {
                Ok(()) => tracing::info!(
                    name = %entry.name,
                    "reclaimed an abandoned staging entry (left by a hard kill mid-copy)"
                ),
                Err(e) => tracing::debug!(name = %entry.name, error = %e, "could not reclaim a staging entry"),
            }
        }
    }

    /// Total number of `ShareRoot::stat` calls this `Core` has made through
    /// `stat_counted` — exposed purely so tests can assert "only the
    /// returned page was stat-ed".
    pub fn stat_call_count(&self) -> u64 {
        self.stat_calls.load(Ordering::Relaxed)
    }

    pub(crate) fn build_entry(
        &self,
        share: ShareId,
        root: &ShareRoot,
        name: &str,
        path: &SafePath,
        st: &Stat,
        user: UserId,
    ) -> Entry {
        let etag = MetaStore::file_etag(st);
        let perms = self.acl.effective(user, share, path);
        let id = self.meta.lookup_fileid(share, st).ok().flatten();
        let is_symlink_denied = st.kind == Kind::Symlink && matches!(root.policy().symlink, SymlinkPolicy::Deny);
        Entry {
            name: name.to_string(),
            kind: st.kind,
            size: st.size,
            mtime_ns: st.mtime_ns,
            etag,
            perms,
            id,
            is_symlink_denied,
            confusable: false,
            btime_ns: st.btime_ns,
        }
    }

    pub fn list(&self, user: UserId, vpath: &str, sort: Sort, order: Order) -> Result<Listing, CoreError> {
        let r = self.resolve_want(user, vpath, Perms::READ)?;
        let dir_stat = self.stat_counted(&r.root, &r.path)?;
        if dir_stat.kind != Kind::Dir {
            return Err(CoreError::InvalidPath("not a directory".into()));
        }
        let dir_etag = MetaStore::file_etag(&dir_stat);
        let dir_path_str = r.path.to_display_string();
        let cache_key = format!("{}:{}:{}:{:?}:{:?}", r.share.get(), dir_path_str, dir_etag, sort, order);
        let max_depth = r.root.policy().max_depth;

        let session = {
            let mut cache = self.listings.lock();
            self.evict_expired(&mut cache);
            if let Some(hit) = cache.get(&cache_key) {
                hit.clone()
            } else {
                let mut names: Vec<String> = r.root.read_dir(&r.path)?.into_iter().map(|e| e.name.to_string()).collect();
                let stats = match sort {
                    Sort::Name => {
                        names.sort();
                        None
                    }
                    _ => {
                        let mut with_stat: Vec<(String, Stat)> = Vec::with_capacity(names.len());
                        for n in &names {
                            let p = r.path.join(n, max_depth)?;
                            let st = self.stat_counted(&r.root, &p)?;
                            with_stat.push((n.clone(), st));
                        }
                        with_stat.sort_by(|a, b| order.apply(cmp_by(sort, &a.1, &b.1)).then_with(|| a.0.cmp(&b.0)));
                        names = with_stat.iter().map(|(n, _)| n.clone()).collect();
                        Some(with_stat.into_iter().collect::<HashMap<_, _>>())
                    }
                };
                if sort == Sort::Name && order == Order::Desc {
                    names.reverse();
                }
                let session = std::sync::Arc::new(ListingSession {
                    share: r.share,
                    dir_path: dir_path_str,
                    names,
                    stats,
                    created: std::time::Instant::now(),
                });
                cache.put(cache_key.clone(), session.clone());
                session
            }
        };

        let total = session.names.len();
        let page_end = total.min(DEFAULT_PAGE_SIZE);
        let mut entries = Vec::with_capacity(page_end);
        for name in &session.names[..page_end] {
            let p = r.path.join(name, max_depth)?;
            let st = match session.stats.as_ref().and_then(|m| m.get(name)) {
                Some(st) => *st,
                None => self.stat_counted(&r.root, &p)?,
            };
            entries.push(self.build_entry(r.share, &r.root, name, &p, &st, user));
        }
        let cursor = if total > page_end { Some(page_end.to_string()) } else { None };

        Ok(Listing {
            entries,
            total,
            dir_etag,
            listing_id: cache_key,
            cursor,
        })
    }

    fn evict_expired(&self, cache: &mut lru::LruCache<String, std::sync::Arc<ListingSession>>) {
        let stale: Vec<String> = cache
            .iter()
            .filter(|(_, v)| v.created.elapsed() > LISTING_TTL)
            .map(|(k, _)| k.clone())
            .collect();
        for k in stale {
            cache.pop(&k);
        }
    }

    /// Unlike [`Self::list`], this allocates a stable id when the entry
    /// does not already have one (`Self::build_entry`'s plain lookup misses)
    /// — see this fn's own extended doc comment below the signature for why
    /// the split lands here and not in `build_entry` itself, which both
    /// callers share.
    pub fn stat_entry(&self, user: UserId, vpath: &str) -> Result<Entry, CoreError> {
        let r = self.resolve_want(user, vpath, Perms::READ)?;
        let st = self.stat_counted(&r.root, &r.path)?;
        let name = r.path.name().unwrap_or("").to_string();
        let mut entry = self.build_entry(r.share, &r.root, &name, &r.path, &st, user);
        if entry.id.is_none() {
            // 's "lazy allocation" table lists exactly two
            // kinds of caller that need a stable id at all: WebDAV/NC sync,
            // and "dead properties · locks · favorites · share links" — and
            // a share link is created from a *specific, already-named*
            // file, the same shape as this function's one caller-picked
            // `vpath`. `list()` deliberately does *not* do this: a plain
            // directory listing addresses potentially every entry in a
            // multi-million-file tree, and materializing a row per entry
            // just because someone browsed past it is exactly the
            // unbounded write volume §2 exists to prevent ("a row is made
            // only when a fileid is actually requested"). A single
            // `stat_entry` call is bounded by construction: one call, one
            // path, one row at most — the same cost `ensure_fileid` already pays for the
              // WebDAV/compat prop-emission path it was originally built for
            // (`aggregate.rs`'s doc comment on `ensure_fileid`). Before
            // this, `GET /api/fs/stat` on an untouched file omitted `id`
            // entirely, which meant `POST /api/fs/link` — the endpoint the
            // web UI needs to mint a single-file download URL — had
            // nothing to send unless the file had *already* been
            // share-linked once, a chicken-and-egg the UI worked around by
            // disabling the download button instead.
            //
            // Best-effort: an allocation failure here must not turn an
            // otherwise-successful stat into an error — the entry is still
            // real and still fully described, just without a durable id
            // this one call happened not to be able to mint.
            // A share's own root has no real `node` row to allocate at all
            // (`ensure_fileid`'s doc comment) — `ensure_fileid_chain` hands
            // back the sentinel `FileId(0)` for it, which is a fact about
            // the *chain*, not a real id, and every share root would
            // otherwise report the identical value through this API. `None`
            // stays the honest answer for that one case; a `vpath` naming
            // an actual file always has at least one path component, so it
            // can never land here.
            match self.ensure_fileid(r.share, &r.path) {
                Ok(id) if id != FileId::new(0) => entry.id = Some(id),
                _ => {}
            }
        }
        Ok(entry)
    }

    pub fn mkdir(&self, user: UserId, vpath: &str) -> Result<Entry, CoreError> {
        let r = self.resolve_want(user, vpath, Perms::CREATE)?;
        r.root.mkdir(&r.path)?;
        self.mark_dirty(r.share, &r.path);
        let st = self.stat_counted(&r.root, &r.path)?;
        let name = r.path.name().unwrap_or("").to_string();
        Ok(self.build_entry(r.share, &r.root, &name, &r.path, &st, user))
    }

    pub fn rename(
        &self,
        user: UserId,
        vpath: &str,
        new_name: &str,
        if_match: Option<&str>,
    ) -> Result<Entry, CoreError> {
        let r = self.resolve_want(user, vpath, Perms::RENAME)?;
        let st = self.stat_counted(&r.root, &r.path)?;
        let cur_etag = MetaStore::file_etag(&st);
        if let Some(im) = if_match {
            if im != cur_etag {
                return Err(CoreError::Precondition { current_etag: cur_etag });
            }
        }

        let max_depth = r.root.policy().max_depth;
        let dest = r.path.parent().join(new_name, max_depth)?;
        if path_exists(&r.root, &dest)? {
            return Err(CoreError::Conflict);
        }
        r.root.rename(&r.path, &dest, true)?;

        if let Ok(Some(id)) = self.meta.lookup_fileid(r.share, &st) {
            if let Ok(parent_id) = self.ensure_fileid_chain(&r.root, r.share, &r.path.parent()) {
                let _ = self.meta.rename_node(id, parent_id, new_name);
            }
        }
        self.mark_dirty(r.share, &r.path);
        self.mark_dirty(r.share, &dest);

        let new_st = self.stat_counted(&r.root, &dest)?;
        Ok(self.build_entry(r.share, &r.root, new_name, &dest, &new_st, user))
    }

    /// Pick `base.txt` -> `base (2).txt` -> `base (3).txt` ... — used by
    /// `OnConflict::Rename`.
    pub(crate) fn unique_name(&self, root: &ShareRoot, dest_dir: &SafePath, name: &str) -> Result<SafePath, CoreError> {
        let max_depth = root.policy().max_depth;
        let (stem, ext) = match name.rsplit_once('.') {
            Some((s, e)) if !s.is_empty() => (s.to_string(), Some(e.to_string())),
            _ => (name.to_string(), None),
        };
        for n in 2..10_000u32 {
            let candidate = match &ext {
                Some(e) => format!("{stem} ({n}).{e}"),
                None => format!("{stem} ({n})"),
            };
            let p = dest_dir.join(&candidate, max_depth)?;
            if !path_exists(root, &p)? {
                return Ok(p);
            }
        }
        Err(CoreError::Conflict)
    }

    fn copy_file_internal(
        &self,
        src_root: &ShareRoot,
        src_path: &SafePath,
        dest_root: &ShareRoot,
        dest_path: &SafePath,
        overwrite: bool,
    ) -> Result<(), CoreError> {
        let src_fh = src_root.open_read(src_path)?;
        let src_st = src_fh.stat()?;
        let max_depth = dest_root.policy().max_depth;

        let tmp_path = dest_path.parent().join_control(&part_name(), max_depth)?;
        let mode = dest_root.policy().mode_file;

        let result = (|| -> Result<(), CoreError> {
            let tmp_fh = dest_root.create_excl(&tmp_path, mode)?;
            // Kernel-side copy ( / `TECH-STACK.md`
            // §3): reflink on btrfs/XFS when block alignment allows it, an
            // in-kernel copy otherwise, no userspace round trip either way.
            // `copy_range_from` falls back to a bounded buffered loop by
            // itself (EXDEV/EOPNOTSUPP/ENOSYS, or always on the portable
            // backend), so this call site doesn't carry its own loop.
            let off = tmp_fh.copy_range_from(&src_fh, 0, 0, src_st.size)?;
            tmp_fh.set_len(off)?;
            tmp_fh.sync_data()?;
            drop(tmp_fh);
            let _ = dest_root.set_times(&tmp_path, src_st.mtime_ns);
            dest_root.rename(&tmp_path, dest_path, !overwrite)?;
            // `sync_data` above made the *bytes* durable; the rename that put
            // them under the real name is a directory operation and can still
            // be in the journal when the power goes, leaving a complete file
            // under a `.scpart-` name nobody will ever look for. Costs one
            // `fsync` per copied file on top of the one already being paid —
            // a copy that reports success and isn't there afterwards is not a
            // trade this project makes.
            dest_root.sync_dir(&dest_path.parent())?;
            Ok(())
        })();

        if result.is_err() {
            let _ = dest_root.unlink(&tmp_path);
        }
        result
    }

    /// `pub(crate)`, not private: `homes.rs::ensure_home` reuses this to seed
    /// a new home from `.template` rather than carrying a second copy-tree
    /// implementation for the same operation.
    pub(crate) fn copy_recursive(
        &self,
        src_root: &ShareRoot,
        src_path: &SafePath,
        dest_root: &ShareRoot,
        dest_path: &SafePath,
    ) -> Result<(), CoreError> {
        let st = src_root.stat(src_path)?;
        let max_depth = dest_root.policy().max_depth;
        if st.kind == Kind::Dir {
            if !path_exists(dest_root, dest_path)? {
                dest_root.mkdir(dest_path)?;
            }
            for entry in src_root.read_dir(src_path)? {
                let sp = src_path.join(&entry.name, max_depth)?;
                let dp = dest_path.join(&entry.name, max_depth)?;
                self.copy_recursive(src_root, &sp, dest_root, &dp)?;
            }
            Ok(())
        } else {
            self.copy_file_internal(src_root, src_path, dest_root, dest_path, path_exists(dest_root, dest_path)?)
        }
    }

    pub fn copy_entries(
        &self,
        user: UserId,
        paths: &[String],
        dest: &str,
        on_conflict: OnConflict,
    ) -> Result<Vec<OpResult>, CoreError> {
        let dest_r = self.resolve_want(user, dest, Perms::CREATE)?;
        let max_depth = dest_r.root.policy().max_depth;
        // Once per call, not once per path: the whole batch stages into this
        // one directory.
        self.sweep_stale_parts(&dest_r.root, &dest_r.path);
        let mut results = Vec::with_capacity(paths.len());
        for p in paths {
            let outcome = (|| -> Result<OpResult, CoreError> {
                let src = self.resolve_want(user, p, Perms::READ)?;
                let name = src
                    .path
                    .name()
                    .ok_or_else(|| CoreError::InvalidPath("cannot copy share root".into()))?
                    .to_string();
                let mut dest_path = dest_r.path.join(&name, max_depth)?;

                if path_exists(&dest_r.root, &dest_path)? {
                    match on_conflict {
                        OnConflict::Fail => {
                            return Ok(OpResult { path: p.clone(), ok: false, error: Some(CoreError::Conflict), will_copy: false })
                        }
                        OnConflict::Skip => return Ok(OpResult { path: p.clone(), ok: true, error: None, will_copy: false }),
                        OnConflict::Rename => {
                            dest_path = self.unique_name(&dest_r.root, &dest_r.path, &name)?;
                        }
                        OnConflict::Overwrite => {}
                    }
                }

                // A directory copy is not one atomic rename: `copy_recursive`
                // creates the destination and fills it entry by entry, so an
                // interruption leaves a half-populated directory sitting at
                // the real name, indistinguishable from a finished one. Stage
                // it under the same reserved `.scpart-{uuid}` name the
                // cross-device move uses (`part_name`) and publish with a
                // single rename. Files need none of this — `copy_file_internal`
                // already stages every one of them.
                //
                // The exception is overwriting a directory that is already
                // there: that is a merge into existing content, and a rename
                // cannot replace a non-empty directory anyway.
                let src_st = self.stat_counted(&src.root, &src.path)?;
                let stage_dir = src_st.kind == Kind::Dir && !path_exists(&dest_r.root, &dest_path)?;
                if stage_dir {
                    let tmp_path = dest_r.path.join_control(&part_name(), max_depth)?;
                    let staged = (|| -> Result<(), CoreError> {
                        self.copy_recursive(&src.root, &src.path, &dest_r.root, &tmp_path)?;
                        dest_r.root.rename(&tmp_path, &dest_path, true)?;
                        dest_r.root.sync_dir(&dest_path.parent())?;
                        Ok(())
                    })();
                    if staged.is_err() {
                        let _ = Self::delete_recursive(&dest_r.root, &tmp_path);
                    }
                    staged?;
                } else {
                    self.copy_recursive(&src.root, &src.path, &dest_r.root, &dest_path)?;
                }
                self.mark_dirty(dest_r.share, &dest_path);
                Ok(OpResult { path: p.clone(), ok: true, error: None, will_copy: false })
            })();
            results.push(outcome.unwrap_or_else(|e| OpResult { path: p.clone(), ok: false, error: Some(e), will_copy: false }));
        }
        Ok(results)
    }

    fn move_one(
        &self,
        user: UserId,
        src_vpath: &str,
        dest_r: &Resolved,
        on_conflict: OnConflict,
        if_match: Option<&str>,
        dry_run: bool,
    ) -> Result<OpResult, CoreError> {
        let src = self.resolve_want(user, src_vpath, Perms::MOVE)?;
        let max_depth = dest_r.root.policy().max_depth;
        let name = src
            .path
            .name()
            .ok_or_else(|| CoreError::InvalidPath("cannot move share root".into()))?
            .to_string();
        let mut dest_path = dest_r.path.join(&name, max_depth)?;
        let src_st = self.stat_counted(&src.root, &src.path)?;

        if path_exists(&dest_r.root, &dest_path)? {
            match on_conflict {
                OnConflict::Fail => return Ok(OpResult { path: src_vpath.into(), ok: false, error: Some(CoreError::Conflict), will_copy: false }),
                OnConflict::Skip => return Ok(OpResult { path: src_vpath.into(), ok: true, error: None, will_copy: false }),
                OnConflict::Rename => {
                    dest_path = self.unique_name(&dest_r.root, &dest_r.path, &name)?;
                }
                OnConflict::Overwrite => {
                    let cur = self.stat_counted(&dest_r.root, &dest_path)?;
                    let cur_etag = MetaStore::file_etag(&cur);
                    match if_match {
                        Some(im) if im == cur_etag => {}
                        _ => {
                            return Ok(OpResult {
                                path: src_vpath.into(),
                                ok: false,
                                error: Some(CoreError::Precondition { current_etag: cur_etag }),
                                will_copy: false,
                            })
                        }
                    }
                }
            }
        }

        let will_copy = src.share != dest_r.share || dest_r.root.root_dev() != src_st.dev;

        if dry_run {
            return Ok(OpResult { path: src_vpath.into(), ok: true, error: None, will_copy });
        }

        if will_copy {
            // Cross-device (or cross-share) move can't rename, so it copies —
            // stage under the same reserved `.scpart-{uuid}` name
            // `copy_file_internal` uses (see `part_name`), then rename into
            // the final name once the copy has fully landed. Without this, a
            // copy that fails or is killed partway leaves broken/partial
            // content visible at the destination's real name; staging keeps
            // the destination absent (nothing to see) until the copy is
            // provably complete.
            let tmp_path = dest_r.path.join_control(&part_name(), max_depth)?;
            let result = (|| -> Result<(), CoreError> {
                self.copy_recursive(&src.root, &src.path, &dest_r.root, &tmp_path)?;
                dest_r.root.rename(&tmp_path, &dest_path, !matches!(on_conflict, OnConflict::Overwrite))?;
                dest_r.root.sync_dir(&dest_path.parent())?;
                Ok(())
            })();
            if result.is_err() {
                if src_st.kind == Kind::Dir {
                    let _ = Self::delete_recursive(&dest_r.root, &tmp_path);
                } else {
                    let _ = dest_r.root.unlink(&tmp_path);
                }
            }
            result?;
            if src_st.kind == Kind::Dir {
                Self::delete_recursive(&src.root, &src.path)?;
            } else {
                src.root.unlink(&src.path)?;
            }
        } else {
            // No `sync_dir` here, unlike the staged branch above and every
            // copy: a same-device move creates no new bytes. If this rename is
            // lost to a power cut the file is still sitting at the source, so
            // what did not survive is the move, not the data.
            src.root.rename(&src.path, &dest_path, !matches!(on_conflict, OnConflict::Overwrite))?;
        }
        self.mark_dirty(src.share, &src.path);
        self.mark_dirty(dest_r.share, &dest_path);
        Ok(OpResult { path: src_vpath.into(), ok: true, error: None, will_copy })
    }

    pub fn move_entries(
        &self,
        user: UserId,
        paths: &[String],
        dest: &str,
        on_conflict: OnConflict,
        if_match: &HashMap<String, String>,
    ) -> Result<Vec<OpResult>, CoreError> {
        let dest_r = self.resolve_want(user, dest, Perms::CREATE)?;
        // Only the cross-device branch of `move_one` stages, but it stages
        // here, so this is where its leftovers would be.
        self.sweep_stale_parts(&dest_r.root, &dest_r.path);
        let mut results = Vec::with_capacity(paths.len());
        for p in paths {
            let im = if_match.get(p).map(|s| s.as_str());
            let outcome = self.move_one(user, p, &dest_r, on_conflict, im, false);
            results.push(outcome.unwrap_or_else(|e| OpResult { path: p.clone(), ok: false, error: Some(e), will_copy: false }));
        }
        Ok(results)
    }

    /// inspect without mutating, so the UI can warn
    /// "this will copy, not move" before the user commits.
    pub fn move_entries_dry_run(
        &self,
        user: UserId,
        paths: &[String],
        dest: &str,
        on_conflict: OnConflict,
        if_match: &HashMap<String, String>,
    ) -> Result<Vec<OpResult>, CoreError> {
        let dest_r = self.resolve_want(user, dest, Perms::CREATE)?;
        let mut results = Vec::with_capacity(paths.len());
        for p in paths {
            let im = if_match.get(p).map(|s| s.as_str());
            let outcome = self.move_one(user, p, &dest_r, on_conflict, im, true);
            results.push(outcome.unwrap_or_else(|e| OpResult { path: p.clone(), ok: false, error: Some(e), will_copy: false }));
        }
        Ok(results)
    }

    /// Free-standing rather than a method: the walk needs nothing but the
    /// share root, and taking `&self` only to hand it back to the recursive
    /// call is what clippy's `only_used_in_recursion` objects to.
    pub(crate) fn delete_recursive(root: &ShareRoot, path: &SafePath) -> Result<(), CoreError> {
        let max_depth = root.policy().max_depth;
        for entry in root.read_dir(path)? {
            let p = path.join(&entry.name, max_depth)?;
            if entry.kind == Kind::Dir {
                Self::delete_recursive(root, &p)?;
            } else {
                root.unlink(&p)?;
            }
        }
        root.rmdir(path)?;
        Ok(())
    }

    pub fn delete(&self, user: UserId, paths: &[String], permanent: bool) -> Result<Vec<OpResult>, CoreError> {
        let mut results = Vec::with_capacity(paths.len());
        for p in paths {
            let outcome = (|| -> Result<OpResult, CoreError> {
                let r = self.resolve_want(user, p, Perms::DELETE)?;
                let st = self.stat_counted(&r.root, &r.path)?;
                let use_trash = !permanent && r.root.policy().trash != sc_vfs::TrashMode::Off;
                if use_trash {
                    self.trash_move(&r, &st)?;
                } else {
                    // Quota charge-back (`FEATURES.md` #49): only a permanent
                    // delete actually frees bytes — trashing just relocates
                    // them (`trash.rs` charges on purge instead). Size must be
                    // read before the delete, not after.
                    let freed = if st.kind == Kind::Dir { self.aggregate(r.share, &r.path)?.rsize } else { st.size };
                    if st.kind == Kind::Dir {
                        Self::delete_recursive(&r.root, &r.path)?;
                    } else {
                        r.root.unlink(&r.path)?;
                    }
                    self.charge_quota(user, -(freed as i64));
                }
                self.mark_dirty(r.share, &r.path);
                Ok(OpResult { path: p.clone(), ok: true, error: None, will_copy: false })
            })();
            results.push(outcome.unwrap_or_else(|e| OpResult { path: p.clone(), ok: false, error: Some(e), will_copy: false }));
        }
        Ok(results)
    }

    /// Whole-file read, bytes verbatim. Returns `(contents, etag)`.
    ///
    /// `read_text` is this plus a lossy UTF-8 conversion, which is right for
    /// the text editor the web UI ships and wrong for everything else — a
    /// WebDAV `GET` of a JPEG that went through `String::from_utf8_lossy`
    /// arrives corrupted. Protocol layers that serve arbitrary content must
    /// call this one.
    pub fn read_bytes(&self, user: UserId, vpath: &str, max: usize) -> Result<(Vec<u8>, String), CoreError> {
        let r = self.resolve_want(user, vpath, Perms::READ)?;
        let st = self.stat_counted(&r.root, &r.path)?;
        if st.kind == Kind::Dir {
            return Err(CoreError::InvalidPath("is a directory".into()));
        }
        if st.size as usize > max {
            return Err(CoreError::Internal(format!("file exceeds max size {max}")));
        }
        let fh = r.root.open_read(&r.path)?;
        let mut buf = vec![0u8; st.size as usize];
        read_exact_at(&fh, &mut buf, 0)?;
        Ok((buf, MetaStore::file_etag(&st)))
    }

    pub fn read_text(&self, user: UserId, vpath: &str, max: usize) -> Result<(String, String), CoreError> {
        let (buf, etag) = self.read_bytes(user, vpath, max)?;
        Ok((String::from_utf8_lossy(&buf).into_owned(), etag))
    }

    /// Free/used accounting for the filesystem backing `vpath`
    /// (and the RFC 4331 properties `sc-dav` derives from
    /// it). `available` is `None` — never `0` — when the backend cannot
    /// answer.
    pub fn quota(&self, user: UserId, vpath: &str) -> Result<crate::entry::Quota, CoreError> {
        let r = self.resolve_want(user, vpath, Perms::READ)?;
        match r.root.statfs_free() {
            Ok((free, total)) => Ok(crate::entry::Quota {
                used: total.saturating_sub(free),
                available: Some(free),
            }),
            Err(_) => Ok(crate::entry::Quota { used: 0, available: None }),
        }
    }

    /// Copy one entry to a **named destination**.
    ///
    /// `copy_entries(paths, dest_dir)` means "copy these into that directory,
    /// keeping their names", which cannot express a rename-on-copy at all.
    /// Composing it with `rename` to fake this destroys the source whenever
    /// the destination lands in the source's own directory — macOS Finder's
    /// "Duplicate" is exactly that path. So this is a primitive, not a
    /// composition: the copy is written to a temp file next to the
    /// destination and renamed into place, and the source is never touched.
    pub fn copy_to(&self, user: UserId, src: &str, dst: &str, overwrite: bool) -> Result<Entry, CoreError> {
        let s = self.resolve_want(user, src, Perms::READ)?;
        let d = self.resolve_want(user, dst, Perms::CREATE)?;

        if s.path.name().is_none() {
            return Err(CoreError::InvalidPath("cannot copy a share root".into()));
        }
        if d.path.name().is_none() {
            return Err(CoreError::InvalidPath("cannot copy onto a share root".into()));
        }
        if s.share == d.share && s.path.to_display_string() == d.path.to_display_string() {
            return Err(CoreError::Conflict);
        }
        // Copying a directory into its own subtree would recurse forever.
        if s.share == d.share && s.path.is_prefix_of(&d.path) {
            return Err(CoreError::InvalidPath("destination is inside the source".into()));
        }

        let src_st = self.stat_counted(&s.root, &s.path)?;
        let dst_exists = path_exists(&d.root, &d.path)?;
        if dst_exists && !overwrite {
            return Err(CoreError::Conflict);
        }

        // Quota check (`FEATURES.md` #49): a copy duplicates bytes, so it is
        // one of the two write paths (with upload finalize) that can grow a
        // user's usage. Directory size comes from the already-cached
        // recursive aggregate (`aggregate.rs`), not a fresh walk. Charged
        // for the full source size regardless of an overwritten destination
        // — not crediting the replaced bytes is a deliberate simplification
        // (see `quota.rs`'s module doc), and errs toward hitting the cap
        // sooner rather than under-counting.
        let copy_bytes = if src_st.kind == Kind::Dir {
            self.aggregate(s.share, &s.path)?.rsize
        } else {
            src_st.size
        };
        self.check_quota(user, copy_bytes)?;

        if src_st.kind == Kind::Dir {
            // A directory copy is not a single atomic rename, so an existing
            // destination has to be cleared first or the two trees merge.
            if dst_exists {
                let dst_st = self.stat_counted(&d.root, &d.path)?;
                if dst_st.kind == Kind::Dir {
                    Self::delete_recursive(&d.root, &d.path)?;
                } else {
                    d.root.unlink(&d.path)?;
                }
            }
            self.copy_recursive(&s.root, &s.path, &d.root, &d.path)?;
        } else {
            if dst_exists {
                let dst_st = self.stat_counted(&d.root, &d.path)?;
                if dst_st.kind == Kind::Dir {
                    Self::delete_recursive(&d.root, &d.path)?;
                }
            }
            // File onto file is an atomic temp-then-rename replace, so the
            // destination survives intact if the copy fails halfway.
            self.copy_file_internal(&s.root, &s.path, &d.root, &d.path, dst_exists)?;
        }
        self.charge_quota(user, copy_bytes as i64);

        self.mark_dirty(d.share, &d.path);
        let st = self.stat_counted(&d.root, &d.path)?;
        let name = d.path.name().unwrap_or("").to_string();
        Ok(self.build_entry(d.share, &d.root, &name, &d.path, &st, user))
    }

    /// Move one entry to a **named destination** — the mirror of
    /// [`Core::copy_to`], and for the same reason: `move_entries` keeps the
    /// source names, so it cannot express a move that also renames (WebDAV
    /// `MOVE` names its destination in the `Destination` header).
    pub fn move_to(&self, user: UserId, src: &str, dst: &str, overwrite: bool) -> Result<Entry, CoreError> {
        let s = self.resolve_want(user, src, Perms::MOVE)?;
        let d = self.resolve_want(user, dst, Perms::CREATE)?;

        if s.path.name().is_none() {
            return Err(CoreError::InvalidPath("cannot move a share root".into()));
        }
        if d.path.name().is_none() {
            return Err(CoreError::InvalidPath("cannot move onto a share root".into()));
        }
        if s.share == d.share && s.path.to_display_string() == d.path.to_display_string() {
            return self.stat_entry(user, src);
        }
        if s.share == d.share && s.path.is_prefix_of(&d.path) {
            return Err(CoreError::InvalidPath("destination is inside the source".into()));
        }

        let src_st = self.stat_counted(&s.root, &s.path)?;
        let dst_exists = path_exists(&d.root, &d.path)?;
        if dst_exists && !overwrite {
            return Err(CoreError::Conflict);
        }

        // Same filesystem: a plain rename, which is atomic and free.
        // Otherwise copy-then-delete, which is what `will_copy` warns about.
        let cross = s.share != d.share || d.root.root_dev() != src_st.dev;
        if cross {
            if dst_exists {
                let dst_st = self.stat_counted(&d.root, &d.path)?;
                if dst_st.kind == Kind::Dir {
                    Self::delete_recursive(&d.root, &d.path)?;
                } else {
                    d.root.unlink(&d.path)?;
                }
            }
            self.copy_recursive(&s.root, &s.path, &d.root, &d.path)?;
            if src_st.kind == Kind::Dir {
                Self::delete_recursive(&s.root, &s.path)?;
            } else {
                s.root.unlink(&s.path)?;
            }
        } else {
            if dst_exists && src_st.kind == Kind::Dir {
                let dst_st = self.stat_counted(&d.root, &d.path)?;
                if dst_st.kind == Kind::Dir {
                    Self::delete_recursive(&d.root, &d.path)?;
                } else {
                    d.root.unlink(&d.path)?;
                }
            }
            s.root.rename(&s.path, &d.path, !overwrite)?;
            if let Ok(Some(id)) = self.meta.lookup_fileid(s.share, &src_st) {
                if let Ok(parent_id) = self.ensure_fileid_chain(&d.root, d.share, &d.path.parent()) {
                    let _ = self.meta.rename_node(id, parent_id, d.path.name().unwrap_or(""));
                }
            }
        }

        self.mark_dirty(s.share, &s.path);
        self.mark_dirty(d.share, &d.path);
        let st = self.stat_counted(&d.root, &d.path)?;
        let name = d.path.name().unwrap_or("").to_string();
        Ok(self.build_entry(d.share, &d.root, &name, &d.path, &st, user))
    }

    /// Atomic replace (`ARCHITECTURE.md` §5.2 / "atomic
    /// replace"): a `.scpart-{rand}` temp file (`part_name`) in the *same*
    /// directory, written, `fsync`-ed, then renamed over the target.
    /// `If-Match` is mandatory for overwriting an existing file. On any
    /// failure after the temp file is created, it is unlinked before the
    /// error is returned — never left behind.
    pub fn write_text(&self, user: UserId, vpath: &str, body: &[u8], if_match: Option<&str>) -> Result<Entry, CoreError> {
        let r = self.resolve_want(user, vpath, Perms::WRITE)?;
        let exists = path_exists(&r.root, &r.path)?;
        let mode = r.root.policy().mode_file;

        let mut old_size = 0u64;
        if exists {
            let cur_st = self.stat_counted(&r.root, &r.path)?;
            old_size = cur_st.size;
            let cur_etag = MetaStore::file_etag(&cur_st);
            match if_match {
                Some(im) if im == cur_etag => {}
                _ => return Err(CoreError::Precondition { current_etag: cur_etag }),
            }
        } else if if_match.is_some() {
            return Err(CoreError::Precondition { current_etag: String::new() });
        }

        // Quota check (`FEATURES.md` #49): only the growth this write adds
        // over what it replaces counts — a same-size or shrinking edit never
        // needs the cap re-checked.
        let delta = body.len() as i64 - old_size as i64;
        if delta > 0 {
            self.check_quota(user, delta as u64)?;
        }

        let max_depth = r.root.policy().max_depth;
        let parent = r.path.parent();
        self.sweep_stale_parts(&r.root, &parent);
        let tmp_path = parent.join_control(&part_name(), max_depth)?;

        let result = (|| -> Result<(), CoreError> {
            let fh = r.root.create_excl(&tmp_path, mode)?;
            write_all_at(&fh, body, 0)?;
            fh.set_len(body.len() as u64)?;
            fh.sync_data()?;
            drop(fh);
            // `no_replace = false`: this is a deliberate, precondition-checked
            // overwrite (or first creation), not a NOREPLACE race.
            r.root.rename(&tmp_path, &r.path, false)?;
            // The rename is the publish; without syncing the directory it
            // holds, an `fsync`-ed body can still end up under the temp name.
            r.root.sync_dir(&r.path.parent())?;
            Ok(())
        })();

        if let Err(e) = result {
            let _ = r.root.unlink(&tmp_path);
            return Err(e);
        }

        // Charge the same `delta` the pre-write check used — including
        // negative, so a shrinking edit frees bytes back to the ledger too.
        if delta != 0 {
            self.charge_quota(user, delta);
        }

        self.mark_dirty(r.share, &r.path);
        let st = self.stat_counted(&r.root, &r.path)?;
        let name = r.path.name().unwrap_or("").to_string();
        Ok(self.build_entry(r.share, &r.root, &name, &r.path, &st, user))
    }
}

pub(crate) fn write_all_at(fh: &sc_vfs::FileHandle, mut buf: &[u8], mut off: u64) -> Result<(), VfsError> {
    while !buf.is_empty() {
        let n = fh.write_at(buf, off)?;
        if n == 0 {
            return Err(VfsError::Io(std::io::Error::new(std::io::ErrorKind::WriteZero, "write_at wrote 0 bytes")));
        }
        buf = &buf[n..];
        off += n as u64;
    }
    Ok(())
}

fn read_exact_at(fh: &sc_vfs::FileHandle, mut buf: &mut [u8], mut off: u64) -> Result<(), VfsError> {
    while !buf.is_empty() {
        let n = fh.read_at(buf, off)?;
        if n == 0 {
            return Err(VfsError::Io(std::io::Error::new(std::io::ErrorKind::UnexpectedEof, "read_at hit EOF early")));
        }
        let tmp = buf;
        buf = &mut tmp[n..];
        off += n as u64;
    }
    Ok(())
}

fn cmp_by(sort: Sort, a: &Stat, b: &Stat) -> std::cmp::Ordering {
    match sort {
        Sort::Size => a.size.cmp(&b.size),
        Sort::Mtime => a.mtime_ns.cmp(&b.mtime_ns),
        Sort::Kind => (a.kind as u8).cmp(&(b.kind as u8)),
        Sort::Name => std::cmp::Ordering::Equal,
    }
}

pub(crate) fn new_listing_cache() -> lru::LruCache<String, std::sync::Arc<ListingSession>> {
    lru::LruCache::new(NonZeroUsize::new(LISTING_CACHE_CAP).unwrap())
}
