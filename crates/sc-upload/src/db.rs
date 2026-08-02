//! SQLite persistence for upload sessions. Owns its own table set inside
//! whatever DB file it's pointed at (per the sc-meta dependency contract:
//! "you may open your own SQLite table set inside the same DB file").
//!
//! All timestamps (`created_ns`/`expires_ns`/`mtime_ns`) are nanoseconds
//! since the Unix epoch, stored as SQLite `INTEGER` (i64). That's ~292
//! years of range at nanosecond precision — ample — even though the
//! in-memory/public types use `i128` for headroom.

use rusqlite::{params, Connection, OptionalExtension};

use crate::interval_set::IntervalSet;
use crate::model::{SessionId, SessionState, SpoolMode, VerifyAlgo};
use sc_vfs::{ShareId, UserId};

pub(crate) const SCHEMA: &str = r#"
CREATE TABLE IF NOT EXISTS upload_sessions (
    id            BLOB PRIMARY KEY,
    user          INTEGER NOT NULL,
    share         INTEGER NOT NULL,
    dest          TEXT NOT NULL,
    part_name     TEXT NOT NULL,
    spool_dir     TEXT,
    mode          INTEGER NOT NULL,
    total_len     INTEGER,
    chunk_size    INTEGER NOT NULL,
    random_access INTEGER NOT NULL,
    received      BLOB NOT NULL,
    next_name     INTEGER NOT NULL DEFAULT 1,
    write_head    INTEGER NOT NULL DEFAULT 0,
    spooled_names BLOB NOT NULL DEFAULT (X''),
    if_match      TEXT,
    filename      TEXT NOT NULL,
    mtime_ns      INTEGER,
    mime          TEXT,
    relative_path TEXT,
    verify        INTEGER,
    verify_digest BLOB,
    created_ns    INTEGER NOT NULL,
    expires_ns    INTEGER NOT NULL,
    state         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_user ON upload_sessions(user);
CREATE INDEX IF NOT EXISTS idx_upload_sessions_expires ON upload_sessions(expires_ns);

CREATE TABLE IF NOT EXISTS upload_touched_dirs (
    share INTEGER NOT NULL,
    dir   TEXT NOT NULL,
    PRIMARY KEY (share, dir)
);

-- Admin-persisted chunk floor/default override.
-- Single row by convention, enforced by the CHECK rather than only by
-- caller discipline. Absence (no row) means "no admin override yet" --
-- `UploadEngine::new` falls back to the `sc.toml`-derived `UploadConfig`.
CREATE TABLE IF NOT EXISTS upload_chunk_settings (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    chunk_min     INTEGER NOT NULL,
    chunk_default INTEGER NOT NULL
);

-- Client-chosen transfer id -> engine session, for the session-folder chunked
-- upload surface (`/dav-uploads/{tid}`).
--
-- `tid` is picked by the client, so it is guessable and collidable and can
-- never be a session key on its own. The primary key is scoped by `user`, and
-- every lookup passes the authenticated principal: that is the one thing
-- standing between transfer-id guessing and session hijacking. A tid owned by
-- someone else must read as "not found", identically to one that never
-- existed, so it is not an existence oracle either.
--
-- `session` is the 16 opaque bytes of `SessionId`, stored as a BLOB the same
-- way `upload_sessions.id` is. `share`/`dest` are captured at bind time so the
-- later PUT/MOVE/DELETE reuse exactly what the session was created against
-- rather than re-resolving a vpath that may since have changed meaning.
CREATE TABLE IF NOT EXISTS upload_alias (
    tid        TEXT NOT NULL,
    user       INTEGER NOT NULL,
    session    BLOB NOT NULL,
    share      INTEGER NOT NULL,
    dest       TEXT NOT NULL,
    created_ns INTEGER NOT NULL,
    PRIMARY KEY (user, tid)
);
-- GC deletes by session, not by (user, tid), and would otherwise scan.
CREATE INDEX IF NOT EXISTS idx_upload_alias_session ON upload_alias(session);
"#;

/// The pragma set from `DESIGN-FOOTPRINT.md` §4.
///
/// **WAL is not optional here.** Without it SQLite uses a rollback journal,
/// where a single writer locks out every reader — and this database is
/// written on every chunk of every concurrent upload while the HTTP layer
/// reads session state on every request. Running the server without this
/// produced `database is locked` within seconds of startup; no unit test
/// caught it because tests open one connection at a time.
const PRAGMAS: &str = "\
    PRAGMA journal_mode = WAL;\
    PRAGMA synchronous = NORMAL;\
    PRAGMA wal_autocheckpoint = 1000;\
    PRAGMA journal_size_limit = 67108864;\
    PRAGMA busy_timeout = 5000;\
    PRAGMA temp_store = MEMORY;";

pub(crate) fn init_schema(conn: &Connection) -> rusqlite::Result<()> {
    // `journal_mode` returns a row, so it cannot go through `execute_batch`
    // on some rusqlite paths — run it first and read the result back.
    conn.pragma_update(None, "journal_mode", "WAL")?;
    conn.execute_batch(PRAGMAS)?;
    conn.execute_batch(SCHEMA)?;
    // `verify_digest` was added after `upload_sessions` first shipped (the
    // whole-file verify fix — see `model.rs`'s `UploadMeta::verify` doc): a
    // DB file created before this change has `verify` but not this column.
    // `CREATE TABLE IF NOT EXISTS` above is a no-op against an existing
    // table, so the column has to be added out-of-band; SQLite has no
    // `ALTER TABLE ADD COLUMN IF NOT EXISTS`, hence the existence check.
    let has_col: bool = conn
        .prepare("SELECT 1 FROM pragma_table_info('upload_sessions') WHERE name = 'verify_digest'")?
        .exists([])?;
    if !has_col {
        conn.execute("ALTER TABLE upload_sessions ADD COLUMN verify_digest BLOB", [])?;
    }
    // `chunk_min_at_creation` was added after `upload_sessions` first shipped,
    // same reason and same migration shape as `verify_digest` above: it
    // snapshots the floor `validate_patch` must enforce for *this* session
    // (`engine.rs`'s in-flight-upload hazard note), which is a different
    // value than the pre-existing `chunk_size` column (that one snapshots the
    // advertised *default*, not the enforced floor). Legacy rows predate the
    // column and get the hard floor as their default: lenient rather than
    // exact, since there is no way to recover a legacy session's real
    // historical floor, and accepting a chunk between the floor and that
    // unknowable value is far less harmful than falsely rejecting one that
    // was legal when the session started.
    let has_col: bool = conn
        .prepare("SELECT 1 FROM pragma_table_info('upload_sessions') WHERE name = 'chunk_min_at_creation'")?
        .exists([])?;
    if !has_col {
        conn.execute(
            &format!(
                "ALTER TABLE upload_sessions ADD COLUMN chunk_min_at_creation INTEGER NOT NULL DEFAULT {}",
                crate::model::CHUNK_MIN_BYTES_FLOOR
            ),
            [],
        )?;
    }
    Ok(())
}

#[derive(Clone, Debug)]
pub(crate) struct Row {
    pub id: SessionId,
    pub user: UserId,
    pub share: ShareId,
    pub dest: String,
    pub part_name: String,
    pub spool_dir: Option<String>,
    pub mode: SpoolMode,
    pub total_len: Option<u64>,
    pub chunk_size: u32,
    /// The chunk-size floor in effect when this session was created
    /// (`ChunkSettings::min()` at `create()` time), fixed for the session's
    /// whole lifetime — see `engine.rs::validate_patch` for why this must be
    /// distinct from the live, admin-changeable value.
    pub chunk_min_at_creation: u64,
    pub random_access: bool,
    pub received: IntervalSet,
    pub next_name: u32,
    pub write_head: u64,
    pub spooled_names: Vec<u32>,
    pub if_match: Option<String>,
    pub filename: String,
    pub mtime_ns: Option<i128>,
    pub mime: Option<String>,
    pub relative_path: Option<String>,
    pub verify: Option<VerifyAlgo>,
    /// Expected digest for `verify`, `None` when `verify` is `None`. Split
    /// from `verify` itself (rather than one `Option<(VerifyAlgo, Vec<u8>)>`
    /// field) because SQLite storage is naturally two columns — an INTEGER
    /// tag and a BLOB — and `model::UploadMeta::verify` is where the two are
    /// actually paired back into one value for callers.
    pub verify_digest: Option<Vec<u8>>,
    pub created_ns: i128,
    pub expires_ns: i128,
    pub state: SessionState,
}

fn mode_to_i64(m: SpoolMode) -> i64 {
    match m {
        SpoolMode::OffsetAddressed => 0,
        SpoolMode::NameOrdered => 1,
    }
}
fn mode_from_i64(v: i64) -> SpoolMode {
    match v {
        1 => SpoolMode::NameOrdered,
        _ => SpoolMode::OffsetAddressed,
    }
}

fn state_to_i64(s: SessionState) -> i64 {
    match s {
        SessionState::Receiving => 0,
        SessionState::Finalizing => 1,
        SessionState::Done => 2,
        SessionState::Aborted => 3,
        // Expired is derived at read time from expires_ns, never stored.
        SessionState::Expired => 0,
    }
}
fn state_from_i64(v: i64) -> SessionState {
    match v {
        1 => SessionState::Finalizing,
        2 => SessionState::Done,
        3 => SessionState::Aborted,
        _ => SessionState::Receiving,
    }
}

fn verify_to_i64(v: Option<VerifyAlgo>) -> Option<i64> {
    v.map(|v| match v {
        VerifyAlgo::Crc32c => 0,
        VerifyAlgo::Blake3 => 1,
    })
}
fn verify_from_i64(v: Option<i64>) -> Option<VerifyAlgo> {
    v.map(|v| if v == 1 { VerifyAlgo::Blake3 } else { VerifyAlgo::Crc32c })
}

pub(crate) fn spooled_encode(names: &[u32]) -> Vec<u8> {
    let mut out = Vec::with_capacity(names.len() * 4);
    for n in names {
        out.extend_from_slice(&n.to_le_bytes());
    }
    out
}

pub(crate) fn spooled_decode(b: &[u8]) -> Vec<u32> {
    b.chunks_exact(4)
        .map(|c| u32::from_le_bytes([c[0], c[1], c[2], c[3]]))
        .collect()
}

pub(crate) fn insert_row(conn: &Connection, r: &Row) -> rusqlite::Result<()> {
    conn.execute(
        "INSERT INTO upload_sessions
            (id, user, share, dest, part_name, spool_dir, mode, total_len, chunk_size,
             random_access, received, next_name, write_head, spooled_names, if_match,
             filename, mtime_ns, mime, relative_path, verify, verify_digest, created_ns,
             expires_ns, state, chunk_min_at_creation)
         VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,?16,?17,?18,?19,?20,?21,?22,?23,?24,?25)",
        params![
            r.id.as_bytes().to_vec(),
            r.user.get() as i64,
            r.share.get() as i64,
            r.dest,
            r.part_name,
            r.spool_dir,
            mode_to_i64(r.mode),
            r.total_len.map(|v| v as i64),
            r.chunk_size as i64,
            r.random_access as i64,
            r.received.to_vec(),
            r.next_name as i64,
            r.write_head as i64,
            spooled_encode(&r.spooled_names),
            r.if_match,
            r.filename,
            r.mtime_ns.map(|v| v as i64),
            r.mime,
            r.relative_path,
            verify_to_i64(r.verify),
            r.verify_digest,
            r.created_ns as i64,
            r.expires_ns as i64,
            state_to_i64(r.state),
            r.chunk_min_at_creation as i64,
        ],
    )?;
    Ok(())
}

