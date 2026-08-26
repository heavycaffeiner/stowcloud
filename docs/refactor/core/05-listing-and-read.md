# Core rebuild: listing and the read side

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (chiefly `entry.go`, `stream.go` and `archive.go`) is
> referenced as a behavioral specification only. The new implementation is
> written completely new; nothing is copied.

## Purpose

The read side of the domain: paged directory listings, ranged file
streaming, random-access reads, and the archive walk. Two files:

- `core/list.go`: `List`, `ListSorted`, the sort, the cursor, `buildEntry`,
  `statAll`.
- `core/read.go`: `Stream`, `OpenStream`, `RandomRead`, `OpenRandom`,
  `ArchiveWalk`, and the numeric clamps those need.

The paging and ordering vocabulary (`Page`, `Cursor`, `SortKey`,
`ParseSortKey`, `NeedsStat`, `ListOptions`, the page size constants) is
specified in 02-domain-types.md and lives in `entry.go`; this document
specifies the operations that consume it. Every operation here takes a
`Resolved`, so the existence rule and the base permission lookup have
already happened (04-resolve.md).

Permission bits, one line each:

| Operation | Requires |
| --- | --- |
| `List`, `ListSorted` | `acl.Read` |
| `Stat` (06-mutations.md) | `acl.Read` |
| `OpenStream` | `acl.Download` |
| `OpenRandom` | `acl.Read \| acl.Download` |
| `ArchiveWalk` | `acl.Read` at the root; `acl.Read` gates descent per directory; each file open goes through `OpenStream`, so streaming its bytes needs `acl.Download` |

`Download` without `Read` is a real configuration (a drop-style grant), and
`Read` without `Download` is the preview-only configuration, which is why
`OpenStream` asks for `Download` alone and `OpenRandom` asks for both: a
random reader exists to parse container formats (a zip's central directory
is the last thing in the file), which is both inspecting and taking bytes.

## Spec: listing (`list.go`)

```go
func (c *Core) List(ctx context.Context, r Resolved, cur Cursor) (Page, error)
func (c *Core) ListSorted(ctx context.Context, r Resolved, cur Cursor, opt ListOptions) (Page, error)
```

`List` is exactly `ListSorted` with the zero `ListOptions`: by name,
ascending, default page size.

`ListSorted` proceeds in this order, and the order is part of the contract:

1. `r.Require(acl.Read)`.
2. Stat the resolved path. A stat failure maps through `mapVFSErr`. A path
   that is not a directory answers `ErrNotFound`, not `ErrDenied`: listing
   a file is asking for something that is not there, and the message wraps
   the sentinel ("list a path that is not a directory").
3. Parse the cursor (below). A malformed cursor is `ErrNotFound`.
4. Read the whole directory with `ReadDir(path, vfs.HideReserved)`, so
   control files never reach a listing.
5. If the sort key `NeedsStat()` (size, mtime), stat every name once via
   `statAll`. The cost is one stat per name in the directory, not per name
   on the page, because the order is global.
6. Sort the whole directory (below).
7. Count directories over the whole directory. `Page.Dirs` and `Page.Total`
   describe the directory, not the page.
8. Apply the cursor and the page cut, build entries for the window, and
   assemble the page.

### The whole-directory sort

The order is applied across the whole directory before the page is cut,
never within the page. Sorting only the returned slice gives an order that
changes as somebody scrolls.

Rules, all preserved exactly:

- **Directories first, in both orders.** Directories are their own group
  ahead of files under every key. `Desc` reverses within each group and
  never puts files first. This is why descending is not a plain negation of
  the comparison: the group test (`isFile[a] != isFile[b]`) is applied
  before the key and is never inverted.
- **The key decides within a group.** `lessByKey` compares two entries
  under one key and reports `decided == false` when the key cannot tell
  them apart: equal sizes, equal mtimes, equal kinds, equal names.
- **The name breaks ties, always ascending.** When the key is undecided,
  `names[a] < names[b]` settles it, and `Desc` does not flip this
  comparison. Two files of the same size keep one stable relative order in
  both directions rather than depending on what the kernel returned.
- **The sort is stable** (`sort.Stable` over an adapter), so equal elements
  keep their pre-sort order on top of the tiebreak.

Key semantics:

| Key | Compares | Source |
| --- | --- | --- |
| `SortName` | the name, bytewise | the directory read |
| `SortKind` | `vfs.Kind` ordinal | the directory read |
| `SortSize` | `Stat.Size` | `statAll` |
| `SortMtime` | `Stat.MtimeNs` | `statAll` |

`statAll` stats every joinable name once and returns a map. A name that
cannot be joined or stat'd is left out of the map and therefore compares as
zero, which puts it at one end of its group rather than dropping the row.

