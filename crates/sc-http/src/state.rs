//! Shared application state threaded through every handler/middleware.

use std::collections::HashMap;
use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Instant;

use sc_auth::AuthService;
use sc_vfs::ids::UserId;
use parking_lot::{Mutex, RwLock};
use rusqlite::{params, Connection, OptionalExtension};

use crate::config::HttpConfig;
use crate::content::SignedUrlKeys;
use crate::content_api::ContentApi;
use crate::core_api::{CoreApi, OpResult};
use crate::listing::ListingCache;
use crate::rate_limit::{IpTokenBucket, KeyedTokenBucket};
use crate::search_api::SearchApi;
use crate::search_limits::SearchConcurrency;
use crate::setup_api::SetupApi;
use crate::ws::WsHub;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum JobKind {
    Copy,
    Move,
    Delete,
    Archive,
    /// `POST /api/admin/index/build` — reuses this same
    /// queue rather than inventing a second progress mechanism.
    IndexBuild,
}

impl JobKind {
    // pub(crate): `routes.rs`'s `done_total_json` reads this to put a `kind`
    // field on the wire so a `GET /api/jobs` reattach can pick the right
    // tray icon/label without the client having to remember it itself.
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            JobKind::Copy => "copy",
            JobKind::Move => "move",
            JobKind::Delete => "delete",
            JobKind::Archive => "archive",
            JobKind::IndexBuild => "index_build",
        }
    }

    fn parse(s: &str) -> Option<Self> {
        Some(match s {
            "copy" => JobKind::Copy,
            "move" => JobKind::Move,
            "delete" => JobKind::Delete,
            "archive" => JobKind::Archive,
            "index_build" => JobKind::IndexBuild,
            _ => return None,
        })
    }
}

#[derive(Clone, Debug)]
pub struct JobStatus {
    pub id: String,
    /// Whoever's request created this job — checked by `job_status`/
    /// `job_cancel`/`job_download` before anything else about the job is
    /// revealed (a job id must not be readable or
    /// cancellable by another account).
    pub owner: UserId,
    pub kind: JobKind,
    pub state: JobState,
    pub done: u64,
    pub total: u64,
    pub current: Option<String>,
    pub errors: Vec<String>,
    /// Per-item results, same `OpResult` shape the synchronous copy/move/
    /// delete endpoints return inline — one entry per path whose outcome is
    /// actually known (`ok` or `failed`; see `attempting` for the third
    /// possibility).
    pub results: Vec<OpResult>,
    /// Paths this job started on but never got a chance to finish recording
    /// — `JobStore::begin_result` writes an `attempting` row before `op(p)`
    /// is even entered, so a crash mid-item leaves exactly this, never
    /// nothing. Non-empty only for an `Interrupted` job, and only ever at
    /// most one entry in practice (the single item that was in flight when
    /// the process stopped) — a `Vec` rather than `Option<String>` because
    /// nothing here needs to assume that in order to stay honest about it.
    pub attempting: Vec<String>,
    /// Paths the job was asked to act on that it never reached — recorded in
    /// full by `insert`, in the same transaction as the parent `jobs` row,
    /// before any work starts. Without this the only durable trace of a job
    /// was what it had already touched, so an interrupted 10-path move could
    /// say "3 done, 1 unknown" but not *which* six were never attempted: the
    /// files are all still there, but the user has no way to tell what is
    /// left to redo, which is a lost job in the terms this feature exists to
    /// serve. Empty for `Archive`/`IndexBuild` — neither has a per-path plan,
    /// and neither destroys anything if it never runs.
    pub pending: Vec<String>,
    /// Finished archive bytes, held in memory until `GET
    /// /api/jobs/{id}/download` reads them once (`routes::job_download`).
    /// Only ever set for `JobKind::Archive`.
    pub artifact: Option<std::sync::Arc<Vec<u8>>>,
}

impl JobStatus {
    pub fn new_running(id: String, owner: UserId, kind: JobKind, total: u64) -> Self {
        Self {
            id,
            owner,
            kind,
            state: JobState::Running,
            done: 0,
            total,
            current: None,
            errors: Vec::new(),
            results: Vec::new(),
            attempting: Vec::new(),
            pending: Vec::new(),
            artifact: None,
        }
    }

    /// The full list of paths this job will work through, in the order it
    /// will attempt them — persisted by `insert` as `pending` rows so an
    /// interrupted job can name what it never got to. See [`Self::pending`].
    pub fn with_pending(mut self, paths: &[String]) -> Self {
        self.pending = paths.to_vec();
        self
    }
}

/// Human-readable text for one failed `OpResult` — pulls the `message` field
/// `sc-server::bridge::http_op_result` puts in `error` when present, and
/// falls back to the raw JSON for whatever set `error` some other way.
fn opresult_error_text(r: &OpResult) -> Option<String> {
    r.error.as_ref().map(|v| v.get("message").and_then(|m| m.as_str()).map(str::to_string).unwrap_or_else(|| v.to_string()))
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub enum JobState {
    Running,
    Done,
    Error,
    Cancelled,
    Interrupted,
}

impl JobState {
    fn as_str(self) -> &'static str {
        match self {
            JobState::Running => "running",
            JobState::Done => "done",
            JobState::Error => "error",
            JobState::Cancelled => "cancelled",
            JobState::Interrupted => "interrupted",
        }
    }

    fn parse(s: &str) -> Option<Self> {
        Some(match s {
            "running" => JobState::Running,
            "done" => JobState::Done,
            "error" => JobState::Error,
            "cancelled" => JobState::Cancelled,
            "interrupted" => JobState::Interrupted,
            _ => return None,
        })
    }
}