pub(crate) fn update_row(conn: &Connection, r: &Row) -> rusqlite::Result<()> {
    conn.execute(
        "UPDATE upload_sessions SET
            total_len=?2, chunk_size=?3, received=?4, next_name=?5, write_head=?6,
            spooled_names=?7, mtime_ns=?8, expires_ns=?9, state=?10
         WHERE id=?1",
        params![
            r.id.as_bytes().to_vec(),
            r.total_len.map(|v| v as i64),
            r.chunk_size as i64,
            r.received.to_vec(),
            r.next_name as i64,
            r.write_head as i64,
            spooled_encode(&r.spooled_names),
            r.mtime_ns.map(|v| v as i64),
            r.expires_ns as i64,
            state_to_i64(r.state),
        ],
    )?;
    Ok(())
}

fn row_from_sqlite(row: &rusqlite::Row) -> rusqlite::Result<Row> {
    let id_blob: Vec<u8> = row.get(0)?;
    let mut id_bytes = [0u8; 16];
    if id_blob.len() == 16 {
        id_bytes.copy_from_slice(&id_blob);
    }
    let received_blob: Vec<u8> = row.get(10)?;
    // A `received` blob that fails `decode`'s re-validation is unrecoverable
    // data, not "nothing received yet" — `decode` re-derives the sorted/
    // non-overlapping/non-touching invariant from scratch, so there is no
    // partial or best-guess reading of corrupt bytes. Defaulting to empty
    // would silently tell a resuming client to start over at offset 0 while
    // the engine still holds (or, worse, has already assembled over) bytes
    // the client believes are acknowledged and may have already discarded
    // locally — propagate instead (`error.rs`'s `From<rusqlite::Error>`
    // unwraps this back into `UploadError::Corrupt`; this is the only way to
    // surface a non-SQLite error through a `rusqlite::Result` callback).
    let received = IntervalSet::decode(&received_blob).map_err(|e| {
        rusqlite::Error::FromSqlConversionFailure(10, rusqlite::types::Type::Blob, Box::new(e))
    })?;
    let spooled_blob: Vec<u8> = row.get(13)?;

    Ok(Row {
        id: SessionId(id_bytes),
        user: UserId::new(row.get::<_, i64>(1)? as u32),
        share: ShareId::new(row.get::<_, i64>(2)? as u32),
        dest: row.get(3)?,
        part_name: row.get(4)?,
        spool_dir: row.get(5)?,
        mode: mode_from_i64(row.get(6)?),
        total_len: row.get::<_, Option<i64>>(7)?.map(|v| v as u64),
        chunk_size: row.get::<_, i64>(8)? as u32,
        random_access: row.get::<_, i64>(9)? != 0,
        received,
        next_name: row.get::<_, i64>(11)? as u32,
        write_head: row.get::<_, i64>(12)? as u64,
        spooled_names: spooled_decode(&spooled_blob),
        if_match: row.get(14)?,
        filename: row.get(15)?,
        mtime_ns: row.get::<_, Option<i64>>(16)?.map(|v| v as i128),
        mime: row.get(17)?,
        relative_path: row.get(18)?,
        verify: verify_from_i64(row.get(19)?),
        verify_digest: row.get(20)?,
        created_ns: row.get::<_, i64>(21)? as i128,
        expires_ns: row.get::<_, i64>(22)? as i128,
        state: state_from_i64(row.get(23)?),
        chunk_min_at_creation: row.get::<_, i64>(24)? as u64,
    })
}

