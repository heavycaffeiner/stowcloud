# Compatibility layer - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

The layer that makes existing sync clients work, and the two mechanisms that
keep it from invading the core. The `grep` that holds principle 4 today becomes
an import-graph check, which reads the real dependency edges instead of the
source text.

## 2. Background & Motivation

`sc-compat-nc` is 10,825 lines and `sc-server/src/nc*.rs` adds another 3,200.
Together they are the largest surface in the port and the one with the least
freedom: the shape is set by clients this repository does not control, so almost
none of it can be improved, only reproduced.

Principle 4 says the compat layer does not invade the core, and that it must be
removable in its entirety by a compile flag. Two things enforce it today and one
of them is weak: the `--no-default-features` build is real, and the `grep` for
`oc:`, `ocs` and `remote.php` in core source reads text, so a constant defined
elsewhere or a name without the vendor prefix passes it while doing exactly what
it exists to prevent (F13).

The boundary itself is well designed and is carried over unchanged in shape: the
compat layer defines port interfaces, the assembly layer implements them, and
the compat layer consumes only public APIs of the core crates. Nothing in the
core-facing half of that boundary carries compat vocabulary, which is why the
grep can be run over it at all.

**Why the boundary is defended this hard.** The clients exist, they are good,
and people already run them; reimplementing a desktop and two mobile apps to
reach the same tree would be the larger project by far. So the layer is worth
having. The risk is equally plain: a compatibility layer that leaks its
vocabulary into the core makes the core permanently shaped by someone else's
protocol, and that shape is not reversible once other subsystems have been
written against it. Every decision in this document follows from refusing that,
which is why the isolation gets three gates and not one.

