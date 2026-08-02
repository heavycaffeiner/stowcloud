//! T3 name index integration tests (§4, §10).

use sc_search::index::FallbackReason;
use sc_search::{IndexBuilder, NameIndex, ShareId};

fn e(share: u32, p: &str) -> (ShareId, String) {
    (ShareId(share), p.to_string())
}

/// A photo-library-shaped corpus: deep, repetitive, prefix-sharing. This is
/// the shape §4.1 says block compression is strongest on.
fn photo_corpus(n: u32) -> Vec<(ShareId, String)> {
    (0..n)
        .map(|i| {
            e(
                1,
                &format!("photos/2026/trip{:03}/IMG_{i:05}.jpg", i / 100),
            )
        })
        .collect()
}

fn paths(r: &sc_search::QueryResult) -> Vec<String> {
    let mut v: Vec<String> = r.hits.iter().map(|h| h.path.clone()).collect();
    v.sort();
    v
}

// ---------------------------------------------------------------------------
// build → query
// ---------------------------------------------------------------------------

#[test]
fn round_trip_build_then_query() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(5000))
        .unwrap();

    let st = idx.stats();
    assert_eq!(st.entries, 5000);
    assert_eq!(st.blocks, 5000 / 32 + 1);
    assert!(st.trigrams > 0);

    let r = idx.query(b"IMG_04242", 100).unwrap();
    assert!(r.fallback.is_none());
    assert_eq!(paths(&r), vec!["photos/2026/trip042/IMG_04242.jpg"]);

    // Directory components are indexed too — the whole path is stored.
    let r = idx.query(b"trip017", 10_000).unwrap();
    assert_eq!(r.hits.len(), 100);

    // A query that matches nothing is an empty answer, not a fallback.
    let r = idx.query(b"no_such_thing_at_all", 10).unwrap();
    assert!(r.hits.is_empty());
    assert!(r.fallback.is_none());
}

#[test]
fn matching_is_case_and_normalisation_insensitive() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(
            dir.path(),
            vec![e(1, "Docs/Report_FINAL.PDF"), e(1, "docs/caf\u{00e9}.txt")],
        )
        .unwrap();

    assert_eq!(idx.query(b"report_final", 10).unwrap().hits.len(), 1);
    assert_eq!(idx.query(b"REPORT", 10).unwrap().hits.len(), 1);
    // NFD query against an NFC-stored name.
    assert_eq!(
        idx.query("cafe\u{0301}".as_bytes(), 10).unwrap().hits.len(),
        1
    );
}

#[test]
fn cjk_substring_search() {
    // §10's "CJK partial match" row, T3 half. A UTF-8 Hangul syllable is exactly
    // three bytes, so one syllable is exactly one trigram.
    let dir = tempfile::tempdir().unwrap();
    let mut entries = vec![
        e(1, "사진/2026/여름휴가사진.jpg"),
        e(1, "사진/2025/겨울여행.png"),
        e(1, "문서/보고서.hwp"),
    ];
    // Pad so the index has real blocks rather than a single degenerate one.
    for i in 0..1000 {
        entries.push(e(1, &format!("기타/파일{i:04}.dat")));
    }
    let idx = IndexBuilder::new().build(dir.path(), entries).unwrap();

    for (q, want) in [
        ("휴가", vec!["사진/2026/여름휴가사진.jpg"]),
        ("여름", vec!["사진/2026/여름휴가사진.jpg"]),
        (
            "사진",
            vec!["사진/2025/겨울여행.png", "사진/2026/여름휴가사진.jpg"],
        ),
        ("보고서", vec!["문서/보고서.hwp"]),
    ] {
        let r = idx.query(q.as_bytes(), 100).unwrap();
        assert!(r.fallback.is_none(), "{q} unexpectedly fell back");
        assert_eq!(paths(&r), want, "query {q}");
    }
}

