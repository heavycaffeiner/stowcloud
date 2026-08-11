//! `UploadEngine` — the TUS core + NC chunking-v2 engine side. See
//! docs/ (state machine + ordering rule) and §7 (NC
//! mapping).
//!
//! ## Naming note (cross-crate)
//! sc-vfs reserves the literal prefix `.scpart-` (`sc_vfs::RESERVED_PREFIXES`)
//! so control files are filtered out of `ShareRoot::read_dir`, and
//! `SafePath::parse`/`join` reject that prefix so *user* input can never
//! collide with one. Trusted internal callers go through
//! `SafePath::join_control`, which grants exactly that one exemption and
//! nothing else.
//!
//! An earlier revision disguised the name as `.{basename}.scpart-{id}` to get
//! past `validate_component`, which had the side effect of defeating
//! `is_reserved_name` — part files then showed up in ordinary directory
//! listings, in the web UI and to WebDAV clients, for the whole duration of
//! an upload. That is precisely what the reserved prefix exists to prevent,
//! so the disguise is gone.

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use parking_lot::Mutex;
use rusqlite::Connection;

use sc_vfs::{FileHandle, SafePath, ShareId, ShareRoot, UserId};

use crate::db;
use crate::error::UploadError;
use crate::interval_set::IntervalSet;
use crate::model::{
    Checksum, ChunkSettings, SessionId, SessionSpec, SessionState, SessionStatus, SpoolMode, UploadConfig,
    VerifyAlgo, CHUNK_MIN_BYTES_FLOOR,
};

fn now_ns() -> i128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos() as i128
}

/// A simple content-address-free ETag: not cryptographic, just enough to
/// notice "the file changed since you last looked" for `If-Match` (§5.4).
pub fn file_etag(stat: &sc_vfs::Stat) -> String {
    format!("{:x}-{:x}", stat.mtime_ns as u64, stat.size)
}

/// `FileHandle::write_at` (like `pwrite`) may perform a short write; loop
/// until the whole buffer lands.
fn write_all_at(fh: &FileHandle, mut buf: &[u8], mut off: u64) -> Result<(), UploadError> {
    while !buf.is_empty() {
        let n = fh.write_at(buf, off)?;
        if n == 0 {
            return Err(UploadError::Io(std::io::Error::new(std::io::ErrorKind::WriteZero, "write_at wrote 0 bytes")));
        }
        buf = &buf[n..];
        off += n as u64;
    }
    Ok(())
}

fn read_exact_at(fh: &FileHandle, mut buf: &mut [u8], mut off: u64) -> Result<(), UploadError> {
    while !buf.is_empty() {
        let n = fh.read_at(buf, off)?;
        if n == 0 {
            return Err(UploadError::Io(std::io::Error::new(std::io::ErrorKind::UnexpectedEof, "read_at hit EOF early")));
        }
        let tmp = buf;
        buf = &mut tmp[n..];
        off += n as u64;
    }
    Ok(())
}

/// Copy the full contents of `src` onto the end of `dst` starting at
/// `dst_offset_start`, via `FileHandle::copy_range_from` (§7.2): on the
/// deployment target this is `copy_file_range` — a reflink on btrfs/XFS,
/// an in-kernel copy otherwise, either way no userspace round trip. When
/// the kernel primitive isn't usable, `sc_vfs` itself falls back to a
/// bounded 256 KiB buffer internally (never buffers a whole chunk in this
/// process, matching §1.3's memory-independence goal) — this function
/// doesn't need its own fallback loop. Returns the number of bytes copied.
///
/// `src`'s length isn't known to the caller in advance (spool files are
/// exactly one NC chunk's worth of bytes, of arbitrary size), so it's
/// `stat`-ed here rather than passing an oversized sentinel `len` — some
/// kernels reject an implausible `copy_file_range` length with `EINVAL`,
/// which isn't one of the errors `copy_range_from` treats as "fall back",
/// and correctly so: an `EINVAL` here would be a real bug, not a portability
/// gap, so it should surface rather than be masked by a generous sentinel.
fn copy_file_contents(src: &FileHandle, dst: &FileHandle, dst_offset_start: u64) -> Result<u64, UploadError> {
    let len = src.stat()?.size;
    Ok(dst.copy_range_from(src, 0, dst_offset_start, len)?)
}

/// In-memory session state, kept behind two independent locks so that the
/// (rare, brief) metadata/DB bookkeeping never blocks the (common, can be
/// large) disk write: `handle` guards only the lazily-opened part-file
/// handle, `row` guards everything else.
struct CachedSession {
    handle: Mutex<Option<Arc<FileHandle>>>,
    row: Mutex<db::Row>,
}

pub struct UploadEngine {
    db: Mutex<Connection>,
    cfg: UploadConfig,
    /// Live chunk floor/default — seeded from `cfg` at `new()`, then
    /// overridden by whatever `upload_chunk_settings` holds (an earlier admin
    /// write persisted in the same DB file), so a restart doesn't lose it.
    /// `cfg.chunk_size_min`/`chunk_size_default` are left untouched after
    /// startup; this is the one field every live reader must consult instead.
    chunk_settings: Arc<ChunkSettings>,
    /// Whether `upload_chunk_settings` holds a row, kept beside the values
    /// because `unwrap_or` collapses the two: once it has run, "an admin
    /// stored this" and "it fell back to `sc.toml`" are the same pair of
    /// numbers, and the settings screen has to report which.
    chunk_settings_overridden: AtomicBool,
    cache: Mutex<HashMap<SessionId, Arc<CachedSession>>>,
}

impl UploadEngine {
    /// `root` is the path to the SQLite database file backing this engine's
    /// own session-state tables (see `db.rs`). Actual file I/O always goes
    /// through the `ShareRoot` passed explicitly into each method that
    /// needs it.
    pub fn new(root: &std::path::Path, cfg: UploadConfig) -> anyhow::Result<Self> {
        let conn = Connection::open(root)?;
        db::init_schema(&conn)?;
        // Precedence: a persisted admin override
        // beats the `sc.toml`-derived `cfg`, which beats `UploadConfig::default()`
        // (`cfg` itself already carries that fallback by construction).
        let stored = db::load_chunk_settings(&conn)?;
        let (min, default) = stored.unwrap_or((cfg.chunk_size_min, cfg.chunk_size_default));
        let chunk_settings = Arc::new(ChunkSettings::new(min, default));
        Ok(Self {
            db: Mutex::new(conn),
            cfg,
            chunk_settings,
            chunk_settings_overridden: AtomicBool::new(stored.is_some()),
            cache: Mutex::new(HashMap::new()),
        })
    }

    pub fn config(&self) -> &UploadConfig {
        &self.cfg
    }

    /// Current live `(min, default)` — what `create()` and every advertised
    /// limit should read instead of `config()`'s startup-time values.
    pub fn chunk_settings(&self) -> (u64, u64) {
        (self.chunk_settings.min(), self.chunk_settings.default_size())
    }

    /// Has an admin ever written these, as opposed to them coming from
    /// `sc.toml`? The settings screen reports the source of every row it
    /// shows, and this is the only thing that can answer for these two.
    pub fn chunk_settings_overridden(&self) -> bool {
        self.chunk_settings_overridden.load(Ordering::Relaxed)
    }

    /// How many sessions (any user) are still accepting bytes right now —
    /// read from the DB rather than `cache`, since a session can be active
    /// without ever having been opened in this process's cache. Used by the
    /// server-settings restart warning, not by anything on the upload path
    /// itself.
    pub fn active_session_count(&self) -> u32 {
        db::count_all_active_sessions(&self.db.lock()).unwrap_or(0)
    }

