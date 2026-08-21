# WebDAV - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Implemented**                  |
| Reviewers  |                                  |

---

## 1. Summary

RFC 4918 Class 2 in Go. The hardened XML scanner restated in `encoding/xml`
terms, a hand-written multistatus writer because Go's XML encoder cannot emit
namespace prefixes correctly, streamed PROPFIND, locking, and dead properties.

## 2. Background & Motivation

`sc-dav` is 6,695 lines, of which `xml.rs` is 1,162 and `locks.rs` is 555. The
XML rules are the security-relevant part and they are short enough to state
completely:

- `DocType` and `PI` events are rejected outright. No attempt is made to safely
  expand entities, because there is no legitimate reason for a DTD in a WebDAV
  body and refusing closes XXE and billion-laughs in one move.
- Body size, element count (10,000) and depth (64) are capped.
- Everything is namespace-resolved. Raw prefixes are never compared: `D:`, `d:`,
  `a:` and the default namespace are the same document, which is what real
  clients require.

Go changes how three of those are implemented and none of what they mean, and it
adds one problem the Rust version does not have: `encoding/xml`'s encoder is not
usable for a multistatus response.

**The refusal is the design, not a shortcut.** The tempting implementation of
the first rule is to allow a DTD and expand entities safely, with a depth cap
and an expansion budget. That is a parser feature with a long history of being
subtly wrong, deployed on a body a stranger sends, to support a construct no
WebDAV client emits. Refusing outright closes XXE and billion-laughs together
and has nothing left to be subtly wrong about. It is the same shape as
rejecting `.` and `..` rather than normalising them
([`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S1): the safest
handling of a construct nobody needs is to not handle it.

The existence rule (S2) applies to this mount exactly as it does to the native
API, and it is worth saying because WebDAV's status vocabulary makes 403
feel natural. A path outside a grant is 404 here too, and the test is a table
run against every mount rather than against the native API alone.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] Class 2: `OPTIONS`, `PROPFIND`, `PROPPATCH`, `MKCOL`, `GET`, `HEAD`,
      `POST`, `DELETE`, `PUT`, `COPY`, `MOVE`, `LOCK`, `UNLOCK`.
- [ ] The three XML rules above, enforced in the scanner rather than trusted to
      the parser.
- [ ] PROPFIND streamed: a large collection never buffers a whole document.
- [ ] Correct namespace prefix declarations on every response.
- [ ] Dead properties, stored in `state.db`, keyed by the identity tuple.
- [ ] Locks with timeout, refresh, depth, and the `If` header.
- [ ] `/dav-uploads`, the chunked-upload collection.
- [ ] `SEARCH` and the `filter-files` `REPORT`, with vendor property
      comparisons collected verbatim and handed to whichever source claims the
      namespace (§4.3.5a). Both phone apps' favourites view depends on it.
- [ ] A fuzz target on the scanner (D16).

### 3.2 Non-Goals

- [ ] Class 3. The current server is Class 2 and nothing asks for more.
- [ ] `golang.org/x/net/webdav`. It is a filesystem-interface server with its
      own path handling, which is precisely the layer this product replaces with
      kernel handles.
- [ ] Shared locks. Exclusive write locks only, as today.
- [ ] **Extending** `SEARCH` beyond what the clients already use. The methods
      themselves are in scope and §4.3.5a specifies them; an earlier draft listed
      them as a non-goal, which would have broken the favourites view on both
      phone apps.

## 4. Technical Design

### 4.1 Architecture Overview

```
internal/dav
  scan.go       the hardened token scanner
  propfind.go   request parsing, response streaming
  proppatch.go  set and remove
  props.go      live properties, and the PropSource decorator hook
  write.go      the multistatus writer
  lock.go       the lock table, the If header
  search.go     SEARCH and REPORT, and the source registry they dispatch to
  method.go     the method dispatch
  uploads.go    /dav-uploads
```

### 4.2 Data Model Changes

`dav_prop` and `dav_lock` in `state.db`, keyed by the identity tuple rather than
by a cache-minted fileid ([`stowcloud-5`](stowcloud-5-store-and-schema.md)
§4.2.2). This is what deletes the `PINNED` bit.

### 4.3 Core Logic

#### 4.3.1 The scanner

`encoding/xml`'s decoder does not fetch external entities and errors on
undefined ones, so XXE is closed by the standard library's default rather than
by this code. Three things it does **not** do, which the scanner therefore must:

1. **It surfaces `xml.Directive` (a DOCTYPE) and `xml.ProcInst` as ordinary
   tokens.** Both are rejected explicitly. A DOCTYPE with no entity references
   is still refused: the rule is "no DTD", not "no DTD that we noticed was
   dangerous".
2. **It has no limits.** Element count, depth and element-name length are
   counted in the scanner loop, against the D5 constants. The body itself is
   capped by `http.MaxBytesReader` at step 6 of the chain before the parser sees
   a byte.
3. **`xml.CharsetReader` is left nil**, so a body declaring an encoding this
   build cannot decode is refused rather than guessed at. UTF-8 and the
   ASCII-compatible declarations Go handles natively are what clients send.

Namespace resolution is the one place Go is straightforwardly better:
`xml.Name.Space` carries the resolved URI, so prefix comparison is not merely
discouraged, it is unavailable. `DAV:` is compared as a URI everywhere.

One correctness note carried over from the current implementation: text content
must be accumulated and trimmed once, not trimmed per token. A parser that
splits text at entity references and trims each fragment turns `Tom &amp; Jerry`
into `Tom&Jerry`. `encoding/xml` returns `xml.CharData` per fragment for the
same reason, so the same rule applies: accumulate, then trim.

#### 4.3.2 The multistatus writer

**Responses are written by hand.** `encoding/xml`'s encoder cannot emit
namespace prefix declarations: it has no model for a prefix bound to a URI in
scope, and it serialises `xml.Name.Space` in ways real WebDAV clients reject.
A multistatus needs `D:` bound to `DAV:` on the root and vendor prefixes bound
alongside it, so marshalling structs is not an option.

```go
// Writer streams a multistatus directly to the response. It emits one response
// element per entry as the entry is produced, so a PROPFIND over a large
// collection never holds the document in memory.
type Writer struct { w io.Writer; ns map[string]string }
func (m *Writer) Response(href string, ok []Prop, notFound []PropName, status int) error
```

Escaping goes through `xml.EscapeText` for content, and href values are
percent-encoded before they are XML-escaped, in that order. Getting the order
wrong is how a file named `a&b` becomes unreachable.

#### 4.3.3 Streaming PROPFIND

The listing comes from `core.List` as a stream
([`stowcloud-7`](stowcloud-7-core-domain.md) §5-1), each entry is turned into a
response element and written, and the connection is flushed periodically. Nothing
accumulates. This is what makes a PROPFIND over a directory of a million entries
a bounded-memory operation, and it is the reason the core's listing API streams
at all.

`Depth: infinity` is refused on collections above a configured size rather than
attempted, with 507, because the honest failure is better than the one that
arrives after ten minutes.

#### 4.3.4 Properties

Live properties come from `core.Entry`. Dead properties come from `state.db`.
Vendor properties come from a decorator hook that the compat layer registers,
which is how `oc:` properties reach a WebDAV response without `internal/dav`
knowing the vocabulary exists:

```go
// PropSource contributes properties for an entry. The compat layer registers
// one; internal/dav knows nothing about what it emits. PropCtx carries what a
// source may know about the request beyond the entry itself, owned rather than
// borrowed so the interface needs no lifetime.
type PropSource interface {
    Props(ctx PropCtx, e core.Entry, want []PropName) []Prop
}
```

#### 4.3.5 Locking

Exclusive write locks, per resource, with a token, a timeout, a depth and an
owner. Stored in `state.db` so a restart does not silently drop every lock a
client believes it holds.

The `If` header parser is its own fuzz target: it is a small grammar, it is
attacker-reachable, and getting it wrong means either honouring a lock nobody
holds or ignoring one somebody does.

A lock count per user is capped (D5) and the refusal is 507, because an
unbounded lock table is a durable resource a client can exhaust for free.

#### 4.3.5a `SEARCH` and `REPORT`

Both are implemented today and both are load-bearing for the mobile clients, so
they are ported rather than dropped.

- **`SEARCH`** (RFC 5323) carries a query over properties. It is how filename
  search reaches this mount.
- **`REPORT`** (RFC 3253) carries `filter-files`, which is how both phone apps
  fetch the favourites list. `stowcloud-13`'s favourites goal depends on it and
  names no other transport.

The design constraint is the same one the property emitter has: **this package
must not learn the vendor vocabulary.** A query can filter on `oc:favorite`,
which `internal/dav` has never heard of. So the parser collects property
comparisons **verbatim**, as namespace-resolved names and values, and hands them
to whichever registered source claims that namespace. The source decides what
the comparison means; this package decides only that the request was well
formed and bounded.

Registering a source is also what puts the method into the `Allow` header. A
build with the compat tag off advertises neither method, which is the isolation
rule holding at the protocol surface rather than only in the import graph.

The scanner's caps apply unchanged: a `SEARCH` body is XML from a stranger and
gets the same DTD refusal, element count, depth and name-length bounds as a
`PROPFIND`.

#### 4.3.6 `/dav-uploads`

The chunked-upload collection, backed by
[`stowcloud-9`](stowcloud-9-upload.md)'s name-ordered spool mode. `internal/dav`
knows it as a collection with unusual semantics, not as a Nextcloud feature; the
compat layer is what points its clients at it.

## 5. API Design

### 5-1. New / Modified

```go
package dav

// Scan reads body under the hardened rules: no DTD, no processing
// instruction, no charset guessing, and every count bounded. It returns
// namespace-resolved names, never prefixes.
func Scan(body []byte, limits Limits) (*Doc, error)

// Handler dispatches a WebDAV method against the core. It takes a resolved
// path, so the ACL check has already happened and a path outside a grant has
// already become a 404.
func (h *Handler) ServeMethod(w http.ResponseWriter, r *http.Request, res core.Resolved)
```

### 5-2. Error Handling

| Status | Case |
|---|---|
| 400 | malformed XML, or a body that is not the expected root element |
| 403 | forbidden by policy, where the caller may know the resource exists |
| 404 | missing, or outside every grant |
| 405 | method not allowed on this resource |
| 409 | a parent collection does not exist |
| 412 | `If`, `If-Match` or a lock token check failed |
| 415 | a body whose media type is not XML where XML is required |
| 423 | locked, or the lock token does not match |
| 424 | failed dependency, inside a multistatus |
| 507 | lock cap, or a `Depth: infinity` refused on size |

| Error | Meaning |
|---|---|
| `ErrDtdForbidden` | a DOCTYPE was present |
| `ErrPiForbidden` | a processing instruction was present |
| `ErrTooManyElements`, `ErrTooDeep` | a D5 bound refused |
| `ErrBadXml` | anything else the scanner rejected, with the reason |
| `ErrLocked` | the resource is locked by another token |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 7a | `scan.go`, its fuzz target, and the escaping helpers | M | Phase 0 | heavycaffeiner |
| Phase 7b | `write.go`: the hand-written multistatus writer | M | 7a | heavycaffeiner |
| Phase 7c | `propfind.go`, `props.go`, the streaming path, the decorator hook | L | 7b, Phase 4 | heavycaffeiner |
| Phase 7d | `proppatch.go` and dead-property storage | M | 7c, Phase 2 | heavycaffeiner |
| Phase 7e | `lock.go`, the `If` parser and its fuzz target | M | 7c | heavycaffeiner |
| Phase 7f | `search.go`, `method.go`, `uploads.go` | M | 7c, Phase 6 | heavycaffeiner |

7a and 7b can start before Phase 4 lands.

### 6-2. Dependencies

None. `encoding/xml`, `net/http` and `strings` cover this document, which is the
one place the Go standard library replaces a hardened third-party dependency
rather than the other way round: `quick-xml` is pinned to 0.41 in the Rust tree
because two advisories (a quadratic duplicate-attribute scan and an unbounded
namespace-declaration allocation) are remotely reachable exactly here.

That is not a claim that `encoding/xml` is immune to the same class. It is a
claim that this scanner bounds what it reads before the parser does, which is
what the pin was buying.

## 7. References

- `crates/sc-dav/src/xml.rs`, `lib.rs`, `locks.rs`,
  `crates/sc-server/src/dav_uploads.rs`: the implementation this translates.
- `crates/sc-dav/src/xml.rs`: the three rules, and the text-trimming note
  §4.3.1 carries over.
- `Cargo.toml:128-134`: the two advisories that made the XML parser a pinned
  dependency, and why they are reachable here.
- RFC 4918, and RFC 9110 §8.8 for the weak validator
  [`stowcloud-7`](stowcloud-7-core-domain.md) §4.3.2 produces.