const SELECT_COLS: &str = "id, user, share, dest, part_name, spool_dir, mode, total_len, chunk_size,
        random_access, received, next_name, write_head, spooled_names, if_match,
        filename, mtime_ns, mime, relative_path, verify, verify_digest, created_ns, expires_ns, state,
        chunk_min_at_creation";

pub(crate) fn load_row(conn: &Connection, id: SessionId) -> rusqlite::Result<Option<Row>> {
    let sql = format!("SELECT {SELECT_COLS} FROM upload_sessions WHERE id=?1");
    conn.query_row(&sql, params![id.as_bytes().to_vec()], row_from_sqlite)
        .optional()
}

pub(crate) fn delete_row(conn: &Connection, id: SessionId) -> rusqlite::Result<()> {
    conn.execute("DELETE FROM upload_sessions WHERE id=?1", params![id.as_bytes().to_vec()])?;
    Ok(())
}

pub(crate) fn count_active_sessions(conn: &Connection, user: UserId) -> rusqlite::Result<u32> {
    conn.query_row(
        "SELECT COUNT(*) FROM upload_sessions WHERE user=?1 AND state IN (0,1)",
        params![user.get() as i64],
        |r| r.get::<_, i64>(0),
    )
    .map(|v| v as u32)
}

/// Global counterpart of [`count_active_sessions`] — every user, not just
/// one. Backs the server-settings restart warning: an admin about to
/// interrupt the service needs "how many sessions total", not a per-user
/// figure they'd have to sum themselves.
pub(crate) fn count_all_active_sessions(conn: &Connection) -> rusqlite::Result<u32> {
    conn.query_row("SELECT COUNT(*) FROM upload_sessions WHERE state IN (0,1)", [], |r| r.get::<_, i64>(0))
        .map(|v| v as u32)
}

