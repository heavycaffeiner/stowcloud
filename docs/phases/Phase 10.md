# Phase 10: Nextcloud compatibility

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-13-compat-nc.md`.

## Scope

The layer that makes existing sync clients work, and the three-package seam
that keeps it from invading the core.

Depends on Phases 5, 6, 7, 8 and 9, one per surface: the mounts, chunked
upload, WebDAV, mobile search, thumbnails. Blocks Phase 13.

## Milestones

- **10a**: `ncport/`, `ncwire/`'s skeleton, the build tag on every file, and all
  five gates wired into `verify.sh` **before a handler exists**.
- **10b**: `router.go`, `dav.go`, `props.go`.
- **10c**: `chunking.go`.
- **10d**: `ocs.go`, `capabilities.go`, `login_flow.go`, `shares.go`.
- **10e**: `search.go`, `trash.go`, `favorites.go`, `recent.go`.
- **10f**: `preview.go`, `direct.go`, `stubs.go`.

10a is small and blocks everything. 10d is independent of 10b.

## Traps

- **The gates land before the code they govern.** A boundary added afterwards is
  a refactor, and this one has been argued for in this codebase once already.
- **Three packages, not one.** `ncport` is the seam and may import core; `nc`
  may import `ncport` and **nothing else** from `internal/`; `ncwire` is the
  only package that sees both sides.
- **Type aliases, not mirror types.** `type Entry = core.Entry` is what makes
  strictness cost no conversion code. The Rust tree tried mirrors once and they
  drifted.
- **G2 checks direct imports, not transitive.** `nc` reaches core types through
  `ncport` by design and would fail its own gate under a transitive check.
- **`StatePort` exists because G2 forbids `nc` from importing the store.** The
  only SQL that knows what a favourite is lives on the core side of the seam.
- **Every file in all three packages carries `//go:build compat_nc`**, and the
  assembly layer's reference lives in one tagged file with a no-op sibling.
- **Capture the compat request corpus from a real client session**, do not write
  it. A guess about what a client sends is the thing this phase exists to stop
  trusting.
- **Reproduce the OCS envelope exactly, including the parts that are wrong.** A
  client parses them.
- **The stub endpoints' exact shape is a client-crash workaround.** Four paths
  must answer **404 rather than an empty success**, and a success returns an
  empty **array** where the client expects a list and an empty **object** where
  it expects a record. The Android client's status probe only special-cases
  404; given a `200 {}` it hands the body to Gson, which allocates a
  non-nullable Kotlin field through `Unsafe.allocateInstance` and leaves it
  null. Rationalising this into "200 with an empty body everywhere" reproduces
  a crash somebody already debugged.
- **The `direct` endpoint** resolves and ACL-checks one file id at issue time
  under the caller, lives minutes, is GET and read only, and is content-origin
  only. The player process carries none of the caller's credentials, which is
  both why it exists and why it is the sharpest thing here.
- **The existence rule holds on every compat mount.** Test it as a table across
  all of them, not against the native API alone.
- **Do not suppress the Android "synced" tick.** All three server-side ways are
  lies about the data: a false size, a withheld ETag, a false encryption flag.
- **`Vpath` conversion lives in the core**, and this layer asks for it. Doing it
  here is the bug the two path types exist to prevent.

## Done when

- The gate is green, including `-race`.
- All five isolation gates pass, and `go build -tags '' ./...` links.
- F13 is closed.
- A real desktop client and a real mobile client each complete an initial sync,
  see a server-side change, and produce a deliberate conflict.