**Two stances decide what this layer will and will not do**
([`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5):

- **S4.** A capability that does not exist is not advertised, in the
  documentation *and* in the capabilities response. Advertising one is worse
  than not having it, because the client then fails in the middle of someone's
  work rather than at configuration time. The non-goal list in §3.2 is
  therefore also a wire-level commitment.
- **S14.** Where a client-side defect could be papered over by sending
  something untrue, it is not. The Android "synced" tick is the worked example:
  three server-side suppressions exist and all three are lies about the data (a
  false size, a withheld ETag, a false encryption flag), so none is used and
  the evidence is recorded instead.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] Sync, browse, share and preview working against the existing desktop and
      mobile clients.
- [ ] The isolation rule enforced by the import graph (F13).
- [ ] A build with the layer stripped that links, gated in CI.
- [ ] Chunked upload v2 over the name-ordered spool mode.
- [ ] OCS: capabilities, shares, the login flow, users.
- [ ] The mobile surfaces: search, favourites, trashbin, the recency query.
- [ ] `oc:fileid` stable across a cache rebuild, per
      [`stowcloud-5`](stowcloud-5-store-and-schema.md) §4.5, and the one-time
      change at cutover written down where an operator will find it.

### 3.2 Non-Goals

- [ ] Anything past the boundary: versioning, comments, tags, groupware,
      federation, office-suite integration, activity streams, external storage
      mounts, workflows. Beyond the sync, browse, share and preview boundary
      lies rebuilding the server this layer only pretends to be.
- [ ] Suppressing the Android client's "synced" tick. All three server-side ways
      to do it are lies about the data. The client asks its own database
      whether every file in a folder has a local copy, with a query that is
      vacuously true for a folder whose children have never been listed on the
      device. Every input to that guard comes from this server and every one is
      already truthful: the recursive size is real, the folder ETag is real,
      and the encryption flag is `0`, which is what a reference server answers
      for an unencrypted folder too. The three server-side suppressions are a
      false size, a withheld ETag and a false encryption flag, so none is used.
- [ ] Improving the OCS shapes. They are wrong in ways this repository cannot
      fix, and reproducing them exactly is the job.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/compat/nc          all files carry //go:build compat_nc
  ports.go       the interfaces the assembly layer implements
  router.go      the mounts: /remote.php/..., /ocs/..., /index.php/...
  dav.go         the WebDAV decoration and the vendor property source
  props.go       oc:, nc: and d: properties
  chunking.go    upload v2
  ocs.go         the OCS envelope
  capabilities.go
  shares.go
  login_flow.go
  search.go  trash.go  favorites.go  recent.go
  store.go       favourites and instance identity
```

The direction of dependency is one way and it is now checkable:

```
internal/compat/nc  ->  internal/{core,dav,auth,acl,store,upload,preview}
                    ->  never the reverse
```

### 4.2 The isolation gates

Three, replacing two.

**1. The import graph.** In `verify.sh`:

```sh
go list -deps ./internal/core/... ./internal/dav/... ./internal/auth/... \
              ./internal/acl/... ./internal/store/... ./internal/upload/... \
              ./internal/vfs/... ./internal/preview/... ./internal/search/... \
  | grep 'internal/compat' && exit 1
```

This reads the real dependency edges the compiler resolved. The grep it replaces
reads source text, so an indirection defeats it; an indirection is exactly what
this catches, because the indirection is still an import.

**2. The stripped build.** `go build -tags '' ./...` must link. Every file in
`internal/compat/nc` carries `//go:build compat_nc`, and every reference to it
from the assembly layer is behind the same tag with a no-op alternative. This is
the direct replacement for `cargo build --no-default-features`.

**3. The text scan, narrowed.** The `oc:`, `ocs` and `remote.php` grep is kept
and pointed at strings and comments only, because it catches a mistake the graph
does not: a core package that has learned the vocabulary without importing
anything, for example an error message mentioning `remote.php` or a field named
after a vendor property.

Keeping the weak gate alongside the strong one is deliberate. It costs
milliseconds and it catches a different class.

### 4.3 Data Model Changes

`favorite` moves into `state.db`, keyed by the identity tuple, which is part of
what deletes the `PINNED` bit
([`stowcloud-5`](stowcloud-5-store-and-schema.md) §4.2.2). The instance
identity, used to build `oc:id`, is a `settings` row.

`oc:fileid` now survives a cache rebuild.
[`stowcloud-5`](stowcloud-5-store-and-schema.md) §4.5 derives `node.id` from the
file's identity instead of letting SQLite mint a rowid, so deleting the cache
costs a rebuild and not a full client reconciliation. That is a change in
behaviour from the current tree, where every id is re-minted, and it is the
reason principle 1 ("the database is a cache you can delete") is true for a
sync client here and is not true today.

Two consequences this layer owns:

- **The one-time change at cutover.** The Rust build's ids were rowids and the
  Go build's are derived, so every `oc:fileid` changes once when the binary is
  replaced, and every attached client performs a full reconciliation. Stated in
  the release notes and in the operator documentation, not discovered.
- **`oc:id` follows.** It is the fileid plus the instance identity, so it is
  stable on the same terms and changes on the same occasions
  ([`stowcloud-5`](stowcloud-5-store-and-schema.md) §4.5.4 lists them).

### 4.4 Core Logic

#### 4.4.1 The ports

The compat layer defines interfaces; `internal/server` implements them from the
real core. This is dependency inversion at the boundary, and it is what lets the
whole layer be stripped: the core has never heard of the interfaces.

```go
// CorePort is what the compat layer needs from the domain. Note what is not
// here: nothing takes or returns a vendor type, and nothing carries a vendor
// name. A ShareID is passed rather than a ShareRoot, because this layer never
// dereferences a directory handle and taking one would mean carrying an open
// descriptor it has no use for.
type CorePort interface {
    List(ctx context.Context, u UserID, p Vpath, cur Cursor) (Page, error)
    Stat(ctx context.Context, u UserID, p Vpath) (core.Entry, error)
    Vpath(share ShareID, sp SharePath) (Vpath, error)
    // ...
}
```

`Vpath` is on the interface for a specific reason recorded in the current tree:
a `SharePath` already has the grant's subpath on its front, and prefixing a
share label onto it without stripping that subpath is the bug the two types
exist to prevent. The conversion lives in the core, and this layer asks for it
rather than doing it.

#### 4.4.2 The mounts

`/remote.php/dav/...`, `/remote.php/webdav/...`, `/ocs/v1.php/...`,
`/ocs/v2.php/...`, `/index.php/...`, and the status endpoint. Each is a
`net/http` handler registered by the assembly layer only when the tag is on.

Every mount resolves the client address through the same
`TrustedProxy` step as the native API
([`stowcloud-8`](stowcloud-8-http-and-api.md) §4.3.1), because there is exactly
one implementation of "who is this request from" and a compat mount with its own
copy is how that stops being true.

#### 4.4.3 Properties

The vendor property source registers with `internal/dav` through the
`PropSource` interface. `internal/dav` emits what the source returns and knows
nothing about `oc:` or `nc:`.

Two behaviours carried over because they were arrived at by fixing something:

- A missing file id emits the sentinel rather than dropping the whole property
  set. A wrong id is visible and debuggable; a silently missing entry is not.
- A share-kind lookup that fails falls back to "not shared" rather than dropping
  the set, because a missing `oc:permissions` is worse than a missing `S`.

#### 4.4.4 Chunked upload v2

The client uploads numbered parts into a collection and then moves the
collection onto the destination. It carries no offsets, so assembly is by name
order, which is why [`stowcloud-9`](stowcloud-9-upload.md) has a
`SpoolNameOrdered` mode named for what it does rather than for the client that
needs it.

The mode's existence in the core-facing crate is the one place the boundary is
under real tension, and the resolution is the same as today: the core may know
that name-ordered assembly is a thing, and may not know who asked for it.

#### 4.4.5 OCS

The envelope, `format=json` and XML, and the status codes that are in the body
rather than the response. Reproduced exactly, including the parts that are
wrong, because a client parses them.

A refusal that OCS produces before any handler runs is logged, which the current
tree added after the case existed and was invisible.

#### 4.4.6 The mobile surfaces

Search, favourites, trashbin, and the recency query. The recency query is the
one with a recorded defect: a bare date literal made both phone apps' request a
400, and the fix is ISO-8601 timestamps
([`stowcloud-8`](stowcloud-8-http-and-api.md) §4.4). The collector behind it is
unchanged; only the query shape moves.

## 5. API Design

### 5-1. New / Modified

No new external surface. Every route in this phase reproduces one a client
already calls, and the specification for each is the proposal listed in §7 plus
the client's own behaviour.

The internal surface is §4.4.1's interfaces, and they are the whole of this
layer's contract with the rest of the tree.

### 5-2. Error Handling

Compat mounts do not use [`stowcloud-8`](stowcloud-8-http-and-api.md)'s
envelope. They use the shapes their clients expect:

| Surface | Error shape |
|---|---|
| `/remote.php/dav` | RFC 4918 multistatus and status codes |
| `/ocs/*` | the OCS envelope, with the real status in `meta.statuscode` |
| chunked upload | WebDAV status codes on the collection |

The mapping from a domain error to each shape lives in this layer, which is
correct: the core does not know these shapes exist, and
[`stowcloud-8`](stowcloud-8-http-and-api.md)'s mapper is for the native API
only.

The existence rule survives the translation: a path outside a grant is 404 on
every compat mount too, and the test for that is a table run against every
mount rather than against the native API alone.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 10a | `ports.go`, the build tag on every file, the three gates in §4.2, the stripped build in CI | S | Phase 5 | heavycaffeiner |
| Phase 10b | `router.go`, `dav.go`, `props.go`: the WebDAV mounts and the property source | L | 10a, Phase 7 | heavycaffeiner |
| Phase 10c | `chunking.go` | M | 10b, Phase 6 | heavycaffeiner |
| Phase 10d | `ocs.go`, `capabilities.go`, `login_flow.go`, `shares.go` | L | 10a | heavycaffeiner |
| Phase 10e | `search.go`, `trash.go`, `favorites.go`, `recent.go` | M | 10b, Phase 8 | heavycaffeiner |

10a is small and blocks everything, so it lands first even though it produces no
client-visible behaviour. 10d is independent of 10b.

### 6-2. Dependencies

None beyond what the phases it depends on already bring. This layer is
translation and routing.

**Non-code dependency**: a real desktop client and a real mobile client, run
against the result. Phase 13 has the harness, but this phase is where a wrong
guess about client behaviour is cheapest to find, so a manual run happens at the
end of 10b, 10c and 10e rather than being saved up.

## 7. References

- `crates/sc-compat-nc/src/`: the whole layer this translates, in particular
  `router.rs`, `props.rs`, `chunking.rs`, `ocs.rs`, `shares.rs`,
  `login_flow.rs`, `capabilities.rs`.
- `crates/sc-server/src/nc.rs`, `nc_search.rs`, `nc_trash.rs`, `recent.rs`: the
  assembly-side half, including the recency query §4.4.6 fixes.
- `scripts/verify.sh:140`: the text grep §4.2 keeps as the third gate.
- `crates/sc-server/tests/compat_mobile.rs`,
  `crates/sc-compat-nc/tests/http_integration.rs`: the behavioural tests a
  ported mount has to keep passing in spirit.
- `crates/sc-compat-nc/src/ports.rs`: the boundary this reproduces, including
  the note on why a `ShareID` is passed rather than a `ShareRoot`.
- `crates/sc-compat-nc/src/props.rs:256`: the two fallbacks §4.4.3 carries over.
