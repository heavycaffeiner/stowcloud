# Watch: change detection

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/watch` is referenced as a behavioral specification only.
> The new implementation is written completely from scratch; nothing is
> copied.

## What watch is

The change detector that keeps listings and aggregates honest when
something outside this server writes to a share: one inotify descriptor,
one reader, one debounce loop, one rescan loop. Target
`engine/service/watch`; imports `infra/vfs`, `kit/{clock,num,task}` and
the kernel.

Consumers receive `InvalEvent`s and act; the watcher never touches the
cache itself. That keeps it a sensor, not a writer.

```go
type InvalEvent struct {
    Share vfs.ShareID
    Dir   string // share-relative; empty when All
    All   bool   // events were lost; the whole share is stale
}
```

## The one backend

```go
const BackendInotify Backend = iota
func ParseBackend(s string) (Backend, error)
```

There is exactly one backend, and an unknown name **refuses at startup**
rather than warning and proceeding. The history is the requirement: a
previous configuration accepted `"fanotify"`, warned, and did something
else, and an operator who configures a transport believes they are
running it. `ParseBackend` is the trust boundary.

## The two-tier hot set

Kernel watches are a bounded resource, so which directories carry one is
a set with **two halves, and only one is an LRU**:

- The **recent half** is capped and evicts oldest-first. Eviction
  happens only through the path that also removes the kernel watch; a
  structure that self-evicts would leave watches nothing removes.
- The **sticky half** is refcounted and never auto-evicted: a WebSocket
  subscription pins the directory it watches (and its ancestor chain)
  and releases on unsubscribe. A fully pinned set exceeds the cap
  rather than dropping a pin.

Building only the LRU half is the silent failure mode, stated here so it
cannot happen by accident: the directory a user is currently looking at
is exactly the one an LRU evicts under load, so invalidations stop
arriving for the folder in front of them while everything else works.

## Fail-closed parsing and escalation

- The inotify record parser is fail-closed: a record that does not parse
  whole (short header, name past the buffer) stops the batch and
  escalates, because a parser that resynchronizes by guessing is a
  parser that silently skips events.
- `IN_Q_OVERFLOW`, a pending-queue overflow, or a parse failure all
  escalate to **whole-share invalidation** (`All: true`): what was
  missed is exactly what is unknown, so there is nothing to replay, and
  one generation bump covers everything.
- A registration the kernel refuses (watch limit reached) is a counted,
  named degradation (`Stats.Degraded`), not a failure: that subtree
  falls back to lazy revalidation.

## Rescan

A share on a filesystem whose changes inotify cannot see (network and
FUSE mounts) is marked `rescan` at registration, and the periodic sweep
is the only thing that notices another host's writes. The sweep walks
and emits directory invalidations like any other source.

## Debounce

Events for one directory coalesce over a window (the 50 ms flush loop);
a busy directory produces one invalidation per window, not one per
write. The kernel read buffer batches many events per syscall.

## Deliberate changes

None. The two-tier set, the refusal-not-downgrade backend rule, the
fail-closed parser, the escalation ladder and the rescan marking carry
whole. This package's audit found nothing to fix.

## Tests

- The hot set: the sticky half never evicts; pins are refcounted;
  touching a pinned key changes nothing; a fully pinned set exceeds the
  cap; unregistering leaves the set consistent; the registered set is
  the rescan snapshot.
- A change in a watched directory reports; a subscription pins the
  ancestor chain.
- Overflow: a synthesized `IN_Q_OVERFLOW` yields `All`; a truncated
  record yields `All` (fail-closed parse).
- An unknown backend name refuses at startup (`fanotify` specifically,
  the regression).
- A refused registration counts as degraded and the tree still answers
  through lazy revalidation.
- Debounce: N rapid writes to one directory produce one event per
  window.