**The parallel-slices rule.** The sort permutes three parallel slices: the
names, the `vfs.DirEntry` values, and the is-file flags. Every swap moves
all three together, through one swap function. This rule has history: an
earlier revision swapped the names alone, so every kind stayed beside
whichever name landed in its index, and a directory read that did not
arrive already sorted drew folders as files and files as folders. The
rebuild must keep the invariant however it implements the sort; a
single slice of one composite element per entry satisfies it structurally
and is the preferred shape.

### The cursor

```go
func cursorOffset(cur Cursor) (int, error)
```

The wire content is an ASCII decimal offset into the sorted listing, minted
and parsed only by this file; protocols treat the `Cursor` as opaque.
Parsing rules:

- The empty cursor is offset 0, the first page.
- Anything that `strconv.Atoi` refuses, and any negative value, is
  `ErrNotFound` ("a malformed listing cursor").
- An offset strictly past the end (`offset > total`) is `ErrNotFound` ("a
  listing cursor past the end of the directory"). An offset exactly at the
  end is not an error: it yields an empty page with the correct `Total`,
  `Dirs`, `DirEtag` and no `Next`, which is what a client that paged to
  precisely the boundary gets instead of a refusal.

Page cut: the window size is `pageSize` (200) when `opt.Limit` is zero,
otherwise `min(opt.Limit, maxPageSize)` (ceiling 2000, clamped rather than
refused). `Next` is minted as `strconv.Itoa(end)` when `end < Total` and is
empty on the last page.

The cursor is an offset, so a listing that changes between pages can skip
or repeat an entry across the boundary. That is accepted: a listing is a
snapshot per request, and a stable cursor over a changing directory would
need server-side listing state this design refuses to hold.

### Building one entry

```go
func (c *Core) buildEntry(r Resolved, name string, p vfs.SafePath) Entry
```

`buildEntry` stats the path and projects the result into the one `Entry`
shape: name, share path, kind, `IsDir` as exactly `Kind.IsDir()`, size,
mtime, btime, `ident.Of(share, st)`, `FileETag`, and the resolver's
permission set.

Two fallbacks are part of the contract:

- **The vanished-entry skeleton.** When the stat fails, the entry vanished
  between the directory read and the stat. The projection returns a
  skeleton, `Entry{Name, Path, Perms}` with everything else zero, rather
  than failing the whole directory over one delete race. A listing shows
  what was there at read time; a row that is a skeleton for one refresh is
  the honest rendering of a race.
- **The kind fallback in the caller.** A symlink cannot be opened under the
  default policy, so its stat fails and the skeleton would carry
  `vfs.KindOther`. The directory read is the one source that survives, so
  when a built entry's kind is `KindOther`, `ListSorted` overwrites it with
  the kind the directory read reported. The stat stays primary because it
  resolves `DT_UNKNOWN` (a filesystem that does not fill `d_type` reports
  every entry as other) and the directory read does not.

One more skip rule in the paging loop: a name the listing showed that
`JoinExisting` then refuses is a control-file race, and the row is skipped
rather than failing the directory.

The page's own token comes from the directory's stat taken in step 2:
`DirEtag, DirEtagWeak = FileETag(dirStat)`. This is the directory inode's
token, not the recursive aggregate; the aggregate is a separate surface
(09-quota-and-aggregates.md).

## Spec: streaming (`read.go`)

### Stream

```go
type Stream struct { /* unexported: file handle, pos, end */ }

func (s *Stream) Remaining() uint64
func (s *Stream) Read(p []byte) (int, error)
func (s *Stream) Close() error

const streamChunk = 256 << 10
```

A `Stream` is a bounded-memory reader over one already-open file,
restricted to `[start, end)`.

- **Chunking.** Every `Read` is clamped to at most 256 KiB per syscall,
  whatever the caller's buffer size. Memory does not scale with file size.
- **One descriptor for the lifetime.** Reads go through `ReadAt` on the
  handle opened at `OpenStream` time, so a rename-based atomic replace by
  another process does not change what is being read mid-download.
- **The shrunk-file EOF rule.** A `ReadAt` that returns zero bytes before
  the end of the range means the file shrank under a concurrent write. The
  stream sets its end to the current position and returns `io.EOF`. A short
  read is still an honest stream: EOF is "no more bytes", which is exactly
  true. It does not return an error, and it does not pad.
- `Remaining` is `end - min(pos, end)`, which is what `Content-Length` is
  built from.
- `Close` releases the descriptor. The stream owns its handle; whoever
  received it from `OpenStream` closes it.

### OpenStream

```go
type FidEntry struct {
    Name     string
    Size     uint64
    MTime    int64
    ETag     string
    ETagWeak bool
}

func (c *Core) OpenStream(ctx context.Context, r Resolved, range_ *[2]uint64) (FidEntry, *Stream, error)
```

Order of operations:

1. `r.Require(acl.Download)`.
2. `OpenRead(path, vfs.IntentRead)`; failure maps through `mapVFSErr`.
3. Stat through the open handle (not the path; the handle is the truth the
   stream will read). A stat failure closes the handle and maps.
4. A directory closes the handle and answers `ErrDenied` ("stream a
   directory").
5. Build the `FidEntry` from the handle's stat: leaf name, size, mtime,
   `FileETag`. It exists so the protocol layer can build download headers
   without a second round trip that could disagree with the descriptor.
6. Clamp the range and construct the stream.

Range semantics: `range_` is an inclusive byte pair `(start, end)`; nil
reads the whole file. Clamping, in order:

- `start = min(range[0], size)`.
- `end = max(min(satAdd(range[1]), size), start)`, where `satAdd` adds one
  saturating at the maximum `uint64`, so a range end of the maximum stays
  valid instead of wrapping to zero.

A start past the size, or a start past the end, produces an empty stream
(`Remaining() == 0`, first `Read` is EOF), never an error. Whether an
unsatisfiable range is a wire error (416) is the protocol layer's decision
from `FidEntry.Size`; the core just clamps.

### RandomRead and OpenRandom

```go
type RandomRead struct {
    Size int64 // exported; the format needs it to find its index
    // unexported file handle
}

func (r *RandomRead) ReadAt(p []byte, off int64) (int, error)
func (r *RandomRead) Close() error

func (c *Core) OpenRandom(ctx context.Context, r Resolved) (FidEntry, *RandomRead, error)
```

A `RandomRead` is a whole file open for reading at arbitrary offsets, which
is what a format that keeps its index at the end needs: a zip's central
directory is the last thing in the file, so a forward-only stream cannot
serve a zip browser.

- Requires `acl.Read | acl.Download`, both bits in one gate.
- The open, stat-through-handle, directory refusal ("read a directory") and
  `FidEntry` steps mirror `OpenStream`.
- It carries `Size` because every such format needs the size to find its
  index, and asking the caller to stat again would produce a second answer
  that can disagree with the descriptor being read.
- The caller closes it.

### ArchiveWalk

```go
type WalkEntry struct {
    RelPath  string // relative to the archive root, slash-joined; the natural zip entry name
    IsDir    bool
    Readable bool   // false: exists but skipped; must not fail the archive
    Size     uint64
    MTimeNs  int64
}

func (c *Core) ArchiveWalk(ctx context.Context, r Resolved, visit func(WalkEntry, *Stream) error) error
```

A server-side zip of a subtree. The core enumerates and hands each file to
the visitor; the zip writer lives in the protocol layer, which owns the
wire format.

Behavior:

1. `r.Require(acl.Read)`, then stat the root; failures map.
2. **A file root** is a one-entry walk: the entry's `RelPath` is the leaf
   name, and the stream comes from `OpenStream` (so `Download` is required
   for the bytes). An open failure here fails the walk, because there is
   nothing else in the archive.
3. **A directory root** descends recursively. Per directory:
   `ReadDir(HideReserved)`; a read failure (vanished or turned unreadable
   after the parent's check) reports nothing further under it and is not an
   error. Per child: an unjoinable name or a failed stat is skipped
   silently (the same race rules as listing).
4. **The ACL descent rule.** A child directory is always visited as a
   `WalkEntry{IsDir: true, Readable: true}` row, but the walk descends into
   it only when the caller holds `acl.Read` at that path (a fresh ACL
   evaluation per directory, not the root's bits). An unreadable subtree
   costs one visit and nothing under it leaks.
5. **Skipped, not failed.** A file the caller may not read
   (`acl.Read` absent at its path), or one that vanished between the stat
   and the open, is visited as `WalkEntry{Readable: false}` with no stream.
   The visitor records it as skipped; it must not fail the archive. This is
   the difference between "the archive is missing a file and says so in its
   manifest" and "a 200 response died mid-body".
6. **One open file at a time.** For a readable file the visitor receives an
   open `*Stream` valid exactly for the duration of the callback; the walk
   closes it on the way out, success or failure, before touching the next
   entry. A large archive never holds more than one descriptor.
7. A non-nil error from the visitor aborts the walk and is returned
   unchanged, which is how a client disconnect propagates.

`RelPath` starts at the archive root's leaf name in both cases: a file
root is one entry named by its leaf, and a directory root prefixes every
descendant with the root's leaf name, components joined with `/`. A walk
rooted at the share root has no leaf name and descendants carry their own
paths unprefixed. The root directory itself is not visited as an entry.

The per-directory ACL question is asked through the evaluator
(`Effective(user, vpath).Has(acl.Read)`); the helper that phrases it
(`canRead` plus the vpath construction) lives beside the walk.

## Rationale

- **Sort before the cut.** The alternative, sorting the page, produces a
  different global order per page boundary. The cost is accepted and made
  visible: the two keys that need a stat pay one stat per directory entry,
  and `NeedsStat` exists so callers can surface that cost.
- **Directories first as a hard group.** Every file manager the users know
  draws two runs. Making the group test un-invertible under `Desc` is what
  keeps "sort by size, descending" from interleaving folders into files.
- **Skeleton rows over failed listings.** Every fallback on the read side
  trades one row's fidelity for the whole response's survival, because a
  directory listing or an archive that fails over one racing delete
  punishes the many for the one.
- **Streams hold their descriptor.** Range requests and archives can run
  for minutes; re-opening by path mid-transfer would splice two versions
  of a file into one response body.
- **The walk yields streams instead of paths.** If the visitor received a
  path it would have to re-resolve and re-open, and the one-open-file bound
  would be the visitor's promise instead of the core's.

## Deliberate changes

- `mapVFSErr` moves out of the listing file into `errors.go`
  (01-errors.md). It was never listing logic; every file calls it.
- The numeric helpers stay beside `Stream` in `read.go`. `satAdd` keeps its
  single caller next to it. The two-value clamps (`min64`, `max64`, the
  local `min`) are replaced by the builtin `min` and `max`; the module is
  past Go 1.21 and a hand-rolled clamp is now just a second spelling.
- `ArchiveWalk` closes the stream in the single-file root case. The current
  code closes streams in the recursive branch but returns from the file
  branch without closing, leaking one descriptor per single-file archive;
  the rebuild closes it on the way out in both branches.
- The sort keeps the all-three-move-together invariant but implements it as
  one slice of composite elements instead of three parallel slices with a
  shared swap, which makes the historical desync bug unrepresentable.
- No behavioral change to sort order, cursor format, page accounting,
  range clamping, the shrunk-file rule, or the walk's skip rules.

## Tests

Listing:

- Sorting keeps each name with its kind: a directory read returned in a
  scrambled order sorts with every kind still beside its name (the
  regression test for the parallel-slices bug).
- Descending keeps directories first; only the within-group order flips.
- Sort by size and by mtime order by the stat value; ties fall back to the
  name ascending in both directions.
- A symlink sorts with the files and is typed `KindSymlink` in the entry
  (the kind fallback), not `KindOther` and not a directory.
- An entry deleted between the directory read and the stat appears as a
  skeleton row with its name, not as a listing failure.
- The empty cursor is page one; `Next` chains through a directory larger
  than one page with no entry repeated or dropped (static directory);
  `Total` and `Dirs` are the same on every page.
- A malformed cursor, a negative cursor, and a cursor past the end each
  answer `ErrNotFound`; a cursor exactly at the end answers an empty page.
- `Limit` zero gives 200 rows, a limit above 2000 gives 2000.
- Listing a file answers `ErrNotFound`. Listing without `acl.Read` answers
  `ErrDenied` from `Require`.
- Reserved control names never appear in a page.

Streaming:

- A whole-file stream returns exactly the content; `Remaining` matches
  before and during.
- Range clamps: end past the size clamps to the size; start past the size
  gives an empty stream; `(0, ^uint64(0))` reads the whole file (the
  `satAdd` saturation case); an inclusive `(2, 4)` returns bytes 2..4.
- Reads are chunked: a `Read` with a buffer larger than 256 KiB returns at
  most 256 KiB.
- A file truncated after open ends the stream with `io.EOF` at the short
  length, no error, and `Remaining` goes to zero.
- A file replaced by atomic rename after open streams the original bytes.
- `OpenStream` without `Download` and `OpenRandom` missing either bit
  answer `ErrDenied`; both refuse a directory.
- `RandomRead.ReadAt` at the tail returns the tail (the central-directory
  access pattern); `Size` matches the handle's stat.

Archive walk:

- A tree walks in full: every file's `RelPath` is slash-joined and relative
  to the root, directories appear as their own entries, sizes and mtimes
  match.
- A subdirectory without `acl.Read` appears as one directory entry and
  nothing beneath it appears.
- An unreadable file appears with `Readable == false` and a nil stream, and
  the walk continues.
- A file deleted mid-walk is reported skipped, not failed.
- At no point are two streams open at once (asserted with a counting stub
  or an fd count).
- A visitor error aborts the walk and comes back unchanged, and the current
  stream is closed.
- A single-file root visits one readable entry and the descriptor is closed
  after the walk returns.
