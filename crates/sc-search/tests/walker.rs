//! T2 walker integration tests (§3, §10).
//!
//! Everything here runs against a real `sc_vfs::ShareRoot` over a real
//! temporary directory — the walker is a filesystem component and mocking the
//! filesystem out of it would test nothing.

use std::collections::BTreeSet;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use sc_search::vfs::{DirEntry, DirSource, ShareSource, VfsError};
use sc_search::{
    Completeness, Hit, KindFilter, Matcher, SafePath, ShareId, Stat, TruncReason, WalkBudget,
    Walker,
};
use sc_vfs::{ShareRoot, SharePolicy};

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

struct Fixture {
    _dir: tempfile::TempDir,
    root: Arc<ShareRoot>,
}

impl Fixture {
    fn source(&self) -> Arc<dyn DirSource> {
        Arc::new(ShareSource::new(self.root.clone(), false))
    }
}

/// `dirs × files_per_dir` files, plus one needle file per directory.
fn fixture(dirs: usize, files_per_dir: usize) -> Fixture {
    let dir = tempfile::tempdir().unwrap();
    for d in 0..dirs {
        let sub = dir.path().join(format!("dir{d:04}"));
        std::fs::create_dir_all(&sub).unwrap();
        for f in 0..files_per_dir {
            std::fs::write(sub.join(format!("IMG_{f:04}.jpg")), b"").unwrap();
        }
        std::fs::write(sub.join(format!("needle_{d:04}.txt")), b"x").unwrap();
    }
    let root = Arc::new(
        ShareRoot::open(ShareId(7), dir.path(), SharePolicy::default()).unwrap(),
    );
    Fixture { _dir: dir, root }
}

fn allow_all(_: ShareId, _: &SafePath) -> bool {
    true
}

/// Run a walk and collect every hit.
fn run(
    w: &Walker,
    sources: &[Arc<dyn DirSource>],
    m: &Matcher,
    acl: &(dyn Fn(ShareId, &SafePath) -> bool + Sync),
    budget: &WalkBudget,
) -> (Vec<Hit>, Completeness) {
    let (tx, rx) = crossbeam_channel::unbounded();
    let starts: Vec<(usize, SafePath)> = (0..sources.len())
        .map(|i| (i, SafePath::root()))
        .collect();
    let done = w.walk_sources(sources, &starts, m, acl, budget, &tx);
    drop(tx);
    (rx.into_iter().collect(), done)
}

/// Wraps a source and records every directory it reads, so ACL pruning can be
/// asserted directly rather than inferred from the absence of hits.
struct Counting {
    inner: Arc<dyn DirSource>,
    reads: Mutex<Vec<String>>,
    stats: AtomicUsize,
}

impl Counting {
    fn new(inner: Arc<dyn DirSource>) -> Arc<Self> {
        Arc::new(Self {
            inner,
            reads: Mutex::new(Vec::new()),
            stats: AtomicUsize::new(0),
        })
    }
    fn reads(&self) -> Vec<String> {
        self.reads.lock().unwrap().clone()
    }
    fn stat_calls(&self) -> usize {
        self.stats.load(Ordering::Relaxed)
    }
}

impl DirSource for Counting {
    fn share(&self) -> ShareId {
        self.inner.share()
    }
    fn read_entries(&self, p: &SafePath) -> Result<Vec<DirEntry>, VfsError> {
        self.reads.lock().unwrap().push(p.to_display_string());
        self.inner.read_entries(p)
    }
    fn stat(&self, p: &SafePath) -> Result<Stat, VfsError> {
        self.stats.fetch_add(1, Ordering::Relaxed);
        self.inner.stat(p)
    }
    fn root_dev(&self) -> u64 {
        self.inner.root_dev()
    }
}

// ---------------------------------------------------------------------------
// correctness
// ---------------------------------------------------------------------------

#[test]
fn finds_every_match_in_a_10k_file_corpus() {
    // 100 dirs × (100 jpgs + 1 needle) = 10_100 files, 100 dirs.
    let fx = fixture(100, 100);
    let w = Walker::new(8);
    let sources = vec![fx.source()];
    let budget = WalkBudget::new(Duration::from_secs(60)).max_results(100_000);

    let (hits, done) = run(&w, &sources, &Matcher::new("needle"), &allow_all, &budget);
    assert_eq!(done, Completeness::Full);
    assert_eq!(hits.len(), 100, "one needle per directory");

    let paths: BTreeSet<String> = hits.iter().map(|h| h.path.clone()).collect();
    for d in 0..100 {
        assert!(paths.contains(&format!("dir{d:04}/needle_{d:04}.txt")));
    }
    for h in &hits {
        assert_eq!(h.share, ShareId(7), "share id comes from the ShareRoot");
        assert!(!h.is_dir);
        // Name-only matching performs no stat, so there is nothing to report.
        assert!(h.size.is_none() && h.mtime_ns.is_none());
    }
    assert_eq!(w.dirs_visited(), 101, "root + 100 subdirectories");
    assert_eq!(w.entries_seen(), 100 + 100 * 101);
}

