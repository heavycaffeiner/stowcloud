//! Emits the cross-implementation search fixtures the Go port is checked
//! against, into `go/testdata/golden/search/`.
//!
//! ```text
//! cargo run -p sc-search --example golden
//! ```
//!
//! An example rather than a test because it writes into the repository and a
//! test that does that is a test nobody can run in parallel. It reads the
//! crate's public API and changes nothing in it: the point of a golden file is
//! that it records what the implementation does, so an implementation bent to
//! make generation easier records the bend instead.
//!
//! **Determinism is the whole contract.** Nothing here reads a clock, a random
//! number, an environment variable or a directory listing, and every map that
//! reaches the output is ordered. Run it twice and diff; a difference is a bug
//! in this file, not something to sort away afterwards.


use std::fmt::Write as _;
use std::fs;
use std::path::{Path, PathBuf};

use sc_search::hll::{hash64, HyperLogLog};
use sc_search::index::{IndexBuilder, NameIndex};
use sc_search::vfs::ShareId;
use sc_search::{fold, trigram, varint};

fn main() -> anyhow::Result<()> {
    let out = fixture_dir();
    fs::create_dir_all(&out)?;

    let corpus = corpus();
    write(&out.join("corpus.txt"), corpus_text(&corpus).as_bytes())?;
    write(&out.join("varint.bin"), &varint_fixture())?;
    write(&out.join("fold.tsv"), fold_fixture().as_bytes())?;
    write(&out.join("trigram.tsv"), trigram_fixture().as_bytes())?;
    write(&out.join("hll.tsv"), hll_fixture().as_bytes())?;

    let index = build_segments(&out, &corpus)?;
    write(&out.join("query.tsv"), query_fixture(&index)?.as_bytes())?;

    // `meta` is deliberately not a fixture: it carries a generation counter and
    // a byte accounting that belong to one writer's history rather than to the
    // format. The reader discovers the segments by listing the directory, so
    // the fixture is openable without it.
    let _ = fs::remove_file(out.join("meta"));
    let _ = fs::remove_file(out.join("meta.tmp"));

    println!("wrote fixtures into {}", out.display());
    Ok(())
}

fn fixture_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../go/testdata/golden/search")
        .components()
        .collect()
}

fn write(path: &Path, bytes: &[u8]) -> anyhow::Result<()> {
    fs::write(path, bytes)?;
    Ok(())
}

// ---------------------------------------------------------------------------
// corpus
// ---------------------------------------------------------------------------

/// The fixed corpus every index fixture is built from.
///
/// Three things about its shape are deliberate. It is over 512 names, so it
/// fills more than the sixteen blocks high-df pruning refuses to run below.
/// Every path starts with the same component, so that a trigram exists in every
/// block and pruning has something to prune, which no natural corpus of this
/// size produces. And it is a third Hangul and CJK, because the folding table
/// and the estimator's distinct-trigram term are exactly where a Latin-only
/// corpus proves nothing.
fn corpus() -> Vec<(ShareId, String)> {
    let mut v: Vec<(ShareId, String)> = Vec::new();

    for i in 0..200 {
        v.push((ShareId(1), format!("data/photos/2026/여름휴가/IMG_{i:04}.jpg")));
    }
    for i in 0..120 {
        v.push((ShareId(1), format!("data/photos/2025/spring/IMG_{i:04}.jpg")));
    }
    for i in 0..60 {
        v.push((ShareId(2), format!("data/documents/보고서-{i:03}.pdf")));
    }
    for i in 0..60 {
        v.push((ShareId(2), format!("data/documents/report-{i:03}.pdf")));
    }
    for i in 0..20 {
        v.push((ShareId(2), format!("data/documents/Report_Final_{i:02}.DOCX")));
    }
    for album in 1..=8 {
        for track in 1..=10 {
            v.push((
                ShareId(3),
                format!("data/音楽/アルバム{album:02}/track{track:02}.flac"),
            ));
        }
    }
    for i in 0..30 {
        v.push((ShareId(3), format!("data/mixed/Vacation_Photo_{i:02}.JPG")));
    }
    for i in 0..10 {
        v.push((ShareId(3), format!("data/settings/.hidden-{i:02}.cfg")));
    }

    // The same name in both normalisations. They fold to identical bytes, so
    // one query has to find both and the index has to store them as two
    // distinct paths.
    v.push((ShareId(3), "data/unicode/caf\u{00e9}.txt".to_string()));
    v.push((ShareId(3), "data/unicode/cafe\u{0301}.txt".to_string()));
    v.push((ShareId(3), "data/unicode/ΣΊΣΥΦΟΣ.txt".to_string()));
    v.push((ShareId(3), "data/unicode/İstanbul.txt".to_string()));
    v.push((ShareId(3), "data/unicode/ẞTRASSE.txt".to_string()));

    v
}