fn now_unix() -> i64 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs() as i64).unwrap_or(0)
}

const SCHEMA: &str = "
    CREATE TABLE IF NOT EXISTS jobs (
        id         TEXT PRIMARY KEY,
        owner      INTEGER NOT NULL,
        kind       TEXT NOT NULL,
        state      TEXT NOT NULL,
        done       INTEGER NOT NULL DEFAULT 0,
        total      INTEGER NOT NULL,
        current    TEXT,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
    );
    CREATE INDEX IF NOT EXISTS jobs_owner_state ON jobs(owner, state);
    CREATE INDEX IF NOT EXISTS jobs_state_updated ON jobs(state, updated_at);
    CREATE TABLE IF NOT EXISTS job_results (
        job_id    TEXT NOT NULL,
        seq       INTEGER NOT NULL,
        path      TEXT NOT NULL,
        status    TEXT NOT NULL,
        error     TEXT,
        will_copy INTEGER NOT NULL DEFAULT 0,
        PRIMARY KEY (job_id, seq)
    );
";

/// Bounded cleanup of finished jobs — run inside `JobStore::insert` itself,
/// never a dedicated thread, so every single job creation exercises it. That
/// is the whole point: an unswept table only happens when its sweeper's
/// caller isn't guaranteed to run (this project already has one of those,
/// `nc_login_flow`). Every mutating file operation creates a job, so the
/// sweeper's caller is exercised by ordinary use, not by a
/// separately-wired background loop that can quietly stop being started.
const JOB_RETAIN_MAX: i64 = 500;
const JOB_RETAIN_AGE_SECS: i64 = 7 * 24 * 3600;

fn sweep(conn: &Connection) {
    let cutoff = now_unix() - JOB_RETAIN_AGE_SECS;
    let sql = "SELECT id FROM jobs WHERE state != 'running' AND (
        updated_at < ?1
        OR id IN (
            SELECT id FROM jobs WHERE state != 'running' ORDER BY updated_at ASC
            LIMIT MAX(0, (SELECT COUNT(*) FROM jobs WHERE state != 'running') - ?2)
        )
    )";
    let ids: Vec<String> = match conn.prepare(sql).and_then(|mut stmt| {
        let rows = stmt.query_map(params![cutoff, JOB_RETAIN_MAX], |r| r.get::<_, String>(0))?;
        rows.collect::<Result<Vec<_>, _>>()
    }) {
        Ok(ids) => ids,
        Err(e) => {
            tracing::error!(error = %e, "job retention sweep query failed");
            return;
        }
    };
    for id in &ids {
        if let Err(e) = conn.execute("DELETE FROM job_results WHERE job_id = ?1", params![id]) {
            tracing::error!(error = %e, job = %id, "failed to sweep job_results");
        }
        if let Err(e) = conn.execute("DELETE FROM jobs WHERE id = ?1", params![id]) {
            tracing::error!(error = %e, job = %id, "failed to sweep jobs");
        }
    }
}

/// SQLite-backed (`jobs.db`, opened once by `sc_server::App::build` the same
/// way as `shares.db`/`upload.db`/`index.db`) so a job — and, per item, an
/// honest record of what has actually happened so far — survives a restart.
/// described this model before it existed; this is that.
///
/// Two things stay in memory only, deliberately:
/// - `cancel_flags`: the hot per-item cancellation check (`spawn_batch_job`'s
///   loop consults it once per path) that must not round-trip through
///   SQLite. A restarted process has no runner left for a flag to gate
///   anyway — a leftover `running` row is reclassified as `interrupted` at
///   `open()`, not resumed, so there is nothing left for a restored flag to
///   mean.
/// - `artifacts`: finished archive bytes (`finish_archive`/`take_artifact`).
///   Kept off disk for the same reason as the archive job itself (no data
///   directory and no GC lifecycle just for zip bytes) — which means they do not
///   survive a restart either. `open()`'s startup sweep accounts for that: a
///   `done` archive job whose bytes are gone is reclassified to `error` with
///   a result that says why, instead of staying `done` while `job_download`
///   quietly 404s underneath it.
pub struct JobStore {
    db: Mutex<Connection>,
    cancel_flags: Mutex<HashMap<String, Arc<AtomicBool>>>,
    artifacts: Mutex<HashMap<String, Arc<Vec<u8>>>>,
}

impl JobStore {
    pub fn open(path: &Path) -> anyhow::Result<Self> {
        Self::from_conn(Connection::open(path)?)
    }

    pub fn open_in_memory() -> anyhow::Result<Self> {
        Self::from_conn(Connection::open_in_memory()?)
    }