#[test]
fn matches_directories_and_respects_the_kind_filter() {
    let fx = fixture(4, 2);
    let w = Walker::new(1);
    let sources = vec![fx.source()];
    let budget = WalkBudget::new(Duration::from_secs(30));

    let (dirs, _) = run(
        &w,
        &sources,
        &Matcher::new("dir").kinds(KindFilter::DIRS_ONLY),
        &allow_all,
        &budget,
    );
    assert_eq!(dirs.len(), 4);
    assert!(dirs.iter().all(|h| h.is_dir));

    let (files, _) = run(
        &w,
        &sources,
        &Matcher::new("dir").kinds(KindFilter::FILES_ONLY),
        &allow_all,
        &budget,
    );
    assert!(files.is_empty());
}

#[test]
fn cjk_substring_matches_on_the_walk_path() {
    // §10's "CJK partial match" row, T2 half.
    let dir = tempfile::tempdir().unwrap();
    std::fs::write(dir.path().join("여름휴가사진.jpg"), b"").unwrap();
    std::fs::write(dir.path().join("겨울사진.jpg"), b"").unwrap();
    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let sources: Vec<Arc<dyn DirSource>> = vec![Arc::new(ShareSource::new(root, false))];
    let w = Walker::new(1);
    let budget = WalkBudget::new(Duration::from_secs(30));

    for (q, n) in [("휴가", 1), ("여름", 1), ("사진", 2)] {
        let (hits, _) = run(&w, &sources, &Matcher::new(q), &allow_all, &budget);
        assert_eq!(hits.len(), n, "query {q}");
    }
}

#[test]
fn reserved_names_are_never_reported_or_descended_into() {
    let dir = tempfile::tempdir().unwrap();
    std::fs::create_dir_all(dir.path().join(".sctrash/deep")).unwrap();
    std::fs::write(dir.path().join(".sctrash/deep/target.txt"), b"").unwrap();
    std::fs::write(dir.path().join(".scpart-1234"), b"").unwrap();
    std::fs::create_dir_all(dir.path().join(".scindex/names")).unwrap();
    std::fs::write(dir.path().join("target.txt"), b"").unwrap();

    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let counting = Counting::new(Arc::new(ShareSource::new(root, false)));
    let sources: Vec<Arc<dyn DirSource>> = vec![counting.clone()];
    let w = Walker::new(1);

    let (hits, done) = run(
        &w,
        &sources,
        &Matcher::new(""),
        &allow_all,
        &WalkBudget::new(Duration::from_secs(30)),
    );
    assert_eq!(done, Completeness::Full);
    let names: Vec<&str> = hits.iter().map(|h| h.name.as_str()).collect();
    assert_eq!(names, vec!["target.txt"]);
    assert_eq!(
        counting.reads(),
        vec![""],
        "no reserved subtree may be enumerated"
    );
}

// ---------------------------------------------------------------------------
// budgets
// ---------------------------------------------------------------------------

#[test]
fn max_entries_truncation_reports_the_real_count() {
    let fx = fixture(60, 60);
    let sources = vec![fx.source()];
    let w = Walker::new(1);

    let full = WalkBudget::new(Duration::from_secs(60)).max_results(1_000_000);
    let (_, done) = run(&w, &sources, &Matcher::new(""), &allow_all, &full);
    assert_eq!(done, Completeness::Full);
    let total_entries = w.entries_seen();
    assert_eq!(total_entries, 60 + 60 * 61);

    let capped = WalkBudget::new(Duration::from_secs(60))
        .max_results(1_000_000)
        .max_entries(500);
    let (_, done) = run(&w, &sources, &Matcher::new(""), &allow_all, &capped);
    match done {
        Completeness::Truncated {
            reason,
            seen,
            elapsed,
        } => {
            assert_eq!(reason, TruncReason::MaxEntries);
            // `seen` is the number actually enumerated, reported honestly: at
            // least the cap (we stop on the directory that crosses it) and
            // never more than the corpus holds.
            assert!(seen >= 500, "seen {seen}");
            assert!(seen < total_entries, "seen {seen} of {total_entries}");
            assert_eq!(seen, w.entries_seen(), "reported seen must be the counter");
            assert!(elapsed < Duration::from_secs(60));
        }
        other => panic!("expected truncation, got {other:?}"),
    }
}

