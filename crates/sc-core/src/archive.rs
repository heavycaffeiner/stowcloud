//! Recursive descendant enumeration for the streaming zip archive.
//!
//! ACL gates *descent*, the same rule `sc-search`'s walker follows: a
//! subtree the caller cannot read is never
//! entered, so an unreadable directory costs nothing and leaks nothing
//! beyond "this name exists and you can't have it" (which its parent
//! listing already disclosed, or it wouldn't be reachable at all).
//!
//! This is a plain recursive walk, not the parallel `sc-search` engine —
//! archiving is not a hot path the way search is, and reusing
//! `sc-search::Walker` here would mean threading `sc-search` as a dependency
//! of `sc-core` for a single-threaded traversal it doesn't need.

use std::sync::Arc;

use sc_acl::Perms;
use sc_vfs::{Kind, SafePath, ShareId, ShareRoot, UserId};

use crate::error::CoreError;
use crate::stream::CoreFileStream;

/// One descendant discovered while walking an archive root.
#[derive(Clone, Debug)]
pub struct WalkEntry {
    /// Path relative to the archive root, `/`-joined — the natural zip entry
    /// name (prefixed with the root's own name, so a multi-path archive
    /// request doesn't collide entries from different roots that happen to
    /// share a leaf name as often).
    pub rel_path: String,
    pub is_dir: bool,
    /// `false` means: this name exists, but the caller may not read it (or
    /// it raced out from under us between the ACL check and the open). The
    /// caller records it as skipped; it must not be treated as an error for
    /// the archive as a whole.
    pub readable: bool,
    pub size: Option<u64>,
    pub mtime_ns: Option<i128>,
}

impl crate::Core {
    /// Enumerate `vpath` (a file or a directory) and, if it is a directory,
    /// everything beneath it. `visit` is called once per entry; for a
    /// readable file it also receives a `CoreFileStream` open for exactly as
    /// long as the callback needs it — the fd closes the moment `visit`
    /// returns, so a large archive never holds more than one file open at a
    /// time.
    ///
    /// Returns `Err` only when the *root* itself cannot be resolved/read;
    /// everything below the root that turns out to be unreadable is reported
    /// through `visit` (`readable: false`) rather than aborting the walk.
    pub fn archive_walk(
        &self,
        user: UserId,
        vpath: &str,
        visit: &mut dyn FnMut(&WalkEntry, Option<&mut CoreFileStream>),
    ) -> Result<(), CoreError> {
        let r = self.resolve_want(user, vpath, Perms::READ)?;
        let base = r.path.name().unwrap_or("").to_string();
        let max_depth = r.root.policy().max_depth;
        let st = r.root.stat(&r.path)?;

        if st.kind != Kind::Dir {
            let (meta, mut stream) = self.open_stream_in(&r.root, &r.path, None)?;
            let entry = WalkEntry {
                rel_path: base,
                is_dir: false,
                readable: true,
                size: Some(meta.size),
                mtime_ns: Some(meta.mtime_ns),
            };
            visit(&entry, Some(&mut stream));
            return Ok(());
        }

        self.walk_rec(user, r.share, &r.root, &r.path, &base, max_depth, visit)
    }

    // As with `Core::compute_aggregate`: every parameter is a distinct,
    // differently-typed piece of recursive-walk state (user, share, fs root,
    // current path, accumulated relative name, max depth, visitor
    // callback), the compiler already catches an order mix-up, and this
    // private helper has exactly one non-recursive call site
    // (`walk_dir_tree`). A struct would just relocate the same 7 fields
    // into a literal there without making either site clearer.
    #[allow(clippy::too_many_arguments)]
    fn walk_rec(
        &self,
        user: UserId,
        share: ShareId,
        root: &Arc<ShareRoot>,
        path: &SafePath,
        rel: &str,
        max_depth: u16,
        visit: &mut dyn FnMut(&WalkEntry, Option<&mut CoreFileStream>),
    ) -> Result<(), CoreError> {
        let entries = match root.read_dir(path) {
            Ok(e) => e,
            // The directory vanished/became unreadable between our parent's
            // ACL check and this recursion step — report nothing further
            // under it rather than failing the whole archive.
            Err(_) => return Ok(()),
        };
        for entry in entries {
            let Ok(child_path) = path.join(&entry.name, max_depth) else {
                continue;
            };
            let child_rel = format!("{rel}/{}", entry.name);
            let is_dir = entry.kind == Kind::Dir;
            let readable = self.acl.effective(user, share, &child_path).contains(Perms::READ);

            if is_dir {
                let we = WalkEntry {
                    rel_path: child_rel.clone(),
                    is_dir: true,
                    readable,
                    size: None,
                    mtime_ns: None,
                };
                visit(&we, None);
                if readable {
                    self.walk_rec(user, share, root, &child_path, &child_rel, max_depth, visit)?;
                }
                continue;
            }

            if !readable {
                let we = WalkEntry { rel_path: child_rel, is_dir: false, readable: false, size: None, mtime_ns: None };
                visit(&we, None);
                continue;
            }

            match self.open_stream_in(root, &child_path, None) {
                Ok((meta, mut stream)) => {
                    let we = WalkEntry {
                        rel_path: child_rel,
                        is_dir: false,
                        readable: true,
                        size: Some(meta.size),
                        mtime_ns: Some(meta.mtime_ns),
                    };
                    visit(&we, Some(&mut stream));
                }
                Err(_) => {
                    // Vanished between the readdir and the open (delete race)
                    // — report as skipped, not as a hard failure.
                    let we = WalkEntry { rel_path: child_rel, is_dir: false, readable: false, size: None, mtime_ns: None };
                    visit(&we, None);
                }
            }
        }
        Ok(())
    }
}