pub(crate) fn sum_reserved_bytes(conn: &Connection, user: UserId) -> rusqlite::Result<u64> {
    conn.query_row(
        "SELECT COALESCE(SUM(total_len), 0) FROM upload_sessions WHERE user=?1 AND state IN (0,1)",
        params![user.get() as i64],
        |r| r.get::<_, i64>(0),
    )
    .map(|v| v as u64)
}

/// Sessions eligible for GC: `Aborted`, or (`Receiving`/`Finalizing`) whose
/// TTL has passed.
pub(crate) fn list_gc_candidates(conn: &Connection, now_ns: i128) -> rusqlite::Result<Vec<Row>> {
    let sql = format!(
        "SELECT {SELECT_COLS} FROM upload_sessions WHERE state=3 OR (state IN (0,1) AND expires_ns < ?1)"
    );
    let mut stmt = conn.prepare(&sql)?;
    let rows = stmt.query_map(params![now_ns as i64], row_from_sqlite)?;
    rows.collect()
}

pub(crate) fn touch_dir(conn: &Connection, share: ShareId, dir: &str) -> rusqlite::Result<()> {
    conn.execute(
        "INSERT OR IGNORE INTO upload_touched_dirs (share, dir) VALUES (?1, ?2)",
        params![share.get() as i64, dir],
    )?;
    Ok(())
}