#[test]
fn false_positive_blocks_are_filtered_not_returned() {
    // The documented cost of postings that point at blocks rather than
    // documents: two names in one block can between them supply every trigram
    // of a query that neither actually contains.
    //
    //   "xabcdz" → abc, bcd, cdz
    //   "ybcdex" → bcd, cde, dex
    //   query "abcde" → abc, bcd, cde   ← all present in the block, no match
    let dir = tempfile::tempdir().unwrap();
    let mut entries = vec![e(1, "p/xabcdz.dat"), e(1, "p/ybcdex.dat")];
    for i in 0..4000 {
        entries.push(e(1, &format!("filler/{i:06}.dat")));
    }
    let idx = IndexBuilder::new().build(dir.path(), entries).unwrap();

    let r = idx.query(b"abcde", 100).unwrap();
    assert!(r.fallback.is_none());
    assert!(r.hits.is_empty(), "{:?}", paths(&r));
    assert_eq!(
        r.candidate_blocks, 1,
        "the intersection should have produced exactly one block to reject"
    );
    assert_eq!(r.false_positive_blocks, 1);
    assert!(r.scanned_entries >= 2, "the block was actually decompressed");

    // The same block answers a real query correctly.
    let r = idx.query(b"xabcdz", 10).unwrap();
    assert_eq!(paths(&r), vec!["p/xabcdz.dat"]);
    assert_eq!(r.false_positive_blocks, 0);
}

// ---------------------------------------------------------------------------
// fallback signals
// ---------------------------------------------------------------------------

#[test]
fn queries_under_three_bytes_signal_a_walk_fallback() {
    // §4.1: "a query under two characters can't form a trigram, so it falls
    // back to a T2 walk even when an index exists."
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(500))
        .unwrap();

    for q in [&b""[..], b"a", b"ab"] {
        let r = idx.query(q, 10).unwrap();
        assert_eq!(r.fallback, Some(FallbackReason::QueryTooShort), "{q:?}");
        assert!(r.hits.is_empty());
        assert!(r.must_fall_back());
    }
    // Three bytes is exactly enough for one trigram, and a selective one is
    // answered from the index. (`jpg` here is *not* selective — it is in
    // every block of a photo corpus, so it is pruned and correctly reports
    // `AllTrigramsPruned`; that case has its own test.)
    assert!(idx.query(b"242", 10).unwrap().fallback.is_none());
    assert_eq!(
        idx.query(b"jpg", 10).unwrap().fallback,
        Some(FallbackReason::AllTrigramsPruned)
    );

    // One Hangul syllable is three bytes, so a single CJK character reaches
    // the index where a single Latin character cannot.
    let cjk = tempfile::tempdir().unwrap();
    let words = ["사진", "문서", "영상", "음악"];
    let idx = IndexBuilder::new()
        .build(
            cjk.path(),
            (0..2000)
                .map(|i| e(1, &format!("자료/{}{i:04}.dat", words[i as usize / 500])))
                .collect(),
        )
        .unwrap();
    let r = idx.query("진".as_bytes(), 10).unwrap();
    assert!(r.fallback.is_none(), "one syllable is one full trigram");
    assert_eq!(r.hits.len(), 10, "limit applied to the 500 사진 entries");
    assert!(r.hits.iter().all(|h| h.path.contains("사진")));
}

#[test]
fn a_query_made_entirely_of_pruned_trigrams_falls_back() {
    let dir = tempfile::tempdir().unwrap();
    // 4000 entries → 125 blocks, all under `common/`, so every trigram of
    // "common" is in 100% of blocks and gets pruned.
    let entries: Vec<_> = (0..4000)
        .map(|i| e(1, &format!("common/{i:06}.dat")))
        .collect();
    let idx = IndexBuilder::new().build(dir.path(), entries).unwrap();
    assert!(idx.stats().pruned_trigrams > 0);

    let r = idx.query(b"common", 10).unwrap();
    assert_eq!(r.fallback, Some(FallbackReason::AllTrigramsPruned));
    assert!(r.hits.is_empty());

    // A selective query still works.
    let r = idx.query(b"003141", 10).unwrap();
    assert!(r.fallback.is_none());
    assert_eq!(paths(&r), vec!["common/003141.dat"]);
}