    fn from_conn(conn: Connection) -> anyhow::Result<Self> {
        // WAL so a poll (reader) never blocks behind the runner's next
        // progress write, same as `sc_upload::db` / `sc_search::settings`
        // — but sync is FULL here, not NORMAL like
        // those two, and deliberately not copied from them: their data is
        // regenerable (an upload resumes, an index rebuilds), while a
        // `job_results` row is the only record that a destructive act
        // (delete/move/overwrite) happened at all. Under WAL, NORMAL only
        // syncs at checkpoints, so a committed `attempting` row can still be
        // lost to a power cut before the next checkpoint — reopening the
        // exact loss window (`begin_result` commits, the file is removed,
        // power dies, the WAL frame was never fsynced, the row is gone) that
        // record-before-act exists to close. FULL fsyncs every commit, so
        // `begin_result` returning success means the row is on disk, not
        // just handed to the OS, before its caller is allowed to touch the
        // filesystem. A bounded busy timeout so a poll racing a progress
        // write trades a few milliseconds' wait, not a 500.
        conn.pragma_update(None, "journal_mode", "WAL")?;
        conn.execute_batch("PRAGMA synchronous = FULL; PRAGMA busy_timeout = 5000;")?;
        conn.execute_batch(SCHEMA)?;

        let now = now_unix();
        // Startup recovery. Any row still `running`
        // here belongs to a runner that no longer exists — the previous
        // process crashed or was restarted (including an admin-triggered
        // one, `routes::admin_restart_server`) with the job mid-flight. It
        // is reclassified honestly, never resumed: resuming a partially
        // applied move/copy/delete risks re-running or skipping an item
        // this process has no way to know the true state of. `current` is
        // cleared (there is no runner left to be "on" anything); `done` and
        // every row already in `job_results` — including any left
        // `attempting` — are left exactly as they were: an accurate record
        // of what finished before the process stopped, not a guess.
        if let Err(e) =
            conn.execute("UPDATE jobs SET state = 'interrupted', current = NULL, updated_at = ?1 WHERE state = 'running'", params![now])
        {
            tracing::error!(error = %e, "failed to reclassify running jobs as interrupted at startup");
        }

        // A `done` archive job's bytes live only in `artifacts` (this
        // struct's doc above), which is always empty right after a fresh
        // `open()`. So every archive job this process finds already `done`
        // has bytes it cannot possibly still hold — reclassified to `error`
        // with a result that says why, so a poller is told the truth
        // instead of a `done` job whose download silently 404s.
        if let Err(e) = reclassify_expired_archives(&conn, now) {
            tracing::error!(error = %e, "failed to reclassify expired archive jobs at startup");
        }

        sweep(&conn);
        Ok(Self { db: Mutex::new(conn), cancel_flags: Mutex::new(HashMap::new()), artifacts: Mutex::new(HashMap::new()) })
    }

    /// Persists the parent `jobs` row before any work starts. Returns `false`
    /// if that row could not be committed — same rule as `begin_result`: a
    /// job id a restart can never find (because the row it would recover
    /// from was never written) is worse than no job id at all, so the caller
    /// must refuse to `spawn_blocking` and answer the request with a failure
    /// instead of a `202` that promises tracking this process cannot deliver.
    /// The parent row and the whole `pending` plan go in one transaction: a
    /// job that exists with only half its plan on disk would under-report
    /// what is left to redo, which is the exact failure `pending` was added
    /// to remove.
    #[must_use]
    pub fn insert(&self, job: JobStatus) -> bool {
        let now = now_unix();
        let ok = {
            let mut conn = self.db.lock();
            let ok = match insert_tx(&mut conn, &job, now) {
                Ok(()) => true,
                Err(e) => {
                    tracing::error!(error = %e, job = %job.id, "failed to persist new job; refusing to start it");
                    false
                }
            };
            sweep(&conn);
            ok
        };
        if ok {
            self.cancel_flags.lock().insert(job.id.clone(), Arc::new(AtomicBool::new(false)));
        }
        ok
    }

