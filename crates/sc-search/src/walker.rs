//! **T2 — the parallel filesystem walker** (§3). The first-class search path.
//!
//! Properties the design insists on, and where each lives:
//!
//! | Requirement | Where |
//! |---|---|
//! | Work-stealing at *directory* granularity | [`Walker::run_level_parallel`] via `crossbeam-deque` |
//! | Zero `statx` for name-only matching | [`scan_dir`] — `DirEntry.kind` comes from `d_type` |
//! | inode-order batching for the stat phase | [`sort_for_stat`] |
//! | Small-corpus single-thread fast path | [`Walker::threads_for_level`] |
//! | BFS, not DFS | [`Walker::walk_sources`] level loop |
//! | ACL pruning, never post-filtering | [`scan_dir`] — the check gates *descent* |
//! | Reserved names skipped | [`scan_dir`] |
//!
//! ### Why BFS
//!
//! On budget exhaustion a DFS has spent everything on one deep branch and can
//! promise nothing. A level-synchronous BFS can always say "every directory
//! shallower than depth *d* was completely enumerated", which is the guarantee
//! the UI needs in order to display a truncated result honestly.
//!
//! Because children discovered while processing level *d* are queued for level
//! *d+1* rather than pushed back into the current level's deques, no new work
//! is ever created inside a level. That makes worker termination trivially
//! correct: a worker that finds no task can exit immediately, since any
//! remaining task is already owned by a worker that will run it.

use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use compact_str::CompactString;
use crossbeam_channel::Sender;
use crossbeam_deque::{Injector, Steal, Stealer, Worker as Deque};
use parking_lot::Mutex;

use crate::fold;
use crate::matcher::Matcher;
use crate::rank::{self, RankInput};
use crate::vfs::{
    display_path, is_reserved_name, join_child, DirSource, Kind, SafePath, ShareId, ShareRoot,
    ShareSource,
};

/// Below this many directories the thread pool costs more than it buys.
/// `sharkdp/fd#1614` measured GNU `find` beating a parallel walker by **8.95×**
/// on a single small directory; this constant is what prevents us reproducing
/// that.
pub const SMALL_CORPUS_DIRS: u64 = 64;

/// Resource ceilings for one walk (§3.5, §8).
#[derive(Clone, Debug)]
pub struct WalkBudget {
    pub deadline: Instant,
    /// Counted in `getdents64` entries, not stats.
    pub max_entries: u64,
    pub max_results: u32,
    pub max_depth: u16,
}

impl WalkBudget {
    /// §8 defaults, parameterised by storage: NVMe 3 s, HDD 8 s.
    pub fn new(budget: Duration) -> Self {
        Self {
            deadline: Instant::now() + budget,
            max_entries: 5_000_000,
            max_results: 1000,
            max_depth: 64,
        }
    }

    pub fn for_storage(rotational: bool) -> Self {
        Self::new(Duration::from_secs(if rotational { 8 } else { 3 }))
    }

    pub fn max_entries(mut self, n: u64) -> Self {
        self.max_entries = n;
        self
    }
    pub fn max_results(mut self, n: u32) -> Self {
        self.max_results = n;
        self
    }
    pub fn max_depth(mut self, n: u16) -> Self {
        self.max_depth = n;
        self
    }
}

