# 10: Share links

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (here `links.go`, `links_sql.go` and `links_browse.go`)
> is referenced as a behavioral specification only. The new implementation is
> written completely from scratch; nothing is copied.

## Purpose

Two files of the rebuilt domain package, plus one interface the store layer
implements:

- `engine/service/core/link.go`: the `Link` type and its predicates, `LinkSpec`,
  `LinkPatch`, `CreateLink`, `ListLinks`, `GetLink`, `UpdateLink`,
  `DeleteLink`, `NoteLinkDownload`, the `LinkStore` interface, and the
  crypto seams (`LinkCipher`, the password hasher and verifier,
  `AttachLinkCrypto`).
- `engine/service/core/linkaccess.go`: everything a bearer of a token can do:
  `LinkPublic`, `LinkStream`, `LinkStreamAt`, `LinkBrowse`,
  `LinkArchiveWalk`, `LinkResolved`, `LinkCheckPassword`, `LinkDrop`,
  `LinkDropFile`, and the shared `linkTarget` resolution.
- `engine/store/state`: the `LinkStore` implementation, owning every
  `share_link` statement and every row scan.

## Spec: the Link shape

A link stores both a hash and an encrypted copy of its token:

- The **sha256 token hash** authenticates public requests. Verifying a
  bearer never needs decryption, so public access does not depend on the
  master key being loaded.
- The **encrypted token** (sealed through the `LinkCipher` seam) lets the
  owner list the full URL again. Legacy rows written before the ciphertext
  column existed remain unrecoverable: the owner sees the link exists but
  cannot re-read its URL, and nothing invents one.
- **Revocation is permanent.** A deleted link's row is gone and the same
  token is never re-minted; a later link on the same target is a new token.

```go
type Link struct {
    ID      int64
    Token   *secret.Secret // the plaintext token, only when recoverable
    Share   ShareID
    Path    vfs.SharePath
    Owner   UserID
    Perms   acl.Perms
    Expires int64  // ns epoch, zero when never
    MaxDown int32  // -1 when unlimited
    Downs   int32
    Label   string
    Note    string
    HasPassword bool   // the one fact about passwords that leaves the package
    CreatedNs   int64
    TokenHash   []byte // the sha256 that authenticates public requests

    dev, ino, btime *int64 // the identity pin; nil dev means path-only
}
```

`Token` is a `secret.Secret` so the plaintext is zeroizable and never lands
in a log by accident. No password material is ever on the struct; only
`HasPassword`.

### The identity pin

At creation, when the target is not the share root and the filesystem
reports a birth time, the link captures `dev`, `ino` and `btime`. Every
bearer access then runs a path-plus-identity cross-check: the path must
still stat, and the stat's dev, ino and birth time must all match the pinned
values. Consequences:

- A rename makes the link dead instead of following the file.
- A delete-and-recreate at the same path makes the link dead: the birth time
  is what distinguishes the original inode from one the filesystem reused.
- A link with `dev == nil` (a root link, a filesystem without birth times,
  or a row that predates the rule) keeps a path-only check.
- An **unverifiable** identity reads as dead. When the pin exists but the
  stat cannot confirm it (no birth time in the stat, mismatched fields),
  the safe reading of "cannot tell" is "dead link".

The cross-check for browse and subpath streaming runs against the link's
own root path, not the subpath: a rename of the shared folder kills the
link, while a file moving inside the folder is an ordinary change to what
the folder contains.

### Predicates

```go
func (l Link) IsDrop() bool          // Create set, neither Read nor Download
func (l Link) IsExpired(now int64) bool // Expires != 0 && Expires <= now
func (l Link) IsExhausted() bool     // MaxDown >= 0 && Downs >= MaxDown
func (l Link) Dev() *int64           // non-nil when an identity is pinned
```

A drop link is write-only by construction: the holder can put files in and
can never list or read what is there.

## Spec: owner operations

### CreateLink

```go
type LinkSpec struct {
    Perms    acl.Perms
    Password *string // hashed with Argon2id before it reaches a row
    Expires  int64   // ns epoch, zero when never
    MaxDown  int32   // -1 when unlimited
    Label    string
    Note     string
}

func (c *Core) CreateLink(ctx context.Context, r Resolved, spec LinkSpec) (Link, secret.Secret, error)
```

In order:

1. Requires `acl.Share` at the resolved target.
2. An empty permission set is refused (`ErrDenied` with a message): a link
   that grants nothing is a mistake, not a link.