    pub fn get(&self, id: &str) -> Option<JobStatus> {
        let conn = self.db.lock();
        let row = conn
            .query_row("SELECT owner, kind, state, done, total, current FROM jobs WHERE id = ?1", params![id], |r| {
                Ok((
                    r.get::<_, i64>(0)?,
                    r.get::<_, String>(1)?,
                    r.get::<_, String>(2)?,
                    r.get::<_, i64>(3)?,
                    r.get::<_, i64>(4)?,
                    r.get::<_, Option<String>>(5)?,
                ))
            })
            .optional()
            .ok()
            .flatten()?;
        let (owner, kind, state, done, total, current) = row;

        let mut results = Vec::new();
        let mut attempting = Vec::new();
        let mut pending = Vec::new();
        if let Ok(mut stmt) = conn.prepare("SELECT path, status, error, will_copy FROM job_results WHERE job_id = ?1 ORDER BY seq") {
            if let Ok(rows) = stmt.query_map(params![id], |r| {
                Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?, r.get::<_, Option<String>>(2)?, r.get::<_, i64>(3)?))
            }) {
                for (path, status, error, will_copy) in rows.flatten() {
                    match status.as_str() {
                        "attempting" => attempting.push(path),
                        "pending" => pending.push(path),
                        "ok" => results.push(OpResult { path, ok: true, error: None, will_copy: will_copy != 0 }),
                        _ => {
                            let error = error.and_then(|s| serde_json::from_str(&s).ok());
                            results.push(OpResult { path, ok: false, error, will_copy: will_copy != 0 });
                        }
                    }
                }
            }
        }
        let errors = results.iter().filter(|r| !r.ok).filter_map(opresult_error_text).collect();
        let artifact = self.artifacts.lock().get(id).cloned();

        Some(JobStatus {
            id: id.to_string(),
            owner: UserId::new(owner as u32),
            kind: JobKind::parse(&kind)?,
            state: JobState::parse(&state)?,
            done: done as u64,
            total: total as u64,
            current,
            errors,
            results,
            attempting,
            pending,
            artifact,
        })
    }

    /// Owner-checked read. Returns `None` for both "no such job" and "not
    /// yours" — the same collapse the rest of this codebase already uses for
    /// share-link passwords, so a caller cannot use response shape to probe
    /// whether a job id exists under another account.
    pub fn get_owned(&self, id: &str, user: UserId) -> Option<JobStatus> {
        self.get(id).filter(|j| j.owner == user)
    }

    /// Non-terminal jobs owned by `user` — `running` (a live runner in this
    /// process, see `running_count`'s doc) or `interrupted` (this process's
    /// startup sweep found it abandoned by the last one). `JobTray` calls
    /// this once on mount to re-attach to whatever a refresh would otherwise
    /// have made it forget, using the durable record `jobs.db` already is,
    /// rather than a client-side store (`localStorage`) that would just be a
    /// second, divergence-prone copy of the same fact.
    pub fn list_open(&self, user: UserId) -> Vec<JobStatus> {
        let conn = self.db.lock();
        let mut stmt = match conn.prepare("SELECT id FROM jobs WHERE owner = ?1 AND state IN ('running', 'interrupted') ORDER BY created_at")
        {
            Ok(s) => s,
            Err(e) => {
                tracing::error!(error = %e, "failed to list open jobs");
                return Vec::new();
            }
        };
        let ids: Vec<String> = match stmt.query_map(params![user.get() as i64], |r| r.get::<_, String>(0)) {
            Ok(rows) => rows.flatten().collect(),
            Err(e) => {
                tracing::error!(error = %e, "failed to list open jobs");
                Vec::new()
            }
        };
        drop(stmt);
        drop(conn);
        ids.iter().filter_map(|id| self.get(id)).collect()
    }

    /// Owner-checked cancel. A no-op (returns `false`, same as "not found")
    /// on someone else's job, on an unknown id, and on a job that already
    /// reached a terminal state — cancelling a finished job has nothing left
    /// to stop. Only raises the in-memory flag; the runner itself is the one
    /// that transitions `state` to `Cancelled` (see `finish`), so a `GET`
    /// right after this never observes a terminal state with stale
    /// `done`/`results`.
    pub fn cancel_owned(&self, id: &str, user: UserId) -> bool {
        let conn = self.db.lock();
        let row: Option<(i64, String)> =
            conn.query_row("SELECT owner, state FROM jobs WHERE id = ?1", params![id], |r| Ok((r.get(0)?, r.get(1)?))).optional().unwrap_or(None);
        drop(conn);
        match row {
            Some((owner, state)) if owner as u32 == user.get() && state == "running" => match self.cancel_flags.lock().get(id) {
                Some(flag) => {
                    flag.store(true, Ordering::SeqCst);
                    true
                }
                None => false,
            },
            _ => false,
        }
    }

    pub fn is_cancelled(&self, id: &str) -> bool {
        self.cancel_flags.lock().get(id).map(|f| f.load(Ordering::SeqCst)).unwrap_or(false)
    }

    /// Test-only stand-in for a wedged `jobs.db` (disk full, a stuck WAL
    /// checkpoint, ...) — drops the table `begin_result` writes to, so its
    /// `INSERT` fails exactly the way a real storage fault would, letting a
    /// test assert `spawn_batch_job` refuses to run `op(p)` rather than
    /// silently proceeding on an unrecorded item.
    #[cfg(test)]
    pub fn break_results_table_for_test(&self) {
        self.db.lock().execute_batch("DROP TABLE job_results").expect("drop job_results for test");
    }

    /// Same idea as `break_results_table_for_test`, one level up: drops the
    /// parent `jobs` table itself, so `insert`'s own `INSERT` fails the way a
    /// wedged `jobs.db` would — letting a test assert a route refuses to
    /// `spawn_blocking` (and answers `500`, not `202`) for a job id that
    /// table could never have recovered.
    #[cfg(test)]
    pub fn break_jobs_table_for_test(&self) {
        self.db.lock().execute_batch("DROP TABLE jobs").expect("drop jobs for test");
    }

    /// Jobs with a live runner in *this* process — the only source of a
    /// `running` row is `insert` from this same process, and the only ways
    /// out are `finish`/`finish_archive`/`finish_index_build` from that same
    /// runner, or the startup sweep in `open` (before any request is served)
    /// that reclassifies a leftover `running` row as `interrupted`. So this
    /// count can never include an orphan from a previous process — exactly
    /// what `routes::admin_restart_server`'s restart-warning needs to answer
    /// "is it safe right now".
    pub fn running_count(&self) -> usize {
        self.db.lock().query_row("SELECT COUNT(*) FROM jobs WHERE state = 'running'", [], |r| r.get::<_, i64>(0)).unwrap_or(0) as usize
    }

    pub fn set_progress(&self, id: &str, done: u64, current: Option<String>) {
        let conn = self.db.lock();
        if let Err(e) =
            conn.execute("UPDATE jobs SET done = ?1, current = ?2, updated_at = ?3 WHERE id = ?4", params![done as i64, current, now_unix(), id])
        {
            tracing::error!(error = %e, job = %id, "failed to persist job progress");
        }
    }

    /// Recorded *before* `op(p)` is entered — the record-before-act half of
    /// the zero-loss requirement. `conn.execute` on this autocommit
    /// connection commits, and — with `jobs.db`'s `PRAGMA synchronous =
    /// FULL` (`from_conn`) — fsyncs before it returns, so a `true` result
    /// means the row is durable on disk, not merely handed to the OS, by the
    /// time the caller is allowed to touch the filesystem. If the process
    /// then dies while `op(p)` is running (or between this write and `op(p)`
    /// starting), this row is left as `attempting`: an honest "outcome
    /// unknown, needs a look" for exactly the one path that was in flight,
    /// never an absent row for a file the operation may already have
    /// removed, moved, or duplicated. `finish_result` overwrites it with the
    /// real outcome once `op(p)` actually returns.
    ///
    /// Returns `false` if the row could not be written (disk full, a
    /// wedged WAL checkpoint, ...) — the caller MUST NOT run `op(p)` for
    /// this item in that case. A destructive filesystem change with a
    /// record already known to have failed to persist is exactly the
    /// silent-loss case this feature exists to prevent, so an item with no
    /// durable "attempting" row is refused, not acted on and hoped for.
    #[must_use]
    pub fn begin_result(&self, id: &str, seq: u64, path: &str) -> bool {
        let conn = self.db.lock();
        match conn.execute(
            "INSERT OR REPLACE INTO job_results (job_id, seq, path, status, error, will_copy) VALUES (?1, ?2, ?3, 'attempting', NULL, 0)",
            params![id, seq as i64, path],
        ) {
            Ok(_) => true,
            Err(e) => {
                tracing::error!(error = %e, job = %id, path, "failed to record job item start; refusing to run the operation for this item");
                false
            }
        }
    }

    /// Overwrites a `begin_result` row with the real outcome, once `op(p)`
    /// has actually returned. `error` is stored as the same JSON text
    /// `OpResult::error` already carries, so `get` reconstructs it byte-for-
    /// byte rather than re-guessing a shape.
    pub fn finish_result(&self, id: &str, seq: u64, result: &OpResult) {
        let status = if result.ok { "ok" } else { "failed" };
        let error_text = result.error.as_ref().map(|v| v.to_string());
        let conn = self.db.lock();
        if let Err(e) = conn.execute(
            "UPDATE job_results SET status = ?1, error = ?2, will_copy = ?3 WHERE job_id = ?4 AND seq = ?5",
            params![status, error_text, result.will_copy as i64, id, seq as i64],
        ) {
            tracing::error!(error = %e, job = %id, "failed to record job item outcome");
        }
    }

    /// Terminal update for a copy/move/delete job. `state` is only ever
    /// written here or in `open`'s startup sweep (the runner is the sole
    /// writer while the process is alive), so there is nothing else to race
    /// against. Every per-item outcome was already persisted by
    /// `begin_result`/`finish_result` as it happened — this call has nothing
    /// left to lose if it never runs.
    pub fn finish(&self, id: &str, state: JobState) {
        let conn = self.db.lock();
        if let Err(e) = conn.execute("UPDATE jobs SET state = ?1, current = NULL, updated_at = ?2 WHERE id = ?3", params![state.as_str(), now_unix(), id])
        {
            tracing::error!(error = %e, job = %id, "failed to persist job completion");
        }
        drop(conn);
        self.cancel_flags.lock().remove(id);
    }

    /// Terminal update for an archive job — same sole-writer rule as
    /// [`Self::finish`], plus the finished zip bytes for `job_download`. The
    /// bytes themselves are never written to `jobs.db` (this struct's doc);
    /// a restart before they're downloaded is handled by `open`'s startup
    /// sweep, not by anything here.
    pub fn finish_archive(&self, id: &str, bytes: Vec<u8>) {
        let conn = self.db.lock();
        if let Err(e) = conn.execute("UPDATE jobs SET state = 'done', current = NULL, updated_at = ?1 WHERE id = ?2", params![now_unix(), id]) {
            tracing::error!(error = %e, job = %id, "failed to persist archive job completion");
        }
        drop(conn);
        self.cancel_flags.lock().remove(id);
        self.artifacts.lock().insert(id.to_string(), Arc::new(bytes));
    }

    /// Takes the finished artifact so a second download of the same job
    /// yields 404 instead of serving stale bytes forever — the in-memory
    /// buffer is freed once the one intended client has it.
    pub fn take_artifact(&self, id: &str, user: UserId) -> Option<Arc<Vec<u8>>> {
        let owner_ok = {
            let conn = self.db.lock();
            conn.query_row("SELECT owner FROM jobs WHERE id = ?1", params![id], |r| r.get::<_, i64>(0))
                .optional()
                .ok()
                .flatten()
                .map(|o| o as u32 == user.get())
                .unwrap_or(false)
        };
        if !owner_ok {
            return None;
        }
        self.artifacts.lock().remove(id)
    }

    /// Terminal update for a `JobKind::IndexBuild` job. Same sole-writer rule
    /// as [`Self::finish`]; `results` stays `OpResult`-shaped for every job
    /// kind, so a per-share `IndexBuildResult` is folded in with `path` set
    /// to the share name and its error text prefixed `"{share}: {error}"` —
    /// the same flat message a poller already got from the in-memory
    /// version of this method.
    pub fn finish_index_build(&self, id: &str, state: JobState, results: &[crate::core_api::IndexBuildResult]) {
        let conn = self.db.lock();
        for (seq, r) in results.iter().enumerate() {
            let status = if r.ok { "ok" } else { "failed" };
            let error_text = (!r.ok)
                .then(|| serde_json::json!({ "message": format!("{}: {}", r.share, r.error.as_deref().unwrap_or("failed")) }).to_string());
            if let Err(e) = conn.execute(
                "INSERT OR REPLACE INTO job_results (job_id, seq, path, status, error, will_copy) VALUES (?1, ?2, ?3, ?4, ?5, 0)",
                params![id, seq as i64, r.share, status, error_text],
            ) {
                tracing::error!(error = %e, job = %id, "failed to persist index-build result");
            }
        }
        if let Err(e) = conn.execute("UPDATE jobs SET state = ?1, current = NULL, updated_at = ?2 WHERE id = ?3", params![state.as_str(), now_unix(), id])
        {
            tracing::error!(error = %e, job = %id, "failed to persist index-build completion");
        }
        drop(conn);
        self.cancel_flags.lock().remove(id);
    }
}