impl Default for WalkBudget {
    fn default() -> Self {
        Self::new(Duration::from_secs(3))
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TruncReason {
    Deadline,
    MaxEntries,
    MaxResults,
    MaxDepth,
}

/// Honest truncation reporting (§3.5). `seen` is the real entry count, not an
/// estimate — the UI shows it verbatim and the index suggester (§4) keys off
/// it.
#[derive(Clone, Debug, PartialEq)]
pub enum Completeness {
    Full,
    Truncated {
        reason: TruncReason,
        seen: u64,
        elapsed: Duration,
    },
}

impl Completeness {
    pub fn is_full(&self) -> bool {
        matches!(self, Completeness::Full)
    }
}

#[derive(Clone, Debug)]
pub struct Hit {
    pub share: ShareId,
    pub path: String,
    pub name: CompactString,
    pub is_dir: bool,
    /// `None` unless the stat phase ran.
    pub size: Option<u64>,
    /// `None` unless the stat phase ran.
    pub mtime_ns: Option<i128>,
    pub score: f32,
}

/// One unit of work: one directory. Parallelism happens at directory
/// boundaries and nowhere else — §1.2 point 3.
#[derive(Debug)]
struct Job {
    src: usize,
    path: SafePath,
    depth: u16,
}

/// An entry that matched by name and is waiting for the inode-ordered stat
/// phase (§3.4).
#[derive(Debug)]
pub struct Pending {
    pub src: usize,
    pub dev: u64,
    /// `Some` only when the directory read handed us an inode number. See
    /// [`sort_for_stat`].
    pub ino: Option<u64>,
    /// Ordinal of the containing directory, and of the entry within it —
    /// i.e. readdir order, preserved.
    pub dir_seq: u64,
    pub ent_seq: u32,
    pub path: SafePath,
    pub name: CompactString,
    pub is_dir: bool,
}

/// Order matched entries for the stat phase.
///
/// §3.4: filesystems lay inodes out in increasing number order, so issuing
/// stats in `(dev, ino)` order makes the disk seek forward only and raises the
/// chance that several inodes come out of one block — measured as a
/// single-digit-multiple reduction in seek latency on rotational media
/// (Kołaczkowski, IPCCC'09).
///
/// `sc_vfs::DirEntry::ino` carries the inode number for free on platforms
/// where `getdents64` (or equivalent) hands one over — Linux always does.
/// Where it doesn't (the portable/Windows backend), `ino` is `None` here and
/// the sort degrades to `(dev, directory order, readdir order)`, which groups
/// the stats by containing directory and preserves kernel readdir order —
/// the best available locality proxy on those platforms, and *not* the same
/// thing as a real inode sort.
pub fn sort_for_stat(pending: &mut [Pending]) {
    pending.sort_unstable_by_key(|p| (p.dev, p.ino.unwrap_or(u64::MAX), p.dir_seq, p.ent_seq));
}

pub struct Walker {
    threads: usize,
    rotational: bool,
    dirs_visited: AtomicU64,
    entries_seen: AtomicU64,
    peak_threads: AtomicUsize,
}

impl Walker {
    pub fn new(threads: usize) -> Self {
        Self {
            threads: threads.max(1),
            rotational: false,
            dirs_visited: AtomicU64::new(0),
            entries_seen: AtomicU64::new(0),
            peak_threads: AtomicUsize::new(0),
        }
    }

    /// Rotational media: fewer threads (§3.3) and inode-ordered stats (§3.4).
    pub fn with_rotational(mut self, rotational: bool) -> Self {
        self.rotational = rotational;
        self
    }

    pub fn threads(&self) -> usize {
        self.threads
    }

    /// §3.3. `dir_hint` is a directory count from a previous walk or a cached
    /// share summary; `None` means "unknown, assume it might be large".
    pub fn decide_threads(rotational: bool, dir_hint: Option<u64>) -> usize {
        // ① small corpus: do not create threads at all.
        if dir_hint.is_some_and(|dirs| dirs < SMALL_CORPUS_DIRS) {
            return 1;
        }
        // ② storage characteristics. Rotational media thrash on seeks when
        //    over-parallelised.
        if rotational {
            return 2;
        }
        std::thread::available_parallelism()
            .map(|n| n.get())
            .unwrap_or(4)
            .min(16)
    }

    /// Directories entered during the most recent walk. The ACL-pruning test
    /// asserts on this: a rejected subtree must not contribute.
    pub fn dirs_visited(&self) -> u64 {
        self.dirs_visited.load(Ordering::Relaxed)
    }

    /// `getdents64`-equivalent entries seen during the most recent walk.
    pub fn entries_seen(&self) -> u64 {
        self.entries_seen.load(Ordering::Relaxed)
    }

    /// Largest thread count actually used in the most recent walk. 1 means the
    /// small-corpus fast path held for the whole walk.
    pub fn peak_threads(&self) -> usize {
        self.peak_threads.load(Ordering::Relaxed)
    }

    /// The contracted entry point.
    ///
    /// **One deviation from the given signature:** `acl` is
    /// `&(dyn Fn(..) -> bool + Sync)` rather than a bare `&dyn Fn`. A plain
    /// `&dyn Fn` is not `Sync`, so it could not be consulted from a worker
    /// thread at all — and consulting it *during* traversal is the entire
    /// point, since post-filtering both leaks existence and wastes the I/O.
    /// Any closure that does not capture non-`Sync` state coerces
    /// automatically, so callers are unaffected.
    pub fn walk(
        &self,
        roots: &[(Arc<ShareRoot>, SafePath)],
        m: &Matcher,
        acl: &(dyn Fn(ShareId, &SafePath) -> bool + Sync),
        budget: &WalkBudget,
        out: &Sender<Hit>,
    ) -> Completeness {
        let sources: Vec<Arc<dyn DirSource>> = roots
            .iter()
            .map(|(r, _)| {
                Arc::new(ShareSource::new(r.clone(), self.rotational)) as Arc<dyn DirSource>
            })
            .collect();
        let starts: Vec<(usize, SafePath)> = roots
            .iter()
            .enumerate()
            .map(|(i, (_, p))| (i, p.clone()))
            .collect();
        self.walk_sources(&sources, &starts, m, acl, budget, out)
    }

    /// The actual engine. Everything above funnels here.
    pub fn walk_sources(
        &self,
        sources: &[Arc<dyn DirSource>],
        starts: &[(usize, SafePath)],
        m: &Matcher,
        acl: &(dyn Fn(ShareId, &SafePath) -> bool + Sync),
        budget: &WalkBudget,
        out: &Sender<Hit>,
    ) -> Completeness {
        let started = Instant::now();
        self.dirs_visited.store(0, Ordering::Relaxed);
        self.entries_seen.store(0, Ordering::Relaxed);
        self.peak_threads.store(0, Ordering::Relaxed);

        let sh = Shared {
            m,
            acl,
            budget,
            out,
            sources,
            seen: AtomicU64::new(0),
            results: AtomicU32::new(0),
            dirs: AtomicU64::new(0),
            stop: AtomicBool::new(false),
            reason: Mutex::new(None),
            depth_limited: AtomicBool::new(false),
            now_ns: now_ns(),
        };

        // Level 0. The roots themselves are ACL-gated: a caller with no read
        // on a root never learns anything about it.
        let mut level: Vec<Job> = Vec::new();
        for (idx, path) in starts {
            let src = &sources[*idx];
            if !(sh.acl)(src.share(), path) {
                continue;
            }
            level.push(Job {
                src: *idx,
                path: path.clone(),
                depth: 0,
            });
        }

        let mut pending: Vec<Pending> = Vec::new();

        // ---- BFS, one level at a time -------------------------------------
        while !level.is_empty() && !sh.stopped() {
            let threads = self.threads_for_level(&sh, level.len());
            self.peak_threads.fetch_max(threads, Ordering::Relaxed);

            let (next, mut pend) = if threads == 1 {
                run_level_sequential(&sh, level)
            } else {
                run_level_parallel(&sh, level, threads)
            };
            pending.append(&mut pend);
            level = next;
        }

        // ---- stat phase (§3.4) --------------------------------------------
        if !pending.is_empty() {
            if self.rotational {
                sort_for_stat(&mut pending);
            }
            for p in pending {
                if Instant::now() >= sh.budget.deadline {
                    sh.stop_with(TruncReason::Deadline);
                    break;
                }
                if sh.stopped() {
                    break;
                }
                let src = &sources[p.src];
                let Ok(st) = src.stat(&p.path) else { continue };
                if !m.post_matches(st.size, st.mtime_ns) {
                    continue;
                }
                sh.emit(
                    src.share(),
                    &p.path,
                    &p.name,
                    p.is_dir,
                    Some(st.size),
                    Some(st.mtime_ns),
                );
            }
        }

        let seen = sh.seen.load(Ordering::Relaxed);
        self.dirs_visited
            .store(sh.dirs.load(Ordering::Relaxed), Ordering::Relaxed);
        self.entries_seen.store(seen, Ordering::Relaxed);

        let elapsed = started.elapsed();
        let reason = *sh.reason.lock();
        let depth_limited = sh.depth_limited.load(Ordering::Relaxed);
        match reason {
            Some(reason) => Completeness::Truncated {
                reason,
                seen,
                elapsed,
            },
            None if depth_limited => Completeness::Truncated {
                reason: TruncReason::MaxDepth,
                seen,
                elapsed,
            },
            None => Completeness::Full,
        }
    }

    /// §3.3 ①, plus the "promote mid-walk" behaviour: a walk starts
    /// single-threaded and only escalates once it has seen enough directories
    /// to know the corpus is worth the threads. A small tree finishes on one
    /// thread from start to end.
    fn threads_for_level(&self, sh: &Shared, level_len: usize) -> usize {
        if self.threads <= 1 || level_len < 2 {
            return 1;
        }
        let so_far = sh.dirs.load(Ordering::Relaxed) + level_len as u64;
        if so_far < SMALL_CORPUS_DIRS {
            return 1;
        }
        self.threads.min(level_len)
    }
}

impl Default for Walker {
    fn default() -> Self {
        Self::new(Self::decide_threads(false, None))
    }
}

// ---------------------------------------------------------------------------
// shared walk state
// ---------------------------------------------------------------------------

struct Shared<'a> {
    m: &'a Matcher,
    acl: &'a (dyn Fn(ShareId, &SafePath) -> bool + Sync),
    budget: &'a WalkBudget,
    out: &'a Sender<Hit>,
    sources: &'a [Arc<dyn DirSource>],
    seen: AtomicU64,
    results: AtomicU32,
    dirs: AtomicU64,
    stop: AtomicBool,
    reason: Mutex<Option<TruncReason>>,
    depth_limited: AtomicBool,
    now_ns: i128,
}

impl Shared<'_> {
    fn stopped(&self) -> bool {
        self.stop.load(Ordering::Relaxed)
    }

