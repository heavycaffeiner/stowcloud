//! Directory aggregate ETag —, and dirty marking on
//! the write path — §4.4. This is the one place `sc-core` allocates `sc-meta`
//! fileids for directories (files never need one just to be listed —
//! 's "lazy allocation"), because the aggregate cache is
//! keyed by fileid and computing it is the thing that actually needs that
//! identity to exist.
//!
//! Recursion depth here is bounded by `SafePath`'s `max_depth` (64 by
//! default), which is exactly the bound the design doc's "explicit stack, no
//! recursion" note is protecting against — so plain recursion is used; it
//! cannot overflow the stack for any path `SafePath::parse` would have
//! accepted in the first place.

use std::collections::HashSet;
use std::sync::Arc;

use sc_meta::{Aggregate, MetaStore};
use sc_vfs::{FileId, Kind, SafePath, ShareId, ShareRoot};
use parking_lot::Mutex;

impl crate::Core {
    /// Walk from the share root down to `path`, allocating (or reusing) a
    /// fileid for every directory component along the way. Files never need
    /// one here.
    pub(crate) fn ensure_fileid_chain(
        &self,
        root: &ShareRoot,
        share: ShareId,
        path: &SafePath,
    ) -> anyhow::Result<FileId> {
        let max_depth = root.policy().max_depth;
        let mut cur_id = FileId::new(0);
        let mut cur_path = SafePath::root();
        for comp in path.components() {
            // `join_existing`: `path` was resolved before it got here, so this
            // is re-walking something that is on disk, not naming anything new.
            // With `join` the whole chain refused an `a:b` ancestor, and since
            // this is what a permanent delete calls to charge quota back, a
            // folder under one could not be deleted at all.
            cur_path = cur_path.join_existing(comp.as_str(), max_depth)?;
            let st = root.stat(&cur_path)?;
            let is_dir = st.kind == Kind::Dir;
            cur_id = self.meta.fileid(share, cur_id, comp.as_str(), &st, is_dir)?;
        }
        Ok(cur_id)
    }

    /// Public door into [`Self::ensure_fileid_chain`], for a caller that
    /// needs a *stable* id for a single path right now rather than the
    /// aggregate that chain was originally built to compute.
    ///
    /// `sc_core::Entry::id` is populated by a pure lookup (`ops.rs`'s
    /// `build_entry`, via `MetaStore::lookup_fileid`) that never allocates —
    /// "lazy allocation": a plain listing of a share with
    /// a million files must not write a million rows just because someone
    /// browsed it. That is the right default for the native API and the web
    /// UI, which have no protocol reason to *need* an id for an entry they
    /// are merely displaying.
    ///
    /// The compat layer is different: `oc:id`/`oc:fileid`
    /// is the key a real client's local sync journal is built on
    /// (`sc-compat-nc/src/props.rs`'s `NcPropSource::emit` doc comment), so
    /// for *that* caller "no id yet" is not an acceptable answer — leaving
    /// it absent (and, before this existed, falling back to a shared
    /// placeholder) means two different resources report the same id, which
    /// a client reads as "these are the same file". This is the honest
    /// alternative promised instead of that placeholder: a real, persisted
    /// row, allocated cheaply (one `INSERT ... ON CONFLICT` per path
    /// component not already known — see `MetaStore::fileid`), and stable
    /// from that point on because it is a durable rowid, not recomputed
    /// per request.
    ///
    /// No ACL check here, deliberately: like [`Self::aggregate`] (the
    /// existing public caller of the same chain), this exists to give an
    /// *already-resolved and already-permission-checked* entry a persisted
    /// identity, not to answer "may this caller see this path" a second
    /// time — that question was already asked and answered by whatever
    /// `resolve`/`list`/`stat_entry` call produced the entry in the first
    /// place.
    pub fn ensure_fileid(&self, share: ShareId, path: &SafePath) -> anyhow::Result<FileId> {
        let root = self
            .shares
            .get(&share)
            .ok_or_else(|| anyhow::anyhow!("ensure_fileid: unknown share {}", share.get()))?
            .root
            .clone();
        self.ensure_fileid_chain(&root, share, path)
    }

