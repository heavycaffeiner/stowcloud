# Upload 01: session lifecycle

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/upload` (here `model.go`, `engine.go`, `store.go`,
> `alias.go`, `sweep.go`) is referenced as a behavioral specification
> only. The new implementation is written completely from scratch;
> nothing is copied.

## Identity

```go
type SessionID [16]byte            // CSPRNG
func NewSessionID() (SessionID, error)
func (id SessionID) String() string // base64url, no padding
func ParseSessionID(s string) (SessionID, error)
```

The id is the whole of a TUS URL, so it is unguessable by construction,
**and** every call still applies the owner check: possession of a URL is
not authorization, it is addressing. `ParseSessionID` is a trust
boundary; a wrong length refuses as `ErrNotFound` rather than padding.

## Control names

| Name | Holds |
| --- | --- |
| `.scpart-{id}` | the part file, in the destination's own directory |
| `.scpart-{id}.d` | a name-ordered session's chunk directory |
| `.scpart-{id}.c` | a cached session's chunks, inside the spool |
| `.scpart-{hex8}` | one chunk file inside either directory |

Everything carries the reserved prefix, including chunk files whose
directories already do: the prefix is what every listing filters on, and
a control file that outlives its directory must stay unlistable wherever
it ends up. The history note the old code carries is kept as a
requirement: an earlier design disguised part names as
`.{basename}.scpart-{id}` to pass component validation, which defeated
the reserved-name filter and put part files in every listing for the
duration of every upload. The part name is `.scpart-{id}` and nothing
else.

The part file lands in the destination's own directory because the
publishing rename is only atomic within one directory.

## States

```go
const (
    StateReceiving  SessionState = iota
    StateFinalizing // set by Finalize before the first byte moves (change)
    StateDone
    StateAborted
    StateExpired    // derived from the clock, never stored
)
```

`StateExpired` is derived so a session needs no writer to expire.
`StateFinalizing` becomes a real transition (overview, change 2):
`Finalize` sets it before assembly begins, and the sweep treats
`StateReceiving` and `StateFinalizing` as the two live states, so a
long-running assembly cannot be swept mid-publish.

## Create

```go
type SessionSpec struct {
    TotalLen     *uint64 // nil: deferred, supplied later, required by finalize
    RandomAccess bool
    IfMatch      string
    Mode         SpoolMode
    Meta         Meta    // filename, mtime, mime, relative path, optional Verify
}

func (e *Engine) Create(ctx context.Context, r core.Resolved, spec SessionSpec) (Session, error)
```

In order: require `Write|Create` on the resolution; refuse a root
destination; the per-account limits (open session count, total declared
bytes); the free-space check **against the destination's parent
directory**, not the share root, because a mount inside the share is the
filesystem the upload actually consumes; mint the id; create the part
file with O_EXCL and a sparse truncate to the declared length (nothing
is copied, and the directory holds exactly one unlistable entry for the
session's life); snapshot the chunk floor and size into the row, because
an admin moving the floor mid-upload must not make a chunk that was
legal when sent illegal now.

## Reads and small writes

```go
func (e *Engine) Get(ctx context.Context, id SessionID, user core.UserID) (Session, error)
func (e *Engine) Offset(...) (uint64, error)
func (e *Engine) SetLength(..., total uint64) error // once, for deferred-length sessions
func (e *Engine) Abort(...) error
```

All owner-scoped: a wrong owner answers `ErrNotFound`, same rule as the
core's operations. `Session.Offset` is the resumable offset (the end of
the first run when the set starts at zero); `Session.Received` is the
bytes actually landed, which differs once a random-access client writes
past a hole. Both travel, because TUS needs the former and a progress
bar wants the latter.

`Abort` closes the part-file handle, deletes the row and the part file,
**and forgets the row lock** (overview, change 3); the old code left the
mutex for the sweep to collect.

## The two-lock discipline

Per session, two locks with a fixed order:

1. the **handle lock** guards the lazily opened part-file descriptor;
2. the **row lock** serializes bookkeeping for one session.

A chunk write takes handle then row, never nested the other way. The
split exists so the rare, brief metadata write never blocks the common,
potentially large disk write. The lock tables themselves are two maps
under their own mutexes; entries die with the session (Abort, Finalize,
sweep).

## Aliases

```go
func (e *Engine) BindAlias(ctx context.Context, tid string, user core.UserID, id SessionID) error
func (e *Engine) LookupAlias(...) (Alias, error)
func (e *Engine) UnbindAlias(...) error
```

The compat chunked protocol names uploads by a client-chosen transfer
id. The id is a trust boundary: length-bounded against
`limits.NameBytes`, control characters and `/` refused, before it ever
reaches a statement. Aliases are owner-scoped rows that die with the
session.

## The sweep

```go
type SweepReport struct {
    ExpiredSessions int // rows past their lifetime, with their part files
    OrphanParts     int // part files with no row, past the grace period
    OrphanSpools    int // spool directories in the same position
    OrphanCaches    int // cache directories whose row is gone
}

func (e *Engine) Sweep(ctx context.Context) (SweepReport, error)
```

Orphan caches are counted apart because no walk of the shares can find
them: the cache spool is not a share, and the session row is the only
thing that names a directory in it. A row-less cache directory is
therefore findable only by walking the spool, which the sweep does. The
grace period protects a part file whose row is one transaction behind.

## Deliberate changes

The three lifecycle changes are the overview's 2 (real
`StateFinalizing`), 3 (`Abort` forgets the row lock) and the row-shape
grouping (store-side). Everything else, including every name, bound and
check above, is behavior-preserving.

## Tests

- Id round-trip; a truncated, padded or oversized wire id refuses.
- Create: root destination refuses; the part file appears under O_EXCL
  (a second create of the same session name refuses); the sparse size
  matches the declared length; the floor snapshot survives an admin
  change mid-session.
- Owner scoping: every surface answers `ErrNotFound` for the wrong
  owner.
- Deferred length: `SetLength` once, a second call refuses; finalize
  without a length refuses.
- Abort: row, part file, handle **and row lock** are gone (inspect the
  lock map size).
- Expiry is derived: a session past its lifetime reads expired with no
  intervening write.
- Sweep: each of the four orphan classes is planted and collected; a
  fresh part file inside the grace period survives.
- Alias: bind/lookup/unbind round-trip; a traversal, control-character
  or oversized transfer id refuses before any row exists.
- The state machine: receiving -> finalizing -> done; a finalizing
  session survives a sweep; an aborted session's chunks refuse.