#[test]
fn deadline_truncation_is_reported() {
    let fx = fixture(80, 40);
    let sources = vec![fx.source()];
    let w = Walker::new(4);
    let mut budget = WalkBudget::new(Duration::from_secs(60)).max_results(1_000_000);
    budget.deadline = Instant::now();

    let (_, done) = run(&w, &sources, &Matcher::new(""), &allow_all, &budget);
    match done {
        Completeness::Truncated { reason, seen, .. } => {
            assert_eq!(reason, TruncReason::Deadline);
            assert_eq!(seen, w.entries_seen());
        }
        other => panic!("expected deadline truncation, got {other:?}"),
    }
}

#[test]
fn max_results_truncation_delivers_exactly_the_cap() {
    let fx = fixture(20, 50);
    let sources = vec![fx.source()];
    let w = Walker::new(1);
    let budget = WalkBudget::new(Duration::from_secs(60)).max_results(37);

    let (hits, done) = run(&w, &sources, &Matcher::new("img"), &allow_all, &budget);
    assert_eq!(hits.len(), 37);
    match done {
        Completeness::Truncated { reason, .. } => assert_eq!(reason, TruncReason::MaxResults),
        other => panic!("expected result-cap truncation, got {other:?}"),
    }
}

#[test]
fn bfs_completes_the_shallow_levels_before_going_deeper() {
    // A wide-and-deep tree: 40 top-level directories, each 6 levels deep.
    // With a tight entry budget a DFS would burn everything on one branch;
    // BFS must have visited every top-level directory instead.
    let dir = tempfile::tempdir().unwrap();
    for t in 0..40 {
        let mut p = dir.path().join(format!("top{t:02}"));
        std::fs::create_dir_all(&p).unwrap();
        std::fs::write(p.join("marker.txt"), b"").unwrap();
        for d in 0..6 {
            p = p.join(format!("level{d}"));
            std::fs::create_dir_all(&p).unwrap();
            for f in 0..20 {
                std::fs::write(p.join(format!("f{f:03}.dat")), b"").unwrap();
            }
        }
    }
    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let sources: Vec<Arc<dyn DirSource>> = vec![Arc::new(ShareSource::new(root, false))];
    let w = Walker::new(1);
    let budget = WalkBudget::new(Duration::from_secs(60))
        .max_results(1_000_000)
        .max_entries(300);

    let (hits, done) = run(&w, &sources, &Matcher::new("marker"), &allow_all, &budget);
    assert!(matches!(done, Completeness::Truncated { .. }));
    assert_eq!(
        hits.len(),
        40,
        "every top-level marker must be found before any depth-2 work"
    );
}

#[test]
fn max_depth_prunes_and_is_reported() {
    let dir = tempfile::tempdir().unwrap();
    let deep = dir.path().join("a/b/c/d");
    std::fs::create_dir_all(&deep).unwrap();
    std::fs::write(deep.join("deep.txt"), b"").unwrap();
    std::fs::write(dir.path().join("a/shallow.txt"), b"").unwrap();

    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let sources: Vec<Arc<dyn DirSource>> = vec![Arc::new(ShareSource::new(root, false))];
    let w = Walker::new(1);
    let budget = WalkBudget::new(Duration::from_secs(30)).max_depth(1);

    let (hits, done) = run(&w, &sources, &Matcher::new(".txt"), &allow_all, &budget);
    let names: Vec<&str> = hits.iter().map(|h| h.name.as_str()).collect();
    assert_eq!(names, vec!["shallow.txt"]);
    assert!(matches!(
        done,
        Completeness::Truncated {
            reason: TruncReason::MaxDepth,
            ..
        }
    ));
}

// ---------------------------------------------------------------------------
// ACL pruning
// ---------------------------------------------------------------------------