    /// First reason wins — it is the one that actually cut the walk short.
    fn stop_with(&self, reason: TruncReason) {
        let mut slot = self.reason.lock();
        if slot.is_none() {
            *slot = Some(reason);
        }
        self.stop.store(true, Ordering::Relaxed);
    }

    fn emit(
        &self,
        share: ShareId,
        path: &SafePath,
        name: &str,
        is_dir: bool,
        size: Option<u64>,
        mtime_ns: Option<i128>,
    ) {
        let n = self.results.fetch_add(1, Ordering::Relaxed);
        if n >= self.budget.max_results {
            self.stop_with(TruncReason::MaxResults);
            return;
        }
        let display = display_path(path);
        let folded = fold::fold_str(name);
        let score = rank::score(&RankInput {
            name_folded: &folded,
            needle: self.m.needle(),
            path: &display,
            mtime_ns,
            now_ns: self.now_ns,
            scope: self.m.scope_prefix(),
            bm25: 0.0, // T2 has no content signal, by construction.
        });
        // A closed receiver means the client hung up; stop walking for it.
        if self
            .out
            .send(Hit {
                share,
                path: display,
                name: CompactString::from(name),
                is_dir,
                size,
                mtime_ns,
                score,
            })
            .is_err()
        {
            self.stop.store(true, Ordering::Relaxed);
        }
    }
}

