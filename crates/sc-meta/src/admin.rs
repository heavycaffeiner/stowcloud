//! DB-size introspection and the incremental-vacuum knob used by the
//! `degrade` ladder in

use crate::MetaStore;

impl MetaStore {
    /// Current on-disk (or in-memory-equivalent) size in bytes, computed
    /// from `page_count * page_size` so it works identically for file-backed
    /// and shared-cache in-memory stores.
    pub fn size_bytes(&self) -> anyhow::Result<u64> {
        let conn = self.conn()?;
        let page_count: i64 = conn.query_row("PRAGMA page_count", [], |r| r.get(0))?;
        let page_size: i64 = conn.query_row("PRAGMA page_size", [], |r| r.get(0))?;
        Ok((page_count * page_size) as u64)
    }

    /// `PRAGMA incremental_vacuum(pages)` — reclaims free pages left behind
    /// by deletes. Requires `auto_vacuum = INCREMENTAL`, which is why that
    /// pragma must be set before the schema is created (`open`/
    /// `open_in_memory` both do this — see the module-level test asserting
    /// `auto_vacuum == 2`).
    pub fn incremental_vacuum(&self, pages: u32) -> anyhow::Result<()> {
        let conn = self.conn()?;
        conn.execute_batch(&format!("PRAGMA incremental_vacuum({pages})"))?;
        Ok(())
    }

    /// Fold the write-ahead log back into the main database file
    /// ('s clean-shutdown step).
    ///
    /// `TRUNCATE`, not `PASSIVE`: passive gives up the moment any other
    /// connection is mid-read, which at shutdown is exactly when we want to
    /// wait instead. This is not a durability measure — the WAL is already
    /// durable, and this whole database is a rebuildable cache
    /// — it just means the next start has nothing
    /// to replay.
    pub fn wal_checkpoint(&self) -> anyhow::Result<()> {
        let conn = self.conn()?;
        conn.execute_batch("PRAGMA wal_checkpoint(TRUNCATE)")?;
        Ok(())
    }
}