    /// Admin write path. Validates, persists, then
    /// flips the live atomics — in that order, so a validation failure or a
    /// disk-write failure never lets the in-memory value drift from what's on
    /// disk. Does *not* touch any session already in flight: `chunk_size` and
    /// `chunk_min_at_creation` are snapshotted once at `create()` time and
    /// never re-read from here (see `validate_patch`'s doc for why re-reading
    /// live here would be a hazard, not a feature).
    pub fn set_chunk_settings(&self, min: u64, default: u64) -> Result<(), UploadError> {
        if min < CHUNK_MIN_BYTES_FLOOR {
            return Err(UploadError::BadRequest(format!("chunk_min must be at least {CHUNK_MIN_BYTES_FLOOR} bytes")));
        }
        if default < min {
            return Err(UploadError::BadRequest("chunk_default must be >= chunk_min".into()));
        }
        {
            let conn = self.db.lock();
            db::save_chunk_settings(&conn, min, default)?;
        }
        self.chunk_settings.set(min, default);
        self.chunk_settings_overridden.store(true, Ordering::Relaxed);
        Ok(())
    }

    // ---------------------------------------------------------------
    // session lifecycle
    // ---------------------------------------------------------------

    pub fn create(&self, share: &ShareRoot, spec: SessionSpec) -> Result<SessionId, UploadError> {
        self.check_resource_limits(spec.user, spec.total_len)?;
        // The directory the `.scpart-` staging file will be created in, which
        // is the filesystem this upload actually consumes. Not the share root:
        // those are two different devices the moment anything is mounted
        // inside the share.
        self.check_free_space(share, &spec.dest.parent(), spec.total_len)?;

        let id = SessionId::new_random();
        let policy = share.policy().clone();
        let max_depth = policy.max_depth;
        let basename = spec
            .dest
            .name()
            .ok_or_else(|| UploadError::BadRequest("destination has no file name".into()))?
            .to_string();
        let parent = spec.dest.parent();
        // `.scpart-{id}` is a reserved control name, so it is filtered out of
        // listings. It must live in the destination's own directory: the
        // rename that publishes it is only atomic within one directory.
        let _ = &basename;
        let part_name_str = format!(".scpart-{}", id.to_b64());
        let part_path = parent
            .join_control(&part_name_str, max_depth)
            .map_err(|e| UploadError::BadRequest(e.to_string()))?;

        // Assembly-free upload: O_EXCL create + sparse ftruncate. Zero copies.
        let fh = share.create_excl(&part_path, policy.mode_file)?;
        if let Some(total) = spec.total_len {
            fh.set_len(total)?;
        }

        let spool_dir_str = if spec.mode == SpoolMode::NameOrdered {
            Some(format!(".scpart-{}.d", id.to_b64()))
        } else {
            None
        };

        let now = now_ns();
        let ttl_ns = self.cfg.session_ttl.as_nanos() as i128;
        // `db::Row` stores the algorithm tag and the expected digest as two
        // separate columns (INTEGER + BLOB); `UploadMeta::verify` is where
        // they're paired back into one value on the read side (`finalize_locked`).
        let (verify, verify_digest) = match &spec.meta.verify {
            Some((algo, digest)) => (Some(*algo), Some(digest.clone())),
            None => (None, None),
        };
        let row = db::Row {
            id,
            user: spec.user,
            share: spec.share,
            dest: spec.dest.to_display_string(),
            part_name: part_name_str,
            spool_dir: spool_dir_str,
            mode: spec.mode,
            total_len: spec.total_len,
            chunk_size: self.chunk_settings.default_size() as u32,
            chunk_min_at_creation: self.chunk_settings.min(),
            random_access: spec.random_access,
            received: IntervalSet::new(),
            next_name: 1,
            write_head: 0,
            spooled_names: Vec::new(),
            if_match: spec.if_match,
            filename: spec.meta.filename,
            mtime_ns: spec.meta.mtime_ns,
            mime: spec.meta.mime,
            relative_path: spec.meta.relative_path,
            verify,
            verify_digest,
            created_ns: now,
            expires_ns: now + ttl_ns,
            state: SessionState::Receiving,
        };

        {
            let conn = self.db.lock();
            db::insert_row(&conn, &row)?;
            let _ = db::touch_dir(&conn, spec.share, &parent.to_display_string());
        }

        let cs = Arc::new(CachedSession {
            handle: Mutex::new(Some(Arc::new(fh))),
            row: Mutex::new(row),
        });
        self.cache.lock().insert(id, cs);
        Ok(id)
    }

    // ---------------------------------------------------------------
    // transfer-id aliases (session-folder chunked upload)
    // ---------------------------------------------------------------

    /// Bind a client-chosen transfer id to a session, scoped to `user`.
    ///
    /// Returns `false` if this user already has that tid; the caller answers
    /// 409 rather than replacing, since a silent rebind would orphan the first
    /// session's spool. Call this **after** `create` succeeds so a failed
    /// creation leaves no dangling alias.
    pub fn bind_alias(
        &self,
        tid: &str,
        user: UserId,
        session: SessionId,
        share: ShareId,
        dest: &str,
    ) -> Result<bool, UploadError> {
        let conn = self.db.lock();
        Ok(db::insert_alias(&conn, tid, user, session, share, dest, now_ns())?)
    }

    /// Resolve a transfer id **within `user`'s namespace**. A tid belonging to
    /// another account resolves to `None`, exactly like one that never existed.
    pub fn lookup_alias(&self, tid: &str, user: UserId) -> Result<Option<(SessionId, ShareId, String)>, UploadError> {
        let conn = self.db.lock();
        Ok(db::load_alias(&conn, tid, user)?.map(|a| (a.session, a.share, a.dest)))
    }

    pub fn unbind_alias(&self, tid: &str, user: UserId) -> Result<(), UploadError> {
        let conn = self.db.lock();
        Ok(db::delete_alias(&conn, tid, user)?)
    }

    pub fn head(&self, id: SessionId, user: UserId) -> Result<SessionStatus, UploadError> {
        let cs = self.load_cached(id)?;
        let row = cs.row.lock();
        if row.user != user {
            return Err(UploadError::NotFound);
        }
        Ok(SessionStatus {
            id,
            share: row.share,
            state: self.effective_state(&row),
            offset: row.received.contiguous_prefix(),
            total_len: row.total_len,
            chunk_size: row.chunk_size,
            run_count: row.received.run_count(),
            expires_ns: row.expires_ns,
            random_access: row.random_access,
            // `NameOrdered` advances `write_head` and leaves `received` empty
            // until `assemble_and_finalize` fills it, so reading `received`
            // here would report 0 for exactly the sessions that need it.
            received_bytes: match row.mode {
                SpoolMode::NameOrdered => row.write_head,
                _ => row.received.contiguous_prefix(),
            },
        })
    }

    pub fn abort(&self, id: SessionId, user: UserId) -> Result<(), UploadError> {
        let cs = self.load_cached(id)?;
        {
            let mut row = cs.row.lock();
            if row.user != user {
                return Err(UploadError::NotFound);
            }
            if row.state == SessionState::Done {
                return Err(UploadError::NotFound);
            }
            row.state = SessionState::Aborted;
            row.expires_ns = now_ns(); // immediately GC-eligible
            let conn = self.db.lock();
            db::update_row(&conn, &row)?;
        }
        if let Some(evicted) = self.cache.lock().remove(&id) {
            *evicted.handle.lock() = None;
        }
        Ok(())
    }

    // ---------------------------------------------------------------
    // native TUS: offset-addressed PATCH
    // ---------------------------------------------------------------

    pub fn patch(
        &self,
        share: &ShareRoot,
        id: SessionId,
        user: UserId,
        offset: u64,
        data: &[u8],
        checksum: Option<Checksum>,
    ) -> Result<u64, UploadError> {
        let cs = self.load_cached(id)?;
        {
            let row = cs.row.lock();
            if row.user != user {
                return Err(UploadError::NotFound);
            }
            self.ensure_receiving(&row)?;
            self.validate_patch(&row, offset, data.len() as u64)?;
        }

        // -----------------------------------------------------------
        // ORDERING RULE (docs/, non-negotiable):
        // the disk write MUST complete before the `received` set is
        // committed to the DB. If the process crashes between the two,
        // the DB still shows the old (smaller) contiguous prefix; the
        // client resends the same bytes at the same offset and they
        // land identically — idempotent, safe, under-reports at worst.
        // Reversing the order would let the DB claim bytes that were
        // never durably written: silent data corruption. See
        // `tests::ordering_rule_crash_between_write_and_commit`, which
        // exercises exactly this window by calling the write phase
        // without the commit phase and reopening a fresh engine against
        // the same DB file.
        // -----------------------------------------------------------
        if !data.is_empty() {
            self.do_write(&cs, share, offset, data)?; // ① disk
        }

        if let Some(ck) = checksum {
            if !ck.matches(data) {
                // Bytes are already on disk but deliberately not committed
                // below: this range stays "not received" and a resend
                // will overwrite it. No corruption results.
                return Err(UploadError::ChecksumMismatch);
            }
        }

        self.do_commit(&cs, offset, data.len() as u64) // ② DB, strictly after ①
    }