// ---------------------------------------------------------------------------
// per-directory work
// ---------------------------------------------------------------------------

fn scan_dir(sh: &Shared, job: &Job, next: &mut Vec<Job>, pending: &mut Vec<Pending>) {
    if sh.stopped() {
        return;
    }
    if Instant::now() >= sh.budget.deadline {
        sh.stop_with(TruncReason::Deadline);
        return;
    }

    let src = &sh.sources[job.src];
    let dir_seq = sh.dirs.fetch_add(1, Ordering::Relaxed);

    // One readdir pass. No stat, anywhere in this function.
    let Ok(entries) = src.read_entries(&job.path) else {
        // Unreadable directory (races, permissions below our ACL layer). Not
        // an error for the search as a whole.
        return;
    };

    let n = entries.len() as u64;
    let seen = sh.seen.fetch_add(n, Ordering::Relaxed) + n;
    if seen >= sh.budget.max_entries {
        sh.stop_with(TruncReason::MaxEntries);
    }

    for (i, ent) in entries.into_iter().enumerate() {
        // .sctrash / .scpart- / .scmeta / .scindex are ours, not the user's.
        if is_reserved_name(&ent.name) {
            continue;
        }
        let is_dir = ent.kind == Kind::Dir;
        let Some(child) = join_child(&job.path, &ent.name) else {
            continue;
        };

        if is_dir {
            if job.depth + 1 > sh.budget.max_depth {
                sh.depth_limited.store(true, Ordering::Relaxed);
                continue;
            }
            // ACL gates *descent*, and gates reporting the directory itself.
            // We never enter and then filter: entering is the leak, and it is
            // also the cost (§3.2, §7.2).
            if !(sh.acl)(src.share(), &child) {
                continue;
            }
        }

        if sh.m.matches_kind(is_dir) && sh.m.matches_name(ent.name.as_bytes()) {
            if sh.m.needs_stat() {
                pending.push(Pending {
                    src: job.src,
                    dev: src.root_dev(),
                    ino: ent.ino,
                    dir_seq,
                    ent_seq: i as u32,
                    path: child.clone(),
                    name: ent.name.clone(),
                    is_dir,
                });
            } else {
                sh.emit(src.share(), &child, &ent.name, is_dir, None, None);
            }
        }

        if is_dir {
            next.push(Job {
                src: job.src,
                path: child,
                depth: job.depth + 1,
            });
        }
    }
}