fn corpus_text(entries: &[(ShareId, String)]) -> String {
    let mut s = String::new();
    for (share, path) in entries {
        let _ = writeln!(s, "{}\t{}", share.0, path);
    }
    s
}

// ---------------------------------------------------------------------------
// varint
// ---------------------------------------------------------------------------

/// `varint.bin` is self-describing, so a failure says which value broke rather
/// than which offset:
///
/// ```text
/// "SCVI"                     magic
/// u32  scalar case count
///   per case: u64 value, u8 encoded length, that many bytes
/// u32  ascending-list case count
///   per case: u32 id count, that many u32 ids, u32 encoded length, those bytes
/// ```
///
/// All integers little-endian. The scalar set is every varint width boundary
/// and both sides of each one, because an off-by-one in the shift loop shows up
/// nowhere else.
fn varint_fixture() -> Vec<u8> {
    let scalars: Vec<u64> = vec![
        0,
        1,
        126,
        127,
        128,
        129,
        255,
        256,
        16_383,
        16_384,
        16_385,
        2_097_151,
        2_097_152,
        268_435_455,
        268_435_456,
        34_359_738_367,
        34_359_738_368,
        u32::MAX as u64 - 1,
        u32::MAX as u64,
        u32::MAX as u64 + 1,
        1 << 49,
        1 << 56,
        1 << 63,
        u64::MAX - 1,
        u64::MAX,
    ];

    let lists: Vec<Vec<u32>> = vec![
        vec![],
        vec![0],
        vec![0, 1],
        vec![0, 1, 5, 400, 401, 100_000],
        vec![127, 128, 255, 256, 16_383, 16_384],
        vec![1, 2, 3, 4, 5, 6, 7, 8, 9, 10],
        vec![u32::MAX - 1, u32::MAX],
        (0..64).map(|i| i * 3).collect(),
    ];

    let mut out = Vec::new();
    out.extend_from_slice(b"SCVI");
    out.extend_from_slice(&(scalars.len() as u32).to_le_bytes());
    for v in &scalars {
        let mut enc = Vec::new();
        varint::put(&mut enc, *v);
        assert_eq!(enc.len(), varint::len(*v), "len disagrees with put for {v}");
        out.extend_from_slice(&v.to_le_bytes());
        out.push(enc.len() as u8);
        out.extend_from_slice(&enc);
    }
    out.extend_from_slice(&(lists.len() as u32).to_le_bytes());
    for ids in &lists {
        let mut enc = Vec::new();
        varint::encode_ascending(&mut enc, ids);
        out.extend_from_slice(&(ids.len() as u32).to_le_bytes());
        for id in ids {
            out.extend_from_slice(&id.to_le_bytes());
        }
        out.extend_from_slice(&(enc.len() as u32).to_le_bytes());
        out.extend_from_slice(&enc);
    }
    out
}

// ---------------------------------------------------------------------------
// fold and trigram
// ---------------------------------------------------------------------------