    /// Write phase only (step ① of the ordering rule). Split out from
    /// `patch` so tests can exercise "wrote to disk, never committed".
    fn do_write(&self, cs: &Arc<CachedSession>, share: &ShareRoot, offset: u64, data: &[u8]) -> Result<(), UploadError> {
        let fh = self.handle_for(cs, share)?;
        write_all_at(&fh, data, offset)
    }

    /// Commit phase only (step ② of the ordering rule).
    fn do_commit(&self, cs: &Arc<CachedSession>, offset: u64, len: u64) -> Result<u64, UploadError> {
        let mut row = cs.row.lock();
        if len > 0 {
            row.received.insert(offset, offset + len)?;
        }
        row.expires_ns = now_ns() + self.cfg.session_ttl.as_nanos() as i128;
        let conn = self.db.lock();
        db::update_row(&conn, &row)?;
        Ok(row.received.contiguous_prefix())
    }

    // ---------------------------------------------------------------
    // NC chunking v2: name-ordered PUT (§7.2)
    // ---------------------------------------------------------------

    pub fn put_named(&self, share: &ShareRoot, id: SessionId, user: UserId, name: u32, data: &[u8]) -> Result<(), UploadError> {
        let cs = self.load_cached(id)?;
        {
            let row = cs.row.lock();
            if row.user != user {
                return Err(UploadError::NotFound);
            }
            self.ensure_receiving(&row)?;
            if row.mode != SpoolMode::NameOrdered {
                return Err(UploadError::BadRequest("session is not NameOrdered".into()));
            }
        }

        let is_fast_path = { cs.row.lock().next_name == name };

        if is_fast_path {
            let head = { cs.row.lock().write_head };
            self.do_write(&cs, share, head, data)?; // ① disk
            {
                let mut row = cs.row.lock();
                row.write_head += data.len() as u64;
                row.next_name += 1;
            }
            // Drain any successors that arrived out of order and were
            // spooled while we were waiting for this one.
            self.drain_spool_chain(&cs, share)?;
        } else {
            self.spool_write(&cs, share, name, data)?; // ① disk (separate spool file)
            let mut row = cs.row.lock();
            if !row.spooled_names.contains(&name) {
                row.spooled_names.push(name);
            }
        }

        let mut row = cs.row.lock();
        row.expires_ns = now_ns() + self.cfg.session_ttl.as_nanos() as i128;
        let conn = self.db.lock();
        db::update_row(&conn, &row)?; // ② DB, after all disk work above
        Ok(())
    }

    fn drain_spool_chain(&self, cs: &Arc<CachedSession>, share: &ShareRoot) -> Result<(), UploadError> {
        loop {
            let next_name = { cs.row.lock().next_name };
            let present = { cs.row.lock().spooled_names.contains(&next_name) };
            if !present {
                break;
            }
            let spool_dir = {
                let row = cs.row.lock();
                self.spool_dir_path(&row, share)?
            };
            let max_depth = share.policy().max_depth;
            let file_path = spool_dir
                .join(&next_name.to_string(), max_depth)
                .map_err(|e| UploadError::BadRequest(e.to_string()))?;
            let sp = share.open_read(&file_path)?;
            let fh = self.handle_for(cs, share)?;
            let head = { cs.row.lock().write_head };
            let n = copy_file_contents(&sp, &fh, head)?;
            drop(sp);
            share.unlink(&file_path)?;

            let mut row = cs.row.lock();
            row.write_head += n;
            row.next_name += 1;
            row.spooled_names.retain(|&x| x != next_name);
        }
        Ok(())
    }

    fn spool_write(&self, cs: &Arc<CachedSession>, share: &ShareRoot, name: u32, data: &[u8]) -> Result<(), UploadError> {
        let spool_dir = {
            let row = cs.row.lock();
            self.spool_dir_path(&row, share)?
        };
        match share.mkdir(&spool_dir) {
            Ok(()) => {}
            Err(sc_vfs::VfsError::AlreadyExists) => {}
            Err(e) => return Err(e.into()),
        }
        let max_depth = share.policy().max_depth;
        let file_path = spool_dir
            .join(&name.to_string(), max_depth)
            .map_err(|e| UploadError::BadRequest(e.to_string()))?;
        match share.create_excl(&file_path, share.policy().mode_file) {
            Ok(fh) => write_all_at(&fh, data, 0),
            Err(sc_vfs::VfsError::AlreadyExists) => {
                // Client re-sent the same chunk name (retry). Idempotent overwrite.
                let fh = share.open_read(&file_path)?;
                fh.set_len(data.len() as u64)?;
                write_all_at(&fh, data, 0)
            }
            Err(e) => Err(e.into()),
        }
    }

    pub fn list_chunks(&self, id: SessionId) -> Result<Vec<u32>, UploadError> {
        let cs = self.load_cached(id)?;
        let row = cs.row.lock();
        if row.mode != SpoolMode::NameOrdered {
            return Ok(Vec::new());
        }
        let mut v: Vec<u32> = (1..row.next_name).collect();
        v.extend(row.spooled_names.iter().copied());
        v.sort_unstable();
        v.dedup();
        Ok(v)
    }

    pub fn assemble_and_finalize(
        &self,
        share: &ShareRoot,
        id: SessionId,
        user: UserId,
        total: u64,
        mtime_ns: Option<i128>,
    ) -> Result<SafePath, UploadError> {
        let cs = self.load_cached(id)?;
        {
            let row = cs.row.lock();
            if row.user != user {
                return Err(UploadError::NotFound);
            }
            if row.mode != SpoolMode::NameOrdered {
                return Err(UploadError::BadRequest("not a NameOrdered session".into()));
            }
        }

        // Merge any remaining spooled chunks, strictly in ascending name
        // order ("assembled in the order of their names" — NC protocol).
        loop {
            let smallest = { cs.row.lock().spooled_names.iter().copied().min() };
            let Some(name) = smallest else { break };
            let next_name = { cs.row.lock().next_name };
            if name != next_name {
                return Err(UploadError::Incomplete); // a chunk name is missing
            }
            let spool_dir = {
                let row = cs.row.lock();
                self.spool_dir_path(&row, share)?
            };
            let max_depth = share.policy().max_depth;
            let file_path = spool_dir
                .join(&name.to_string(), max_depth)
                .map_err(|e| UploadError::BadRequest(e.to_string()))?;
            let sp = share.open_read(&file_path)?;
            let fh = self.handle_for(&cs, share)?;
            let head = { cs.row.lock().write_head };
            let n = copy_file_contents(&sp, &fh, head)?;
            drop(sp);
            share.unlink(&file_path)?;

            let mut row = cs.row.lock();
            row.write_head += n;
            row.next_name += 1;
            row.spooled_names.retain(|&x| x != name);
        }

        let write_head = { cs.row.lock().write_head };
        if write_head != total {
            // Protocol-neutral wording on purpose: the caller declared this
            // total in whatever header its own protocol uses, and naming that
            // header here would put a vendor's wire vocabulary in a core
            // crate (the isolation gate in `scripts/verify.sh` checks for
            // exactly that). The HTTP layer that read the header is the one
            // that can name it in the response.
            return Err(UploadError::BadRequest(format!(
                "declared total length mismatch: assembled {write_head}, expected {total}"
            )));
        }

        let spool_dir_to_remove = {
            let row = cs.row.lock();
            if row.spool_dir.is_some() {
                Some(self.spool_dir_path(&row, share)?)
            } else {
                None
            }
        };

        {
            let mut row = cs.row.lock();
            row.received = IntervalSet::full(total);
            row.total_len = Some(total);
            if mtime_ns.is_some() {
                row.mtime_ns = mtime_ns;
            }
        }

        let published = self.finalize_locked(&cs, id, share)?;

        if let Some(sd) = spool_dir_to_remove {
            let _ = share.rmdir(&sd); // best-effort; should be empty
        }
        Ok(published)
    }

