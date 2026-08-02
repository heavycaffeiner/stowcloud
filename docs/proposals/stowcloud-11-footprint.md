# Resource Footprint: 32 GB SSD, 12 TB HDD RAID - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-03                       |
| Status     | Implemented                      |
| Reviewers  |                                  |

---

## 1. Summary

The hardware floor this project is designed against, and what it forces. A
32 GB system SSD with 2–8 GB of RAM, and a 12 TB rotational RAID for data,
shared with whatever else the box runs.

Two findings from validating against that floor changed the schema and the
watch strategy outright.

## 2. Background & Motivation

A design that only works on generous hardware is a design that has not been
validated. Picking a deliberately modest floor turns "how much does this cost"
from an opinion into arithmetic — and twice it produced an answer that
invalidated a design already written down.

| Item | 32 GB SSD |
|---|---|
| OS + container runtime | ~8 GB |
| image + binary | ~1 GB |
| headroom: WAL, temp, logs, upgrades | ~4 GB |
| **available to spend** | **~18 GB** |
| — SQLite target | ≤ 4 GB, hard guard, off by default |
| — thumbnail cache | ≤ 2 GB default |

## 3. Goals & Non-Goals

### 3.1 Goals

- [x] Every persistent structure has a measured per-file cost.
- [x] A directory rename stays O(1), not O(subtree).
- [x] Immediate change reflection that degrades rather than fails when the
      kernel's watch limit is reached.
- [x] Honest performance targets for cold rotational storage.

### 3.2 Non-Goals

- [ ] A size guard that is on by default. It exists, and it stays off: an
      instance that stops accepting writes because a cache grew is worse than
      one that uses more disk than expected.
- [ ] Tuning for NVMe. The fast path is welcome, but the design is validated
      against the slow one.

## 4. Technical Design

### 4.1 Finding 1 — a path column broke the budget

The original schema stored the full path per file. Average path depth in a
deep tree is 80–150 bytes, and it gets indexed too:

| Files | with `path` | with `(parent, name)` |
|---|---|---|
| 1 M | ~160 MB | ~105 MB |
| 10 M | ~1.6 GB | ~1.05 GB |
| 60 M | **~10 GB** | ~6.3 GB |

Size was not even the worst of it. **Renaming a directory meant updating the
path on every descendant row** — an O(1) operation on the filesystem becoming
O(subtree) in the database, so a rename under a 100k-entry directory was a
100k-row update.

The fix is `(parent, name)` normalisation in a single node table, with the
rowid serving as the stable file id so identity costs no extra storage. The
old metadata and search tables stored overlapping tuples; merging them removed
the duplication as well.

### 4.2 Finding 2 — "reflects immediately" collides with the watch limit

`fs.inotify.max_user_watches` cannot be raised from inside a container, and a
watch is per directory. A large tree therefore cannot be fully watched, and a
design that assumed it could was making a promise the kernel does not sell.

So the watch strategy is tiered rather than absolute: a hot set is watched
directly, and everything else falls back to lazy revalidation plus a periodic
rescan. A queue overflow bumps the share generation, which invalidates every
cached aggregate at once rather than trying to replay what was missed.

The property that survives is the one that matters: a stale answer is
detectable and self-correcting, never silently wrong.

### 4.3 The size guard, and why it is off

A hard cap on the database exists and defaults to off. Turning it on means an
instance can refuse writes to protect its own disk — correct for an operator
who has decided that, wrong as an unrequested default, because the failure it
produces is worse than the condition it prevents.

The thumbnail cache and the optional search index both live **outside** that
guard, on the data volume, and are independently deletable. That separation is
what lets each be reclaimed without touching the other, and it is a hard
requirement on anything added later.

### 4.4 Cold rotational storage

Every timing in this project is stated with its cache state, because warm and
cold differ by orders of magnitude on rotational media: a tree walk that takes
under a second warm takes tens of minutes cold, one seek per directory.

Consequences that show up elsewhere as design decisions: thread counts drop on
rotational media to avoid seek thrash, metadata reads are batched in inode
order so the disk seeks forward monotonically, and search deadlines and
concurrency caps are storage-class aware with the *slower* tier winning when a
query spans both.

## 5. API Design

No wire surface of its own. What this specifies is consumed as configuration:
the database size guard and its free-space margin, the thumbnail cache cap,
and the storage-class detection that the search and watch subsystems read.

Storage class is detected once per share from the kernel's rotational flag
plus an NVMe check, or from the filesystem type for network mounts, and
cached — a per-request probe would itself be a cost.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Duration | Owner |
|---|---|---|---|
| Phase 1 | Schema normalisation to `(parent, name)` | done | heavycaffeiner |
| Phase 2 | Tiered watch strategy + generation invalidation | done | heavycaffeiner |
| Phase 3 | Size guard, free-space margin, cache cap | done | heavycaffeiner |
| Phase 4 | Storage-class detection and its consumers | done | heavycaffeiner |

### 6-2. Dependencies

- `statvfs` for free space; `/sys/block/*/queue/rotational` for storage class.
- SQLite in WAL mode, with incremental vacuum driven by the housekeeping
  command.

## 7. References

- `crates/sc-meta/`, `crates/sc-server/src/storage_class.rs`
- `stowcloud-2-core-vfs.md` (the node table and aggregate ETag this budgets),
  `stowcloud-5-search.md` (the index kept outside the guard),
  `stowcloud-6-preview-sharing.md` (the thumbnail cache cap)