/// The inputs are hex because a folded byte string is not required to be valid
/// UTF-8, and a tab-separated file cannot carry a tab, a newline or a lone
/// surrogate in a field. The third column is the lossy rendering, for a reader.
fn fold_fixture() -> String {
    let cases: Vec<&[u8]> = vec![
        b"",
        b"a",
        b"IMG_0001.JPG",
        b"img_0001.jpg",
        b"Vacation_Photo.JPG",
        b"REPORT_FINAL.DOCX",
        b"data/documents/Report_Final_00.DOCX",
        b".hidden-00.cfg",
        "여름휴가사진.jpg".as_bytes(),
        "보고서-000.pdf".as_bytes(),
        "音楽/アルバム01/track01.flac".as_bytes(),
        "caf\u{00e9}.txt".as_bytes(),
        "cafe\u{0301}.txt".as_bytes(),
        "CAF\u{00c9}.TXT".as_bytes(),
        "ΣΊΣΥΦΟΣ.txt".as_bytes(),
        "İstanbul.txt".as_bytes(),
        "ẞTRASSE.txt".as_bytes(),
        "Ω-OMEGA.txt".as_bytes(),
        // Not valid UTF-8: ASCII folds, everything else survives byte for
        // byte, because a filename that is not UTF-8 must still be findable.
        b"ABC\xff\xfe.bin",
        b"\xc3\x28",
        b"\xff",
    ];

    let mut s = String::from("# input_hex\tfolded_hex\tdisplay\n");
    for input in cases {
        let _ = writeln!(
            s,
            "{}\t{}\t{}",
            hex(input),
            hex(&fold::fold(input)),
            String::from_utf8_lossy(input)
        );
    }
    s
}

/// Trigrams of the input bytes exactly as given. Folding is a separate function
/// with its own fixture, so composing the two here would make a failure
/// ambiguous about which one moved.
fn trigram_fixture() -> String {
    let cases: Vec<&[u8]> = vec![
        b"",
        b"a",
        b"ab",
        b"abc",
        b"abcd",
        b"aaaa",
        b"img_0001.jpg",
        b"data/photos/2026/",
        "휴".as_bytes(),
        "휴가".as_bytes(),
        "여름휴가사진.jpg".as_bytes(),
        "보고서".as_bytes(),
        "アルバム".as_bytes(),
        "音楽".as_bytes(),
        b"\xff\xfe\xfd\xfc",
    ];

    let mut s = String::from("# input_hex\ttrigrams_hex_csv\tdisplay\n");
    for input in cases {
        let tris: Vec<String> = trigram::distinct(input).iter().map(|t| hex(t)).collect();
        let _ = writeln!(
            s,
            "{}\t{}\t{}",
            hex(input),
            tris.join(","),
            String::from_utf8_lossy(input)
        );
    }
    s
}

// ---------------------------------------------------------------------------
// hyperloglog
// ---------------------------------------------------------------------------

/// Two kinds of row, because an estimate that disagrees is almost always a hash
/// that disagrees, and finding that out from the estimate alone costs an
/// afternoon:
///
/// ```text
/// hash  <input hex>   -     <u64 hash, 16 hex digits>
/// <rule> <n>          <p>   <f64 estimate bits, 16 hex digits>
/// ```
///
/// The estimate is the exact bit pattern rather than a rounded decimal: the
/// register update and the bias correction are both floating point, and a
/// fixture that rounds cannot tell a correct implementation from one that is
/// wrong in the last place.
///
/// The rules, which the reader replicates:
///
/// * `item`    inserts `item-{i}` for i in 0..n
/// * `trigram` inserts every 3-byte window of
///   `photos/2026/trip{i % 977}/IMG_{i:05}.jpg` for i in 0..n
/// * `dup`     inserts the same string n times
fn hll_fixture() -> String {
    let mut s = String::from("# kind\targ\tprecision\tresult\n");

    let hashes: Vec<&[u8]> = vec![
        b"",
        b"a",
        b"abc",
        b"item-0",
        b"item-1",
        b"item-999999",
        "휴가".as_bytes(),
        "보고서".as_bytes(),
        b"\xff\xfe\xfd",
        b"the same trigram source over and over",
    ];
    for h in hashes {
        let _ = writeln!(s, "hash\t{}\t-\t{:016x}", hex(h), hash64(h));
    }

    for p in [4u8, 8, 14] {
        for n in [0u64, 1, 10, 100, 1_000, 10_000] {
            let mut hll = HyperLogLog::new(p);
            for i in 0..n {
                hll.add(format!("item-{i}").as_bytes());
            }
            let _ = writeln!(s, "item\t{n}\t{p}\t{:016x}", hll.estimate().to_bits());
        }
    }

    for n in [1_000u64, 50_000] {
        let mut hll = HyperLogLog::new(14);
        for i in 0..n {
            let name = format!("photos/2026/trip{}/IMG_{:05}.jpg", i % 977, i);
            for w in name.as_bytes().windows(3) {
                hll.add(w);
            }
        }
        let _ = writeln!(s, "trigram\t{n}\t14\t{:016x}", hll.estimate().to_bits());
    }

    for n in [1u64, 10_000] {
        let mut hll = HyperLogLog::new(14);
        for _ in 0..n {
            hll.add(b"the same trigram source over and over");
        }
        let _ = writeln!(s, "dup\t{n}\t14\t{:016x}", hll.estimate().to_bits());
    }

    s
}