    // ---------------------------------------------------------------
    // shared finalize core (§5.3) — native and NC paths converge here
    // ---------------------------------------------------------------

    /// Publish the assembled upload, and answer the share-relative path it
    /// landed at. `SessionStatus` carries the share and the offsets but not
    /// the destination, so three of the four callers cannot name the file
    /// they just published without this.
    pub fn finalize(
        &self,
        share: &ShareRoot,
        id: SessionId,
        user: UserId,
    ) -> Result<SafePath, UploadError> {
        let cs = self.load_cached(id)?;
        {
            let row = cs.row.lock();
            if row.user != user {
                return Err(UploadError::NotFound);
            }
        }
        self.finalize_locked(&cs, id, share)
    }

    fn finalize_locked(&self, cs: &Arc<CachedSession>, id: SessionId, share: &ShareRoot) -> Result<SafePath, UploadError> {
        let (total, if_match, mtime, verify) = {
            let row = cs.row.lock();
            let total = row.total_len.ok_or(UploadError::Incomplete)?;
            if !row.received.is_complete(total) {
                return Err(UploadError::Incomplete);
            }
            // Both columns are always written together (`create`, above), so
            // a `verify` with no matching `verify_digest` is a schema-level
            // inconsistency, not a legitimate "opt-in but nothing to compare"
            // state -- `zip` drops it rather than calling `verify_whole_file`
            // with a fabricated empty expectation that would reject a
            // perfectly good upload.
            let verify = row.verify.zip(row.verify_digest.clone());
            (total, row.if_match.clone(), row.mtime_ns, verify)
        };

        let (part_path, dest_path) = {
            let row = cs.row.lock();
            let max_depth = share.policy().max_depth;
            let pp = self.part_path(&row, share)?;
            let dp = SafePath::parse(&row.dest, max_depth).map_err(|e| UploadError::BadRequest(e.to_string()))?;
            (pp, dp)
        };

        let fh = self.handle_for(cs, share)?;
        if let Some((algo, expected)) = &verify {
            if let Err(e) = self.verify_whole_file(&fh, *algo, expected, total) {
                // A verify failure must not leave the mismatching bytes
                // sitting under the part name until GC's TTL catches up
                // (up to `session_ttl`, 24h by
                // default) -- that only moves the problem from "a corrupt
                // file at the destination" to "a full-size orphan slowly
                // filling the 32 GB budget".
                // Unlink now. The client's declared digest no longer matches
                // what actually landed on disk, so the session cannot be
                // usefully resumed either -- dropping it forces a fresh
                // upload rather than a silent retry loop against bytes that
                // will never verify.
                drop(fh);
                let _ = share.unlink(&part_path);
                if let Some(evicted) = self.cache.lock().remove(&id) {
                    *evicted.handle.lock() = None;
                }
                let conn = self.db.lock();
                let _ = db::delete_row(&conn, id);
                return Err(e);
            }
        }

        // Decide replace-vs-create *before* dropping the handle: transplanting
        // the previous file's mode/owner needs the open fd.
        let no_replace = match share.stat(&dest_path) {
            Ok(prev) => {
                if let Some(want) = &if_match {
                    let got = file_etag(&prev);
                    if &got != want {
                        return Err(UploadError::PreconditionFailed);
                    }
                }
                // Inherit the replaced file's permissions and ownership before
                // the rename. Without this an atomic replace silently strips
                // whatever access the *other* services sharing this directory
                // (Jellyfin, *arr, rsync) had a moment ago — the exact failure
                // exists to prevent. Ownership is
                // best-effort inside `transplant_metadata_from`; the mode is
                // not.
                fh.transplant_metadata_from(&prev)?;
                false
            }
            Err(sc_vfs::VfsError::NotFound) => true, // RENAME_NOREPLACE: no window for a new file (§5.4)
            Err(e) => return Err(e.into()),
        };

        fh.sync_data()?;
        drop(fh);

        if let Some(mt) = mtime {
            share.set_times(&part_path, mt)?;
        }

        // Evict the cache entry (dropping our last handle reference) before
        // renaming, so we never depend on platform-specific semantics for
        // renaming/deleting a file that's still open.
        if let Some(evicted) = self.cache.lock().remove(&id) {
            *evicted.handle.lock() = None;
        }

        share.rename(&part_path, &dest_path, no_replace)?;
        // `sync_data` above made the uploaded bytes durable. This makes the
        // rename that published them durable too — otherwise a power cut
        // between the two leaves a complete upload under its `.scpart-` name,
        // with the session row already gone.
        share.sync_dir(&dest_path.parent())?;

        let conn = self.db.lock();
        db::delete_row(&conn, id)?;
        Ok(dest_path)
    }

    /// Compare the finished file's digest against `expected`
    /// (`UploadMeta::verify`). Before this fix, `verify` carried only an
    /// algorithm selector and this function computed a digest and handed it
    /// to `tracing::debug!` with nothing to compare against, so the check
    /// could never fail no matter what was actually on disk
    /// (`docs/). Reusing `UploadError::ChecksumMismatch`
    /// rather than a new variant: `UploadBridge::upload_err`
    /// (`sc-server/src/bridge.rs`) is an exhaustive match with no catch-all
    /// arm, and the per-chunk checksum path already maps this exact variant
    /// to `412` — the nearest available status to TUS's `460` for "an
    /// integrity check failed, don't blindly retry the same bytes" — which
    /// is equally the right answer for a whole-file mismatch.
    fn verify_whole_file(&self, fh: &FileHandle, algo: VerifyAlgo, expected: &[u8], total: u64) -> Result<(), UploadError> {
        use subtle::ConstantTimeEq;
        let mut buf = vec![0u8; total as usize];
        read_exact_at(fh, &mut buf, 0)?;
        // Constant-time comparison: `expected` is client-supplied and a
        // digest mismatch is exactly the kind of equality check `subtle`
        // exists for, even though a CRC32C/BLAKE3 digest is not itself
        // secret — one comparison style for both algorithms is simpler than
        // arguing one of them doesn't need it.
        let matched = match algo {
            VerifyAlgo::Crc32c => {
                let sum = crc32c::crc32c(&buf).to_be_bytes();
                sum.as_slice().ct_eq(expected).unwrap_u8() == 1
            }
            VerifyAlgo::Blake3 => {
                let sum = blake3::hash(&buf);
                sum.as_bytes().as_slice().ct_eq(expected).unwrap_u8() == 1
            }
        };
        if matched {
            Ok(())
        } else {
            Err(UploadError::ChecksumMismatch)
        }
    }

    // ---------------------------------------------------------------
    // GC (§9)
    // ---------------------------------------------------------------

    pub fn gc(&self, shares: &dyn Fn(ShareId) -> Option<Arc<ShareRoot>>) -> anyhow::Result<usize> {
        let now = now_ns();
        let rows = {
            let conn = self.db.lock();
            db::list_gc_candidates(&conn, now)?
        };
        let mut cleaned = 0usize;
        for row in &rows {
            let Some(share) = shares(row.share) else { continue };
            if let Ok(pp) = self.part_path(row, &share) {
                let _ = share.unlink(&pp);
            }
            if let Some(sd_name) = &row.spool_dir {
                let max_depth = share.policy().max_depth;
                if let Ok(dest) = SafePath::parse(&row.dest, max_depth) {
                    if let Ok(sd) = dest.parent().join(sd_name, max_depth) {
                        if let Ok(entries) = share.read_dir(&sd) {
                            for e in entries {
                                if let Ok(f) = sd.join(e.name.as_str(), max_depth) {
                                    let _ = share.unlink(&f);
                                }
                            }
                        }
                        let _ = share.rmdir(&sd);
                    }
                }
            }
            {
                let conn = self.db.lock();
                // `?` here used to abort the whole sweep on the first row
                // whose delete failed (e.g. a transient SQLite busy error),
                // silently leaving every later candidate in this same batch
                // uncleaned until the next sweep noticed them independently
                // — one bad row stopping every other share's cleanup is the
                // exact failure mode a periodic sweep exists to be resilient
                // against. Log and move on; the row stays a live GC
                // candidate and is retried next sweep.
                if let Err(e) = db::delete_row(&conn, row.id) {
                    tracing::warn!(session = %row.id, error = %e, "upload gc: could not delete session row; will retry next sweep");
                    continue;
                }
                // The alias dies with the session it points at. Leaving it
                // would let a transfer id keep addressing a freed session id.
                if let Err(e) = db::delete_aliases_for_session(&conn, row.id) {
                    tracing::warn!(session = %row.id, error = %e, "upload gc: could not delete transfer-id alias");
                }
            }
            self.cache.lock().remove(&row.id);
            cleaned += 1;
        }

        cleaned += self.sweep_orphans(shares)?;
        Ok(cleaned)
    }