// ---------------------------------------------------------------------------
// delta segments
// ---------------------------------------------------------------------------

#[test]
fn appended_entries_are_immediately_queryable() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(1000))
        .unwrap();
    assert_eq!(idx.stats().delta_entries, 0);

    idx.append(&[e(1, "photos/2026/new/JUST_ARRIVED.jpg")]).unwrap();

    let r = idx.query(b"just_arrived", 10).unwrap();
    assert_eq!(paths(&r), vec!["photos/2026/new/JUST_ARRIVED.jpg"]);
    assert_eq!(idx.stats().delta_entries, 1);
    assert_eq!(idx.stats().delta_segments, 1);

    // Base entries keep working.
    assert_eq!(idx.query(b"IMG_00500", 10).unwrap().hits.len(), 1);
}

#[test]
fn appending_does_not_rewrite_the_base_segment() {
    // The point of the segment split: a write costs a few bytes appended to a
    // delta, no matter how large the base is (§4.2, "writes are O(1)").
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(20_000))
        .unwrap();
    let base_path = dir.path().join("base.idx");
    let before = std::fs::metadata(&base_path).unwrap();

    for i in 0..50 {
        idx.append(&[e(1, &format!("incoming/file{i:03}.dat"))]).unwrap();
    }

    let after = std::fs::metadata(&base_path).unwrap();
    assert_eq!(before.len(), after.len(), "base.idx must be immutable");
    assert_eq!(idx.stats().base_entries, 20_000);
    assert_eq!(idx.stats().delta_entries, 50);
    assert_eq!(idx.query(b"file027", 10).unwrap().hits.len(), 1);
}

#[test]
fn appended_entries_survive_a_reopen() {
    let dir = tempfile::tempdir().unwrap();
    {
        let idx = IndexBuilder::new()
            .build(dir.path(), photo_corpus(200))
            .unwrap();
        idx.append(&[e(2, "later/added_after_build.txt")]).unwrap();
    }
    let idx = NameIndex::open(dir.path()).unwrap();
    assert_eq!(idx.stats().delta_entries, 1);
    let r = idx.query(b"added_after_build", 10).unwrap();
    assert_eq!(paths(&r), vec!["later/added_after_build.txt"]);
    assert_eq!(r.hits[0].share, ShareId(2));
}

// ---------------------------------------------------------------------------
// tombstones
// ---------------------------------------------------------------------------

#[test]
fn tombstoned_paths_disappear_from_results() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(1000))
        .unwrap();
    let victim = "photos/2026/trip005/IMG_00500.jpg";
    assert_eq!(idx.query(b"IMG_00500", 10).unwrap().hits.len(), 1);

    idx.tombstone(&[e(1, victim)]).unwrap();

    assert!(idx.query(b"IMG_00500", 10).unwrap().hits.is_empty());
    // The base segment is untouched — the tombstone is a separate file.
    assert_eq!(idx.stats().base_entries, 1000);
    assert_eq!(idx.stats().tombstones, 1);
    // Neighbours in the same block are unaffected.
    assert_eq!(idx.query(b"IMG_00501", 10).unwrap().hits.len(), 1);
}

#[test]
fn a_rename_is_a_tombstone_plus_an_append() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(500))
        .unwrap();
    idx.tombstone(&[e(1, "photos/2026/trip001/IMG_00100.jpg")]).unwrap();
    idx.append(&[e(1, "photos/2026/trip001/RENAMED_00100.jpg")]).unwrap();

    assert!(idx.query(b"IMG_00100", 10).unwrap().hits.is_empty());
    assert_eq!(idx.query(b"RENAMED_00100", 10).unwrap().hits.len(), 1);
}

