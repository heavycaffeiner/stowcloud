# Footprint

Both builds, same host, same share, same requests. The Rust build is the
optimised profile, not the debug one: comparing a debug binary against an
optimised one measures the profile rather than the port.

## The expectation, written before the measurement

Two numbers were expected to regress, and saying so first is the point: an
expectation written after the fact is a description.

- **The preview worker pool.** The Rust build forks its workers and the Go build
  execs them, so each worker is a fresh process image rather than a
  copy-on-write child. More resident memory per worker is the cost of the
  worker being a real process, which is what makes the sandbox cover it.
- **Possibly the SQLite write path**, because the driver is a translation rather
  than the C library.

## Measured, 2026-08-21

| Measure | Rust (release) | Go | Verdict |
|---|---|---|---|
| resident, idle | 24.9 MB | 11.5 MB | Go lower |
| resident, after a 200-directory walk | 56.9 MB | 103.5 MB | **Go higher** |
| that walk's wall time | 16.27 s | 10.20 s | Go faster |
| binary size | 16.8 MB | 19.2 MB | Go larger |
| image size | not built here | 19.2 MB | under the 30 MB target |

The walk is 200 directories of 25 files each, listed one directory per request
over the API, with both servers holding the same share and answering the same
25 entries per directory.

## What the numbers say

**Idle memory is less than half.** A Go runtime with a garbage collector was the
thing most likely to lose here and it does not, at rest.

**The walk costs roughly twice the memory and takes two thirds of the time.**
That is the trade this port makes rather than a defect: the Go build materialises
more per request and answers faster. It is inside the budget the resource target
sets, on a machine far smaller than the 32 GB the design assumes.

**Neither predicted regression was measured.** The preview pool and the SQLite
write path both need a workload this run did not have: no previews were
generated and no sustained write path was exercised. Those two lines are
therefore not "no regression", they are "not measured", and the difference
matters because they are exactly the two the expectation named.

## The preview worker pool, measured 2026-08-23

The regression the expectation named most confidently, taken against real
workers decoding a real image. `TestWorkerPoolFootprint` in `internal/preview`.

| Workers | Resident processes | Total | Each |
|---|---|---|---|
| 1 | 1 | 10.0 MB | 10.0 MB |
| 2 | 2 | 20.6 MB | 10.3 MB |
| 4 | 4 | 38.7 MB | 9.7 MB |

**The prediction holds and the shape is the useful part.** Cost is linear in
the worker count at about 10 MB each, with no shared page to amortise, which is
exactly what exec'ing a worker rather than forking one buys and costs. A fork
would show the second and fourth workers costing far less than the first. The
sandbox is what the difference pays for.

Read from `/proc/<pid>/status` rather than from inside the process: what
matters is what the kernel accounts to it, which a runtime statistic does not
report.

**One thing this measurement found that it was not looking for.** The pool has
no caller anywhere in the server, so these numbers describe a subsystem that
never runs in the product as it ships. They are what the sizing needs once it
does. `RISKS.md` has the finding.

## What is not measured here
- **The SQLite write path** under sustained writes.
- **Cold node population time** against the store proposal's threshold.
- **Steady-state invalidation rate**, which needs the watcher under real churn.
- **The image on both architectures.** Only this machine's architecture was
  built.

Each of those is a measurement that was planned and not made, listed rather than
quietly dropped, because a footprint document that reports only what was
convenient to measure is the kind of measurement that cannot change a decision.
