//! What this server did, per account, on the caller's behalf (`journal.db`).
//!
//! Not an audit log. The file write has already succeeded by the time a row is
//! written, so a failure here is logged and dropped, and a missing row never
//! means a write did not happen. Nothing may treat the absence of a row as
//! evidence.
//!
//! Not in `meta.db` either. That file is a cache that may be deleted and
//! rebuilt, and this record cannot be rebuilt from anything: there is no way to
//! reconstruct who wrote what before it existed. A separate file makes the
//! difference visible in the data directory, where an operator can see it.
//!
//! The name is not `activity.db`, deliberately. This is not the reference
//! server's Activity app: one row per (account, file) holding the last thing
//! that account did to it, no per-event history, and no reader other than the
//! account itself.

use std::path::Path;

use parking_lot::Mutex;
use rusqlite::Connection;
use sc_vfs::{SafePath, ShareId, UserId};

/// Rows kept per account. The oldest beyond this are deleted in the same
/// transaction as the upsert, which matches nothing in the common case.
///
/// There is deliberately no age window beside it. A prune comparing a stored
/// timestamp against `now` deletes the whole table when the clock jumps
/// forward, which is an ordinary event on a small box with a dead RTC before
/// NTP corrects it. This cap is deterministic and clock-independent, and it is
/// what actually bounds the table.
const MAX_ROWS_PER_USER: usize = 500;

const SCHEMA: &str = "\
    CREATE TABLE IF NOT EXISTS write_event (\
        user   INTEGER NOT NULL,\
        share  INTEGER NOT NULL,\
        path   TEXT    NOT NULL,\
        op     TEXT    NOT NULL,\
        at_ns  INTEGER NOT NULL,\
        UNIQUE (user, share, path)\
    );\
    CREATE INDEX IF NOT EXISTS write_event_by_user ON write_event(user, at_ns DESC);";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WriteOp {
    Upload,
    Edit,
    Copy,
    Move,
    Restore,
}

impl WriteOp {
    pub fn as_str(self) -> &'static str {
        match self {
            WriteOp::Upload => "upload",
            WriteOp::Edit => "edit",
            WriteOp::Copy => "copy",
            WriteOp::Move => "move",
            WriteOp::Restore => "restore",
        }
    }

    /// A stored label back into the enum. An unrecognised one reads as
    /// `Upload`: a row written by a future version says something happened,
    /// and dropping the row over a word would lose that.
    fn from_str(s: &str) -> Self {
        match s {
            "edit" => WriteOp::Edit,
            "copy" => WriteOp::Copy,
            "move" => WriteOp::Move,
            "restore" => WriteOp::Restore,
            _ => WriteOp::Upload,
        }
    }
}

/// One row, as stored. The path is share-relative with no leading slash, which
/// is what the write side already holds and what the read side turns into a
/// vpath.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WriteRow {
    pub share: ShareId,
    pub path: String,
    pub op: WriteOp,
    pub at_ns: i64,
}

pub struct WriteJournal {
    db: Mutex<Connection>,
}

impl WriteJournal {
    pub fn open(path: &Path) -> anyhow::Result<Self> {
        Self::from_conn(Connection::open(path)?)
    }

    #[cfg(test)]
    pub fn open_in_memory() -> anyhow::Result<Self> {
        Self::from_conn(Connection::open_in_memory()?)
    }

    fn from_conn(conn: Connection) -> anyhow::Result<Self> {
        conn.pragma_update(None, "journal_mode", "WAL")?;
        conn.execute_batch("PRAGMA synchronous = NORMAL; PRAGMA busy_timeout = 5000;")?;
        conn.execute_batch(SCHEMA)?;
        Ok(WriteJournal { db: Mutex::new(conn) })
    }

    /// Upsert the newest event for one file, then apply this account's row cap.
    ///
    /// Never fails a caller: the write it describes has already succeeded.
    pub fn note(&self, user: UserId, share: ShareId, path: &SafePath, op: WriteOp, at_ns: i64) {
        // The share root itself is not a file anybody wrote.
        if path.is_empty() {
            return;
        }
        let path = path.to_display_string();
        if let Err(e) = self.note_inner(user, share, &path, op, at_ns) {
            tracing::warn!(error = %e, "could not record a write in the journal");
        }
    }