pub(crate) fn list_touched_dirs(conn: &Connection) -> rusqlite::Result<Vec<(ShareId, String)>> {
    let mut stmt = conn.prepare("SELECT share, dir FROM upload_touched_dirs")?;
    let rows = stmt.query_map([], |r| {
        Ok((ShareId::new(r.get::<_, i64>(0)? as u32), r.get::<_, String>(1)?))
    })?;
    rows.collect()
}

/// `(share, name)` pairs currently in use by a live (not-yet-GC'd) session:
/// its part file and, if any, its spool directory. Used by the orphan sweep
/// to avoid deleting files a live session still owns.
pub(crate) fn list_live_names(conn: &Connection) -> rusqlite::Result<std::collections::HashSet<(ShareId, String)>> {
    let mut out = std::collections::HashSet::new();
    let mut stmt = conn.prepare("SELECT share, part_name, spool_dir FROM upload_sessions")?;
    let rows = stmt.query_map([], |r| {
        let share = ShareId::new(r.get::<_, i64>(0)? as u32);
        let part_name: String = r.get(1)?;
        let spool_dir: Option<String> = r.get(2)?;
        Ok((share, part_name, spool_dir))
    })?;
    for row in rows {
        let (share, part_name, spool_dir) = row?;
        out.insert((share, part_name));
        if let Some(sd) = spool_dir {
            out.insert((share, sd));
        }
    }
    Ok(out)
}

/// Read the persisted admin override, if one has ever been written.
/// `UploadEngine::new` falls back to the `sc.toml`-derived `UploadConfig`
/// when this returns `None`.
pub(crate) fn load_chunk_settings(conn: &Connection) -> rusqlite::Result<Option<(u64, u64)>> {
    conn.query_row(
        "SELECT chunk_min, chunk_default FROM upload_chunk_settings WHERE id = 1",
        [],
        |r| Ok((r.get::<_, i64>(0)? as u64, r.get::<_, i64>(1)? as u64)),
    )
    .optional()
}

/// Upsert the single settings row — `UploadEngine::set_chunk_settings` calls
/// this only after its own floor/ordering validation has already passed.
pub(crate) fn save_chunk_settings(conn: &Connection, chunk_min: u64, chunk_default: u64) -> rusqlite::Result<()> {
    conn.execute(
        "INSERT INTO upload_chunk_settings (id, chunk_min, chunk_default) VALUES (1, ?1, ?2)
         ON CONFLICT(id) DO UPDATE SET chunk_min = excluded.chunk_min, chunk_default = excluded.chunk_default",
        params![chunk_min as i64, chunk_default as i64],
    )?;
    Ok(())
}