#[test]
fn a_path_recreated_after_deletion_comes_back() {
    // Tombstones carry the sequence number they were written at, so a later
    // append of the same path wins. Without that, "delete then re-create"
    // would silently lose the file until the next merge.
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(200))
        .unwrap();
    let p = "photos/2026/trip000/IMG_00042.jpg";

    idx.tombstone(&[e(1, p)]).unwrap();
    assert!(idx.query(b"IMG_00042", 10).unwrap().hits.is_empty());

    idx.append(&[e(1, p)]).unwrap();
    assert_eq!(idx.query(b"IMG_00042", 10).unwrap().hits.len(), 1);

    idx.tombstone(&[e(1, p)]).unwrap();
    assert!(idx.query(b"IMG_00042", 10).unwrap().hits.is_empty());
}

#[test]
fn tombstones_survive_a_reopen() {
    let dir = tempfile::tempdir().unwrap();
    {
        let idx = IndexBuilder::new()
            .build(dir.path(), photo_corpus(200))
            .unwrap();
        idx.tombstone(&[e(1, "photos/2026/trip000/IMG_00007.jpg")]).unwrap();
    }
    let idx = NameIndex::open(dir.path()).unwrap();
    assert!(idx.query(b"IMG_00007", 10).unwrap().hits.is_empty());
    assert_eq!(idx.stats().tombstones, 1);
}

// ---------------------------------------------------------------------------
// merge
// ---------------------------------------------------------------------------

#[test]
fn needs_merge_tracks_the_ratio_threshold() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .merge_ratio(0.15)
        .build(dir.path(), photo_corpus(20_000))
        .unwrap();
    assert!(!idx.needs_merge(), "a fresh base needs no merge");

    let base_bytes = idx.stats().base_bytes;
    let mut i = 0;
    while !idx.needs_merge() {
        idx.append(&[e(1, &format!("incoming/{i:08}.dat"))]).unwrap();
        i += 1;
        assert!(i < 100_000, "threshold never tripped");
    }
    let st = idx.stats();
    assert!(
        (st.delta_bytes + st.tomb_bytes) as f64 > 0.15 * base_bytes as f64,
        "{st:?}"
    );
}

#[test]
fn merge_preserves_results_and_shrinks_the_index() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(3000))
        .unwrap();

    // A realistic churn: many small appends (each its own framed, separately
    // compressed record) plus some deletions.
    for i in 0..400 {
        idx.append(&[e(1, &format!("incoming/2026/report_{i:04}.pdf"))]).unwrap();
    }
    for i in 0..200 {
        idx.tombstone(&[e(1, &format!("photos/2026/trip{:03}/IMG_{i:05}.jpg", i / 100))]).unwrap();
    }

    let before_size = idx.size_bytes();
    let before_stats = idx.stats();
    assert!(before_stats.delta_entries == 400);

    let sample: Vec<&[u8]> = vec![b"report_0123", b"IMG_02500", b"trip029", b"incoming"];
    let before: Vec<Vec<String>> = sample
        .iter()
        .map(|q| paths(&idx.query(q, 10_000).unwrap()))
        .collect();

    idx.merge(&|| true).unwrap();

    let after_stats = idx.stats();
    assert_eq!(after_stats.delta_entries, 0);
    assert_eq!(after_stats.delta_segments, 0);
    assert_eq!(after_stats.tombstones, 0);
    assert_eq!(after_stats.base_entries, 3000 - 200 + 400);
    assert_eq!(after_stats.generation, 1);

    let after: Vec<Vec<String>> = sample
        .iter()
        .map(|q| paths(&idx.query(q, 10_000).unwrap()))
        .collect();
    assert_eq!(before, after, "merge must not change what the index answers");

    assert!(
        idx.size_bytes() < before_size,
        "merge should shrink: {} -> {}",
        before_size,
        idx.size_bytes()
    );
    assert!(!idx.needs_merge());
    assert!(!dir.path().join("tomb.idx").exists());
}

#[test]
fn a_closed_gate_aborts_the_merge_without_damage() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(2000))
        .unwrap();
    for i in 0..100 {
        idx.append(&[e(1, &format!("incoming/{i:04}.dat"))]).unwrap();
    }
    let before = idx.stats();

    idx.merge(&|| false).unwrap();

    let after = idx.stats();
    assert_eq!(before.base_entries, after.base_entries);
    assert_eq!(before.delta_entries, after.delta_entries);
    assert_eq!(after.generation, 0, "no generation was produced");
    assert_eq!(
        paths(&idx.query(b"incoming/0042", 10).unwrap()),
        vec!["incoming/0042.dat"]
    );
    assert!(!dir.path().join("base.idx.new").exists() || dir.path().join("base.idx").exists());
}