    fn note_inner(
        &self,
        user: UserId,
        share: ShareId,
        path: &str,
        op: WriteOp,
        at_ns: i64,
    ) -> rusqlite::Result<()> {
        let mut conn = self.db.lock();
        let tx = conn.transaction()?;
        tx.execute(
            "INSERT INTO write_event (user, share, path, op, at_ns) VALUES (?1, ?2, ?3, ?4, ?5)\
             ON CONFLICT (user, share, path) DO UPDATE SET op = excluded.op, at_ns = excluded.at_ns",
            rusqlite::params![user.0, share.0, path, op.as_str(), at_ns],
        )?;
        // Driven by the `(user, at_ns DESC)` index, and matching nothing until
        // an account actually holds more than the cap.
        tx.execute(
            "DELETE FROM write_event WHERE user = ?1 AND rowid NOT IN (\
                 SELECT rowid FROM write_event WHERE user = ?1 \
                 ORDER BY at_ns DESC, share, path LIMIT ?2)",
            rusqlite::params![user.0, MAX_ROWS_PER_USER as i64],
        )?;
        tx.commit()
    }

    /// The account's rows no older than `since_ns`.
    ///
    /// Ordered by `(at_ns descending, share ascending, path ascending)`: a
    /// total order, so two identical requests over an unchanged table return
    /// the identical sequence. Two rows can share a timestamp, and a database
    /// is free to return equal keys in any order.
    ///
    /// Not limited, because only the caller can drop rows for scope, liveness
    /// and kind and only it can count to its own limit. The row cap bounds this
    /// at 500. Rows are not verified here; only the caller can resolve one.
    pub fn newest(&self, user: UserId, since_ns: i64) -> Vec<WriteRow> {
        match self.newest_inner(user, since_ns) {
            Ok(rows) => rows,
            Err(e) => {
                tracing::warn!(error = %e, "could not read the write journal");
                Vec::new()
            }
        }
    }

    fn newest_inner(&self, user: UserId, since_ns: i64) -> rusqlite::Result<Vec<WriteRow>> {
        let conn = self.db.lock();
        let mut st = conn.prepare(
            "SELECT share, path, op, at_ns FROM write_event \
             WHERE user = ?1 AND at_ns >= ?2 ORDER BY at_ns DESC, share, path",
        )?;
        let rows = st
            .query_map(rusqlite::params![user.0, since_ns], |r| {
                Ok(WriteRow {
                    share: ShareId::new(r.get::<_, u32>(0)?),
                    path: r.get(1)?,
                    op: WriteOp::from_str(&r.get::<_, String>(2)?),
                    at_ns: r.get(3)?,
                })
            })?
            .collect::<rusqlite::Result<Vec<_>>>()?;
        Ok(rows)
    }

    /// Everything this account ever recorded, gone. Both id columns are
    /// `INTEGER PRIMARY KEY` with no `AUTOINCREMENT`, so the largest id is
    /// reused once its row is gone, and the next holder of this one must not
    /// inherit a history.
    pub fn forget_user(&self, user: UserId) {
        let conn = self.db.lock();
        if let Err(e) = conn.execute("DELETE FROM write_event WHERE user = ?1", [user.0]) {
            tracing::warn!(error = %e, "could not purge an account's write journal");
        }
    }

    /// The same, for a deleted share: keeping the rows would hand them to
    /// whatever share next takes that id, which is a wrong claim about who
    /// wrote a file.
    pub fn forget_share(&self, share: ShareId) {
        let conn = self.db.lock();
        if let Err(e) = conn.execute("DELETE FROM write_event WHERE share = ?1", [share.0]) {
            tracing::warn!(error = %e, "could not purge a share's write journal");
        }
    }

    /// Delete rows whose file the caller proved is gone, and only those, and
    /// only where `at_ns` still matches what the caller read.
    ///
    /// The condition is not a nicety. A read that finds a file missing and a
    /// write that recreates it can interleave, and a delete keyed on
    /// `(user, share, path)` alone would then throw away the row the write just
    /// made. A row that changed under the reader describes something that
    /// happened after the observation, and the observation does not get to
    /// overrule it.
    ///
    /// Not called for a revoked grant, an unmounted share or any other failure
    /// that can reverse: this record cannot be rebuilt, so the only deletion it
    /// performs is the one that is certainly correct.
    pub fn forget(&self, user: UserId, rows: &[WriteRow]) {
        if rows.is_empty() {
            return;
        }
        if let Err(e) = self.forget_inner(user, rows) {
            tracing::warn!(error = %e, "could not drop dead rows from the write journal");
        }
    }

    fn forget_inner(&self, user: UserId, rows: &[WriteRow]) -> rusqlite::Result<()> {
        let mut conn = self.db.lock();
        let tx = conn.transaction()?;
        {
            let mut st = tx.prepare(
                "DELETE FROM write_event \
                 WHERE user = ?1 AND share = ?2 AND path = ?3 AND at_ns = ?4",
            )?;
            for r in rows {
                st.execute(rusqlite::params![user.0, r.share.0, r.path, r.at_ns])?;
            }
        }
        tx.commit()
    }
}