/// What a transfer id resolves to. Deliberately not the whole session row:
/// the caller needs the handle and the share to address the engine, and
/// nothing else about the alias is interesting to it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Alias {
    pub session: SessionId,
    pub share: ShareId,
    pub dest: String,
}

/// Bind `tid` to a session for `user`. Returns `false` when this user already
/// has that tid — a rebind is refused rather than silently replacing, because
/// overwriting the row would orphan the first session's spool with no way left
/// to address it.
pub(crate) fn insert_alias(
    conn: &Connection,
    tid: &str,
    user: UserId,
    session: SessionId,
    share: ShareId,
    dest: &str,
    now_ns: i128,
) -> rusqlite::Result<bool> {
    let n = conn.execute(
        "INSERT OR IGNORE INTO upload_alias (tid, user, session, share, dest, created_ns)
         VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
        params![
            tid,
            user.get() as i64,
            session.as_bytes().to_vec(),
            share.get() as i64,
            dest,
            now_ns as i64
        ],
    )?;
    Ok(n == 1)
}

/// Resolve `tid` **within `user`'s namespace**. See the table's DDL comment
/// for why the user scope is not optional.
pub(crate) fn load_alias(conn: &Connection, tid: &str, user: UserId) -> rusqlite::Result<Option<Alias>> {
    conn.query_row(
        "SELECT session, share, dest FROM upload_alias WHERE user=?1 AND tid=?2",
        params![user.get() as i64, tid],
        |r| {
            let blob: Vec<u8> = r.get(0)?;
            let mut bytes = [0u8; 16];
            if blob.len() == 16 {
                bytes.copy_from_slice(&blob);
            }
            Ok(Alias {
                session: SessionId(bytes),
                share: ShareId::new(r.get::<_, i64>(1)? as u32),
                dest: r.get(2)?,
            })
        },
    )
    .optional()
}

pub(crate) fn delete_alias(conn: &Connection, tid: &str, user: UserId) -> rusqlite::Result<()> {
    conn.execute(
        "DELETE FROM upload_alias WHERE user=?1 AND tid=?2",
        params![user.get() as i64, tid],
    )?;
    Ok(())
}