#[test]
fn acl_rejection_prevents_the_subtree_from_ever_being_read() {
    let dir = tempfile::tempdir().unwrap();
    for top in ["public", "secret"] {
        for d in 0..10 {
            let sub = dir.path().join(top).join(format!("sub{d:02}"));
            std::fs::create_dir_all(&sub).unwrap();
            for f in 0..5 {
                std::fs::write(sub.join(format!("target{f}.txt")), b"").unwrap();
            }
        }
    }
    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let counting = Counting::new(Arc::new(ShareSource::new(root, false)));
    let sources: Vec<Arc<dyn DirSource>> = vec![counting.clone()];
    let w = Walker::new(4);

    let deny_secret = |_: ShareId, p: &SafePath| {
        p.components().first().map(|c| c.as_str()) != Some("secret")
    };
    let (hits, done) = run(
        &w,
        &sources,
        &Matcher::new("target"),
        &deny_secret,
        &WalkBudget::new(Duration::from_secs(60)).max_results(1_000_000),
    );
    assert_eq!(done, Completeness::Full);

    // No results leak...
    assert_eq!(hits.len(), 50);
    assert!(hits.iter().all(|h| h.path.starts_with("public/")));

    // ...and, more importantly, the subtree was never enumerated at all. This
    // is the property §7.3 relies on: there is no timing channel because there
    // is no work proportional to the denied data.
    let reads = counting.reads();
    assert!(
        reads.iter().all(|p| !p.starts_with("secret")),
        "walker read inside a denied subtree: {reads:?}"
    );
    // root + public + 10 subdirs of public = 12. The `secret` directory itself
    // is never opened either.
    assert_eq!(reads.len(), 12, "{reads:?}");
    assert_eq!(w.dirs_visited(), 12);
}

#[test]
fn a_denied_root_yields_nothing_and_reads_nothing() {
    let fx = fixture(5, 5);
    let counting = Counting::new(fx.source());
    let sources: Vec<Arc<dyn DirSource>> = vec![counting.clone()];
    let w = Walker::new(1);
    let deny_all = |_: ShareId, _: &SafePath| false;

    let (hits, done) = run(
        &w,
        &sources,
        &Matcher::new(""),
        &deny_all,
        &WalkBudget::new(Duration::from_secs(30)),
    );
    assert!(hits.is_empty());
    assert_eq!(done, Completeness::Full);
    assert!(counting.reads().is_empty());
    assert_eq!(w.dirs_visited(), 0);
}

// ---------------------------------------------------------------------------
// parallelism policy
// ---------------------------------------------------------------------------

#[test]
fn small_corpus_stays_on_one_thread() {
    // fd#1614: parallelising a tiny corpus made it 8.95× *slower* than GNU
    // find. Even asked for 8 threads, a sub-64-directory walk must not spawn
    // any.
    let fx = fixture(10, 20);
    let sources = vec![fx.source()];
    let w = Walker::new(8);
    let (hits, done) = run(
        &w,
        &sources,
        &Matcher::new("needle"),
        &allow_all,
        &WalkBudget::new(Duration::from_secs(30)),
    );
    assert_eq!(done, Completeness::Full);
    assert_eq!(hits.len(), 10);
    assert_eq!(w.dirs_visited(), 11);
    assert_eq!(w.peak_threads(), 1, "small corpus must not spawn threads");
}

#[test]
fn large_corpus_is_promoted_to_the_thread_pool() {
    let fx = fixture(200, 5);
    let sources = vec![fx.source()];
    let w = Walker::new(4);
    let (hits, done) = run(
        &w,
        &sources,
        &Matcher::new("needle"),
        &allow_all,
        &WalkBudget::new(Duration::from_secs(60)).max_results(1_000_000),
    );
    assert_eq!(done, Completeness::Full);
    assert_eq!(hits.len(), 200);
    assert!(
        w.peak_threads() > 1,
        "a 201-directory corpus should promote past the fast path"
    );
}

#[test]
fn a_single_thread_walker_and_a_pool_agree() {
    let fx = fixture(120, 8);
    let sources = vec![fx.source()];
    let budget = WalkBudget::new(Duration::from_secs(60)).max_results(1_000_000);

    let seq = Walker::new(1);
    let (mut a, _) = run(&seq, &sources, &Matcher::new("img"), &allow_all, &budget);
    let par = Walker::new(8);
    let (mut b, _) = run(&par, &sources, &Matcher::new("img"), &allow_all, &budget);

    assert!(par.peak_threads() > 1);
    a.sort_by(|x, y| x.path.cmp(&y.path));
    b.sort_by(|x, y| x.path.cmp(&y.path));
    let pa: Vec<&str> = a.iter().map(|h| h.path.as_str()).collect();
    let pb: Vec<&str> = b.iter().map(|h| h.path.as_str()).collect();
    assert_eq!(pa, pb);
    assert_eq!(a.len(), 120 * 8);
}