/// Now, in nanoseconds since the epoch. Saturates rather than wrapping; a
/// 64-bit nanosecond epoch runs out in 2262.
pub fn now_ns() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| i64::try_from(d.as_nanos()).unwrap_or(i64::MAX))
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sp(s: &str) -> SafePath {
        SafePath::parse(s, 32).unwrap()
    }

    const ALICE: UserId = UserId(1);
    const BOB: UserId = UserId(2);
    const S1: ShareId = ShareId(1);
    const S2: ShareId = ShareId(2);

    #[test]
    fn one_row_per_file_holding_the_newest_event() {
        let j = WriteJournal::open_in_memory().unwrap();
        j.note(ALICE, S1, &sp("a.txt"), WriteOp::Upload, 100);
        j.note(ALICE, S1, &sp("a.txt"), WriteOp::Edit, 200);

        let rows = j.newest(ALICE, 0);
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].op, WriteOp::Edit);
        assert_eq!(rows[0].at_ns, 200);
    }

    #[test]
    fn the_order_is_total_so_two_reads_agree() {
        let j = WriteJournal::open_in_memory().unwrap();
        // Same timestamp, so only the tie-break decides.
        j.note(ALICE, S2, &sp("b.txt"), WriteOp::Upload, 100);
        j.note(ALICE, S1, &sp("z.txt"), WriteOp::Upload, 100);
        j.note(ALICE, S1, &sp("a.txt"), WriteOp::Upload, 100);
        j.note(ALICE, S1, &sp("c.txt"), WriteOp::Upload, 300);

        let first = j.newest(ALICE, 0);
        assert_eq!(first, j.newest(ALICE, 0));
        let names: Vec<&str> = first.iter().map(|r| r.path.as_str()).collect();
        assert_eq!(names, vec!["c.txt", "a.txt", "z.txt", "b.txt"]);
    }

    #[test]
    fn since_ns_filters_and_another_account_is_never_returned() {
        let j = WriteJournal::open_in_memory().unwrap();
        j.note(ALICE, S1, &sp("old.txt"), WriteOp::Upload, 100);
        j.note(ALICE, S1, &sp("new.txt"), WriteOp::Upload, 300);
        j.note(BOB, S1, &sp("theirs.txt"), WriteOp::Upload, 400);

        let rows = j.newest(ALICE, 200);
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].path, "new.txt");
    }

    #[test]
    fn the_cap_holds_at_500_and_evicts_the_oldest() {
        let j = WriteJournal::open_in_memory().unwrap();
        for i in 0..600i64 {
            j.note(ALICE, S1, &sp(&format!("f{i:04}.txt")), WriteOp::Upload, i);
        }
        let rows = j.newest(ALICE, i64::MIN);
        assert_eq!(rows.len(), MAX_ROWS_PER_USER);
        assert_eq!(rows[0].path, "f0599.txt");
        assert_eq!(rows[MAX_ROWS_PER_USER - 1].path, "f0100.txt");
    }

    #[test]
    fn a_deleted_account_or_share_leaves_no_rows() {
        let j = WriteJournal::open_in_memory().unwrap();
        j.note(ALICE, S1, &sp("a.txt"), WriteOp::Upload, 100);
        j.note(ALICE, S2, &sp("b.txt"), WriteOp::Upload, 100);
        j.note(BOB, S1, &sp("c.txt"), WriteOp::Upload, 100);

        j.forget_share(S1);
        assert_eq!(j.newest(ALICE, 0).len(), 1);
        assert!(j.newest(BOB, 0).is_empty());

        j.forget_user(ALICE);
        assert!(j.newest(ALICE, 0).is_empty());
    }

    /// The reader saw the file missing; a write recreated it a moment later.
    /// The row that changed under the reader survives.
    #[test]
    fn a_row_rewritten_since_the_observation_is_not_forgotten() {
        let j = WriteJournal::open_in_memory().unwrap();
        j.note(ALICE, S1, &sp("report.pdf"), WriteOp::Upload, 100);
        let observed = j.newest(ALICE, 0);

        j.note(ALICE, S1, &sp("report.pdf"), WriteOp::Upload, 500);
        j.forget(ALICE, &observed);

        let rows = j.newest(ALICE, 0);
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].at_ns, 500);
    }

    #[test]
    fn an_unchanged_dead_row_is_forgotten() {
        let j = WriteJournal::open_in_memory().unwrap();
        j.note(ALICE, S1, &sp("gone.txt"), WriteOp::Upload, 100);
        let observed = j.newest(ALICE, 0);
        j.forget(ALICE, &observed);
        assert!(j.newest(ALICE, 0).is_empty());
    }
}