#[test]
fn a_gate_that_closes_partway_through_also_leaves_the_index_intact() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(dir.path(), photo_corpus(8000))
        .unwrap();
    idx.append(&[e(1, "incoming/late.dat")]).unwrap();

    let calls = std::cell::Cell::new(0usize);
    idx.merge(&|| {
        let n = calls.get();
        calls.set(n + 1);
        n < 2 // open long enough to start walking the base, then shut
    })
    .unwrap();

    assert_eq!(idx.stats().generation, 0);
    assert_eq!(idx.stats().base_entries, 8000);
    assert_eq!(idx.query(b"IMG_04000", 10).unwrap().hits.len(), 1);
    assert_eq!(idx.query(b"late.dat", 10).unwrap().hits.len(), 1);
}

#[test]
fn merged_index_reopens_cleanly() {
    let dir = tempfile::tempdir().unwrap();
    {
        let idx = IndexBuilder::new()
            .build(dir.path(), photo_corpus(1000))
            .unwrap();
        idx.append(&[e(3, "after/merge_me.txt")]).unwrap();
        idx.tombstone(&[e(1, "photos/2026/trip000/IMG_00001.jpg")]).unwrap();
        idx.merge(&|| true).unwrap();
    }
    let idx = NameIndex::open(dir.path()).unwrap();
    assert_eq!(idx.stats().base_entries, 1000);
    assert_eq!(idx.stats().delta_entries, 0);
    assert_eq!(idx.query(b"merge_me", 10).unwrap().hits.len(), 1);
    assert!(idx.query(b"IMG_00001", 10).unwrap().hits.is_empty());
}

// ---------------------------------------------------------------------------
// crash safety
// ---------------------------------------------------------------------------

#[test]
fn a_torn_delta_tail_is_truncated_and_the_rest_survives() {
    let dir = tempfile::tempdir().unwrap();
    {
        let idx = IndexBuilder::new()
            .build(dir.path(), photo_corpus(200))
            .unwrap();
        idx.append(&[e(1, "delta/first_record.txt")]).unwrap();
        idx.append(&[e(1, "delta/second_record.txt")]).unwrap();
        idx.append(&[e(1, "delta/third_record.txt")]).unwrap();
    }

    // Simulate losing power mid-append: chop bytes off the end of the delta.
    let delta = dir.path().join("delta.000.idx");
    let len = std::fs::metadata(&delta).unwrap().len();
    let bytes = std::fs::read(&delta).unwrap();
    std::fs::write(&delta, &bytes[..(len as usize) - 7]).unwrap();

    let idx = NameIndex::open(dir.path()).unwrap();
    assert_eq!(idx.query(b"first_record", 10).unwrap().hits.len(), 1);
    assert_eq!(idx.query(b"second_record", 10).unwrap().hits.len(), 1);
    assert!(idx.query(b"third_record", 10).unwrap().hits.is_empty());
    // Base is untouched.
    assert_eq!(idx.query(b"IMG_00100", 10).unwrap().hits.len(), 1);

    // And the file was repaired, so the next append lands cleanly.
    idx.append(&[e(1, "delta/after_recovery.txt")]).unwrap();
    assert_eq!(idx.query(b"after_recovery", 10).unwrap().hits.len(), 1);
    let idx = NameIndex::open(dir.path()).unwrap();
    assert_eq!(idx.query(b"after_recovery", 10).unwrap().hits.len(), 1);
    assert_eq!(idx.stats().delta_entries, 3);
}

