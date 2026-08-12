//! The other half of the store port's driver measurement: the same workload
//! against this implementation, so the port has a number to be compared with
//! rather than an impression.
//!
//! Skipped unless `SC_MEASURE_ROWS` names a row count, because it is minutes
//! of work and not a unit test.
//!
//!   SC_MEASURE_ROWS=2000000 cargo test -p sc-meta --test measure -- --nocapture

use std::time::{Duration, Instant};

use sc_meta::MetaStore;
use sc_vfs::{FileId, Kind, ShareId, Stat};

/// How many directories sit above a file, and so how many aggregates one
/// write invalidates.
const CHAIN_DEPTH: usize = 8;

const SHARE: ShareId = ShareId::new(7);
const DEV: u64 = 0x40_0001;

fn stat(ino: u64, btime: i128, dir: bool) -> Stat {
    Stat {
        dev: DEV,
        ino,
        btime_ns: Some(btime),
        mtime_ns: btime,
        ctime_ns: None,
        size: if dir { 4096 } else { ino },
        mode: 0o644,
        uid: 0,
        gid: 0,
        nlink: 1,
        kind: if dir { Kind::Dir } else { Kind::File },
    }
}

fn report(what: &str, n: usize, elapsed: Duration) {
    println!(
        "MEASURE {what:<42} {n:>9} in {:>8.2}s = {:>9.0}/s",
        elapsed.as_secs_f64(),
        n as f64 / elapsed.as_secs_f64()
    );
}

#[test]
fn driver_measurement() {
    let Ok(raw) = std::env::var("SC_MEASURE_ROWS") else {
        println!("set SC_MEASURE_ROWS to a row count to run the driver measurement");
        return;
    };
    let rows: usize = raw.parse().expect("SC_MEASURE_ROWS is not a row count");

    let dir = tempfile::tempdir().expect("temp dir");
    let store = MetaStore::open(&dir.path().join("meta.db")).expect("open");

    // The directories above the files, root first.
    let mut chain = Vec::with_capacity(CHAIN_DEPTH);
    let mut parent = FileId::new(0);
    for i in 0..CHAIN_DEPTH {
        let id = store
            .fileid(SHARE, parent, &format!("d{i}"), &stat(i as u64 + 1, i as i128, true), true)
            .expect("directory");
        chain.push(id);
        parent = id;
    }

    // The cold walk. One transaction per file, which is what this
    // implementation does: allocation takes a pooled connection and commits on
    // its own.
    let start = Instant::now();
    for i in 0..rows {
        let ino = (CHAIN_DEPTH + 1 + i) as u64;
        store
            .fileid(SHARE, parent, &format!("f{i}"), &stat(ino, i as i128, false), false)
            .expect("file");
    }
    report("cold populate, one transaction per file", rows, start.elapsed());

    // Steady state: a change arrives, the ancestors are invalidated and the
    // directory's own aggregate is stored again.
    let events = (rows / 10).min(200_000);
    let start = Instant::now();
    for i in 0..events {
        store.mark_dirty_chain(SHARE, &chain).expect("invalidate");
        store
            .put_dir_etag(
                SHARE,
                *chain.last().expect("chain"),
                &sc_meta::Aggregate { etag: format!("e{i}"), rsize: 1, rcount: 1 },
                0,
            )
            .expect("aggregate");
    }
    report("steady-state invalidation", events, start.elapsed());
}