/// The parent `jobs` row plus one `pending` `job_results` row per planned
/// path, committed together — see [`JobStore::insert`].
fn insert_tx(conn: &mut Connection, job: &JobStatus, now: i64) -> rusqlite::Result<()> {
    let tx = conn.transaction()?;
    tx.execute(
        "INSERT INTO jobs (id, owner, kind, state, done, total, current, created_at, updated_at)
         VALUES (?1, ?2, ?3, 'running', 0, ?4, NULL, ?5, ?5)",
        params![job.id, job.owner.get() as i64, job.kind.as_str(), job.total as i64, now],
    )?;
    for (seq, path) in job.pending.iter().enumerate() {
        tx.execute(
            "INSERT INTO job_results (job_id, seq, path, status, error, will_copy) VALUES (?1, ?2, ?3, 'pending', NULL, 0)",
            params![job.id, seq as i64, path],
        )?;
    }
    tx.commit()
}

/// Every archive job this fresh process finds still `done` has bytes it
/// cannot possibly hold (`artifacts` starts empty every `open()`) — turns
/// each into an `error` with one result row explaining why, so `job_status`
/// tells the truth instead of promising a download that 404s.
fn reclassify_expired_archives(conn: &Connection, now: i64) -> rusqlite::Result<()> {
    let ids: Vec<String> = {
        let mut stmt = conn.prepare("SELECT id FROM jobs WHERE kind = 'archive' AND state = 'done'")?;
        let rows = stmt.query_map([], |r| r.get::<_, String>(0))?;
        rows.collect::<Result<Vec<_>, _>>()?
    };
    for id in &ids {
        let error_text = serde_json::json!({ "message": "archive expired across a restart; request the download again" }).to_string();
        conn.execute(
            "INSERT OR REPLACE INTO job_results (job_id, seq, path, status, error, will_copy) VALUES (?1, 0, '', 'failed', ?2, 0)",
            params![id, error_text],
        )?;
        conn.execute("UPDATE jobs SET state = 'error', updated_at = ?1 WHERE id = ?2", params![now, id])?;
    }
    Ok(())
}

