# Core rebuild: domain types

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (chiefly `root.go`, `entry.go`, `etag.go`, `ops.go`) is
> referenced as a behavioral specification only. The new implementation is
> written completely new; nothing is copied.

## Purpose

The pure vocabulary of the domain: identifiers, the one listing shape, the
change token, and the paging and ordering types. These are the types every
protocol renders from, so their fields are chosen once here and no protocol
gets a private extension. Everything in this document is buildable before the
`Core` struct exists, which is why it is step 1 of the build order.

Files: `core/ident.go`, `core/entry.go`, `core/etag.go`.

## Spec: identifiers (`ident.go`)

```go
// UserID is an account, as the auth layer addresses one. Opaque to the core:
// grants name a user id, never a username.
type UserID int64

// ShareID aliases the VFS share id, the only id scheme the core recognises
// a share by.
type ShareID = vfs.ShareID

// Token is a caller-supplied validator: the current ETag the client last
// saw, sent to prove nothing changed in between.
type Token string
```

`ShareID` stays a type alias, not a distinct type: the VFS mints and consumes
these ids, and a distinct type would force conversions at every crossing
while proving nothing, since both sides already share one id space.

Reserved id values, restated here because they constrain the whole scheme:

- `999_999` is the homes share (11-homes-and-recent.md).
- Ids at and above `1_000_000` are dynamically created shares; the offset
  keeps them clear of a legacy deployment's low ids
  (03-share-registry.md).

### Instance identity

```go
// NewInstanceID mints the identity a deployment presents to clients.
func NewInstanceID() (string, error)
```

16 bytes from `crypto/rand`, hex-encoded. It lives in the core because it is
a property of the deployment, not of a protocol. Minted once and never
regenerated: a client that saw one identity and then another treats the
server as a different server and re-syncs everything it holds. A failure of
the random source is returned, never papered over with a weaker source.

## Spec: the listing shape (`entry.go`)

```go
// Entry is the one listing shape. Every protocol renders from this and
// nothing adds a field for one protocol's benefit: a vendor-specific
// property is decorated at the protocol layer through its PropSource hook,
// never here.
type Entry struct {
    Name     string
    Path     vfs.SharePath
    Kind     vfs.Kind
    IsDir    bool
    Size     uint64
    MTimeNs  int64
    BTimeNs  *int64
    Ident    cache.Ident
    ETag     string
    ETagWeak bool
    Perms    acl.Perms
}
```

Field rules the rebuild preserves:

- `Kind` and `IsDir` are both carried. `IsDir` is exactly `Kind.IsDir()` and
  nothing more: a symlink is not a directory whatever it points at, because
  under the default policy it cannot be entered. Both exist because a client
  needs to tell a symlink from a file to draw it and to decide what opening
  it means, and a boolean cannot; with only `IsDir`, every symlink reached
  the interface as an ordinary file.
- `BTimeNs` is a pointer because a filesystem without birth times has no
  value to report, and zero is a real timestamp.
- `Ident` is the stable identity the cache mints (`cache.IdentOf`); only the
  core builds entries, so only the core mints identities, which is what
  makes them trustworthy to protocols.
- `Perms` is the caller's effective permission set at the entry's path. Under
  a share link it is the link's permission set instead, overwritten by the
  link surface (10-share-links.md).

### Pages and cursors

```go
type Page struct {
    Entries     []Entry
    Dirs        int    // how many of Total were directories; also the index where files begin
    DirEtag     string // the directory's own change token
    DirEtagWeak bool
    Next        Cursor // empty when this page is the last
    Total       int    // the whole directory, not just this page
}

// Cursor is an opaque position in a listing. The empty value is the first
// page; anything else is a value a previous Page returned.
type Cursor string
```

`Dirs` and `Total` are counted over the whole directory, not the page: the
grid draws the boundary between the directory run and the file run from
them, and the page it would otherwise count over is a slice that may hold
neither.

The cursor's wire content is an ASCII decimal offset, but that is an
implementation detail of the core, minted and parsed only here. Protocols
treat it as opaque. A malformed or negative cursor, or one past the end of
the directory, answers `ErrNotFound` (05-listing-and-read.md).