// ---------------------------------------------------------------------------
// level execution
// ---------------------------------------------------------------------------

fn run_level_sequential(sh: &Shared, level: Vec<Job>) -> (Vec<Job>, Vec<Pending>) {
    let mut next = Vec::new();
    let mut pending = Vec::new();
    for job in level {
        if sh.stopped() {
            break;
        }
        scan_dir(sh, &job, &mut next, &mut pending);
    }
    (next, pending)
}

/// Work-stealing over the directories of one BFS level.
///
/// The level's jobs go into a shared [`Injector`]; each worker owns a local
/// deque it steals batches into, and workers steal from each other when the
/// injector runs dry. Children are *not* pushed back — they belong to the next
/// level — so a worker that cannot find a task is done, full stop.
fn run_level_parallel(sh: &Shared, level: Vec<Job>, threads: usize) -> (Vec<Job>, Vec<Pending>) {
    let injector: Injector<Job> = Injector::new();
    for job in level {
        injector.push(job);
    }

    let deques: Vec<Deque<Job>> = (0..threads).map(|_| Deque::new_lifo()).collect();
    let stealers: Vec<Stealer<Job>> = deques.iter().map(|d| d.stealer()).collect();

    let next = Mutex::new(Vec::new());
    let pending = Mutex::new(Vec::new());

    std::thread::scope(|scope| {
        for (me, dq) in deques.into_iter().enumerate() {
            let injector = &injector;
            let stealers = &stealers;
            let next = &next;
            let pending = &pending;
            scope.spawn(move || {
                let mut local_next: Vec<Job> = Vec::new();
                let mut local_pending: Vec<Pending> = Vec::new();
                while let Some(job) = find_task(&dq, injector, stealers, me) {
                    scan_dir(sh, &job, &mut local_next, &mut local_pending);
                    if sh.stopped() {
                        break;
                    }
                }
                if !local_next.is_empty() {
                    next.lock().append(&mut local_next);
                }
                if !local_pending.is_empty() {
                    pending.lock().append(&mut local_pending);
                }
            });
        }
    });

    (next.into_inner(), pending.into_inner())
}