    /// Mark `path`'s ancestor chain (parent up to the share root) dirty.
    /// Ancestors that never got a fileid allocated are simply skipped —
    /// absence from `diretag` already means "must recompute", so there is
    /// nothing to mark ("our own writes").
    /// Best-effort: any I/O error along the way just stops early rather than
    /// failing the caller's operation, which has already committed on disk.
    pub fn mark_dirty(&self, share: ShareId, path: &SafePath) {
        let Some(entry) = self.shares.get(&share) else {
            return;
        };
        let root = entry.root.clone();
        drop(entry);

        let max_depth = root.policy().max_depth;
        let mut ancestors = vec![SafePath::root()];
        let mut cur = SafePath::root();
        for comp in path.parent().components() {
            // Same reason, and this one failed quietly: the `break` stopped
            // marking ancestors dirty part-way, so every directory above an
            // awkward name kept serving a stale aggregate ETag.
            cur = match cur.join_existing(comp.as_str(), max_depth) {
                Ok(p) => p,
                Err(_) => break,
            };
            ancestors.push(cur.clone());
        }

        // The share root itself is never given a real `node` row (it has no
        // parent to be named under — `ensure_fileid_chain` returns the
        // `FileId(0)` sentinel for it without inserting anything), but its
        // aggregate *is* still cached in `diretag` keyed by that sentinel.
        // `lookup_fileid` can never find a row for it (there isn't one), so
        // it has to be pushed unconditionally rather than discovered.
        let mut chain = vec![FileId::new(0)];
        for anc in ancestors.iter().skip(1) {
            let Ok(st) = root.stat(anc) else { continue };
            if let Ok(Some(id)) = self.meta.lookup_fileid(share, &st) {
                chain.push(id);
            }
        }
        if let Err(e) = self.meta.mark_dirty_chain(share, &chain) {
            // Leaving this alone would let every cached aggregate in the
            // chain keep reading as fresh, and a sync client that polls a
            // directory ETag would conclude nothing changed. Falling back to
            // the whole-share bump costs a recompute for directories that did
            // not change and loses nothing.
            tracing::warn!(error = %e, share = share.get(),
                           "dirty-marking failed; invalidating the whole share instead");
            if let Err(e) = self.invalidate_share(share) {
                tracing::error!(error = %e, share = share.get(),
                                "whole-share invalidation also failed; cached directory ETags may be stale");
            }
        }
    }

    /// O(1) whole-share invalidation: bump the share's generation counter
    /// (`sc-meta`'s `bump_share_gen`) so every cached directory aggregate
    /// reads as stale on its next lookup, without walking or naming a
    /// single path. `sc-watch` calls this when its own dirty queue
    /// overflows `full_threshold`, or the OS reports a lost/overflowed
    /// batch of events — in both cases enumerating which paths changed is
    /// no longer possible or worth it.
    pub fn invalidate_share(&self, share: ShareId) -> anyhow::Result<u64> {
        self.meta.bump_share_gen(share)
    }

    /// Recursive directory aggregate ETag, cached in `sc-meta`'s `diretag`
    /// table and single-flighted per directory fileid so concurrent readers
    /// of the same stale directory don't all recompute it at once.
    pub fn aggregate(&self, share: ShareId, vpath: &SafePath) -> anyhow::Result<Aggregate> {
        let root = {
            let entry = self
                .shares
                .get(&share)
                .ok_or_else(|| anyhow::anyhow!("aggregate: unknown share {}", share.get()))?;
            entry.root.clone()
        };
        let gen = self.meta.share_gen(share)?;
        let target_id = self.ensure_fileid_chain(&root, share, vpath)?;
        let mut visited = HashSet::new();
        let mut held = Vec::new();
        self.compute_aggregate(share, &root, vpath, target_id, gen, &mut visited, &mut held)
    }