3. **Escalation guard.** The link's permissions must be a subset of what the
   creator holds at the target right now (`r.perms.Has(spec.Perms)`). A link
   is a delegation of the creator's own access; it can never carry a
   permission the creator lacks.
4. A non-zero expiry in the past is refused.
5. The target is stat'ed. A file-drop permission shape (Create without Read
   or Download) on a non-directory is refused: a drop targets a folder.
6. The identity pin is captured (dev, ino, birth time) when the target is
   not the share root and the stat carries a birth time; otherwise the link
   is path-only.
7. **The password is hashed before it reaches the row**, through the
   attached Argon2id hasher. History note, kept in the spec so it is never
   repeated: an earlier version stored the password as it arrived, and the
   verifier then compared candidates against the plaintext as though it were
   a hash, so every password-protected link refused its own password while
   the plaintext sat in the database. The rebuild's hasher seam fails closed:
   no hasher attached means creation with a password errors, never stores
   the plaintext.
8. The token is minted: 16 bytes of CSPRNG, presented as base64url without
   padding (22 characters). Its sha256 is the stored hash.
9. The current key version is read from the store (`LinkStore.KeyVersion`),
   and the token is sealed through the `LinkCipher` with AAD binding the
   token hash and the key version. Sealing with no cipher attached fails
   with an error rather than panicking mid-mint.
10. The row is inserted through the `LinkStore` with `downloads = 0` and the
    creation timestamp from the injected clock.
11. Returns the `Link` and the bearer secret. The plaintext token leaves the
    core exactly once, here.

### ListLinks and GetLink

```go
func (c *Core) ListLinks(ctx context.Context, owner UserID, at *Resolved) ([]Link, error)
func (c *Core) GetLink(ctx context.Context, owner UserID, id int64) (Link, error)
```

- `ListLinks` returns every link the owner holds, ordered by id, optionally
  narrowed to links whose share and path match one resolved target.
- `GetLink` looks the row up by id and refuses with `ErrNotFound` when the
  row is absent **or owned by someone else**. The two cases are
  indistinguishable on purpose: an id-probing client learns nothing about
  which ids exist.
- Both decrypt the sealed token opportunistically when a cipher is attached
  and the row carries ciphertext; a row that cannot be opened simply has a
  nil `Token`.

### UpdateLink

```go
type LinkPatch struct {
    Perms    *acl.Perms
    Password **string
    Expires  **int64
    MaxDown  **int32
    Label    *string
    Note     *string
}

func (c *Core) UpdateLink(ctx context.Context, owner UserID, id int64, patch LinkPatch) (Link, error)
```

The double pointer is the tri-state the patch needs. An outer nil leaves the
field alone, which is what lets a screen edit one thing without resetting
the rest. An outer non-nil with an inner nil **clears**: a password of
`&nil` removes the password, an expiry of `&nil` removes the expiry, a
max-downloads of `&nil` removes the cap. "Leave it" and "remove it" are
different requests and both need a spelling.

Behaviors:

1. Ownership through `GetLink`; someone else's link is `ErrNotFound`.
2. A permission change is re-checked against the creator's access **as it is
   now**, not as it was at mint time: the path is resolved again with
   `acl.Share` required, and the new set must be a subset of the current
   access. A grant revoked since creation must not be re-widened through an
   update; the update is the moment to ask again. An empty set is refused.
3. A new expiry in the past is refused.
4. A new password is hashed before it reaches the store, same as creation.
5. All present fields are applied in one store transaction
   (`LinkStore.Update`), then the fresh row is read back and returned.

### DeleteLink

```go
func (c *Core) DeleteLink(ctx context.Context, owner UserID, id int64) error
```

