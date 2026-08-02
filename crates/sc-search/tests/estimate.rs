//! Estimator accuracy (§6, §10 "estimate accuracy").
//!
//! The point of this file: the estimate must be checked against an index we
//! actually built, on more than one corpus shape. A model that is only
//! calibrated on photo-library names is not a model, it is a constant.

use sc_search::{estimate_name_index, CorpusScanner, IndexBuilder, ShareId};

/// Build the same names through both paths and report
/// `(predicted, actual, formula)`.
fn compare(names: &[String], block_size: u32) -> (u64, u64, String) {
    // Feed the scanner in tree order — that is what a T2 walk delivers, and it
    // is the order the builder will chunk into blocks. Sampling out-of-order
    // would measure a compression ratio the builder never sees.
    let mut names: Vec<String> = names.to_vec();
    names.sort_by(|a, b| sc_search::index::tree_cmp(a, b));

    let mut scanner = CorpusScanner::new(block_size, 100);
    for n in &names {
        scanner.observe(n, false);
    }
    let stats = scanner.finish();
    let est = estimate_name_index(&stats, block_size);

    let dir = tempfile::tempdir().unwrap();
    let entries: Vec<(ShareId, String)> = names.iter().map(|n| (ShareId(1), n.clone())).collect();
    let idx = IndexBuilder::new()
        .block_size(block_size)
        .build(dir.path(), entries)
        .unwrap();
    let actual = idx.stats().base_bytes;

    (est.index_bytes, actual, est.formula)
}

fn assert_within(predicted: u64, actual: u64, tolerance: f64, what: &str, formula: &str) {
    let err = (predicted as f64 - actual as f64) / actual as f64;
    assert!(
        err.abs() <= tolerance,
        "{what}: predicted {predicted} vs actual {actual} ({:+.1}%, tolerance ±{:.0}%)\n{formula}",
        err * 100.0,
        tolerance * 100.0
    );
}

/// Photo library: long shared prefixes, sequential numbering. §4.1 calls this
/// out as the shape block compression is strongest on.
fn photo_names(n: u32) -> Vec<String> {
    (0..n)
        .map(|i| format!("photos/{}/{:02}/IMG_{i:06}.jpg", 2020 + i / 4000, i / 300 % 12))
        .collect()
}

/// Document tree: unrelated names, mixed extensions, shallower nesting.
fn document_names(n: u32) -> Vec<String> {
    let exts = ["pdf", "docx", "txt", "md", "csv", "xlsx"];
    (0..n)
        .map(|i| {
            let h = sc_search::hll::hash64(&(i as u64).to_le_bytes());
            format!(
                "docs/dept{:02}/{:x}-{:x}.{}",
                i % 40,
                h,
                h.rotate_left(19),
                exts[i as usize % exts.len()]
            )
        })
        .collect()
}

/// CJK corpus. §4.1 warns that "17 B/file is a Latin measurement" and that CJK
/// pushes the posting dictionary up; §6.2 says measure it rather than guess,
/// which is what the HyperLogLog term does. This asserts the measurement works.
fn cjk_names(n: u32) -> Vec<String> {
    let words = ["여름휴가", "겨울여행", "가족사진", "회사문서", "음악파일"];
    (0..n)
        .map(|i| {
            format!(
                "자료/{}/{}_{i:06}.jpg",
                words[i as usize % words.len()],
                words[(i as usize / 7) % words.len()]
            )
        })
        .collect()
}

#[test]
fn predicts_a_20k_name_photo_corpus_within_25_percent() {
    let names = photo_names(20_000);
    let (predicted, actual, formula) = compare(&names, 32);
    assert_within(predicted, actual, 0.25, "photo library, 20k names", &formula);
    eprintln!("photo 20k:\n{formula}\nactual {actual}\n");
}

#[test]
fn predicts_a_20k_name_document_corpus_within_25_percent() {
    let names = document_names(20_000);
    let (predicted, actual, formula) = compare(&names, 32);
    assert_within(predicted, actual, 0.25, "document tree, 20k names", &formula);
    eprintln!("docs 20k:\n{formula}\nactual {actual}\n");
}

#[test]
fn predicts_a_cjk_corpus_within_25_percent() {
    let names = cjk_names(20_000);
    let (predicted, actual, formula) = compare(&names, 32);
    assert_within(predicted, actual, 0.25, "CJK corpus, 20k names", &formula);
    eprintln!("cjk 20k:\n{formula}\nactual {actual}\n");
}

#[test]
fn tracks_block_size_changes() {
    let names = photo_names(10_000);
    for bs in [8u32, 16, 32, 64] {
        let (predicted, actual, formula) = compare(&names, bs);
        assert_within(predicted, actual, 0.25, &format!("block_size={bs}"), &formula);
    }
}

#[test]
fn the_measured_compression_ratio_actually_differs_by_corpus() {
    // §6.1: this single measurement is what absorbs the multiple-times
    // difference between corpus shapes. If it did not vary, the model would be
    // a hardcoded constant wearing a disguise.
    let ratio = |names: &[String]| {
        let mut s = CorpusScanner::new(32, 100);
        for n in names {
            s.observe(n, false);
        }
        s.finish().sample_compress_ratio
    };
    let photo = ratio(&photo_names(5000));
    let docs = ratio(&document_names(5000));
    assert!(
        photo < docs * 0.8,
        "photo names must compress markedly better: {photo:.3} vs {docs:.3}"
    );
}

#[test]
fn cjk_costs_more_per_file_than_latin_and_the_estimate_knows_it() {
    let stats_for = |names: &[String]| {
        let mut s = CorpusScanner::new(32, 100);
        for n in names {
            s.observe(n, false);
        }
        s.finish()
    };
    let latin = stats_for(&photo_names(20_000));
    let cjk = stats_for(&cjk_names(20_000));

    let l = estimate_name_index(&latin, 32);
    let c = estimate_name_index(&cjk, 32);
    let per_file = |b: u64| b as f64 / 20_000.0;
    assert!(
        per_file(c.index_bytes) > per_file(l.index_bytes),
        "CJK {:.1} B/file should exceed Latin {:.1} B/file",
        per_file(c.index_bytes),
        per_file(l.index_bytes)
    );
}

#[test]
fn per_file_cost_lands_in_the_designed_range() {
    // §4.1's revised budget: ~20–30 B/file, versus plocate's ~17 B on a Latin
    // corpus and the ~195 B/file the FTS5 draft would have cost.
    let names = photo_names(50_000);
    let (_, actual, _) = compare(&names, 32);
    let per_file = actual as f64 / 50_000.0;
    assert!(
        (5.0..40.0).contains(&per_file),
        "{per_file:.1} B/file is outside the designed envelope"
    );
}

#[test]
fn the_formula_is_arithmetic_an_admin_can_check() {
    let mut s = CorpusScanner::new(32, 100);
    for n in photo_names(2_100_000 / 1000) {
        s.observe(&n, false);
    }
    let est = estimate_name_index(&s.finish(), 32);
    // Every term that goes into the total is named and shown.
    for term in [
        "blocks", "names", "blockdir", "dict", "postings", "header", "total", "build",
    ] {
        assert!(est.formula.contains(term), "missing `{term}`:\n{}", est.formula);
    }
    assert!(est.formula.contains("B/file"));
    assert!(est.formula.contains("duty cycle"));
}
