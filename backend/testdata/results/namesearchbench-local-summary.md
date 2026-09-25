# Namesearch benchmark comparison (local exploratory)

This report is **local exploratory evidence only**, not a release performance claim. It records three repeated runs for each corpus size using the same deterministic corpus shape and command family. Timings are wall-clock observations in nanoseconds and depend on this machine, Go toolchain, filesystem, cache state, and concurrency. No unverified multiplier claim is made.

## Fixed inputs

- Seed: `1`
- Shape: depth `3`, fanout `32`, namespaces `2`
- Runs per size: `3` (`r1`, `r2`, `r3`)
- Hardware: `13th Gen Intel(R) Core(TM) i5-13500H`
- OS/architecture: Linux/amd64
- Filesystem: `xfs` (working tree filesystem)
- Cache label: `cold-unspecified` (the harness records the requested label; it does not claim kernel-cache eviction)
- Concurrency: `1`
- Toolchain: recorded in each aggregate JSON report
- Exact commands: `namesearchbench-local-runs.command.txt`
- Raw samples: each `*-r{1,2,3}.raw.json` and `*.raw.csv`; aggregate reports and captured stdout are stored alongside them.

## Corpus identity and correctness

| corpus | content hash (all 3 runs) | checks |
| ---: | --- | --- |
| 10,000 | `6ed9b2c674804f654740579b418a9f3559212a432ceb7eb473b1bd7bd7050ec4` | 10/10 pass in each run |
| 100,000 | `07213f5791e2044f0b166932a62a1d26d47fe62edbafe8d23353b6fd6767c05f` | 10/10 pass in each run |

The checks include exact, prefix, substring, common, no-hit, Unicode, path, and short-query fallback correctness, plus non-empty content hash and aggregate check status. The same hash across repeats demonstrates that the repeated runs used identical generated content for each size.

## Timing aggregates

For every row, `samples` are `r1, r2, r3` in nanoseconds; `mean`, `variance`, and `standard deviation` are computed over those three samples. Variance is sample variance (`n-1` denominator), in ns². Query rows are keyed by kind/query; merge appears twice because the post-update merge is separately labeled.

### 10,000 entries

| operation | query/kind | samples (ns) | mean (ns) | variance (ns²) | stddev (ns) |
| --- | --- | ---: | ---: | ---: | ---: |
| generate | — | 19,480,708; 19,310,696; 16,893,215 | 18,561,540 | 2,094,706,415,092 | 1,447,310 |
| build | — | 6,121,455; 8,231,303; 10,954,453 | 8,435,737 | 5,870,812,362,268 | 2,422,976 |
| merge | initial | 31,976,798; 41,857,550; 48,929,801 | 40,921,383 | 72,508,384,168,419 | 8,515,186 |
| open | — | 89,087; 108,921; 100,389 | 99,466 | 98,986,297 | 9,949 |
| query | exact / `東京-00000000.txt` | 160,054; 183,319; 146,838 | 163,404 | 341,131,040 | 18,470 |
| query | prefix / `report-prefix` | 2,570; 2,806; 2,245 | 2,540 | 79,340 | 282 |
| query | substring / `annual-substring` | 3,738; 3,667; 2,873 | 3,426 | 230,617 | 480 |
| query | common / `common` | 882; 1,268; 846 | 999 | 54,729 | 234 |
| query | no-hit / `no-such-name-9f4e` | 1,209; 1,260; 1,120 | 1,196 | 5,020 | 71 |
| query | unicode / `東京` | 265,522; 300,169; 240,310 | 268,667 | 903,193,239 | 30,053 |
| query | path / `file-` | 1,355; 1,351; 1,109 | 1,272 | 19,849 | 141 |
| query | short / `ab` | 94; 164; 141 | 133 | 1,273 | 36 |
| update | — | 725,061; 776,393; 801,231 | 767,562 | 1,508,961,561 | 38,845 |
| merge | post-update | 27,762,916; 35,698,828; 43,472,147 | 35,644,630 | 61,697,187,693,144 | 7,854,756 |

### 100,000 entries

| operation | query/kind | samples (ns) | mean (ns) | variance (ns²) | stddev (ns) |
| --- | --- | ---: | ---: | ---: | ---: |
| generate | — | 146,940,402; 174,434,510; 147,013,750 | 156,129,554 | 251,304,905,603,728 | 15,852,599 |
| build | — | 35,424,422; 39,633,936; 35,887,411 | 36,981,923 | 5,328,469,417,657 | 2,308,348 |
| merge | initial | 318,216,862; 314,550,606; 309,847,273 | 314,204,914 | 17,602,132,399,224 | 4,195,490 |
| open | — | 595,190; 854,768; 637,143 | 695,700 | 19,416,905,486 | 139,345 |
| query | exact / `東京-00000000.txt` | 472,510; 570,251; 454,131 | 498,964 | 3,895,824,187 | 62,417 |
| query | prefix / `report-prefix` | 2,139; 1,937; 1,591 | 1,889 | 76,804 | 277 |
| query | substring / `annual-substring` | 3,037; 3,235; 2,871 | 3,048 | 33,209 | 182 |
| query | common / `common` | 640; 657; 629 | 642 | 199 | 14 |
| query | no-hit / `no-such-name-9f4e` | 814; 732; 846 | 797 | 3,457 | 59 |
| query | unicode / `東京` | 1,088,103; 1,132,595; 1,379,381 | 1,200,026 | 24,620,956,857 | 156,911 |
| query | path / `file-` | 962; 1,086; 1,163 | 1,070 | 10,284 | 101 |
| query | short / `ab` | 118; 113; 116 | 116 | 6 | 3 |
| update | — | 1,178,403; 1,516,603; 985,645 | 1,226,884 | 72,241,880,721 | 268,778 |
| merge | post-update | 247,577,513; 242,696,442; 258,170,554 | 249,481,503 | 62,580,918,987,211 | 7,910,810 |

## 1M scope

A 1,000,000-entry run was **not run**. Its runtime and temporary index resource use were outside this local evidence budget. These 10k/100k observations must not be extrapolated into a 1M claim.
