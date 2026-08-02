use std::sync::Arc;
use std::time::Duration;

use sc_acl::{AclEngine, Grant, Perms, Principal};
use sc_core::{Core, ShareDef};
use sc_meta::MetaStore;
use sc_vfs::{SafePath, ShareId, SharePolicy, UserId};

use crate::{WatchConfig, Watcher};

const USER: UserId = UserId::new(1);
const SHARE: ShareId = ShareId::new(1);

fn setup() -> (Arc<Core>, Arc<MetaStore>, tempfile::TempDir) {
    let dir = tempfile::tempdir().unwrap();
    let meta = Arc::new(MetaStore::open_in_memory().unwrap());
    let acl = Arc::new(AclEngine::new());
    acl.replace_grants(vec![Grant {
        id: 1,
        principal: Principal::User(USER),
        share: SHARE,
        subpath: SafePath::root(),
        allow: Perms::all(),
        deny: Perms::empty(),
        inherit: true,
        label: Some("root".to_string()),
    }]);
    let core = Core::new(meta.clone(), acl);
    core.register_share(ShareDef {
        id: SHARE,
        name: "root".to_string(),
        host_path: dir.path().to_path_buf(),
        policy: SharePolicy::default(),
        shared_externally: false,
    })
    .unwrap();
    (Arc::new(core), meta, dir)
}

#[test]
fn subscribe_then_external_change_delivers_inval_event() {
    let (core, _meta, dir) = setup();
    core.mkdir(USER, "/root/watched").unwrap();

    let (tx, rx) = crossbeam_channel::unbounded();
    let watcher = Watcher::start(WatchConfig::default(), core.clone(), tx).unwrap();

    let watched_path = SafePath::parse("watched", 64).unwrap();
    watcher.subscribe(SHARE, &watched_path).unwrap();

    // External modification: written directly through the host filesystem,
    // bypassing `Core` entirely, exactly like Jellyfin/rsync/Samba would.
    std::fs::write(dir.path().join("watched").join("external.txt"), b"hello").unwrap();

    // Generous on purpose. What is under test is *that* an external write
    // produces an invalidation, not how fast — the event cannot arrive before
    // the 200 ms debounce elapses anyway, and on a loaded machine the watcher
    // thread may not be scheduled promptly after that. A 1 s bound failed in
    // the Rocky VM under a full parallel `cargo test --workspace` while
    // passing alone, which says nothing about the product.
    let event = rx.recv_timeout(Duration::from_secs(10)).expect("expected an InvalEvent");
    assert_eq!(event.share, SHARE);
    assert_eq!(event.path, "watched");
}

#[test]
fn debounce_coalesces_a_burst_into_one_event() {
    let (core, _meta, dir) = setup();
    core.mkdir(USER, "/root/burst").unwrap();

    let (tx, rx) = crossbeam_channel::unbounded();
    let watcher = Watcher::start(WatchConfig::default(), core.clone(), tx).unwrap();
    let watched_path = SafePath::parse("burst", 64).unwrap();
    // `touch` (unlike `subscribe`) registers only this one directory, not
    // its ancestor chain — isolates the test to a single watched path so a
    // second, legitimate event for the parent can't be mistaken for a
    // debounce failure.
    watcher.touch(SHARE, &watched_path);

    let target = dir.path().join("burst");
    let started = std::time::Instant::now();
    for i in 0..20 {
        std::fs::write(target.join(format!("f{i}.txt")), b"x").unwrap();
    }
    let burst_took = started.elapsed();

    let first = rx.recv_timeout(Duration::from_secs(10)).expect("expected at least one InvalEvent");
    assert_eq!(first.path, "burst");

    // Drain whatever else the debounce decided to emit.
    let mut events = 1;
    while rx.recv_timeout(crate::backend::DEBOUNCE * 3).is_ok() {
        events += 1;
    }

    // Twenty writes must never produce twenty events — that is the whole point
    // of the debounce, and it holds however slow the machine is.
    assert!(events < 20 / 2, "burst barely coalesced: {events} events from 20 writes");

    // The stronger claim — *exactly* one — only follows if the burst actually
    // fit inside a single window. It does on any unloaded machine, and did not
    // in the Rocky VM under a full parallel test run, where the writes spread
    // past 200 ms and a second window fired. That is the debounce working, not
    // failing, so asserting it unconditionally would be testing the scheduler.
    if burst_took < crate::backend::DEBOUNCE {
        assert_eq!(
            events, 1,
            "burst fit in one {:?} window but produced {events} events",
            crate::backend::DEBOUNCE
        );
    }
}

#[test]
fn hot_set_eviction_keeps_registrations_at_or_under_cap() {
    let (core, _meta, _dir) = setup();
    for i in 0..20 {
        core.mkdir(USER, &format!("/root/d{i}")).unwrap();
    }

    let (tx, _rx) = crossbeam_channel::unbounded();
    let cfg = WatchConfig { hot_set_max: 5, ..WatchConfig::default() };
    let watcher = Watcher::start(cfg, core.clone(), tx).unwrap();

    for i in 0..20 {
        let p = SafePath::parse(&format!("d{i}"), 64).unwrap();
        watcher.touch(SHARE, &p);
        assert!(watcher.stats().registered <= 5, "registered watch count exceeded hot_set_max");
    }
    assert!(watcher.stats().registered <= 5);
}