#[cfg(test)]
mod job_store_tests {
    use super::*;

    const OWNER: UserId = UserId::new(7);

    fn paths() -> Vec<String> {
        ["/a", "/b", "/c", "/d"].iter().map(|s| s.to_string()).collect()
    }

    fn ok_result(path: &str) -> OpResult {
        OpResult { path: path.to_string(), ok: true, error: None, will_copy: false }
    }

    /// The whole point of `pending`: after a restart kills the runner, the
    /// job must still be able to name every path — the ones it finished, the
    /// one whose outcome nobody can know, and the ones it never started.
    /// Before this the last group existed only as arithmetic (`total - done`)
    /// with no way back to the paths themselves.
    #[test]
    fn a_restart_leaves_an_interrupted_job_able_to_name_what_it_never_reached() {
        let dir = tempfile::tempdir().expect("tempdir");
        let db = dir.path().join("jobs.db");
        let id = "J-restart";

        {
            let store = JobStore::open(&db).expect("open");
            assert!(store.insert(JobStatus::new_running(id.into(), OWNER, JobKind::Move, 4).with_pending(&paths())));
            assert!(store.begin_result(id, 0, "/a"));
            store.finish_result(id, 0, &ok_result("/a"));
            store.set_progress(id, 1, Some("/a".into()));
            // `/b` starts and the process dies here — no `finish_result`.
            assert!(store.begin_result(id, 1, "/b"));
        }

        // Fresh process, same file.
        let store = JobStore::open(&db).expect("reopen");
        let job = store.get_owned(id, OWNER).expect("job survived the restart");
        assert_eq!(job.state, JobState::Interrupted);
        assert_eq!(job.results.iter().map(|r| r.path.as_str()).collect::<Vec<_>>(), ["/a"]);
        assert_eq!(job.attempting, ["/b"], "the item in flight is unknown, not absent and not a result");
        assert_eq!(job.pending, ["/c", "/d"], "the untouched remainder must be nameable, not just countable");
        // Every requested path is accounted for exactly once.
        assert_eq!(job.results.len() + job.attempting.len() + job.pending.len(), 4);
        assert_eq!(store.running_count(), 0, "a reclassified job must not keep a restart gate closed");
    }

