# HTTP 04: WebDAV

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/dav` and `go/internal/server/davmount.go` is referenced as a
> behavioral specification only. The new implementation is written
> completely from scratch; nothing is copied.

## Boundary

Target `engine/http/dav`. WebDAV is a protocol package over already-resolved,
already-authorized core capabilities. The mount translates a URL and method
into a core resolution. The method handler never parses a virtual path and
never reimplements grant evaluation.

The native mount is `/dav`. Compatibility aliases are data supplied by 05.
`OPTIONS` is unauthenticated discovery and advertises class 1 and 2. Every
other request needs an app-password Basic credential and receives
`WWW-Authenticate: Basic realm="WebDAV", charset="UTF-8"` when absent or
invalid. The account password is never accepted on this protocol.

## URL mapping

Split the raw URL path on `/` first, then percent-decode each segment. A
decoded `/`, malformed escape, NUL, `.` or `..` segment is refused. Decoding
the entire path before splitting is forbidden because an encoded separator
would create a boundary after the security decision.

The virtual DAV root has no backing directory. `PROPFIND /dav[/]` projects
the caller's authorized roots and does not call `core.Resolve`. Other paths
resolve with the permission implied by the method:

| Method | Resolve requirement |
| --- | --- |
| GET, HEAD, PROPFIND, SEARCH, REPORT, COPY source | Read |
| PUT, MKCOL, PROPPATCH, LOCK, UNLOCK | Write |
| DELETE | Delete |
| MOVE source | Write + Delete |
| COPY/MOVE destination | Write + Create |

`Destination` accepts an absolute URL or path. An absolute URL whose host
differs case-insensitively from the request Host is 502. Scheme is not
compared because a trusted reverse proxy may terminate TLS. Its path goes
through the same split-before-decode function. `Overwrite` is false only for
case-insensitive `F`; all other/absent values mean true.

## XML scanner and limits

Every XML body has a 256 KiB raw limit, then structural limits (`Limits`):
element count, nesting depth, name bytes, accumulated text bytes, property
count, condition/list count and report-leaf count. The scanner:

- permits only the leading XML declaration;
- refuses DOCTYPE and every other processing instruction;
- disables entity expansion and defines no custom entity map;
- installs no charset reader (UTF-8 only);
- explicitly tracks namespace declarations because `encoding/xml` does not
  reject an undeclared prefix itself.

A parser never retains client markup. Dead-property values are collected as
text and serialized afresh.

## PROPFIND

Body choices are exact:

- empty or whitespace-only body means `allprop`;
- `propname` wins if both it and `allprop` appear (the smaller disclosure);
- `include` collects names but never changes mode away from `allprop`;
- a `prop` set with no `allprop`/`propname` is named-property mode;
- duplicate names collapse in first-seen order.

Depth defaults to infinity where the method requires it and accepts only
`0`, `1`, or `infinity` according to each operation. Before a depth-infinity
walk starts, the target collection's entry estimate is compared with the
configured ceiling; excess returns 507 with no partial walk. Allowed walks
stream multistatus responses and flush at a fixed bounded cadence under
fasthttp's stream writer.

Live properties include resource type, content length/type, ETag, modification
time, display name, allowed methods, lock discovery and supported lock. MIME
type comes from a bounded extension table, never by opening a file during a
property listing. Dead properties are keyed by `ident.Ident` through a narrow
port, so rename follows identity. DAV imports no state package.

## PROPPATCH

Instructions preserve document order. A set followed by remove is not the
reverse. Any attempt to set/remove a live property refuses the **whole
transaction**: that property reports 403 and every otherwise-valid dependent
instruction reports 424. No dead property is committed. A request containing
only dead-property operations commits atomically.

Stored property names must be valid XML names before they become tags. Every
text value and href is escaped. Client markup is never replayed raw.

## Conditional requests

`If-None-Match` uses weak comparison for GET/HEAD revalidation. The WebDAV
`If` header uses strong ETag comparison for write preconditions; a weak ETag
is parsed and retained but never satisfies a condition. This asymmetry is
intentional and tested side by side.

The bounded If grammar is:

```text
If        = 1*( NoTagList | TaggedList )
NoTagList = 1*List
TaggedList= ResourceTag 1*List
List      = "(" 1*Condition ")"
Condition = ["Not"] (StateToken | ETag)
```

Limits cap lists, conditions and token bytes. Tagged resource paths use the
same URL decoder. Evaluation is OR across lists and AND within one list;
`Not` negates one condition. Submitted lock tokens are collected only from
positive state-token conditions that occur in a list which actually
satisfies.

## Locks

Locks survive restart in `state.db`. Tokens are 16 CSPRNG bytes rendered in
UUID shape and sent as `urn:uuid:{token}`. A token is necessary but not
sufficient: the authenticated principal must own it. Timeouts default to
5 minutes, clamp to 60 minutes, and `Infinite` means the clamp. Depth is zero
or infinity.

Conflict matrix:

| Requested | Existing shared | Existing exclusive |
| --- | --- | --- |
| shared | coexist | conflict |
| exclusive | conflict | conflict |

Conflicts include the same path, a covering depth-infinity ancestor, and for
a requested depth-infinity lock, any descendant lock. Prefix matching always
checks a `/` boundary (`/a` does not cover `/ab`). A LOCK of a nonexistent
resource creates a lock-null resource; its later PUT/MKCOL materializes it and
the lock remains attached as RFC 4918 requires.

No submitted token on a covered write yields 423. An If header that parsed
but whose conditions did not hold yields 412. Clients rely on this split.

### Closing the two races

Presentation uses a `LockPort` with two transactional operations:

```go
type LockSnapshot struct { /* covering locks plus resource validators */ }

