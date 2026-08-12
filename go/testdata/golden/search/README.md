# Search golden fixtures

What the Rust implementation produces, so that "does the Go search work" is a
diff rather than a judgement. Every file here is generated:

```sh
cargo run -p sc-search --example golden
```

The generator is `crates/sc-search/examples/golden.rs`. It is deterministic:
two consecutive runs produce byte-identical output, and a difference is a bug
in the generator rather than something to sort away afterwards. Regenerate and
commit the result; never hand-edit a file in this directory.

`.gitattributes` marks the whole tree `-text` so git does not normalise line
endings in it. A fixture git rewrote is a fixture that fails for a reason
neither implementation caused.

## corpus.txt

`share_id <TAB> path`, one per line, in the order the generator produced them
rather than in index order. `base.idx` is built from exactly these 585 names
after the reader sorts them into tree order, which is part of what the fixture
checks.

It is committed because a fixture nobody can regenerate is a fixture nobody can
debug. Three things about it are deliberate:

- Over 512 names, so it fills more than the sixteen blocks below which high-df
  pruning refuses to run.
- Every path starts with `data/`, so a trigram exists in every block and
  pruning has something to prune. No natural corpus this small produces one.
- A third of it is Hangul and CJK, with one name in both NFC and NFD. The
  folding table and the estimator's distinct-trigram term are exactly where a
  Latin-only corpus proves nothing.

## varint.bin

Self-describing, so a failure names the value that broke rather than an offset.
All integers little-endian.

```text
"SCVI"
u32  scalar case count
  per case: u64 value, u8 encoded length, that many bytes
u32  ascending-list case count
  per case: u32 id count, that many u32 ids, u32 encoded length, those bytes
```

The scalar set is every varint width boundary and both sides of each one.

## fold.tsv, trigram.tsv

`# ` introduces a comment line. Columns:

| File | Columns |
|---|---|
| `fold.tsv` | `input_hex`, `folded_hex`, lossy display |
| `trigram.tsv` | `input_hex`, comma-separated 6-hex-digit trigrams in order, lossy display |

Hex, not raw bytes, because a folded byte string is not required to be valid
UTF-8 and a tab-separated field cannot carry a tab, a newline or a lone
surrogate. The display column is for a reader and carries no information the
first two do not.

`trigram.tsv` takes its input bytes exactly as given, without folding. Folding
has its own fixture, and composing the two here would make a failure ambiguous
about which one moved.

## hll.tsv

`kind <TAB> arg <TAB> precision <TAB> result`. Two kinds of row, because an
estimate that disagrees is almost always a hash that disagrees:

| kind | arg | result |
|---|---|---|
| `hash` | input bytes, hex | the 64-bit hash, 16 hex digits |
| `item` | n | the f64 estimate's bit pattern, 16 hex digits |
| `trigram` | n | as above |
| `dup` | n | as above |

The estimate is a bit pattern rather than a rounded decimal: the register
update and the bias correction are both floating point, and a fixture that
rounds cannot tell a correct implementation from one that is wrong in the last
place.

The insert rules, which the reader replicates:

- `item` inserts `item-{i}` for i in `0..n`
- `trigram` inserts every 3-byte window of
  `photos/2026/trip{i % 977}/IMG_{i:05}.jpg` for i in `0..n`
- `dup` inserts `the same trigram source over and over`, n times

## base.idx, delta.000.idx, tomb.idx

A full index over `corpus.txt`, plus an overlay. Five names were appended after
the build and three tombstoned, two of which are in the base segment and one of
which was appended, because those are different paths on the read side.

`meta` is deliberately absent. It carries a generation counter and a byte
accounting that belong to one writer's history rather than to the format, and
the reader discovers the segments by listing the directory, so the fixture
opens without it.

**One caveat about byte-identity**, because the strategy this fixture exists for
asks for it. The header, the block directory, the trigram dictionary and the
posting lists are the format's own bytes and a correct writer reproduces them
exactly. The block payloads are zstd frames, and zstd's format does not
constrain an encoder's block splitting, match finding or entropy table choice,
so two independent encoders agreeing byte for byte would be a coincidence
rather than a property. `OPEN-QUESTIONS.md` Q2 carries what that means for the
check.

## query.tsv

`needle_hex <TAB> limit <TAB> fallback` followed by zero or more hit fields,
each `share:score_bits:path`.

`fallback` is `-`, `QueryTooShort` or `AllTrigramsPruned`. It is a column of
its own because "I found nothing" and "I cannot answer this" are different
answers, and conflating them turns a fallback into a wrong empty result.

The score is the f32 bit pattern, for the reason the estimate is. The path
comes last so a `:` inside it needs no escaping.