// ---------------------------------------------------------------------------
// stat phase
// ---------------------------------------------------------------------------

#[test]
fn name_only_matching_performs_zero_stats() {
    // §1.2 point 2 / §3.2: this is the single largest optimisation available,
    // so it gets an assertion rather than a comment.
    let fx = fixture(10, 20);
    let counting = Counting::new(fx.source());
    let sources: Vec<Arc<dyn DirSource>> = vec![counting.clone()];
    let w = Walker::new(1);
    let (hits, _) = run(
        &w,
        &sources,
        &Matcher::new("img"),
        &allow_all,
        &WalkBudget::new(Duration::from_secs(30)).max_results(100_000),
    );
    assert_eq!(hits.len(), 200);
    assert_eq!(counting.stat_calls(), 0);
}

#[test]
fn size_filter_stats_only_the_entries_that_already_matched() {
    let dir = tempfile::tempdir().unwrap();
    for i in 0..50 {
        std::fs::write(dir.path().join(format!("filler{i:02}.bin")), vec![0u8; 999]).unwrap();
    }
    std::fs::write(dir.path().join("target_small.txt"), b"abc").unwrap();
    std::fs::write(dir.path().join("target_big.txt"), vec![0u8; 4096]).unwrap();

    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let counting = Counting::new(Arc::new(ShareSource::new(root, false)));
    let sources: Vec<Arc<dyn DirSource>> = vec![counting.clone()];
    let w = Walker::new(1);

    let m = Matcher::new("target").size_range(0, 100);
    assert!(m.needs_stat());
    let (hits, done) = run(
        &w,
        &sources,
        &m,
        &allow_all,
        &WalkBudget::new(Duration::from_secs(30)),
    );
    assert_eq!(done, Completeness::Full);
    assert_eq!(hits.len(), 1);
    assert_eq!(hits[0].name.as_str(), "target_small.txt");
    assert_eq!(hits[0].size, Some(3));
    assert!(hits[0].mtime_ns.is_some());
    // Two names matched; 50 fillers did not. Only the matches were stat'ed.
    assert_eq!(counting.stat_calls(), 2);
}

#[test]
fn rotational_walks_sort_the_stat_phase() {
    // The ordering itself is unit-tested in `walker::tests`; here we only
    // check that a rotational walker still produces the right answers.
    let dir = tempfile::tempdir().unwrap();
    for i in 0..30 {
        std::fs::write(dir.path().join(format!("target{i:02}.bin")), vec![0u8; i]).unwrap();
    }
    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let sources: Vec<Arc<dyn DirSource>> = vec![Arc::new(ShareSource::new(root, true))];
    let w = Walker::new(2).with_rotational(true);
    let (hits, _) = run(
        &w,
        &sources,
        &Matcher::new("target").size_range(10, 19),
        &allow_all,
        &WalkBudget::new(Duration::from_secs(30)),
    );
    assert_eq!(hits.len(), 10);
    assert!(hits.iter().all(|h| (10..=19).contains(&h.size.unwrap())));
}

// ---------------------------------------------------------------------------
// odd names
// ---------------------------------------------------------------------------

#[test]
fn unusual_but_valid_names_are_matchable() {
    // Windows filenames are UTF-16 and cannot hold arbitrary byte sequences,
    // so the portable case is "unusual Unicode" rather than "invalid UTF-8".
    let dir = tempfile::tempdir().unwrap();
    let names = ["café_日本語.txt", "ünïcödé ✓.dat", "emoji_🚀_file.bin"];
    for n in names {
        std::fs::write(dir.path().join(n), b"").unwrap();
    }
    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let sources: Vec<Arc<dyn DirSource>> = vec![Arc::new(ShareSource::new(root, false))];
    let w = Walker::new(1);
    let budget = WalkBudget::new(Duration::from_secs(30));

    for (q, want) in [("日本語", 1), ("✓", 1), ("🚀", 1), ("CAFÉ", 1)] {
        let (hits, _) = run(&w, &sources, &Matcher::new(q), &allow_all, &budget);
        assert_eq!(hits.len(), want, "query {q:?}");
    }
}