    // Each parameter is a distinct, differently-typed piece of single-flight
    // walk state (share id, fs root, current path, current fileid,
    // generation stamp, cycle-guard set, deadlock-guard vec) — the compiler
    // already rejects an order mix-up since no two are the same type.
    // Bundling them into a struct would only move the same 7 fields into a
    // literal at this fn's one recursive call site (`aggregate` and self);
    // it wouldn't reduce risk or improve readability.
    #[allow(clippy::too_many_arguments)]
    fn compute_aggregate(
        &self,
        share: ShareId,
        root: &ShareRoot,
        dir_path: &SafePath,
        dir_id: FileId,
        gen: u64,
        visited: &mut HashSet<String>,
        held: &mut Vec<FileId>,
    ) -> anyhow::Result<Aggregate> {
        if let Some(agg) = self.meta.dir_etag(share, dir_id)? {
            return Ok(agg);
        }

        // Single-flight: only one thread computes a given directory's
        // aggregate at a time. Re-check the cache after acquiring the lock —
        // another thread may have just finished.
        //
        // `held` is what keeps this from deadlocking against *itself*. The
        // lock is keyed by fileid, and a fileid is `(share, dev, ino, btime)`
        // — which is unique per directory only as long as the backend reports
        // real inode identity. The portable (Windows dev) backend does not:
        // `dev_ino` returns a constant `(0, 0)`, so two directories created
        // inside the same btime tick collide onto one id. This walk then
        // recurses from a directory into a child carrying the *same* id, and
        // `parking_lot::Mutex` is not reentrant — the thread blocks forever
        // on a lock it is already holding. Observed as a test hanging with
        // no CPU use. Skipping the re-acquire is correct in every case:
        // holding it once already excludes every other thread.
        let lock = if held.contains(&dir_id) {
            None
        } else {
            held.push(dir_id);
            Some(
                self.inflight
                    .entry(dir_id)
                    .or_insert_with(|| Arc::new(Mutex::new(())))
                    .clone(),
            )
        };
        let _guard = lock.as_ref().map(|l| l.lock());

        if let Some(agg) = self.meta.dir_etag(share, dir_id)? {
            return Ok(agg);
        }

        // Cycle detection is keyed on the *path* rather than `(dev, ino)`:
        // this traversal never follows symlinks (only `Kind::Dir` is ever
        // recursed into, and the default `SymlinkPolicy::Deny` means a
        // symlink never reports as `Dir` in the first place), so a plain
        // top-down walk cannot revisit the same directory by a different
        // path — the only theoretical cycle source ('s
        // "hard-linked directory, abnormal FS") is additionally bounded by
        // `SafePath`'s `max_depth`, which `join` enforces on every step
        // below. `(dev, ino)` would be the ideal key, but the portable
        // Windows dev backend reports a constant placeholder for both
        // (`backend/portable.rs`'s `dev_ino`), which makes every directory
        // collide and misfire this check; the path is always meaningful.
        let dir_stat = root.stat(dir_path)?;
        let _ = dir_stat; // still fetched: fails fast if the directory vanished mid-walk
        if !visited.insert(dir_path.to_display_string()) {
            return Ok(Aggregate {
                etag: hex16(blake3::hash(b"").as_bytes()),
                rsize: 0,
                rcount: 0,
            });
        }

        let max_depth = root.policy().max_depth;
        let mut names: Vec<String> = root.read_dir(dir_path)?.into_iter().map(|e| e.name.to_string()).collect();
        names.sort(); // readdir order is unstable; sorting is mandatory (§4.5)

        let mut hasher = blake3::Hasher::new();
        let (mut rsize, mut rcount) = (0u64, 0u64);
        for name in &names {
            let child_path = dir_path.join_existing(name, max_depth)?;
            let st = root.stat(&child_path)?;
            let (child_etag, size, count) = if st.kind == Kind::Dir {
                let child_id = self.meta.fileid(share, dir_id, name, &st, true)?;
                let agg =
                    self.compute_aggregate(share, root, &child_path, child_id, gen, visited, held)?;
                (agg.etag, agg.rsize, agg.rcount)
            } else {
                (MetaStore::file_etag(&st), st.size, 1)
            };
            hasher.update(name.as_bytes());
            hasher.update(child_etag.as_bytes());
            rsize += size;
            rcount += count;
        }
        visited.remove(&dir_path.to_display_string());

        let agg = Aggregate {
            etag: hex16(hasher.finalize().as_bytes()),
            rsize,
            rcount,
        };
        self.meta.put_dir_etag(share, dir_id, &agg, gen)?;
        Ok(agg)
    }
}

fn hex16(bytes: &[u8]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut out = String::with_capacity(32);
    for &b in &bytes[..16] {
        out.push(HEX[(b >> 4) as usize] as char);
        out.push(HEX[(b & 0xf) as usize] as char);
    }
    out
}
