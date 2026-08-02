//! File ETag (pure function, no cache needed) and directory aggregate ETag
//! (cached in `diretag`, invalidated by dirty-marking or by a share-wide
//! generation bump). See — the algorithm that *computes*
//! an aggregate (walking children, hashing names+etags) lives above this
//! crate; `sc-meta` only stores/retrieves/invalidates the result.

use sc_vfs::{FileId, ShareId, Stat};
use rusqlite::{params, OptionalExtension};

use crate::MetaStore;

#[derive(Clone, Debug)]
pub struct Aggregate {
    pub etag: String,
    pub rsize: u64,
    pub rcount: u64,
}

impl MetaStore {
    /// `blake3(dev, ino, size, mtime_ns)[..16]`, hex-encoded. No content
    /// hashing — reading a 10 GiB file to compute its ETag is not on the
    /// table. `mtime_ns` is truncated to 64 bits
    /// (nanosecond timestamps fit comfortably; the truncation only matters
    /// for dates outside any plausible filesystem's range).
    pub fn file_etag(st: &Stat) -> String {
        let mut buf = [0u8; 32];
        buf[0..8].copy_from_slice(&st.dev.to_le_bytes());
        buf[8..16].copy_from_slice(&st.ino.to_le_bytes());
        buf[16..24].copy_from_slice(&st.size.to_le_bytes());
        buf[24..32].copy_from_slice(&(st.mtime_ns as i64).to_le_bytes());
        let hash = blake3::hash(&buf);
        hex_encode(&hash.as_bytes()[..16])
    }

    /// Cached aggregate for `id`, or `None` if there is no cached row, it's
    /// marked dirty, or it was computed against a share generation that has
    /// since been bumped (`share.gen` mismatch —/4.6).
    /// A `None` here means "the caller must recompute and call
    /// `put_dir_etag`"; `sc-meta` itself never walks the tree.
    pub fn dir_etag(&self, share: ShareId, id: FileId) -> anyhow::Result<Option<Aggregate>> {
        let conn = self.conn()?;
        let cur_gen = Self::share_gen_on(&conn, share)?;

        let row: Option<(String, i64, i64, i64, i64)> = conn
            .query_row(
                "SELECT etag, rsize, rcount, gen, valid FROM diretag WHERE share = ?1 AND fileid = ?2",
                params![share.get() as i64, id.get()],
                |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?, r.get(3)?, r.get(4)?)),
            )
            .optional()?;

        let Some((etag, rsize, rcount, gen, valid)) = row else {
            return Ok(None);
        };
        if valid == 0 || gen as u64 != cur_gen {
            return Ok(None);
        }
        Ok(Some(Aggregate {
            etag,
            rsize: rsize as u64,
            rcount: rcount as u64,
        }))
    }

    /// Store a freshly computed aggregate, stamped with the share
    /// generation it was computed against.
    pub fn put_dir_etag(&self, share: ShareId, id: FileId, agg: &Aggregate, gen: u64) -> anyhow::Result<()> {
        let conn = self.conn()?;
        conn.execute(
            "INSERT INTO diretag(share, fileid, etag, rsize, rcount, gen, valid) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, 1) \
             ON CONFLICT(share, fileid) DO UPDATE SET \
                etag = excluded.etag, rsize = excluded.rsize, rcount = excluded.rcount, \
                gen = excluded.gen, valid = 1",
            params![
                share.get() as i64,
                id.get(),
                agg.etag,
                agg.rsize as i64,
                agg.rcount as i64,
                gen as i64
            ],
        )?;
        Ok(())
    }

    /// Mark every id in `chain` (normally a node's ancestors, root-ward)
    /// as dirty in one transaction. Safe to call for
    /// an id with no existing row yet: a placeholder row is inserted,
    /// already `valid = 0`, so the next `dir_etag` correctly reports "must
    /// recompute" instead of erroring.
    pub fn mark_dirty_chain(&self, share: ShareId, chain: &[FileId]) -> anyhow::Result<()> {
        if chain.is_empty() {
            return Ok(());
        }
        let mut conn = self.conn()?;
        let tx = conn.transaction()?;
        for id in chain {
            tx.execute(
                "INSERT INTO diretag(share, fileid, etag, rsize, rcount, gen, valid) \
                 VALUES (?1, ?2, '', 0, 0, 0, 0) \
                 ON CONFLICT(share, fileid) DO UPDATE SET valid = 0",
                params![share.get() as i64, id.get()],
            )?;
        }
        tx.commit()?;
        Ok(())
    }

    /// Bump and return a share's generation counter. This is the `O(1)`
    /// whole-share invalidation device: every `diretag` row computed against
    /// an older generation instantly reads as invalid via `dir_etag`,
    /// without touching a single row.
    pub fn bump_share_gen(&self, share: ShareId) -> anyhow::Result<u64> {
        let conn = self.conn()?;
        conn.execute(
            "INSERT INTO share_gen(share, gen) VALUES (?1, 1) \
             ON CONFLICT(share) DO UPDATE SET gen = gen + 1",
            params![share.get() as i64],
        )?;
        let gen: i64 = conn.query_row(
            "SELECT gen FROM share_gen WHERE share = ?1",
            params![share.get() as i64],
            |r| r.get(0),
        )?;
        Ok(gen as u64)
    }

    pub fn share_gen(&self, share: ShareId) -> anyhow::Result<u64> {
        let conn = self.conn()?;
        Self::share_gen_on(&conn, share)
    }

    fn share_gen_on(conn: &rusqlite::Connection, share: ShareId) -> anyhow::Result<u64> {
        let gen: Option<i64> = conn
            .query_row(
                "SELECT gen FROM share_gen WHERE share = ?1",
                params![share.get() as i64],
                |r| r.get(0),
            )
            .optional()?;
        Ok(gen.unwrap_or(0) as u64)
    }
}

fn hex_encode(bytes: &[u8]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut out = String::with_capacity(bytes.len() * 2);
    for &b in bytes {
        out.push(HEX[(b >> 4) as usize] as char);
        out.push(HEX[(b & 0xf) as usize] as char);
    }
    out
}