#[test]
fn a_missing_meta_file_does_not_stop_the_index_opening() {
    // The index is a cache; every failure mode must degrade, never explode.
    let dir = tempfile::tempdir().unwrap();
    {
        let idx = IndexBuilder::new()
            .build(dir.path(), photo_corpus(300))
            .unwrap();
        idx.append(&[e(1, "x/present.txt")]).unwrap();
    }
    std::fs::remove_file(dir.path().join("meta")).unwrap();
    let idx = NameIndex::open(dir.path()).unwrap();
    assert_eq!(idx.query(b"present", 10).unwrap().hits.len(), 1);
    assert_eq!(idx.query(b"IMG_00150", 10).unwrap().hits.len(), 1);
}

#[test]
fn an_empty_directory_opens_as_an_empty_index() {
    let dir = tempfile::tempdir().unwrap();
    let idx = NameIndex::open(&dir.path().join("names")).unwrap();
    assert_eq!(idx.stats().entries, 0);
    let r = idx.query(b"anything", 10).unwrap();
    assert!(r.hits.is_empty());
    assert!(r.fallback.is_none());
    assert!(!idx.needs_merge());

    // It is still writable, and a delta-only index answers correctly.
    idx.append(&[e(1, "only/in/delta.txt")]).unwrap();
    assert_eq!(idx.query(b"delta.txt", 10).unwrap().hits.len(), 1);
    assert!(idx.needs_merge(), "delta with no base always wants a merge");
}

// ---------------------------------------------------------------------------
// size / shape
// ---------------------------------------------------------------------------

#[test]
fn block_size_trades_size_against_false_positives() {
    // §4.1: a larger block compresses better and shortens posting lists, at
    // the cost of a less precise intersection.
    let corpus = photo_corpus(6000);
    let d1 = tempfile::tempdir().unwrap();
    let d32 = tempfile::tempdir().unwrap();
    let i1 = IndexBuilder::new().block_size(1).build(d1.path(), corpus.clone()).unwrap();
    let i32 = IndexBuilder::new().block_size(32).build(d32.path(), corpus).unwrap();

    assert!(
        i32.size_bytes() < i1.size_bytes(),
        "block_size=32 must beat block_size=1: {} vs {}",
        i32.size_bytes(),
        i1.size_bytes()
    );
    // Both must still return the same answer.
    assert_eq!(
        paths(&i1.query(b"IMG_03333", 10).unwrap()),
        paths(&i32.query(b"IMG_03333", 10).unwrap())
    );
}

#[test]
fn multiple_shares_are_kept_distinct() {
    let dir = tempfile::tempdir().unwrap();
    let idx = IndexBuilder::new()
        .build(
            dir.path(),
            vec![e(1, "shared/name.txt"), e(2, "shared/name.txt")],
        )
        .unwrap();
    let r = idx.query(b"shared/name", 10).unwrap();
    assert_eq!(r.hits.len(), 2);

    idx.tombstone(&[e(1, "shared/name.txt")]).unwrap();
    let r = idx.query(b"shared/name", 10).unwrap();
    assert_eq!(r.hits.len(), 1);
    assert_eq!(r.hits[0].share, ShareId(2));
}

#[test]
fn limit_is_respected_and_ranking_is_applied() {
    let dir = tempfile::tempdir().unwrap();
    let mut entries = vec![e(1, "report"), e(1, "notes/report_final.pdf")];
    for i in 0..30 {
        entries.push(e(1, &format!("archive/old_report_{i:04}.pdf")));
    }
    // Filler so that "report" stays selective rather than being pruned for
    // appearing in more than 60% of blocks.
    for i in 0..4000 {
        entries.push(e(1, &format!("bulk/{i:06}.dat")));
    }
    let idx = IndexBuilder::new().build(dir.path(), entries).unwrap();

    let r = idx.query(b"report", 5).unwrap();
    assert!(r.fallback.is_none());
    assert_eq!(r.hits.len(), 5);
    // 3.0 exact name + 2.0 prefix beats 2.0 prefix alone.
    assert_eq!(r.hits[0].name, "report");
    assert_eq!(r.hits[1].name, "report_final.pdf");
    assert!(r.hits[0].score > r.hits[1].score);
    assert_eq!(idx.query(b"report", 10_000).unwrap().hits.len(), 32);
}