    /// Orphan `.scpart-*` sweep (§9): never scans the whole share, only the
    /// set of parent directories any session has ever touched
    /// (`upload_touched_dirs`), and only removes entries with no matching
    /// live session and an mtime older than the session TTL. Matches on
    /// `read_dir_control` (control files are hidden from `read_dir`) — see the
    /// module-level naming note for why.
    fn sweep_orphans(&self, shares: &dyn Fn(ShareId) -> Option<Arc<ShareRoot>>) -> anyhow::Result<usize> {
        let dirs = {
            let conn = self.db.lock();
            db::list_touched_dirs(&conn)?
        };
        let live = {
            let conn = self.db.lock();
            db::list_live_names(&conn)?
        };
        let now = now_ns();
        let ttl_ns = self.cfg.session_ttl.as_nanos() as i128;
        let mut cleaned = 0usize;
        for (share_id, dir_str) in dirs {
            let Some(share) = shares(share_id) else { continue };
            let max_depth = share.policy().max_depth;
            let Ok(dir) = SafePath::parse(&dir_str, max_depth) else { continue };
            let Ok(entries) = share.read_dir_control(&dir) else { continue };
            for entry in entries {
                let name = entry.name.as_str();
                if !name.starts_with(".scpart-") {
                    continue;
                }
                if live.contains(&(share_id, name.to_string())) {
                    continue;
                }
                let Ok(child) = dir.join_control(name, max_depth) else { continue };
                let Ok(st) = share.stat(&child) else { continue };
                let age_ns = now - st.mtime_ns;
                if age_ns < ttl_ns {
                    // Not old enough: could be a session created concurrently
                    // with this sweep, rather than a genuine orphan.
                    continue;
                }
                if st.kind == sc_vfs::Kind::Dir {
                    if let Ok(inner) = share.read_dir(&child) {
                        for f in inner {
                            if let Ok(fp) = child.join(f.name.as_str(), max_depth) {
                                let _ = share.unlink(&fp);
                            }
                        }
                    }
                    let _ = share.rmdir(&child);
                } else {
                    let _ = share.unlink(&child);
                }
                cleaned += 1;
            }
        }
        Ok(cleaned)
    }

    // ---------------------------------------------------------------
    // shared helpers
    // ---------------------------------------------------------------

    fn load_cached(&self, id: SessionId) -> Result<Arc<CachedSession>, UploadError> {
        {
            let cache = self.cache.lock();
            if let Some(cs) = cache.get(&id) {
                return Ok(cs.clone());
            }
        }
        let row = {
            let conn = self.db.lock();
            db::load_row(&conn, id)?.ok_or(UploadError::NotFound)?
        };
        let cs = Arc::new(CachedSession {
            handle: Mutex::new(None),
            row: Mutex::new(row),
        });
        let mut cache = self.cache.lock();
        let entry = cache.entry(id).or_insert(cs);
        Ok(entry.clone())
    }

    fn handle_for(&self, cs: &Arc<CachedSession>, share: &ShareRoot) -> Result<Arc<FileHandle>, UploadError> {
        {
            let slot = cs.handle.lock();
            if let Some(h) = slot.as_ref() {
                return Ok(h.clone());
            }
        }
        let part_path = {
            let row = cs.row.lock();
            self.part_path(&row, share)?
        };
        let fh = Arc::new(share.open_read(&part_path)?);
        let mut slot = cs.handle.lock();
        if slot.is_none() {
            *slot = Some(fh);
        }
        Ok(slot.as_ref().unwrap().clone())
    }

    fn part_path(&self, row: &db::Row, share: &ShareRoot) -> Result<SafePath, UploadError> {
        let max_depth = share.policy().max_depth;
        let dest = SafePath::parse(&row.dest, max_depth).map_err(|e| UploadError::BadRequest(e.to_string()))?;
        dest.parent()
            .join_control(&row.part_name, max_depth)
            .map_err(|e| UploadError::BadRequest(e.to_string()))
    }

    fn spool_dir_path(&self, row: &db::Row, share: &ShareRoot) -> Result<SafePath, UploadError> {
        let name = row
            .spool_dir
            .as_ref()
            .ok_or_else(|| UploadError::BadRequest("session has no spool dir".into()))?;
        let max_depth = share.policy().max_depth;
        let dest = SafePath::parse(&row.dest, max_depth).map_err(|e| UploadError::BadRequest(e.to_string()))?;
        dest.parent()
            .join_control(name, max_depth)
            .map_err(|e| UploadError::BadRequest(e.to_string()))
    }

    fn effective_state(&self, row: &db::Row) -> SessionState {
        if matches!(row.state, SessionState::Receiving | SessionState::Finalizing) && now_ns() > row.expires_ns {
            SessionState::Expired
        } else {
            row.state
        }
    }

    fn ensure_receiving(&self, row: &db::Row) -> Result<(), UploadError> {
        match self.effective_state(row) {
            SessionState::Receiving => Ok(()),
            SessionState::Expired | SessionState::Aborted => Err(UploadError::Gone),
            _ => Err(UploadError::BadRequest("session is not receiving".into())),
        }
    }

    fn validate_patch(&self, row: &db::Row, offset: u64, len: u64) -> Result<(), UploadError> {
        if !row.random_access {
            let expected = row.received.contiguous_prefix();
            if offset != expected {
                return Err(UploadError::Conflict { expected, got: offset });
            }
        }
        if let Some(total) = row.total_len {
            let end = offset.checked_add(len).ok_or(UploadError::Unprocessable)?;
            if end > total {
                return Err(UploadError::Unprocessable);
            }
        }
        if len == 0 {
            return Ok(());
        }
        let is_last = row.total_len.map(|total| offset + len == total).unwrap_or(false);
        // Compares against the *session's own* snapshot, not the engine's
        // live `chunk_settings` — an admin raising or lowering the floor
        // mid-upload must not retroactively make an already-legal chunk size
        // illegal for a session that started under the old floor
        // (the in-flight-upload hazard).
        let whole_file_small = row.total_len.map(|total| total < row.chunk_min_at_creation).unwrap_or(false);
        if !is_last && !whole_file_small && len < row.chunk_min_at_creation {
            return Err(UploadError::ChunkTooSmall { min: row.chunk_min_at_creation });
        }
        Ok(())
    }

    fn check_resource_limits(&self, user: UserId, total_len: Option<u64>) -> Result<(), UploadError> {
        let conn = self.db.lock();
        let count = db::count_active_sessions(&conn, user)?;
        if count >= self.cfg.max_sessions_per_user {
            return Err(UploadError::ResourceExhausted("max_sessions_per_user".into()));
        }
        let reserved = db::sum_reserved_bytes(&conn, user)?;
        let add = total_len.unwrap_or(0);
        if reserved.saturating_add(add) > self.cfg.max_reserved_bytes_per_user {
            return Err(UploadError::ResourceExhausted("max_reserved_bytes_per_user".into()));
        }
        Ok(())
    }