fn find_task(
    local: &Deque<Job>,
    injector: &Injector<Job>,
    stealers: &[Stealer<Job>],
    me: usize,
) -> Option<Job> {
    if let Some(job) = local.pop() {
        return Some(job);
    }
    loop {
        let mut retry = false;
        match injector.steal_batch_and_pop(local) {
            Steal::Success(job) => return Some(job),
            Steal::Retry => retry = true,
            Steal::Empty => {}
        }
        for (i, s) in stealers.iter().enumerate() {
            if i == me {
                continue;
            }
            match s.steal() {
                Steal::Success(job) => return Some(job),
                Steal::Retry => retry = true,
                Steal::Empty => {}
            }
        }
        if !retry {
            return None;
        }
        std::hint::spin_loop();
    }
}

fn now_ns() -> i128 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i128)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vfs::SafePath;

    fn pend(dev: u64, ino: Option<u64>, dir: u64, ent: u32) -> Pending {
        Pending {
            src: 0,
            dev,
            ino,
            dir_seq: dir,
            ent_seq: ent,
            path: SafePath::root(),
            name: CompactString::from("x"),
            is_dir: false,
        }
    }

    #[test]
    fn inode_order_sort_when_inodes_are_known() {
        let mut v = vec![
            pend(1, Some(900), 0, 0),
            pend(1, Some(10), 5, 3),
            pend(0, Some(5000), 9, 9),
            pend(1, Some(400), 2, 1),
        ];
        sort_for_stat(&mut v);
        let keys: Vec<(u64, Option<u64>)> = v.iter().map(|p| (p.dev, p.ino)).collect();
        assert_eq!(
            keys,
            vec![
                (0, Some(5000)),
                (1, Some(10)),
                (1, Some(400)),
                (1, Some(900))
            ]
        );
    }

    #[test]
    fn falls_back_to_readdir_order_grouped_by_directory() {
        let mut v = vec![
            pend(0, None, 3, 1),
            pend(0, None, 1, 2),
            pend(0, None, 1, 0),
            pend(0, None, 2, 7),
        ];
        sort_for_stat(&mut v);
        let keys: Vec<(u64, u32)> = v.iter().map(|p| (p.dir_seq, p.ent_seq)).collect();
        assert_eq!(keys, vec![(1, 0), (1, 2), (2, 7), (3, 1)]);
    }

    #[test]
    fn decide_threads_small_corpus_is_single_threaded() {
        assert_eq!(Walker::decide_threads(false, Some(10)), 1);
        assert_eq!(Walker::decide_threads(false, Some(63)), 1);
        assert_eq!(Walker::decide_threads(true, Some(1)), 1);
    }

    #[test]
    fn decide_threads_rotational_is_two() {
        assert_eq!(Walker::decide_threads(true, Some(100_000)), 2);
        assert_eq!(Walker::decide_threads(true, None), 2);
    }

    #[test]
    fn decide_threads_ssd_is_capped_at_sixteen() {
        let n = Walker::decide_threads(false, None);
        assert!((1..=16).contains(&n), "got {n}");
        assert_eq!(Walker::decide_threads(false, Some(1_000_000)), n);
    }
}
