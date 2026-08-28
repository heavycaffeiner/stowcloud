# Upload rebuild: overview

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/upload` is referenced as a behavioral specification only.
> The new implementation is written completely from scratch; nothing is
> copied.

## What the engine is

The resumable-upload state machine every protocol drives: TUS, the
chunked WebDAV compat surface, and the native API all create sessions,
append bytes, and finalize through one engine. The engine does not know
which protocol created a session; the spool-mode names describe what a
mode does, never which client needs it. That protocol isolation held in
the old package and is load-bearing here.

Old package: 3,928 lines source, 2,258 test. The audit found no
defects; its findings are duplications, one dead enum state, one
oversized file, and one boundary wart (the fake share id), all resolved
by this document set.

## Package layout

```
engine/service/upload/
  model.go       SessionID, SessionState, SpoolMode, Algo, Checksum, Session, SessionSpec
  errors.go      the typed refusal set
  engine.go      Engine, Options, New, the two-lock discipline, Close
  session.go     Create, Get, Offset, SetLength, Abort, the row lifecycle
  spool.go       PutNamed, PatchAt, the direct spool write path
  intervals.go   IntervalSet and its invariants
  alias.go       transfer-id binding
  verify.go      whole-file verification
  finalize.go    Assemble, Finalize, publish
  cachelayout.go the spool naming, parsing, recovery      (split of old cache.go)
  cachemerge.go  the merger and its wake/progress protocol (split)
  cacheadmin.go  the enable switch, budget math            (split)
  settings.go    chunk-size settings
  sweep.go       expiry and orphan collection
```

The three-way split of the old 1,027-line `cache.go` is the audit's
finding 3: naming/layout, the merger, and the admin surface change for
different reasons.

## The documents

| Document | Contents |
| --- | --- |
| [01-session-lifecycle.md](01-session-lifecycle.md) | Session identity, states, create/get/abort/expiry, aliases, the two-lock discipline, the sweep |
| [02-spool-modes.md](02-spool-modes.md) | Offset-addressed and name-ordered writing, intervals, assembly |
| [03-cache-spool.md](03-cache-spool.md) | The cache window, the merger, budgets, recovery, and the safe-root capability that replaces the fake share id |
| [04-verification-and-limits.md](04-verification-and-limits.md) | Checksums, whole-file verify, finalize, the trust-boundary checks |

## Dependencies

| Dependency | Role |
| --- | --- |
| `engine/service/core` | Resolve results in, `PublishPart` out; the ACL check at create |
| `engine/store/state` | the upload aggregate: sessions, intervals, aliases, chunk settings |
| `engine/infra/vfs` | part files, durable writes, the spool root |
| `engine/kit/{clock,limits,num,task}` | time, bounds, narrowing, the merger goroutine |

The state-side row shape: the audit (finding 13) notes
`state.UploadSession` is a 27-field struct mixing sub-concerns. The
rebuild groups it into embedded sub-structs (destination/identity,
assembly cursor, cache bookkeeping, lifecycle stamps) **in the store's
row type**, without changing a column: the grouping is Go shape, not
schema.

## Deliberate changes

1. **`SetChunkSettings` is dropped; `ApplySettings` stays** (audit
   finding 1): two write paths did the same job and only one had a
   production caller.
2. **`StateFinalizing` becomes real** (audit finding 2). The old enum
   declares it and nothing ever sets it, while the expiry logic already
   treats it as non-expiring; a session that is publishing is not
   receiving and must not expire mid-publish. `Finalize` sets it before
   the first byte moves and the terminal state replaces it. The
   alternative, deleting the state, would leave a long assembly
   sweepable at exactly the wrong moment.
3. **`Abort` forgets the row lock** (audit finding 4): the old code
   leaves an aborted session's mutex in the map until the sweep. The
   lifecycle table in 01 makes handle-close and row-forget one step.
4. **The fake share id dies** (audit finding 12). The cache spool opens
   through a vfs capability for non-share safe roots instead of minting
   `ShareID(0)`; 03 owns the shape, and the vfs change is one exported
   constructor (`vfs.OpenScratchRoot`) whose admission rules are the
   same minus the share-id semantics.
5. **The 27-field row groups into sub-structs** (audit finding 13),
   store-side, column-compatible.
6. **`cache.go` splits in three** (audit finding 3).

## Feature inventory

| Old surface | Document |
| --- | --- |
| `SessionID` mint/parse, `Session`, `SessionSpec`, `Meta` | 01 |
| `Create`/`Get`/`Offset`/`SetLength`/`Abort` | 01 |
| `BindAlias`/`LookupAlias`/`UnbindAlias`, transfer-id validation | 01 |
| `Sweep`, `SweepReport`, orphan classes | 01 |
| `PutNamed`/`PatchAt`, `ListChunks`, the two-lock discipline | 02 |
| `IntervalSet` (insert, contiguous prefix, missing, complete, run bound) | 02 |
| `Assemble` | 02 |
| Cache: budgets, merger, `RecoverCache`, `SetCacheEnabled`/`CacheEnabled`/`CacheAvailable`, `CacheFullError` | 03 |
| `Algo`/`ParseAlgo`/`Algorithms`, `Checksum`/`ParseChecksum`, `VerifyWholeFile` | 04 |
| `Finalize`, the destination re-check, `PublishPart` handoff | 04 |
| `Settings`, `ApplySettings`, the floor/default/override ladder | 04 |
| The typed errors (`CacheFullError`, `ConflictError`, `ChunkTooSmallError`, `IncompleteError`, `ExhaustedError`, ...) | each at its surface |

## Platform

`//go:build linux` throughout, as the old package: the types are openat2
handles underneath.
