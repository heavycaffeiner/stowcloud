//! `node` table access: fileid allocation, path resolution, rename, GC.
//!
//! See `ARCHITECTURE.md` §4.1. The single unique index is `(share, dev, ino,
//! btime_ns)` — the forward direction (path -> fileid) is answered from a
//! `statx` result we already have in hand; the reverse direction (fileid ->
//! path) is answered by rowid lookup + walking `parent` up to the share root
//! sentinel (`FileId(0)`). No path is ever stored.

use sc_vfs::{FileId, ShareId, Stat};
use rusqlite::{params, OptionalExtension};

use crate::{MetaStore, NODE_FLAG_IS_DIR, NODE_FLAG_PINNED};

/// Chain-walking is bounded so a corrupt `parent` cycle (which should never
/// happen, but "should never happen" is not a proof) fails loudly instead of
/// looping forever.
const MAX_RESOLVE_HOPS: usize = 8192;

impl MetaStore {
    /// Return the stable fileid for `(share, st.dev, st.ino, st.btime_ns)`,
    /// allocating a new row on first use. **Lazy allocation**: this is the
    /// only thing in `sc-meta` that ever inserts into `node`, and it is only
    /// called by consumers that actually need a stable id (DAV rename
    /// tracking, dead properties, locks, share links —
    /// `ARCHITECTURE.md` §4.1). A web-UI-only deployment that never calls
    /// this creates zero rows.
    ///
    /// If a row already exists for this physical file, `parent`/`name`/
    /// `size`/`mtime_ns`/the directory bit are refreshed to match what the
    /// caller just observed (lazy revalidation of an out-of-band rename or
    /// write — the filesystem is the source of truth, this cache just
    /// catches up).
    pub fn fileid(
        &self,
        share: ShareId,
        parent: FileId,
        name: &str,
        st: &Stat,
        is_dir: bool,
    ) -> anyhow::Result<FileId> {
        let conn = self.conn()?;
        let btime = st.btime_ns.map(|b| b as i64);
        let dev = st.dev as i64;
        let ino = st.ino as i64;

        let existing: Option<(i64, i64, String, i64)> = conn
            .query_row(
                "SELECT id, parent, name, flags FROM node \
                 WHERE share = ?1 AND dev = ?2 AND ino = ?3 AND btime_ns IS ?4",
                params![share.get() as i64, dev, ino, btime],
                |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?)),
            )
            .optional()?;

        if let Some((id, cur_parent, cur_name, cur_flags)) = existing {
            let new_flags = if is_dir {
                cur_flags | NODE_FLAG_IS_DIR
            } else {
                cur_flags & !NODE_FLAG_IS_DIR
            };
            let size = st.size as i64;
            let mtime = st.mtime_ns as i64;
            if cur_parent != parent.get()
                || cur_name != name
                || new_flags != cur_flags
            {
                conn.execute(
                    "UPDATE node SET parent = ?1, name = ?2, size = ?3, mtime_ns = ?4, flags = ?5 \
                     WHERE id = ?6",
                    params![parent.get(), name, size, mtime, new_flags, id],
                )?;
            } else {
                // Still refresh the cheap sort/display cache even when
                // identity didn't change (§5 of: size/
                // mtime here exist purely to avoid RAID seeks).
                conn.execute(
                    "UPDATE node SET size = ?1, mtime_ns = ?2 WHERE id = ?3",
                    params![size, mtime, id],
                )?;
            }
            return Ok(FileId::new(id));
        }

        // The gate is here, not at the top of the function: the branch above
        // only refreshes an existing row, which cannot grow the file, and a
        // file that already has an id keeps working under DAV while the
        // free-space floor holds. Allocating a *new* id is what adds a row.
        self.ensure_writable()?;

        let flags = if is_dir { NODE_FLAG_IS_DIR } else { 0 };
        conn.execute(
            "INSERT INTO node(share, parent, name, dev, ino, btime_ns, flags, size, mtime_ns) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)",
            params![
                share.get() as i64,
                parent.get(),
                name,
                dev,
                ino,
                btime,
                flags,
                st.size as i64,
                st.mtime_ns as i64,
            ],
        )?;
        Ok(FileId::new(conn.last_insert_rowid()))
    }

    /// Pure lookup by physical identity — never allocates. Used to check
    /// "does this file already have a stable id" without the side effect of
    /// creating one.
    pub fn lookup_fileid(&self, share: ShareId, st: &Stat) -> anyhow::Result<Option<FileId>> {
        let conn = self.conn()?;
        let btime = st.btime_ns.map(|b| b as i64);
        let id: Option<i64> = conn
            .query_row(
                "SELECT id FROM node WHERE share = ?1 AND dev = ?2 AND ino = ?3 AND btime_ns IS ?4",
                params![share.get() as i64, st.dev as i64, st.ino as i64, btime],
                |r| r.get(0),
            )
            .optional()?;
        Ok(id.map(FileId::new))
    }

    /// Walk `parent` from `id` up to the share-root sentinel (`FileId(0)`),
    /// joining component names with `/` along the way. `None` means the id
    /// doesn't exist (already GC'd, or never allocated).
    ///
    /// Deliberately `O(depth)`, not `O(1)` — see `ARCHITECTURE.md` §4.1: the
    /// only index is on physical identity, so path resolution is always a
    /// parent-chain walk, never a forward lookup. This is the other half of
    /// the trade that makes `rename_node` a single-row update.
    pub fn resolve_path(&self, id: FileId) -> anyhow::Result<Option<(ShareId, String)>> {
        let conn = self.conn()?;
        let mut cur = id.get();
        let mut names: Vec<String> = Vec::new();
        let mut share: Option<i64> = None;
        let mut hops = 0usize;

        while cur != 0 {
            hops += 1;
            anyhow::ensure!(
                hops <= MAX_RESOLVE_HOPS,
                "resolve_path: parent chain exceeded {MAX_RESOLVE_HOPS} hops \
                 (cyclic/corrupt node table?)"
            );

            let row: Option<(i64, i64, String)> = conn
                .query_row(
                    "SELECT share, parent, name FROM node WHERE id = ?1",
                    params![cur],
                    |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)),
                )
                .optional()?;

            let Some((s, parent, name)) = row else {
                return Ok(None);
            };
            share.get_or_insert(s);
            names.push(name);
            cur = parent;
        }

        let Some(share) = share else {
            return Ok(None);
        };
        names.reverse();
        Ok(Some((ShareId::new(share as u32), names.join("/"))))
    }

    /// Rename/move a node in place: a single `UPDATE` of the node's own
    /// `(parent, name)`. Descendants are untouched — they reference their
    /// parent by id, so `resolve_path` for every descendant is correct on
    /// the very next call with no further writes (`ARCHITECTURE.md` §4.1,
    /// "measured estimate after the write").
    pub fn rename_node(&self, id: FileId, new_parent: FileId, new_name: &str) -> anyhow::Result<()> {
        let conn = self.conn()?;
        let n = conn.execute(
            "UPDATE node SET parent = ?1, name = ?2 WHERE id = ?3",
            params![new_parent.get(), new_name, id.get()],
        )?;
        anyhow::ensure!(n == 1, "rename_node: no such fileid {}", id.get());
        Ok(())
    }

    /// Delete rows whose `(dev, ino)` no longer exists on disk, per
    /// `alive(dev, ino)` supplied by the caller (which actually has a
    /// filesystem handle — `sc-meta` never touches the FS itself). Rows
    /// carrying the `pinned` bit (dead properties / locks / favorites /
    /// share links reference them, `ARCHITECTURE.md` §4.1) are always kept:
    /// a live fileid must never be reissued to a different physical file
    /// out from under a client that still remembers it.
    ///
    /// Returns the number of rows removed.
    pub fn gc_dead_nodes(
        &self,
        share: ShareId,
        alive: &dyn Fn(u64, u64) -> bool,
    ) -> anyhow::Result<usize> {
        let mut conn = self.conn()?;

        let candidates: Vec<(i64, i64, i64, i64)> = {
            let mut stmt =
                conn.prepare("SELECT id, dev, ino, flags FROM node WHERE share = ?1")?;
            let mapped = stmt.query_map(params![share.get() as i64], |r| {
                Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?))
            })?;
            let rows: Result<Vec<_>, _> = mapped.collect();
            rows?
        };

        let dead: Vec<i64> = candidates
            .into_iter()
            .filter(|(_, dev, ino, flags)| {
                flags & NODE_FLAG_PINNED == 0 && !alive(*dev as u64, *ino as u64)
            })
            .map(|(id, ..)| id)
            .collect();

        if dead.is_empty() {
            return Ok(0);
        }

        let tx = conn.transaction()?;
        for id in &dead {
            tx.execute("DELETE FROM diretag WHERE fileid = ?1", params![id])?;
            tx.execute("DELETE FROM dav_prop WHERE fileid = ?1", params![id])?;
            tx.execute("DELETE FROM node WHERE id = ?1", params![id])?;
        }
        tx.commit()?;
        Ok(dead.len())
    }
}