/// Drop every alias pointing at a session. Called when the session itself goes
/// away (GC): a surviving alias would let a tid keep addressing a freed session
/// id, which a later session could in principle be allocated.
pub(crate) fn delete_aliases_for_session(conn: &Connection, session: SessionId) -> rusqlite::Result<()> {
    conn.execute(
        "DELETE FROM upload_alias WHERE session=?1",
        params![session.as_bytes().to_vec()],
    )?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::UploadError;
    use crate::model::SessionState;

    fn open() -> Connection {
        let conn = Connection::open_in_memory().unwrap();
        init_schema(&conn).unwrap();
        conn
    }

    fn row(id: SessionId) -> Row {
        Row {
            id,
            user: UserId::new(1),
            share: ShareId::new(1),
            dest: "f.bin".into(),
            part_name: "part".into(),
            spool_dir: None,
            mode: SpoolMode::OffsetAddressed,
            total_len: Some(10),
            chunk_size: 4,
            chunk_min_at_creation: 4,
            random_access: false,
            received: IntervalSet::new(),
            next_name: 1,
            write_head: 0,
            spooled_names: vec![],
            if_match: None,
            filename: "f.bin".into(),
            mtime_ns: None,
            mime: None,
            relative_path: None,
            verify: None,
            verify_digest: None,
            created_ns: 0,
            expires_ns: 0,
            state: SessionState::Receiving,
        }
    }

    /// A `received` blob damaged on disk (bitrot, truncated write — anything
    /// that lands outside the engine's own encode path) must be reported, not
    /// read back as "nothing received yet". Before the fix, `row_from_sqlite`
    /// did `IntervalSet::decode(&received_blob).unwrap_or_default()`, so this
    /// exact case returned `Ok(Some(Row { received: IntervalSet::new(), .. }))`
    /// — a fabricated fresh session — instead of an error a caller could act
    /// on. See `db.rs`'s `row_from_sqlite` for why that's unsafe.
    #[test]
    fn corrupt_received_blob_is_reported_not_swallowed() {
        let conn = open();
        let id = SessionId([7u8; 16]);
        insert_row(&conn, &row(id)).unwrap();

        // `count = 1` declared with no run bytes following — truncated, and
        // exactly the shape `IntervalSet::decode` rejects with `Corrupt`.
        conn.execute(
            "UPDATE upload_sessions SET received = ?1 WHERE id = ?2",
            params![vec![1u8], id.as_bytes().to_vec()],
        )
        .unwrap();

        let err = load_row(&conn, id).expect_err("corrupt blob must not decode as Ok");
        let upload_err = UploadError::from(err);
        assert!(
            matches!(upload_err, UploadError::Corrupt),
            "expected UploadError::Corrupt, got {upload_err:?}"
        );
    }

    #[test]
    fn chunk_settings_round_trip_and_upsert() {
        let conn = open();
        assert_eq!(load_chunk_settings(&conn).unwrap(), None);

        save_chunk_settings(&conn, 6 * 1024 * 1024, 12 * 1024 * 1024).unwrap();
        assert_eq!(load_chunk_settings(&conn).unwrap(), Some((6 * 1024 * 1024, 12 * 1024 * 1024)));

        // A second write overwrites the same row rather than erroring or
        // inserting a duplicate.
        save_chunk_settings(&conn, 8 * 1024 * 1024, 16 * 1024 * 1024).unwrap();
        assert_eq!(load_chunk_settings(&conn).unwrap(), Some((8 * 1024 * 1024, 16 * 1024 * 1024)));
    }

    /// The alias table's whole reason for existing: the same client-chosen
    /// `tid` under two users is two independent rows, and neither user can
    /// read the other's binding.
    #[test]
    fn alias_is_scoped_by_user_and_refuses_rebind() {
        let conn = open();
        let alice = UserId::new(1);
        let bob = UserId::new(2);
        let a = SessionId([1u8; 16]);
        let b = SessionId([2u8; 16]);

        assert!(insert_alias(&conn, "t1", alice, a, ShareId::new(1), "s/a.bin", 0).unwrap());
        // Same tid, different user: a separate binding, not a collision.
        assert!(insert_alias(&conn, "t1", bob, b, ShareId::new(1), "s/b.bin", 0).unwrap());

        assert_eq!(load_alias(&conn, "t1", alice).unwrap().unwrap().session, a);
        assert_eq!(load_alias(&conn, "t1", bob).unwrap().unwrap().session, b);

        // A second bind by the same user is refused, not overwritten.
        assert!(!insert_alias(&conn, "t1", alice, b, ShareId::new(1), "s/x.bin", 0).unwrap());
        assert_eq!(load_alias(&conn, "t1", alice).unwrap().unwrap().session, a);

        // Unbinding one user leaves the other's alone.
        delete_alias(&conn, "t1", alice).unwrap();
        assert_eq!(load_alias(&conn, "t1", alice).unwrap(), None);
        assert!(load_alias(&conn, "t1", bob).unwrap().is_some());

        delete_aliases_for_session(&conn, b).unwrap();
        assert_eq!(load_alias(&conn, "t1", bob).unwrap(), None);
    }

    /// A legacy row created before `chunk_min_at_creation` existed gets the
    /// hard floor, not 0 or NULL — see `init_schema`'s migration doc.
    #[test]
    fn legacy_row_migration_default_is_the_hard_floor() {
        let conn = open();
        let id = SessionId([9u8; 16]);
        insert_row(&conn, &row(id)).unwrap();
        conn.execute("ALTER TABLE upload_sessions DROP COLUMN chunk_min_at_creation", []).unwrap();
        init_schema(&conn).unwrap();
        let loaded = load_row(&conn, id).unwrap().unwrap();
        assert_eq!(loaded.chunk_min_at_creation, crate::model::CHUNK_MIN_BYTES_FLOOR);
    }
}