### Ordering

```go
type SortKey uint8

const (
    SortName SortKey = iota // default; costs nothing extra
    SortKind
    SortSize                // needs a stat per entry
    SortMtime               // needs a stat per entry
)

func ParseSortKey(s string) SortKey
func (k SortKey) NeedsStat() bool

type ListOptions struct {
    Sort  SortKey
    Desc  bool // reverses within each group; directories stay ahead of files
    Limit int  // 0 means the default page size
}
```

`ParseSortKey` is the trust boundary for the query parameter, and an unknown
value is the default rather than a refusal: a listing is a read, and failing
it over a spelling would take the folder away instead of showing it in an
order the caller did not ask for. Recognised spellings: `"kind"`, `"size"`,
`"mtime"`; everything else is `SortName`.

Page size constants: default 200, ceiling 2000. A `Limit` past the ceiling
is clamped, not refused: a client asking for more than the ceiling wants as
much as it can have.

## Spec: change tokens (`etag.go`)

```go
// FileETag derives a change token from the identity, size, mtime and ctime
// that statx actually exposes.
func FileETag(st vfs.Stat) (token string, weak bool)
```

Behavior, preserved exactly:

- The input is a fixed 40-byte little-endian layout: dev, ino, size,
  mtime-ns, ctime-ns (zero when the filesystem reports none). The hash is
  blake3-256, truncated to 16 bytes, hex-encoded, 32 characters.
- ctime is included because a rename or a move changes it where mtime does
  not, which is how a file replaced by a move is told from one that was
  not. mtime alone misses exactly the in-place rewrite case.
- **The token is always weak.** Linux statx has no inode change-version
  field, so a metadata-derived token cannot be a strong validator, and
  reporting it as strong would be a false guarantee. The weak flag being the
  second return value is the point: every caller is forced to carry it.
- Content hashing to make the token strong is deliberately rejected: it
  reads every byte of every file on every listing, and the product's premise
  is a multi-terabyte tree.

The consequence for preconditions (06-mutations.md): RFC 9110 requires
strong comparison for `If-Match`, a weak token can never pass it, so a
supplied validator is always refused with the current token attached, and an
explicit unconditional retry is the only way past.

Directory tokens are not produced here; a directory's token is its
aggregate's hash (09-quota-and-aggregates.md). The little-endian encoder
(`le64` in the current tree) is a private helper of this file.

## Rationale

- **Pure files first.** These types have no dependency on `Core`, stores or
  the ACL evaluator beyond value types, so they compile and test alone, and
  every later step builds against a settled vocabulary.
- **One listing shape.** The alternative, per-protocol entry structs, is how
  a field gets added for one protocol and silently misreported by another.
  Decoration happens above the core, uniformly.
- **`Token` moves to `ident.go`.** It currently sits in `ops.go` beside the
  precondition function; it is vocabulary, not mutation logic, and the
  precondition function keeps living with the mutations that call it.

## Deliberate changes

- `Token`, `UserID`, `ShareID`, `NewInstanceID` consolidate into `ident.go`
  (currently spread across `root.go` and `ops.go`). No behavioral change.
- No field changes to `Entry`, `Page`, or the sort types. The wire shapes
  protocols derive from them must survive the rebuild byte-for-byte.

## Tests

- `FileETag` is deterministic; changes to any of dev, ino, size, mtime,
  ctime change the token; a nil ctime and a zero ctime produce the same
  token (the encoding folds them); the weak flag is always true; the token
  is 32 lowercase hex characters.
- `ParseSortKey` maps the three named spellings and defaults everything
  else, including the empty string.
- `NeedsStat` is true exactly for `SortSize` and `SortMtime`.
- `NewInstanceID` returns 32 hex characters and two calls differ.
- `Entry.IsDir` equals `Kind.IsDir()` for every kind the VFS defines
  (asserted where entries are built, 05-listing-and-read.md, but stated here
  as the invariant).