    fn check_free_space(&self, share: &ShareRoot, dir: &SafePath, total_len: Option<u64>) -> Result<(), UploadError> {
        let need = total_len.unwrap_or(0).saturating_add(self.cfg.free_space_margin);
        match share.space(dir) {
            // `available`, not `free`: the root-reserved blocks are not ours
            // to write into, so counting them here would admit an upload that
            // is going to hit ENOSPC partway.
            Ok(s) if s.available < need => Err(UploadError::ResourceExhausted("insufficient free disk space".into())),
            _ => Ok(()), // probe failure/unsupported is not itself a rejection reason
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{SessionSpec, UploadMeta};
    use sc_vfs::{ShareId, SharePolicy, UserId};
    use std::path::Path;

    fn engine(dir: &Path, cfg: UploadConfig) -> UploadEngine {
        UploadEngine::new(&dir.join("up.db"), cfg).unwrap()
    }

    /// Share files live in a `data/` subdirectory, kept separate from the
    /// engine's own `up.db` (also created directly under `dir` by the
    /// `engine()` helper) so tests can assert on the share's directory
    /// contents without the SQLite file showing up as a stray entry.
    fn share(dir: &Path) -> ShareRoot {
        let data_dir = dir.join("data");
        std::fs::create_dir_all(&data_dir).unwrap();
        ShareRoot::open(ShareId::new(1), &data_dir, SharePolicy::default()).unwrap()
    }

    fn spec(dest: &str, total: Option<u64>, random_access: bool, mode: SpoolMode) -> SessionSpec {
        SessionSpec {
            user: UserId::new(1),
            share: ShareId::new(1),
            dest: SafePath::parse(dest, 64).unwrap(),
            total_len: total,
            random_access,
            if_match: None,
            mode,
            meta: UploadMeta {
                filename: dest.to_string(),
                ..Default::default()
            },
        }
    }

    fn small_cfg() -> UploadConfig {
        UploadConfig {
            chunk_size_min: 4,
            max_sessions_per_user: 32,
            max_reserved_bytes_per_user: u64::MAX,
            session_ttl: std::time::Duration::from_secs(3600),
            free_space_margin: 0,
            ..Default::default()
        }
    }

    #[test]
    fn ordering_rule_crash_between_write_and_commit() {
        let root = tempfile::tempdir().unwrap();
        let db_path = root.path().join("up.db");
        let sh = share(root.path());
        let cfg = small_cfg();

        let id = {
            let e = UploadEngine::new(&db_path, cfg.clone()).unwrap();
            let id = e.create(&sh, spec("hello.bin", Some(5), false, SpoolMode::OffsetAddressed)).unwrap();
            let cs = e.load_cached(id).unwrap();
            // Write phase only — simulate a crash before the commit phase.
            e.do_write(&cs, &sh, 0, b"hello").unwrap();
            id
            // `e` dropped here: nothing was ever committed to the DB.
        };

        // Fresh engine instance reopening the same DB file: this proves the
        // under-report survives a real process restart, not just an
        // in-memory cache miss.
        let e2 = UploadEngine::new(&db_path, cfg.clone()).unwrap();
        let status = e2.head(id, UserId::new(1)).unwrap();
        assert_eq!(status.offset, 0, "uncommitted write must not be visible");

        // Client resends the same bytes at the same offset: idempotent.
        let off = e2.patch(&sh, id, UserId::new(1), 0, b"hello", None).unwrap();
        assert_eq!(off, 5);
        e2.finalize(&sh, id, UserId::new(1)).unwrap();

        let bytes = std::fs::read(root.path().join("data").join("hello.bin")).unwrap();
        assert_eq!(bytes, b"hello");
    }

    #[test]
    fn assembly_free_offset_addressed_upload() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let e = engine(root.path(), small_cfg());

        let id = e.create(&sh, spec("file.bin", Some(10), false, SpoolMode::OffsetAddressed)).unwrap();
        // Part file must exist immediately, sparse-sized.
        let entries: Vec<_> = std::fs::read_dir(root.path().join("data")).unwrap().collect();
        assert_eq!(entries.len(), 1, "only the part file should exist before finalize");

        let off = e.patch(&sh, id, UserId::new(1), 0, b"0123456789", None).unwrap();
        assert_eq!(off, 10);
        e.finalize(&sh, id, UserId::new(1)).unwrap();

        let bytes = std::fs::read(root.path().join("data").join("file.bin")).unwrap();
        assert_eq!(bytes, b"0123456789");
        // Part file must be gone (renamed away), no stray files left over.
        let entries: Vec<_> = std::fs::read_dir(root.path().join("data")).unwrap().map(|e| e.unwrap().file_name()).collect();
        assert_eq!(entries, vec![std::ffi::OsString::from("file.bin")]);
    }

    #[test]
    fn chunk_minimum_enforced_except_last_and_small_files() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let cfg = UploadConfig {
            chunk_size_min: 8,
            ..small_cfg()
        };
        let e = engine(root.path(), cfg);

        // Mid-stream chunk below the minimum, NOT the last chunk -> rejected.
        let id = e.create(&sh, spec("a.bin", Some(20), false, SpoolMode::OffsetAddressed)).unwrap();
        let err = e.patch(&sh, id, UserId::new(1), 0, &[0u8; 4], None).unwrap_err();
        assert!(matches!(err, UploadError::ChunkTooSmall { .. }));

        // First chunk meets the minimum, then a final short chunk (offset+len==total) is fine.
        e.patch(&sh, id, UserId::new(1), 0, &[1u8; 16], None).unwrap();
        let off = e.patch(&sh, id, UserId::new(1), 16, &[2u8; 4], None).unwrap();
        assert_eq!(off, 20);
        e.finalize(&sh, id, UserId::new(1)).unwrap();

        // Whole file smaller than the minimum: single short chunk is fine.
        let id2 = e.create(&sh, spec("b.bin", Some(3), false, SpoolMode::OffsetAddressed)).unwrap();
        let off2 = e.patch(&sh, id2, UserId::new(1), 0, &[9u8; 3], None).unwrap();
        assert_eq!(off2, 3);
        e.finalize(&sh, id2, UserId::new(1)).unwrap();
    }

    #[test]
    fn set_chunk_settings_rejects_below_floor_and_default_under_min() {
        let root = tempfile::tempdir().unwrap();
        let e = engine(root.path(), small_cfg());

        let err = e.set_chunk_settings(CHUNK_MIN_BYTES_FLOOR - 1, 10 * 1024 * 1024).unwrap_err();
        assert!(matches!(err, UploadError::BadRequest(_)));

        let err = e.set_chunk_settings(8 * 1024 * 1024, 4 * 1024 * 1024).unwrap_err();
        assert!(matches!(err, UploadError::BadRequest(_)));

        // A valid write actually takes effect.
        e.set_chunk_settings(8 * 1024 * 1024, 16 * 1024 * 1024).unwrap();
        assert_eq!(e.chunk_settings(), (8 * 1024 * 1024, 16 * 1024 * 1024));
    }

    /// The in-flight-upload hazard: an admin
    /// raising the floor after a session has already started must not make a
    /// chunk size that was legal at creation suddenly get rejected.
    #[test]
    fn raising_the_floor_mid_upload_does_not_retroactively_reject_a_smaller_chunk() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let e = engine(root.path(), small_cfg()); // chunk_size_min: 4

        let id = e.create(&sh, spec("a.bin", Some(20), false, SpoolMode::OffsetAddressed)).unwrap();
        // Admin raises the floor well above this session's snapshot.
        e.set_chunk_settings(8 * 1024 * 1024, 16 * 1024 * 1024).unwrap();

        // A mid-stream chunk of 4 bytes was legal under the floor in effect
        // when this session was created, and must still be accepted.
        let off = e.patch(&sh, id, UserId::new(1), 0, &[1u8; 4], None).unwrap();
        assert_eq!(off, 4);