// ---------------------------------------------------------------------------
// segments
// ---------------------------------------------------------------------------

/// `base.idx` from the corpus, then an overlay: `delta.000.idx` holding names
/// that arrived after the build, and `tomb.idx` holding deletions. Two of the
/// deletions are of base entries and one is of an appended entry, because those
/// are different code paths on the read side and a fixture that only covers the
/// first proves half of it.
fn build_segments(dir: &Path, corpus: &[(ShareId, String)]) -> anyhow::Result<NameIndex> {
    // `build` removes every segment file in the directory first, which is what
    // makes a second run of this generator produce the same bytes: the delta
    // and tombstone writers append.
    let index = IndexBuilder::new().build(dir, corpus.to_vec())?;

    index.append(&appended())?;
    index.tombstone(&tombstoned())?;
    Ok(index)
}

fn appended() -> Vec<(ShareId, String)> {
    vec![
        (ShareId(1), "data/photos/2026/여름휴가/IMG_9000.jpg".to_string()),
        (ShareId(1), "data/photos/2026/여름휴가/IMG_9001.jpg".to_string()),
        (ShareId(2), "data/documents/report-900.pdf".to_string()),
        (ShareId(3), "data/mixed/Vacation_Photo_99.JPG".to_string()),
        (ShareId(3), "data/unicode/naïve.txt".to_string()),
    ]
}

fn tombstoned() -> Vec<(ShareId, String)> {
    vec![
        // In the base segment.
        (ShareId(1), "data/photos/2026/여름휴가/IMG_0000.jpg".to_string()),
        (ShareId(2), "data/documents/report-000.pdf".to_string()),
        // Appended above, so the tombstone has to win on sequence order.
        (ShareId(3), "data/unicode/naïve.txt".to_string()),
    ]
}

// ---------------------------------------------------------------------------
// queries
// ---------------------------------------------------------------------------

/// One row per query, with a variable number of trailing hit fields:
///
/// ```text
/// needle_hex  limit  fallback  share:score_bits:path  ...
/// ```
///
/// `fallback` is `-`, `QueryTooShort` or `AllTrigramsPruned`, and it is a
/// column of its own because "I found nothing" and "I cannot answer this" are
/// different answers and conflating them turns a fallback into a wrong empty
/// result.
///
/// The score is the f32 bit pattern. The path comes last so that a `:` inside
/// it needs no escaping.
fn query_fixture(index: &NameIndex) -> anyhow::Result<String> {
    let queries: Vec<(&str, usize)> = vec![
        ("ab", 20),
        ("data", 20),
        ("IMG_0001", 20),
        ("img_0001.jpg", 20),
        ("Report_Final_00.DOCX", 20),
        ("report-0", 5),
        ("휴가", 8),
        ("여름휴가", 4),
        ("보고서", 6),
        ("アルバム01", 12),
        ("音楽", 4),
        ("caf\u{00e9}", 10),
        ("cafe\u{0301}", 10),
        ("CAF\u{00c9}", 10),
        (".hidden-00.cfg", 5),
        ("vacation_photo", 6),
        ("IMG_9000", 5),
        ("naïve", 5),
        ("IMG_0000.jpg", 5),
        ("zzzznotfound", 5),
    ];

    let mut s =
        String::from("# needle_hex\tlimit\tfallback\tshare:score_bits:path (zero or more)\n");
    for (needle, limit) in queries {
        let r = index.query(needle.as_bytes(), limit)?;
        let mut row = format!(
            "{}\t{}\t{}",
            hex(needle.as_bytes()),
            limit,
            match r.fallback {
                None => "-".to_string(),
                Some(f) => format!("{f:?}"),
            }
        );
        for h in &r.hits {
            let _ = write!(row, "\t{}:{:08x}:{}", h.share.0, h.score.to_bits(), h.path);
        }
        row.push('\n');
        s.push_str(&row);
    }
    Ok(s)
}

// ---------------------------------------------------------------------------

fn hex(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        let _ = write!(s, "{b:02x}");
    }
    s
}
