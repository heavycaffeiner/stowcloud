# Preview rebuild: overview

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/preview` (and `go/internal/jail`,
> `go/internal/preview/worker`) is referenced as a behavioral
> specification only. The new implementation is written completely from
> scratch; nothing is copied.

## What preview is

Thumbnails and archive listings over hostile input. Every image decoded
here arrived from a user, so the whole subsystem is built around one
assumption: **the decoder will be exploited someday**, and when it is,
the process it runs in must have nothing worth taking. That is the
worker: a separate process, jailed before it reads its first message,
holding descriptors instead of paths, decoding under pixel and byte
ceilings it enforces on itself whatever the parent said.

## Package layout

```
engine/service/preview/         the service: cache, presets, decode, exif, archive
engine/service/preview/worker   the jailed process's main loop
engine/infra/jail               the sandbox: seccomp, Landlock, rlimits, re-exec
```

`jail` stays domain infrastructure (its only import is `kit/num`); the
seqpacket descriptor-passing transport moves **here from vfs** (the
survey's second intruder), as `worker/transport.go`.

## The documents

| Document | Contents |
| --- | --- |
| [01-jail.md](01-jail.md) | seccomp, Landlock, rlimits, re-exec, and the dead-exports decision |
| [02-worker-protocol.md](02-worker-protocol.md) | the wire codec, descriptor passing, decode limits, the pool |
| [03-cache.md](03-cache.md) | the thumbnail cache, negatives, presets |
| [04-archive-listing.md](04-archive-listing.md) | zip central-directory listing |

## The verified defences (must survive)

The audit verified these sound; each is a requirement in its document:

- Format sniffed from magic bytes, never a declared name; dimensions
  checked before the pixel buffer is allocated **and** after decode, in
  case a decoder ignored its own header.
- EXIF parsing with bounded entry count, bounded scan prefix, recursion
  capped at 2, every offset checked before indexing.
- The worker jailed before the control socket opens; paths never cross
  the boundary, only descriptors over `SCM_RIGHTS`.
- The parent can lower the worker's decode ceiling, never raise it.
- The worker re-enforces its own input ceiling with a limit reader,
  defence in depth against its own parent.
- Thumbnail writes are durable atomic replaces.
- Dead workers reaped and lazily replaced; every failure path in pool
  start closes what it opened.

## The two big decisions

1. **The dead security exports become real** (audit defect, medium:
   `jail.ApplyLimits` and `jail.SealDescriptors` are exported, described
   by comments across three files as load-bearing, and called by
   nothing). The rebuild wires both into worker startup: RLIMIT_AS
   becomes the real backstop the decode-limit comments claim, and
   descriptor sealing closes what the worker inherited beyond its
   control socket. 01 specifies the order. The alternative, deleting
   them, would leave the in-process pixel ceiling as the only memory
   bound, and a decoder exploit is exactly the case where in-process
   bounds stop counting.
2. **The seqpacket transport moves from vfs to the worker package.**
   vfs is the filesystem boundary; a socketpair codec never belonged
   there.

The audit's suggested separate decode-limits document lands inside 02
instead, as the document plan already decided.

## Feature inventory

| Old surface | Document |
| --- | --- |
| `jail.Apply`/`Policy`/`ParsePolicy`/`PolicyNames`/`Status`/`Refuse` | 01 |
| `jail.InstallSeccomp`/`FilterKind`/`ABIVersion`/`AllowedSyscalls` | 01 |
| `jail.Limits`/`DefaultLimits`/`ApplyLimits` (now wired) | 01 |
| `jail.RestrictAndReexec`/`Reexeced`/`SealDescriptors` (now wired) | 01 |
| `Request`/`Response`/`Encode`/`Decode`, the version check | 02 |
| `Pool`/`NewPool`/`Generate`, worker reaping, deadlines | 02 |
| `DecodeBounded`/`DecodeLimits`/`DefaultDecodeLimits`, `Sniff`, `EncodePNG` | 02 |
| `ReadOrientation`/`Orientation` | 02 |
| `worker.Run`, GOMAXPROCS(1), the jail-then-socket order | 02 |
| `Cache`/`NewCache`/`Put`/`Get`, `Negatives`, `Preset`s | 03 |
| `ListArchive`/`ArchiveListing`, `safeArchiveName`, entry caps | 04 |
| `Service`/`NewService`/`ServiceOptions`, the ACL check before work | 03 |

## Platform

Linux-only throughout: the jail is seccomp and Landlock, and the service
holds share roots.