    /// `list_open` is what `GET /api/jobs` (and so a browser refresh) reads —
    /// an interrupted job has to come back through it carrying the same
    /// breakdown, or the tray can show the job but not what to do about it.
    #[test]
    fn list_open_carries_the_breakdown_a_refreshed_tray_needs() {
        let dir = tempfile::tempdir().expect("tempdir");
        let db = dir.path().join("jobs.db");
        {
            let store = JobStore::open(&db).expect("open");
            assert!(store.insert(JobStatus::new_running("J-1".into(), OWNER, JobKind::Delete, 4).with_pending(&paths())));
            assert!(store.begin_result("J-1", 0, "/a"));
        }
        let store = JobStore::open(&db).expect("reopen");
        let open = store.list_open(OWNER);
        assert_eq!(open.len(), 1);
        assert_eq!(open[0].state, JobState::Interrupted);
        assert_eq!(open[0].attempting, ["/a"]);
        assert_eq!(open[0].pending, ["/b", "/c", "/d"]);
        assert!(store.list_open(UserId::new(8)).is_empty(), "another account must not see it");
    }

    /// A cancel stops at an item boundary, so everything after it was never
    /// touched — same question ("what still needs doing?"), same answer.
    #[test]
    fn a_cancelled_job_still_names_the_items_it_skipped() {
        let store = JobStore::open_in_memory().expect("open");
        assert!(store.insert(JobStatus::new_running("J-2".into(), OWNER, JobKind::Copy, 4).with_pending(&paths())));
        assert!(store.begin_result("J-2", 0, "/a"));
        store.finish_result("J-2", 0, &ok_result("/a"));
        assert!(store.cancel_owned("J-2", OWNER));
        store.finish("J-2", JobState::Cancelled);

        let job = store.get_owned("J-2", OWNER).expect("job");
        assert_eq!(job.state, JobState::Cancelled);
        assert_eq!(job.pending, ["/b", "/c", "/d"]);
        assert!(job.attempting.is_empty());
    }

    /// The plan and the parent row are one transaction — a job that cannot
    /// record its plan must not exist at all, since a half-recorded plan
    /// under-reports what is left to redo.
    #[test]
    fn a_job_whose_plan_cannot_be_written_is_refused_outright() {
        let store = JobStore::open_in_memory().expect("open");
        store.break_results_table_for_test();
        assert!(!store.insert(JobStatus::new_running("J-3".into(), OWNER, JobKind::Delete, 4).with_pending(&paths())));
        assert_eq!(store.running_count(), 0, "the parent row must have rolled back with the plan");
    }
}

/// A semaphore that can be swapped out for a differently-sized one live
/// (admin settings screen, `archive.max_concurrent`), same replace-outright
/// pattern as `search_limits::SearchConcurrency`: a stream already holding a
/// permit from the old semaphore is unaffected, only new `try_acquire_owned`
/// calls see the new cap.
pub struct ResizableSemaphore(RwLock<Arc<tokio::sync::Semaphore>>);

impl ResizableSemaphore {
    pub fn new(n: usize) -> Self {
        Self(RwLock::new(Arc::new(tokio::sync::Semaphore::new(n.max(1)))))
    }

    pub fn current(&self) -> Arc<tokio::sync::Semaphore> {
        self.0.read().clone()
    }

    pub fn resize(&self, n: usize) {
        *self.0.write() = Arc::new(tokio::sync::Semaphore::new(n.max(1)));
    }
}

/// Request extension set by `TrustedProxy` — the resolved client address,
/// after `CF-Connecting-IP` validation (or the raw peer address if that
/// validation didn't apply/succeed).
#[derive(Clone, Copy, Debug)]
pub struct ClientIp(pub std::net::IpAddr);

/// Request extension set by `HostGuard` — which of the two hosts this
/// request arrived on ( /).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum HostOrigin {
    App,
    Content,
}

/// Request extension set by `RequestId` — echoed back as `Sc-Trace`.
#[derive(Clone, Copy, Debug)]
pub struct RequestId(pub uuid::Uuid);