        // A brand-new session, however, is created under the new floor.
        let id2 = e.create(&sh, spec("b.bin", Some(20 * 1024 * 1024), false, SpoolMode::OffsetAddressed)).unwrap();
        let err = e.patch(&sh, id2, UserId::new(1), 0, &[1u8; 4], None).unwrap_err();
        assert!(matches!(err, UploadError::ChunkTooSmall { min } if min == 8 * 1024 * 1024));
    }

    /// Persistence across a restart: a fresh `UploadEngine` opening the same
    /// DB file must pick up the last admin write, not the `sc.toml` value it
    /// was originally constructed with.
    #[test]
    fn chunk_settings_persist_across_engine_restart() {
        let root = tempfile::tempdir().unwrap();
        let db_path = root.path().join("up.db");
        {
            let e = UploadEngine::new(&db_path, small_cfg()).unwrap();
            e.set_chunk_settings(9 * 1024 * 1024, 18 * 1024 * 1024).unwrap();
        }
        let e2 = UploadEngine::new(&db_path, small_cfg()).unwrap();
        assert_eq!(e2.chunk_settings(), (9 * 1024 * 1024, 18 * 1024 * 1024));
    }

    #[test]
    fn nc_name_ordered_fast_and_spill_paths_assemble_correctly() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let e = engine(root.path(), small_cfg());

        let id = e.create(&sh, spec("nc.bin", None, false, SpoolMode::NameOrdered)).unwrap();
        let c1 = vec![1u8; 5];
        let c2 = vec![2u8; 5];
        let c3 = vec![3u8; 5];

        // Send out of order: 3 spills, 2 spills, then 1 arrives (fast path)
        // and must drain both spooled successors in one go.
        e.put_named(&sh, id, UserId::new(1), 3, &c3).unwrap();
        e.put_named(&sh, id, UserId::new(1), 2, &c2).unwrap();
        assert_eq!(e.list_chunks(id).unwrap(), vec![2, 3]);
        e.put_named(&sh, id, UserId::new(1), 1, &c1).unwrap();

        // All three are now absorbed into the part file in order. Chunk 1 is
        // no longer a separate spool file, but list_chunks must still report
        // it as received — a client resuming via PROPFIND must never be
        // told to resend an already-absorbed chunk (see list_chunks' impl
        // comment: resending it would spill into the spool dir out of order
        // and break assembly).
        assert_eq!(e.list_chunks(id).unwrap(), vec![1, 2, 3]);

        e.assemble_and_finalize(&sh, id, UserId::new(1), 15, None).unwrap();
        let bytes = std::fs::read(root.path().join("data").join("nc.bin")).unwrap();
        let mut expected = c1;
        expected.extend(c2);
        expected.extend(c3);
        assert_eq!(bytes, expected);

        // No spool directory left behind.
        let entries: Vec<_> = std::fs::read_dir(root.path().join("data")).unwrap().map(|e| e.unwrap().file_name()).collect();
        assert_eq!(entries, vec![std::ffi::OsString::from("nc.bin")]);
    }

    #[test]
    fn nc_reverse_order_all_spilled_then_assembled() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let e = engine(root.path(), small_cfg());

        let id = e.create(&sh, spec("rev.bin", None, false, SpoolMode::NameOrdered)).unwrap();
        let chunks: Vec<Vec<u8>> = (1..=5).map(|n| vec![n as u8; 3]).collect();
        for (i, c) in chunks.iter().enumerate().rev() {
            let name = (i + 1) as u32;
            e.put_named(&sh, id, UserId::new(1), name, c).unwrap();
        }
        e.assemble_and_finalize(&sh, id, UserId::new(1), 15, None).unwrap();
        let bytes = std::fs::read(root.path().join("data").join("rev.bin")).unwrap();
        let expected: Vec<u8> = chunks.into_iter().flatten().collect();
        assert_eq!(bytes, expected);
    }

    #[test]
    fn resource_limits_reject_over_reservation() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let cfg = UploadConfig {
            max_reserved_bytes_per_user: 100,
            max_sessions_per_user: 1000,
            ..small_cfg()
        };
        let e = engine(root.path(), cfg);

        e.create(&sh, spec("first.bin", Some(50), false, SpoolMode::OffsetAddressed)).unwrap();
        // A second declaration that would push the user's reserved total over the cap.
        let err = e.create(&sh, spec("second.bin", Some(60), false, SpoolMode::OffsetAddressed)).unwrap_err();
        assert!(matches!(err, UploadError::ResourceExhausted(_)));
    }

    #[test]
    fn resource_limits_reject_too_many_sessions() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let cfg = UploadConfig {
            max_sessions_per_user: 2,
            max_reserved_bytes_per_user: u64::MAX,
            ..small_cfg()
        };
        let e = engine(root.path(), cfg);
        e.create(&sh, spec("a.bin", Some(1), false, SpoolMode::OffsetAddressed)).unwrap();
        e.create(&sh, spec("b.bin", Some(1), false, SpoolMode::OffsetAddressed)).unwrap();
        let err = e.create(&sh, spec("c.bin", Some(1), false, SpoolMode::OffsetAddressed)).unwrap_err();
        assert!(matches!(err, UploadError::ResourceExhausted(_)));
    }

    #[test]
    fn gc_sweeps_expired_sessions_and_orphan_part_files() {
        let root = tempfile::tempdir().unwrap();
        let sh = std::sync::Arc::new(share(root.path()));
        let cfg = UploadConfig {
            session_ttl: std::time::Duration::from_millis(1),
            ..small_cfg()
        };
        let e = engine(root.path(), cfg);

        let id = e.create(&sh, spec("gone.bin", Some(10), false, SpoolMode::OffsetAddressed)).unwrap();
        std::thread::sleep(std::time::Duration::from_millis(20));

        let resolver = |sid: ShareId| -> Option<Arc<ShareRoot>> {
            if sid == ShareId::new(1) {
                Some(sh.clone())
            } else {
                None
            }
        };
        let cleaned = e.gc(&resolver).unwrap();
        assert!(cleaned >= 1);
        assert!(e.head(id, UserId::new(1)).is_err());
        let entries: Vec<_> = std::fs::read_dir(root.path().join("data")).unwrap().collect();
        assert_eq!(entries.len(), 0, "part file must be gone after gc");
    }

    #[test]
    fn parallel_random_access_patch_produces_byte_identical_file() {
        let root = tempfile::tempdir().unwrap();
        let sh = Arc::new(share(root.path()));
        let cfg = UploadConfig {
            chunk_size_min: 1,
            ..small_cfg()
        };
        let e = Arc::new(engine(root.path(), cfg));

        const N: usize = 4;
        const PART: usize = 1000;
        let total = (N * PART) as u64;
        let id = e
            .create(&sh, spec("parallel.bin", Some(total), true, SpoolMode::OffsetAddressed))
            .unwrap();

        let mut handles = Vec::new();
        for i in 0..N {
            let e = e.clone();
            let sh = sh.clone();
            handles.push(std::thread::spawn(move || {
                let data = vec![i as u8; PART];
                let offset = (i * PART) as u64;
                e.patch(&sh, id, UserId::new(1), offset, &data, None).unwrap();
            }));
        }
        for h in handles {
            h.join().unwrap();
        }

        let status = e.head(id, UserId::new(1)).unwrap();
        assert_eq!(status.offset, total, "all parallel writes must be visible as one contiguous prefix");

        e.finalize(&sh, id, UserId::new(1)).unwrap();
        let bytes = std::fs::read(root.path().join("data").join("parallel.bin")).unwrap();
        assert_eq!(bytes.len(), total as usize);
        for i in 0..N {
            assert!(bytes[i * PART..(i + 1) * PART].iter().all(|&b| b == i as u8));
        }
    }

    #[test]
    fn contiguous_prefix_semantics_for_standard_tus_resume() {
        // A standard (non-random-access-aware) TUS client resuming a
        // random-access session must be able to trust HEAD's Upload-Offset
        // as "everything before this point is safely on disk"; sending from
        // there on is always correct even though the session allows gaps.
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let e = engine(root.path(), small_cfg());

        let id = e.create(&sh, spec("resume.bin", Some(30), true, SpoolMode::OffsetAddressed)).unwrap();
        // Random-access client writes the tail first.
        e.patch(&sh, id, UserId::new(1), 20, &[b'c'; 10], None).unwrap();
        let status = e.head(id, UserId::new(1)).unwrap();
        assert_eq!(status.offset, 0, "gap at the front: prefix must still be 0");

        // A "standard" client now resumes from offset 0 as HEAD instructed.
        e.patch(&sh, id, UserId::new(1), 0, &[b'a'; 10], None).unwrap();
        let status = e.head(id, UserId::new(1)).unwrap();
        assert_eq!(status.offset, 10, "front filled in, but still a gap before the tail run");

        e.patch(&sh, id, UserId::new(1), 10, &[b'b'; 10], None).unwrap();
        let status = e.head(id, UserId::new(1)).unwrap();
        assert_eq!(status.offset, 30, "gap closed: fully contiguous now");

        e.finalize(&sh, id, UserId::new(1)).unwrap();
        let bytes = std::fs::read(root.path().join("data").join("resume.bin")).unwrap();
        assert_eq!(&bytes[0..10], &[b'a'; 10][..]);
        assert_eq!(&bytes[10..20], &[b'b'; 10][..]);
        assert_eq!(&bytes[20..30], &[b'c'; 10][..]);
    }

    #[test]
    fn non_random_access_rejects_out_of_order_offset() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let e = engine(root.path(), small_cfg());
        let id = e.create(&sh, spec("seq.bin", Some(10), false, SpoolMode::OffsetAddressed)).unwrap();
        let err = e.patch(&sh, id, UserId::new(1), 5, &[0u8; 5], None).unwrap_err();
        assert!(matches!(err, UploadError::Conflict { expected: 0, got: 5 }));
    }

    #[test]
    fn checksum_mismatch_does_not_advance_offset_and_is_recoverable() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let e = engine(root.path(), small_cfg());
        let id = e.create(&sh, spec("ck.bin", Some(5), false, SpoolMode::OffsetAddressed)).unwrap();
        let bad = Checksum::Crc32c(0xdead_beef);
        let err = e.patch(&sh, id, UserId::new(1), 0, b"hello", Some(bad)).unwrap_err();
        assert!(matches!(err, UploadError::ChecksumMismatch));
        let status = e.head(id, UserId::new(1)).unwrap();
        assert_eq!(status.offset, 0);

        // Resend without checksum (or a correct one) succeeds and overwrites.
        let off = e.patch(&sh, id, UserId::new(1), 0, b"hello", None).unwrap();
        assert_eq!(off, 5);
    }

    #[test]
    fn abort_then_gc_removes_part_file() {
        let root = tempfile::tempdir().unwrap();
        let sh = Arc::new(share(root.path()));
        let e = engine(root.path(), small_cfg());
        let id = e.create(&sh, spec("aborted.bin", Some(10), false, SpoolMode::OffsetAddressed)).unwrap();
        e.abort(id, UserId::new(1)).unwrap();
        // abort() only flips state (DB row survives for GC to find the
        // parent directory); HEAD keeps working and reports the terminal
        // state so the HTTP layer can map it to 404/410 as it sees fit.
        let status = e.head(id, UserId::new(1)).unwrap();
        assert_eq!(status.state, SessionState::Aborted);

        let resolver = |sid: ShareId| -> Option<Arc<ShareRoot>> {
            if sid == ShareId::new(1) {
                Some(sh.clone())
            } else {
                None
            }
        };
        e.gc(&resolver).unwrap();
        assert!(e.head(id, UserId::new(1)).is_err(), "gc must have deleted the session row");
        let entries: Vec<_> = std::fs::read_dir(root.path().join("data")).unwrap().collect();
        assert_eq!(entries.len(), 0);
    }
    /// An in-flight upload must not be visible in ordinary directory
    /// listings.
    ///
    /// Regression: part files were once named `.{basename}.scpart-{id}` to
    /// dodge `SafePath`'s reserved-prefix rejection. That disguise also
    /// dodged `is_reserved_name`, so every partial upload showed up in the
    /// web UI and to WebDAV clients for the whole duration of the transfer.
    /// The maintenance sweep must still be able to see them, though — hence
    /// `read_dir_control`.
    #[test]
    fn part_file_is_hidden_from_listings_but_visible_to_maintenance() {
        let dir = tempfile::tempdir().unwrap();
        let sh = share(dir.path());
        let e = engine(dir.path(), small_cfg());

        let id = e.create(&sh, spec("doc.bin", Some(8), false, SpoolMode::OffsetAddressed)).unwrap();
        e.patch(&sh, id, UserId::new(1), 0, b"abcd", None).unwrap();

        let root = SafePath::parse("", 64).unwrap();
        let visible: Vec<String> =
            sh.read_dir(&root).unwrap().into_iter().map(|d| d.name.to_string()).collect();
        assert!(
            visible.iter().all(|n| !n.contains("scpart")),
            "part file leaked into a normal listing: {visible:?}"
        );

        let all: Vec<String> =
            sh.read_dir_control(&root).unwrap().into_iter().map(|d| d.name.to_string()).collect();
        assert!(
            all.iter().any(|n| n.starts_with(".scpart-")),
            "maintenance sweep cannot see the part file: {all:?}"
        );

        // ...and once published it appears under its real name only.
        e.patch(&sh, id, UserId::new(1), 4, b"efgh", None).unwrap();
        e.finalize(&sh, id, UserId::new(1)).unwrap();
        let after: Vec<String> =
            sh.read_dir(&root).unwrap().into_iter().map(|d| d.name.to_string()).collect();
        assert_eq!(after, vec!["doc.bin".to_string()], "unexpected leftovers: {after:?}");
    }

    /// `verify` used to carry only an algorithm selector and never compared
    /// anything against it (`docs/) — this is the
    /// positive case: a correct digest must still let the upload finalize.
    #[test]
    fn verify_accepts_a_correct_digest() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let e = engine(root.path(), small_cfg());

        let data = b"hello verify";
        let digest = crc32c::crc32c(data).to_be_bytes().to_vec();
        let s = SessionSpec {
            user: UserId::new(1),
            share: ShareId::new(1),
            dest: SafePath::parse("verified.bin", 64).unwrap(),
            total_len: Some(data.len() as u64),
            random_access: false,
            if_match: None,
            mode: SpoolMode::OffsetAddressed,
            meta: UploadMeta {
                filename: "verified.bin".into(),
                verify: Some((VerifyAlgo::Crc32c, digest)),
                ..Default::default()
            },
        };
        let id = e.create(&sh, s).unwrap();
        e.patch(&sh, id, UserId::new(1), 0, data, None).unwrap();
        e.finalize(&sh, id, UserId::new(1)).unwrap();

        let bytes = std::fs::read(root.path().join("data").join("verified.bin")).unwrap();
        assert_eq!(bytes, data);
    }

    /// The defect this guards against: before the fix, this exact scenario
    /// (`verify` set, digest deliberately wrong) still finalized and
    /// published the file — `verify_whole_file` computed the real digest and
    /// only logged it, with nothing to compare it to.
    #[test]
    fn verify_rejects_a_wrong_digest_and_leaves_nothing_published() {
        let root = tempfile::tempdir().unwrap();
        let sh = share(root.path());
        let e = engine(root.path(), small_cfg());

        let data = b"hello verify";
        let wrong_digest = vec![0xde, 0xad, 0xbe, 0xef];
        let s = SessionSpec {
            user: UserId::new(1),
            share: ShareId::new(1),
            dest: SafePath::parse("bad.bin", 64).unwrap(),
            total_len: Some(data.len() as u64),
            random_access: false,
            if_match: None,
            mode: SpoolMode::OffsetAddressed,
            meta: UploadMeta {
                filename: "bad.bin".into(),
                verify: Some((VerifyAlgo::Crc32c, wrong_digest)),
                ..Default::default()
            },
        };
        let id = e.create(&sh, s).unwrap();
        e.patch(&sh, id, UserId::new(1), 0, data, None).unwrap();
        let err = e.finalize(&sh, id, UserId::new(1)).unwrap_err();
        assert!(matches!(err, UploadError::ChecksumMismatch));

        // Nothing under the real name, and no lingering part file either --
        // a verify failure must not "move the problem" from a corrupt
        // destination file to an orphan that just waits out GC's TTL.
        let entries: Vec<_> = std::fs::read_dir(root.path().join("data")).unwrap().collect();
        assert_eq!(entries.len(), 0, "verify failure must leave no file behind: {entries:?}");

        // The session itself is gone too, not left resumable against bytes
        // that will never verify.
        assert!(e.head(id, UserId::new(1)).is_err());
    }
}