Admit(ctx, LockRequest) (ActiveLock, error)
Snapshot(ctx, targets []LockTarget, nowNs int64) (LockSnapshot, error)
```

`Admit` runs expiry deletion, the conflict scan, per-principal count check and
insert in one serialized write transaction. Two concurrent conflicting LOCKs
cannot both pass. Shared-lock rows prevent a simple `(share,path)` unique key,
so transaction serialization plus the in-transaction recheck is the database
guarantee, not an invalid uniqueness constraint.

For a mutating method, one `Snapshot` supplies **both** If-header state and
the covering-lock guard for every source/destination target. Evaluation never
re-reads the table between those checks. This closes the second audit race.
The filesystem mutation follows the snapshot; WebDAV locks are cooperative,
so a new LOCK may race after validation exactly as it may on other compliant
servers. Lock admission itself remains atomic.

These operations require a forward amendment to
`../foundation/state.md`: the state aggregate owns the serialized transaction
and neutral row/request shapes. The command-level adapter maps them to DAV
types, keeping `engine/http/dav` from importing persistence.

Expired lock rows are swept at startup and periodically. Reads still ignore
expired rows, but the table cannot grow permanently.

## Content and collection methods

- GET/HEAD supports one byte range only. Multi-range is refused rather than
  answered as a subset. Validators and content length are chosen before
  streaming.
- PUT writes through `core.CreateFile`/publish semantics and applies strong
  preconditions and lock snapshot first. No direct filesystem write exists.
- MKCOL refuses a request body it cannot interpret and creates exactly one
  collection.
- DELETE, COPY and MOVE honor Depth/Overwrite, preconditions and lock coverage
  on all affected endpoints. Cross-share behavior comes from core.
- A streamed read error after commitment is logged; no second HTTP error is
  attempted.

## Multistatus writing

The writer emits one fixed DAV prefix at the root and deterministic vendor
prefixes sorted by namespace. Text goes through XML escaping. Hrefs use a
dedicated segment/path percent encoder because `url.URL.EscapedPath` leaves
sub-delimiters such as `&` unsuitable inside XML href text. Both native DAV
and compat use this one encoder; no second path escaper exists.

The sticky writer error ends later writes as no-ops. Flush cadence is bounded,
and a client disconnect cancels the traversal.

## Extension seams

`PropSource` contributes namespaces and properties; `QuerySource` claims a
report/search vocabulary; `UploadCollection` provides engine-neutral chunk
verbs and header names. Registering sources is what adds SEARCH and REPORT to
`Allow`; an unregistered vocabulary refuses rather than returning an empty
successful result. The complete compat registration is in 05.

Upload member names use **canonical decimal**: digits only, within the
configured range, and no leading zero unless the number is exactly zero and
zero is allowed by that collection. Nextcloud v2 chooses 1 through 10,000, so
`00001` is refused. This resolves the old parser disagreement.

## Deliberate changes

1. **LOCK conflict admission becomes one serialized state transaction**
   (DAV audit finding 4).
2. **If evaluation and lock guard share one snapshot** (finding 5).
3. **Expired DAV locks gain startup and periodic sweeps** (finding 18).
4. **DAV imports no persistence type**; `identOf` and state row conversion
   move behind the port (finding 17).
5. **One href/path encoder serves DAV and compat** (finding 14).
6. **Canonical decimal chunk naming is shared with compat**, refusing the
   latent leading-zero alias (compat finding 7).

The XML defenses, parsing choices, conflict matrix, principal ownership,
status distinctions and durable file behavior otherwise carry whole.

## Tests

- Fuzz PROPFIND, PROPPATCH, LOCK info, REPORT and If parsing under every raw
  and structural bound; no panic, unbounded allocation, entity expansion or
  undeclared prefix acceptance.
- Exact PROPFIND mode table, including whitespace body, propname precedence
  and include behavior.
- PROPPATCH transaction: live property causes 403/424 and zero persisted dead
  changes; ordered dead-only operations commit exactly.
- Weak If-None-Match versus strong If-header table.
- Full If grammar fixtures: tagged/no-tag lists, Not, malformed bounds and
  submitted-token extraction only from a satisfied list.
- Lock matrix over same/ancestor/descendant paths, depth, ownership, timeout,
  lock-null creation, 423 versus 412.
- Real database race: hundreds of concurrent conflicting LOCKs yield exactly
  one exclusive lock; concurrent shared requests coexist; race detector clean.
- A mutation's If and lock evaluation performs one store snapshot (instrumented
  port) and cannot observe two table versions.
- Periodic sweep removes expired rows while live lock behavior stays unchanged.
- Split-before-decode, encoded separator/traversal, foreign Destination and
  reverse-proxy scheme mismatch fixtures.
- Single-range and multi-range cases; a post-commit read failure only logs.
- Depth-infinity ceiling refuses before traversal; allowed large traversal
  flushes before completion and cancels on disconnect.
- XML output injection corpus for text, href and stored property names;
  deterministic namespace prefixes and byte-golden multistatus fixtures.
- Extension registration controls advertised methods exactly; canonical chunk
  names agree with compat on every shared vector.