#[derive(Clone)]
pub struct AppState {
    pub cfg: Arc<HttpConfig>,
    pub auth: Arc<AuthService>,
    pub core: Arc<dyn CoreApi>,
    pub uploads: Arc<dyn crate::upload_api::UploadApi>,
    /// Signed content-URL byte serving (`GET /c/{token}`,
    /// ) — `FileId`-keyed, deliberately separate from
    /// `core` (see `content_api` module docs).
    pub content: Arc<dyn ContentApi>,
    /// `GET /api/search[/stream]` — backed by
    /// `sc-search`'s walker in `sc-server`; see `search_api` module docs for
    /// why this isn't just another `CoreApi` method.
    pub search: Arc<dyn SearchApi>,
    /// `GET /api/recent` — the same walker as `search`, collected through a
    /// bounded top-N instead of stopped at the first N matches found. Its own
    /// port for the same reason `search` has one.
    pub recent: Arc<dyn crate::recent_api::RecentApi>,
    /// `GET`/`POST /api/setup` — bound to a real gate
    /// by `sc-server`, which is the only crate that knows about the one-time
    /// token file. Defaults to [`crate::setup_api::SetupClosed`], so an
    /// `AppState` built without one cannot create an administrator.
    pub setup: Arc<dyn SetupApi>,
    /// `/api/auth/oidc/**` and the admin link routes, bound to a real
    /// `sc_oidc::OidcProvider` by `sc-server` when `[oidc]` is configured and
    /// active. Defaults to [`crate::oidc_api::OidcDisabled`], so an
    /// `AppState` built without one answers `oidc.disabled` everywhere
    /// instead of pretending a provider exists.
    pub oidc: Arc<dyn crate::oidc_api::OidcApi>,
    /// Per-IP budget for `GET /api/auth/oidc/start` and the callback (§5-1's
    /// `429 rate.limited`), separate from the general `rate_limiter` for the
    /// same reason `setup_rate` is: 60/s is right for ordinary API traffic
    /// and far too generous for an unauthenticated endpoint that writes a
    /// database row and can make this server call out to a third party.
    pub oidc_rate: Arc<IpTokenBucket>,
    pub signed_url_keys: Arc<Mutex<SignedUrlKeys>>,
    pub listings: Arc<ListingCache>,
    pub ws: Arc<WsHub>,
    pub jobs: Arc<JobStore>,
    pub rate_limiter: Arc<IpTokenBucket>,
    /// Share-link password attempts, keyed by the token in the URL
    /// (10 per hour per token).
    pub link_rate: Arc<KeyedTokenBucket>,
    /// Share-link listing and zip attempts, keyed by the token in the URL
    /// (60 burst, one a second after that). Its own budget rather than the
    /// general per-IP limiter: one office behind a NAT shares an IP bucket,
    /// and a botnet gets one per host against a single link.
    pub link_browse_rate: Arc<KeyedTokenBucket>,
    /// Per-user search rate limit (30/min), keyed by
    /// the caller's `UserId`.
    pub search_rate: Arc<KeyedTokenBucket>,
    /// `POST /api/setup` attempts, per IP, *in addition to* the general
    /// `rate_limiter`. That one is tuned for ordinary API traffic (60/s) and
    /// is far too generous for an unauthenticated endpoint that creates an
    /// administrator: at 60/s a token guesser gets five million attempts a
    /// day. This bucket is the one that actually bounds it.
    pub setup_rate: Arc<IpTokenBucket>,
    /// Global concurrent-search cap, split per storage-class tier
    /// (T2 is I/O-bound enough that unlimited
    /// concurrent walks would starve other services, and an HDD-bound walk
    /// needs a tighter cap than an NVMe-bound one). `try_acquire` (not
    /// `.await`) because an exhausted budget should reject immediately with
    /// `429 Retry-After`, not make the caller wait server-side.
    pub search_concurrency: Arc<SearchConcurrency>,
    /// Global concurrent-archive-stream cap ('s rate-
    /// limit shape; each stream holds an open fd and walks a tree for its
    /// entire duration, so unbounded concurrency here is a resource-
    /// exhaustion vector). Default 4 — see `crate::routes::fs_archive`.
    pub archive_concurrency: Arc<ResizableSemaphore>,
    /// Global concurrent-folder-size cap (`GET /api/fs/size`). Sized like
    /// search's tier caps and for the same reason: each request can start a
    /// recursive tree walk, and an uncapped endpoint that does that is a way
    /// to make the server walk the whole disk. Over the cap the answer is
    /// `429` with `Retry-After`, immediately, never a server-side queue.
    pub folder_size_concurrency: Arc<ResizableSemaphore>,
    /// Server-local CSRF derivation key. The token
    /// handed to the client is `HMAC(csrf_key, sha256(session_token))`,
    /// which lets `Csrf` verify without any additional storage beyond the
    /// session token the cookie already carries.
    pub csrf_key: [u8; 32],
    pub boot_time: Instant,
    /// Server-settings admin screen (`ServerSettingsSection.svelte`) — reads
    /// the effective config and applies live/staged overrides. Defaults to
    /// [`crate::settings_api::UnimplementedSettings`], same pattern as
    /// `setup`/`uploads` above, so `AppState` stays constructible in tests
    /// without a real backend wired in.
    pub settings: Arc<dyn crate::settings_api::SettingsApi>,
    /// Signalled by `POST /api/admin/restart` after a settings change that
    /// needs one; `sc_server::lib::cmd_serve`'s `tokio::select!` waits on
    /// this alongside the OS shutdown signal so a UI-triggered restart runs
    /// through the exact same graceful-shutdown sequence, then exits with a
    /// distinct code `systemd`'s `Restart=on-failure` reacts to.
    pub restart_signal: Arc<tokio::sync::Notify>,
}