#[test]
fn raw_inotify_backend_delivers_inval_event_too() {
    // `WatchBackend::InotifyFull` only has a distinct implementation on
    // Linux (`backend::linux::LinuxInotifyBackend`, the transport whose
    // reader loop also contains the `EventMask::Q_OVERFLOW` check) —
    // everywhere else `new_backend` silently falls back to the same
    // portable backend the first test above already exercises. Kept
    // portable rather than `#[cfg(target_os = "linux")]`-gated since
    // re-running it elsewhere costs nothing.
    let (core, _meta, dir) = setup();
    core.mkdir(USER, "/root/watched2").unwrap();

    let (tx, rx) = crossbeam_channel::unbounded();
    let cfg = WatchConfig { backend: crate::WatchBackend::InotifyFull, ..WatchConfig::default() };
    let watcher = Watcher::start(cfg, core.clone(), tx).unwrap();
    let watched_path = SafePath::parse("watched2", 64).unwrap();
    watcher.subscribe(SHARE, &watched_path).unwrap();

    std::fs::write(dir.path().join("watched2").join("external.txt"), b"hello").unwrap();

    let event = rx.recv_timeout(Duration::from_secs(10)).expect("expected an InvalEvent from the raw inotify backend");
    assert_eq!(event.share, SHARE);
    assert_eq!(event.path, "watched2");
}

#[test]
fn pending_queue_over_full_threshold_falls_back_to_full_invalidation() {
    let (core, meta, dir) = setup();
    core.mkdir(USER, "/root/a").unwrap();
    core.mkdir(USER, "/root/b").unwrap();
    let gen_before = meta.share_gen(SHARE).unwrap();

    let (tx, rx) = crossbeam_channel::unbounded();
    let cfg = WatchConfig { full_threshold: 1, ..WatchConfig::default() };
    let watcher = Watcher::start(cfg, core.clone(), tx).unwrap();
    watcher.touch(SHARE, &SafePath::parse("a", 64).unwrap());
    watcher.touch(SHARE, &SafePath::parse("b", 64).unwrap());

    // Two directories dirty at once already exceeds `full_threshold: 1` —
    // the debounce loop must see that before its normal 200 ms window would
    // otherwise flush "a" and "b" individually, and fall back to bumping
    // the share generation instead (`FEATURES.md` #130).
    std::fs::write(dir.path().join("a").join("x.txt"), b"x").unwrap();
    std::fs::write(dir.path().join("b").join("y.txt"), b"y").unwrap();

    let event = rx.recv_timeout(Duration::from_secs(10)).expect("expected a full-invalidation InvalEvent");
    assert_eq!(event.share, SHARE);
    assert_eq!(event.path, "", "full invalidation must carry no single path");

    let gen_after = meta.share_gen(SHARE).unwrap();
    assert!(gen_after > gen_before, "share generation must bump on full invalidation");
}

#[test]
fn kernel_overflow_flag_falls_back_to_full_invalidation() {
    let (core, meta, _dir) = setup();
    core.mkdir(USER, "/root/watched").unwrap();
    let gen_before = meta.share_gen(SHARE).unwrap();

    let (tx, rx) = crossbeam_channel::unbounded();
    let watcher = Watcher::start(WatchConfig::default(), core.clone(), tx).unwrap();
    watcher.touch(SHARE, &SafePath::parse("watched", 64).unwrap());

    // Simulates the OS itself reporting a lost event batch
    // (`IN_Q_OVERFLOW`/`notify::Flag::Rescan`) rather than actually
    // overflowing a real kernel queue, which needs tens of thousands of
    // events in one burst to reproduce reliably in a unit test.
    watcher.inner.overflow.store(true, std::sync::atomic::Ordering::Relaxed);

    let event = rx.recv_timeout(Duration::from_secs(10)).expect("expected a full-invalidation InvalEvent");
    assert_eq!(event.share, SHARE);
    assert_eq!(event.path, "");

    let gen_after = meta.share_gen(SHARE).unwrap();
    assert!(gen_after > gen_before, "share generation must bump when the kernel reports an overflow");
}

#[test]
fn periodic_rescan_notices_a_change_made_without_any_os_event() {
    let (core, _meta, _dir) = setup();
    core.mkdir(USER, "/root/nfsish").unwrap();

    let (tx, rx) = crossbeam_channel::unbounded();
    let watcher = Watcher::start(WatchConfig::default(), core.clone(), tx).unwrap();
    watcher.touch(SHARE, &SafePath::parse("nfsish", 64).unwrap());

    // No filesystem write at all: a periodic rescan (`FEATURES.md` #129) is
    // the only thing that can ever notice a change an NFS/FUSE peer made
    // behind this host's back, since no local inotify event ever fires for
    // it. `rescan_one_share` is the sweep itself (the part that actually
    // notices and re-marks dirty); called directly here rather than waiting
    // out the real `RESCAN_INTERVAL` or faking a `statfs` NFS/FUSE magic
    // number just to pass the `watch_unreliable` gate, which is a one-line
    // filter this test isn't exercising.
    crate::rescan_one_share(&watcher.inner, SHARE);

    let event = rx.recv_timeout(Duration::from_secs(10)).expect("rescan sweep must still produce an InvalEvent");
    assert_eq!(event.share, SHARE);
    assert_eq!(event.path, "nfsish");
}