Ownership first (so deleting another owner's link is `ErrNotFound`), then
the row is deleted with the owner in the predicate as well, so the check and
the delete cannot disagree. Permanent: nothing resurrects a token.

### NoteLinkDownload

```go
func (c *Core) NoteLinkDownload(ctx context.Context, link Link) error
```

Consumes one download against the cap, atomically, via a conditional UPDATE
in the store (`ConsumeDownload`):

```sql
UPDATE share_link SET downloads = downloads + 1
WHERE id = ? AND (max_downloads IS NULL OR downloads < max_downloads)
```

The conditional UPDATE is the whole mechanism: a read-then-write lets N
concurrent requests all observe room under the cap and all proceed. One
affected row is a consumed slot. Zero affected rows is disambiguated by a
follow-up lookup: a vanished row is `ErrNotFound`, a present row is
`ErrLinkExpired` (the cap is reached). A transfer that dies after the
consume still counts; the cap bounds attempts, not completions.

## Spec: bearer operations

### The liveness rule

Every bearer surface applies the same rule, and the error mapping is part of
the security design:

- **An unknown token is `ErrNotFound`.** A token that names nothing is
  absent, not gone. Reporting it as gone would assert it once existed,
  letting a stranger sort guesses into tokens that were real links and
  tokens that never were.
- **Every other failure is `ErrLinkExpired`.** Expiry, an exhausted cap, an
  unregistered share, an unparseable stored path, a stat failure, and a
  failed identity cross-check all collapse to one answer: the link is dead,
  and the answer does not tell a stranger why. Distinguishing "the file was
  renamed" from "the cap ran out" leaks the target's history to whoever
  holds a stale token.

### LinkPublic

```go
func (c *Core) LinkPublic(ctx context.Context, token string) (Link, Entry, error)
```

Resolves a token for a bearer. The token is hashed before it touches any
query; the plaintext is accepted at exactly this boundary and nowhere else.
Order: resolve by hash (unknown is `ErrNotFound`), expiry and cap, share
registered, stored path parses, target stats, identity cross-check. On
success the returned `Entry` carries the link's permissions, built from an
internally constructed `Resolved` whose permission set is the link's own.

### LinkStream and LinkStreamAt

```go
func (c *Core) LinkStream(ctx context.Context, link Link, range_ *[2]uint64) (FidEntry, *Stream, error)
func (c *Core) LinkStreamAt(ctx context.Context, link Link, sub string, range_ *[2]uint64) (FidEntry, *Stream, error)
```

The public download path, which has no user session to resolve through.
`LinkStream` opens the link's own target under the full liveness rule.
`LinkStreamAt` opens a file beneath a folder link (or the link's own file
when `sub` is empty): the identity cross-check runs against the link's base
path, the subpath resolves through `linkTarget`, and a directory target or a
failed subpath stat is `ErrNotFound` (the subpath layer is a listing
namespace; a missing entry inside a live link is an ordinary miss, not a
dead link).

### LinkCheckPassword

```go
func (c *Core) LinkCheckPassword(ctx context.Context, link Link, candidate string) (bool, error)
```

Reads the stored password hash through the store (`PasswordHash`). A link
with no password accepts anything. Otherwise the candidate goes through the
attached verifier, which fails closed: no verifier attached is an error,
never a pass. A nonexistent link cannot reach here, because a bearer only
ever carries a token that resolved.

### Browsing: linkTarget, LinkBrowse

A link names one path; everything reachable through it is beneath that path
and nowhere else.

```go
func (c *Core) linkTarget(link Link, sub string) (*vfs.ShareRoot, vfs.SafePath, error)
```

- Empty subpath (after trimming slashes) is the link's own path.
- A non-empty subpath is **parsed, never joined as text**: `ParseSafePath`
  refuses `..`, absolute paths and reserved names, so a visitor cannot name
  anything outside the folder the link was made for. A subpath that fails to
  parse or to join is `ErrNotFound`.
- An unregistered share or an unparseable base path is `ErrLinkExpired`.

```go
type LinkEntry struct {
    Name  string
    IsDir bool
    Size  uint64
}

type LinkListing struct {
    Path    string      // subpath relative to the link's root, empty at the top
    IsDir   bool        // a file link answers IsDir false with no entries
    Name    string
    Size    uint64
    Entries []LinkEntry
}

func (c *Core) LinkBrowse(ctx context.Context, link Link, sub string) (LinkListing, error)
```

`LinkBrowse` checks expiry and cap, resolves the subpath, cross-checks the
identity against the base, and stats the target (a missing target is
`ErrNotFound`). A file answers with `IsDir` false and no entries, which lets
one endpoint serve file links and folder links. A directory lists with
reserved names hidden, stats each entry for its size (a listing that showed
every file as zero bytes would be wrong rather than sparse; an entry whose
stat fails keeps the readdir kind and a zero size), and sorts directories
first, then by name.

**The link's permissions apply to the whole subtree. There is no per-entry
ACL check under a link.** A link is one grant: whoever holds the token has
exactly what the link was given and nothing more. This is why
`LinkArchiveWalk` exists apart from the session `ArchiveWalk`: the session
walk asks the ACL what the resolved user may read, and a link has no user;
driven by the ACL it visits every directory and reads nothing, producing an
empty archive.

### LinkArchiveWalk and LinkResolved

```go
func (c *Core) LinkArchiveWalk(ctx context.Context, link Link, sub string,
    visit func(WalkEntry, *Stream) error) error
func (c *Core) LinkResolved(link Link, sub string) (Resolved, error)
```

The archive walk descends with the link's grant standing for every entry. A
directory that vanishes or becomes unreadable mid-walk contributes nothing
rather than failing the archive; a file that fails to open is visited as
unreadable. `LinkResolved` mints the `Resolved` a link's permissions grant
at a subpath, for surfaces that take a `Resolved` rather than a stream; it
keeps "what may this token do" in one place.

### LinkDrop and LinkDropFile

The upload-only surface for drop links (Create without Read or Download):
the bearer can write into the link's directory and can never list it.

```go
func (c *Core) LinkDrop(ctx context.Context, link Link, name string, body []byte) (Entry, error)
func (c *Core) LinkDropFile(ctx context.Context, link Link, name string, body io.Reader) (Entry, error)
```

Common rules:

- Requires the link to carry `acl.Create`; refused with `ErrDenied`
  otherwise.
- Liveness first: expired (and for the streaming path, exhausted) is
  `ErrLinkExpired`; so is a missing share, an unparseable path, or a base
  that is not a directory.
- The name is the visitor's, so it goes through the safe join, and it lands
  directly under the link's own path: a drop admits files into one folder,
  not into a tree the uploader chooses.
- **Never overwrites.** The bearer cannot see the folder, so an overwrite
  would let them destroy, or probe for, a file they cannot name.
- The write is durable and `NoClobber`: the no-overwrite decision is
  enforced by the filesystem open, not only by the existence check above
  it, so a race with a concurrent upload cannot clobber.

They differ in what "name taken" does:

- `LinkDrop` (buffered body) gives the taken name a counting suffix via the
  shared unique-sibling-name helper, the same "name (2).ext" shape the
  keep-both conflict policy uses, so one server invents one shape of name.
  It then marks the parent chain dirty and returns the entry with the
  link's permissions.
- `LinkDropFile` (streaming body) refuses a taken name with `ErrExists`.
  It resolves through `CreateFile` with `acl.Write` added to the link's
  permissions for that one resolution only (publishing a file is a write,
  and the no-clobber check has already ruled out the overwrite that Write
  would otherwise permit); every other surface still reads `link.Perms`,
  so the link is not widened. The body is copied positionally with
  `WriteAt` and truncated to the bytes actually written.

The two policies serve two callers (a form post that retries with a new
name versus a streaming endpoint that reports the conflict); both are kept.

## Spec: the LinkStore interface

Every SQL statement and row scan moves behind `LinkStore`, defined in
`engine/service/core/link.go` and implemented in `engine/store/state`. The core
hands it domain-free values and receives rows back; the schema, the
statements and the scanning live with the schema's owner. Field types are
primitives so the store does not import the core or the ACL package.

```go
// LinkRow is one share_link row as it crosses the store boundary. Pointer
// fields are NULLable columns; nil is NULL.
type LinkRow struct {
    ID           int64
    TokenHash    []byte
    TokenEnc     []byte  // nil for legacy rows without ciphertext
    TokenKeyVer  *uint32 // nil when no ciphertext
    Share        int64
    Path         string
    Dev, Ino     *int64  // the identity pin; all three set or none
    Btime        *int64  // present only with an explicit presence marker in the row
    Owner        int64
    Perms        uint16
    PasswordHash *string
    ExpiresNs    *int64  // nil when never
    MaxDown      *int64  // nil when unlimited
    Downloads    int64
    Label        *string
    Note         *string
    CreatedNs    int64
}

// LinkRowPatch mirrors LinkPatch at the row level. Outer nil leaves the
// column; inner nil sets it NULL. Non-nullable columns use a single pointer.
type LinkRowPatch struct {
    Perms        *int64
    PasswordHash **string
    ExpiresNs    **int64
    MaxDown      **int64
    Label        *string
    Note         *string
}

type LinkStore interface {
    // Insert stores a new link with downloads zero and returns its id.
    Insert(ctx context.Context, row LinkRow) (int64, error)

    // ByID and ByHash return (row, false, nil) when nothing matches.
    ByID(ctx context.Context, id int64) (LinkRow, bool, error)
    ByHash(ctx context.Context, tokenHash []byte) (LinkRow, bool, error)

    // ListByOwner returns the owner's rows ordered by id.
    ListByOwner(ctx context.Context, owner int64) ([]LinkRow, error)

    // Delete removes the row only when both id and owner match.
    Delete(ctx context.Context, id, owner int64) error

    // ConsumeDownload increments downloads if the cap allows, in one
    // conditional UPDATE. consumed false with a nil error means the cap is
    // reached or the row is gone; the caller disambiguates via ByID.
    ConsumeDownload(ctx context.Context, id int64) (consumed bool, err error)

    // PasswordHash reads one column; nil means no password is set.
    PasswordHash(ctx context.Context, id int64) (*string, error)

    // Update applies every present patch field in one transaction. Each
    // field is its own constant statement: a statement assembled from the
    // fields a patch happens to carry has text that depends on input, which
    // constant statements exist to prevent.
    Update(ctx context.Context, id int64, patch LinkRowPatch) error

    // KeyVersion reads the durable key_version row the auth package keeps
    // in step with the key ring. A missing row is version zero.
    KeyVersion(ctx context.Context) (uint32, error)
}
```

Implementation notes for the store side:

- The identity pin persists as four columns (`dev`, `ino`, `btime_present`,
  `btime_ns`); the row surfaces a pin only when all of dev, ino, the
  presence marker and the birth time are set. A partial pin scans as no pin.
- The scan validates every narrowing (share id, perms, caps, counts) and
  reports a row that does not fit as an error, not a silent truncation: the
  row is trust-boundary input to the domain.
- The core converts `LinkRow` to `Link` in one place, including the
  opportunistic token decryption (ciphertext present, key version present,
  cipher attached; a failed open leaves `Token` nil rather than erroring).
- The store never sees a plaintext token or password; it stores what it is
  handed.

## Spec: the crypto seams

These stay attach-time wiring in the core; only the SQL moved.

```go
// LinkCipher is the at-rest cryptography, satisfied by the auth package's
// cipher (XChaCha20-Poly1305, nonce || ciphertext, AAD binding the token
// hash and the key version). An interface so a Core can hold a zero value
// that refuses to seal until the server wires the master key.
type LinkCipher interface {
    Seal(token, tokenHash []byte, keyVer uint32) ([]byte, error)
    Open(blob, tokenHash []byte, keyVer uint32) ([]byte, error)
}

type passwordHasher func(ctx context.Context, plain string) (string, error)
type passwordVerifier func(ctx context.Context, enc, candidate string) (bool, error)

func (c *Core) AttachLinkCrypto(cipher LinkCipher, hash passwordHasher, verify passwordVerifier)
```

- The ciphertext format is owned by the auth package; the core carries a
  cipher the server wired from the loaded master key, so the format is
  defined in exactly one place and a key rotation keeps opening what it
  re-seals. The AAD binds the token hash and key version, so a ciphertext
  cannot be moved to another row or replayed under another key version.
- Attach is one call because the three are one feature: links that cannot
  open their own tokens, hash their own passwords or verify them are not a
  partial feature.
- Every routing helper fails closed with a plain error when its seam is
  unwired: sealing (otherwise every creation panics mid-mint), hashing
  (otherwise the fallback nobody notices is writing the password down), and
  verifying (otherwise an unwired verifier silently passes).

## Rationale

- **Hash plus ciphertext.** One column serves verification without keys, the
  other serves owner recovery with keys. Either alone loses a feature:
  hash-only makes URLs unlistable, ciphertext-only makes every public
  request depend on the master key.
- **The identity pin.** A path alone re-targets a link to whatever now sits
  at the path, which turns "share this file" into "share whatever replaces
  this file". The birth time is the part inode reuse cannot forge.
- **Collapsed liveness errors.** The bearer is a stranger; the error surface
  is what a stranger can learn. One live/dead bit plus the absent/existed
  distinction (`ErrNotFound` for unknown tokens) is the minimum that still
  lets a client render "this link never existed" differently from "this
  link is no longer available".
- **Conditional UPDATEs for the cap.** Same argument as the quota reserve
  in 09: enforcement under concurrency requires the check and the mutation
  to be one atomic statement.
- **LinkStore as the one persistence seam.** The reference's `links_sql.go`
  makes the domain package the owner of a table's spelling. Moving the
  statements and scans behind an interface puts schema changes in the store
  layer, gives tests a fake with no database, and keeps the core's
  responsibility at "what a link means".

## Deliberate changes

1. **All `share_link` SQL and scanning moves to `engine/store/state`.** The
   core keeps `Link`, the predicates, the operations, and the `LinkStore`
   interface above. `linkKeyVer` becomes `LinkStore.KeyVersion`. The bind
   helpers (`passwordArg`, `expiryArg`, `maxDownArg`, `stringArg`) and the
   `scanner`/`scanLink` machinery are store-side concerns and are redesigned
   there, not carried over.
2. **`lastDot` leaves this file.** It serves the unique-sibling-name helper,
   which lives in `resolve.go` in the new tree (00-overview); link code
   calls the helper, not the character scan.
3. **Row-to-domain conversion is one function.** The reference decrypts
   inside the scan; the rebuild scans in the store and converts (including
   the opportunistic decrypt) in one core function, so the trust-boundary
   validation of a row has a single home.
4. **`NoteLinkDownload`'s zero-row disambiguation goes through the store
   pair** (`ConsumeDownload` returning a bool, then `ByID`); the observable
   errors (`ErrNotFound` for a vanished row, `ErrLinkExpired` for a reached
   cap) are unchanged.

No observable behavior changes: tokens, hashes, liveness answers, the drop
naming shapes, and the browse listing order are all as the reference.

## Tests

Domain (with a fake `LinkStore` where a database adds nothing):

- CreateLink: refused without Share; refused with empty perms; escalation
  guard refuses a superset of the creator's perms; past expiry refused;
  drop shape on a file refused; the returned token is 22 base64url chars;
  the stored row carries the sha256 of the token, never the token; the
  password reaches the store hashed (fake hasher observes the plaintext,
  the store fake sees only the hash); creation with a password and no
  hasher errors; creation with no cipher errors; the identity pin is set
  for a non-root target with a birth time and absent otherwise.
- GetLink/ListLinks: another owner's link is `ErrNotFound` by id; listing
  filters by resolved path; a legacy row (no ciphertext) lists with a nil
  `Token`; a sealed row round-trips its token through a real cipher.
- UpdateLink: outer-nil leaves fields; `&nil` clears password, expiry and
  cap; perms widening re-checks current access and refuses after a revoked
  grant; past expiry refused; empty perms refused; wrong owner is
  `ErrNotFound`.
- DeleteLink: wrong owner is `ErrNotFound`; a deleted token no longer
  resolves.
- NoteLinkDownload: consumes to the cap then answers `ErrLinkExpired`; a
  vanished row answers `ErrNotFound`; N concurrent consumers against a cap
  of one admit exactly one (store-side test).
- LinkPublic/LinkStream: unknown token is `ErrNotFound`; expired,
  exhausted, unregistered share, stat failure and identity mismatch are all
  `ErrLinkExpired`; a rename kills the link; a recreate at the same path
  kills the link; a path-only legacy link survives inode reuse; the entry
  carries the link's perms.
- LinkCheckPassword: no password accepts anything; wrong password refuses;
  no verifier attached errors.
- LinkBrowse/linkTarget: `..`, absolute and reserved subpaths answer
  `ErrNotFound`; a subpath cannot reach outside the linked folder (fixture
  with a sibling directory); a file link answers `IsDir` false with no
  entries; entries sort directories first then by name; entry sizes come
  from stats; base identity mismatch is `ErrLinkExpired` while a missing
  subpath is `ErrNotFound`.
- LinkArchiveWalk: archives a tree with no per-entry ACL involvement (a
  user-less link reads every file); a vanished subdirectory yields a
  partial archive, not an error.
- LinkDrop: refused without Create; a taken name gets the counting suffix
  and both files survive; the write is NoClobber (concurrent create races
  do not clobber); the parent aggregate is marked dirty.
- LinkDropFile: a taken name is `ErrExists`; the uploaded bytes round-trip;
  the added Write permission is visible on no other surface (a drop bearer
  still cannot list or stream).

Store (`engine/store/state`):

- Insert/ByID/ByHash/ListByOwner round-trip every column, including NULLs,
  the partial-pin rule (any missing pin column scans as no pin), and the
  narrowing validations (a row with an out-of-range perms value errors).
- Delete requires both id and owner.
- Update applies each field's constant statement and the clear-vs-leave
  semantics per column.
- ConsumeDownload's conditional UPDATE under concurrency.
- KeyVersion: missing row is zero.
