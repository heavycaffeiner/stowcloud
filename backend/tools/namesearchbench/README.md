# namesearchbench

`namesearchbench` is a deterministic exploratory harness for the existing Stowcloud filename index. It does not add an alternate engine and does not use Bleve. It is **not a release performance claim**; timings are local observations and must not be compared across machines without preserving the recorded inputs.

## Run

From the `backend` directory:

```sh
go run ./tools/namesearchbench \
  -seed 1 -count 10000 -depth 3 -fanout 32 -namespaces 2 \
  -cache cold-unspecified -filesystem unspecified -concurrency 1 \
  -out /tmp/namesearch-10k.json \
  -raw-json /tmp/namesearch-10k.raw.json \
  -raw-csv /tmp/namesearch-10k.raw.csv
```

The default count is 10,000. Use `-count 100000` for 100k or `-count 1000000` for a configurable million-entry run. `-seed`, `-depth`, `-fanout`, and `-namespaces` define the corpus shape. The generator uses a fixed split-mix sequence and emits stable namespace/path rows. The report includes a SHA-256 content hash over canonical `namespace<TAB>path<LF>` rows.

The command measures these phases separately:

- `generate`: deterministic corpus and query construction
- `build`: append corpus entries to the index
- `merge`: construct and publish the immutable base
- `open`: reopen the generated index
- `query`: exact, prefix, substring, common, no-hit, Unicode, path, and short queries
- `update`: append a bounded update batch
- `merge` (`post-update`): absorb the update

Query correctness checks compare index hits to a direct folded substring oracle. Short queries must report the existing index fallback (`query_too_short`). The report records toolchain, OS/architecture, CPU/GOMAXPROCS, hardware input, filesystem/cache/concurrency inputs, source path, raw samples, aggregate rows, and check results.

## Reproducibility and evidence

Do not commit fabricated result numbers. A saved exploratory result must include the exact command, seed, shape, content hash, toolchain, hardware, filesystem, cache state, and concurrency. Raw JSON/CSV are samples of measured rows; aggregate JSON contains all measurements and checks. An equivalent baseline is intentionally absent because no separate real implementation is available in this repository. Do not add one by importing a new search engine without explicit approval.
For the repeated local exploratory evidence set, see `backend/testdata/results/namesearchbench-local-summary.md` and the exact command log `backend/testdata/results/namesearchbench-local-runs.command.txt`. The result directory contains three aggregate/raw/stdout artifacts for both 10k and 100k corpora; all repeats use the same content hash per corpus size. A 1M run was intentionally not run because its resource cost was outside the evidence budget.

Allocation and core-operation benchmarks are in `main_test.go`:

```sh
go test ./tools/namesearchbench -run '^$' -bench . -benchmem
```
