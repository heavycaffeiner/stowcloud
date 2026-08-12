# Phase briefs

One file per phase of the Go rewrite. Each is a complete brief: read it and you
know what to build, what not to get wrong, and when you are done.

## Running one

```
/goal Read "docs/phases/Phase 0.md" and then go
```

Then, when it lands, the next:

```
/goal Read "docs/phases/Phase 1.md" and then go
```

Each file's **Done when** section is the completion condition for that goal.
Nothing else in the file is.

## Why one goal per phase and not one for the rewrite

Three reasons, in order of weight. Phase 5 alone is large enough to exhaust a
context, so a single goal would be interrupted mid-phase with no clean
boundary. Several phases can invalidate the plan (Phase 0c settles four
assumptions by compiling; Phase 2e can send Phase 2 back), and those decisions
should reach a person rather than be absorbed. And a phase boundary is where
the gate is green and the tree is committable, which is the only place it is
safe to stop.

## Order

`docs/proposals/stowcloud-0-motivation-and-findings.md` §6-1 holds the
dependency table. The short form:

```
0 -> 1, 2
1, 2 -> 2.5 -> 3
3 -> 4, 5, 11
4 -> 5, 6, 7, 8, 9
5 -> 7, 10, 11, 12  6 -> 7, 10, 12
7 -> 10   8 -> 10   9 -> 10
10, 11, 12 -> 13
```

Phases 6, 8 and 9 are independent of each other and can be taken in any order.
**Phase 7 is not independent of Phase 6**: `/dav-uploads` is backed by the
upload engine's name-ordered spool mode.

## One thing to do out of order

**Milestone 8a runs against the Rust tree**, which Phase 13 deletes. It emits
the golden fixtures the whole search port is verified against, it depends on
nothing, and it can be done at any time. Do it early. Losing the chance costs
the strongest verification in the plan.

## The files

| File | Phase | Proposals |
|---|---|---|
| [Ground rules.md](Ground%20rules.md) | every phase reads this first | 0, 1 |
| [Phase 0.md](Phase%200.md) | gate and toolchain | 2 |
| [Phase 1.md](Phase%201.md) | vfs, paths, jail, hardening | 3, 4 |
| [Phase 2.md](Phase%202.md) | store and schema | 5 |
| [Phase 2.5.md](Phase%202.5.md) | contract corrections before auth | 0, 1, 2, 3, 5, 6, 8, 15, 17 |
| [Phase 3.md](Phase%203.md) | auth and ACL | 6 |
| [Phase 4.md](Phase%204.md) | core domain | 7 |
| [Phase 5.md](Phase%205.md) | HTTP and API | 8 |
| [Phase 6.md](Phase%206.md) | upload | 9 |
| [Phase 7.md](Phase%207.md) | WebDAV | 10 |
| [Phase 8.md](Phase%208.md) | search | 11 |
| [Phase 9.md](Phase%209.md) | preview and the jailed worker | 12, 4 |
| [Phase 10.md](Phase%2010.md) | Nextcloud compatibility | 13 |
| [Phase 11.md](Phase%2011.md) | SMB, OIDC, operational surface | 14, 15 |
| [Phase 12.md](Phase%2012.md) | frontend API client | 16 |
| [Phase 13.md](Phase%2013.md) | parity and cutover | 17 |