/// Filenames on Linux are byte strings: only NUL and `/` are forbidden, so a
/// name need not be valid UTF-8 at all. Such a file must still be findable,
/// which is why matching happens on bytes (§4.2, "filenames are byte strings").
#[cfg(unix)]
#[test]
fn genuinely_invalid_utf8_names_are_still_walked() {
    use std::ffi::OsStr;
    use std::os::unix::ffi::OsStrExt;

    let dir = tempfile::tempdir().unwrap();
    let raw = b"broken_\xff\xfe_name.bin";
    std::fs::write(dir.path().join(OsStr::from_bytes(raw)), b"").unwrap();

    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let sources: Vec<Arc<dyn DirSource>> = vec![Arc::new(ShareSource::new(root, false))];
    let w = Walker::new(1);
    let (hits, _) = run(
        &w,
        &sources,
        &Matcher::new("broken"),
        &allow_all,
        &WalkBudget::new(Duration::from_secs(30)),
    );
    assert_eq!(hits.len(), 1, "a non-UTF-8 name must not be skipped");
}

// ---------------------------------------------------------------------------
// misc
// ---------------------------------------------------------------------------

#[test]
fn the_share_root_entry_point_works_end_to_end() {
    // The contracted `walk(&[(Arc<ShareRoot>, SafePath)], ..)` signature,
    // exercised against real shares rather than through `walk_sources`.
    let a = fixture(4, 6);
    let b = fixture(4, 6);
    let roots = vec![
        (a.root.clone(), SafePath::root()),
        (b.root.clone(), SafePath::parse("dir0001", 64).unwrap()),
    ];
    let (tx, rx) = crossbeam_channel::unbounded();
    let w = Walker::new(Walker::decide_threads(false, Some(9)));
    assert_eq!(w.threads(), 1, "9 directories is a small corpus");

    let done = w.walk(
        &roots,
        &Matcher::new("needle"),
        &allow_all,
        &WalkBudget::for_storage(false).max_results(1_000_000),
        &tx,
    );
    drop(tx);
    let hits: Vec<Hit> = rx.into_iter().collect();
    assert_eq!(done, Completeness::Full);
    // 4 needles from share a's whole tree, 1 from share b's single subdirectory.
    assert_eq!(hits.len(), 5);
    assert!(hits.iter().all(|h| h.share == ShareId(7)));
    assert_eq!(
        hits.iter().filter(|h| h.path == "dir0001/needle_0001.txt").count(),
        2,
        "the same relative path appears once per root"
    );
}

#[test]
fn multiple_roots_are_walked_together() {
    let a = fixture(3, 4);
    let b = fixture(3, 4);
    let sources = vec![a.source(), b.source()];
    let w = Walker::new(1);
    let (hits, done) = run(
        &w,
        &sources,
        &Matcher::new("needle"),
        &allow_all,
        &WalkBudget::new(Duration::from_secs(30)),
    );
    assert_eq!(done, Completeness::Full);
    assert_eq!(hits.len(), 6);
}

#[test]
fn ranking_puts_the_exact_name_first() {
    let dir = tempfile::tempdir().unwrap();
    for n in ["report", "report_final_v2", "quarterly_report_draft"] {
        std::fs::write(dir.path().join(n), b"").unwrap();
    }
    let root = Arc::new(ShareRoot::open(ShareId(1), dir.path(), SharePolicy::default()).unwrap());
    let sources: Vec<Arc<dyn DirSource>> = vec![Arc::new(ShareSource::new(root, false))];
    let w = Walker::new(1);
    let (mut hits, _) = run(
        &w,
        &sources,
        &Matcher::new("report"),
        &allow_all,
        &WalkBudget::new(Duration::from_secs(30)),
    );
    assert_eq!(hits.len(), 3);
    hits.sort_by(|a, b| b.score.partial_cmp(&a.score).unwrap());
    assert_eq!(hits[0].name.as_str(), "report");
    assert_eq!(hits[1].name.as_str(), "report_final_v2");
    assert_eq!(hits[2].name.as_str(), "quarterly_report_draft");
}

#[test]
fn a_hung_up_receiver_stops_the_walk() {
    let fx = fixture(100, 20);
    let sources = vec![fx.source()];
    let w = Walker::new(2);
    let (tx, rx) = crossbeam_channel::unbounded::<Hit>();
    drop(rx);
    let starts = vec![(0usize, SafePath::root())];
    let done = w.walk_sources(
        &sources,
        &starts,
        &Matcher::new(""),
        &allow_all,
        &WalkBudget::new(Duration::from_secs(60)).max_results(1_000_000),
        &tx,
    );
    // Whatever it reports, it must return promptly rather than walking the
    // whole tree for a client that left.
    let _ = done;
    assert!(w.dirs_visited() < 101);
}
