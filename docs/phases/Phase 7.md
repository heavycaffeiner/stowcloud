# Phase 7: WebDAV

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-10-webdav.md`.

## Scope

RFC 4918 Class 2, the hardened XML scanner, the hand-written multistatus
writer, locking, dead properties, `SEARCH` and `REPORT`, `/dav-uploads`.

Depends on Phases 2, 4 and 6. Blocks Phase 10.

**Phase 7 is not independent of Phase 6.** `/dav-uploads` is backed by the
upload engine's name-ordered spool mode, so 7f waits on it.

## Milestones

- **7a**: `scan.go`, its fuzz target, the escaping helpers.
- **7b**: `write.go`: the hand-written multistatus writer.
- **7c**: `propfind.go`, `props.go`, the streaming path, the decorator hook.
- **7d**: `proppatch.go` and dead-property storage.
- **7e**: `lock.go`, the `If` parser and its fuzz target.
- **7f**: `search.go`, `method.go`, `uploads.go`.

7a and 7b can start before Phase 4 lands.

## Traps

- **`encoding/xml` surfaces `xml.Directive` and `xml.ProcInst` as ordinary
  tokens.** Reject both explicitly. A DOCTYPE with no entity references is
  still refused: the rule is "no DTD", not "no DTD we noticed was dangerous".
- **Leave `xml.CharsetReader` nil.** A body declaring an encoding this build
  cannot decode is refused rather than guessed at.
- **Accumulate text and trim once**, never per token. Trimming per fragment
  turns `Tom &amp; Jerry` into `Tom&Jerry`.
- **The multistatus writer is hand-rolled.** `encoding/xml`'s encoder has no
  model for a prefix bound to a URI in scope and produces documents real
  clients reject.
- **Percent-encode an href, then XML-escape it, in that order.** The other order
  makes a file named `a&b` unreachable.
- **PROPFIND streams.** Nothing accumulates, and `Depth: infinity` above a
  configured collection size is refused with 507 rather than attempted.
- **`SEARCH` and the `filter-files` `REPORT` are in scope.** An earlier draft of
  the proposal listed them as a non-goal, which would have broken the
  favourites view on both phone apps. Property comparisons are collected
  **verbatim** as resolved names and values and handed to whichever registered
  source claims the namespace; this package never learns the vendor vocabulary.
- **Registering a source is what puts the method into `Allow`**, so a build with
  the compat tag off advertises neither method. That is the isolation rule
  holding at the protocol surface.
- **A `SEARCH` body gets the same caps as a `PROPFIND`.** It is XML from a
  stranger.
- **Locks live in `state.db`**, so a restart does not silently drop every lock a
  client believes it holds. The lock count per user is capped and the refusal
  is 507.
- **A path outside a grant is 404 here too.** WebDAV's status vocabulary makes
  403 feel natural and it is wrong.

## Done when

- The gate is green, including `-race`.
- The scanner and `If`-header fuzz corpora run in the gate.
- A `REPORT` for favourites returns a document a real client accepts.
- A build with the compat tag off advertises neither `SEARCH` nor `REPORT`.
