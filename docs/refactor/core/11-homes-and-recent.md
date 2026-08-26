# 11: Homes and recent

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/core` (here `homes.go` and `recent.go`) is referenced as a
> behavioral specification only. The new implementation is written completely
> from scratch; nothing is copied.

## Purpose

Two files of the rebuilt domain package:

- `engine/core/home.go`: per-user home directories: `EnableHomes`,
  `ensureHome`, the template seeding, and the home grant.
- `engine/core/recent.go`: the journal-backed "what this account wrote"
  listing: `Recent`, `RecentHit`, `RecentQuery`.

The raw grant INSERT the reference keeps in `homes.go` leaves the core in
this rebuild. See "Deliberate changes".

## Spec: homes

Homes are **off by default**. An operator turns them on; nothing else does.

### Not a second resolution mechanism

One share root is opened for the whole homes tree and every user's home is a
subdirectory under it, registered under the reserved share id `999999`. A
home reaches a caller through the same grant-projected virtual root and the
same single `Resolve` every other share uses. This is a load-bearing
decision: a home that resolved by a different path would be a second place
the existence rule and the permission check could be got wrong, and the
single-gate invariant (00-overview) exists precisely so there is one such
place.

Constants:

```go
const (
    homeLabel    = "Home"
    homeShareID  = 999_999 // reserved; no admin share may take it
    templateName = ".template"
)

// homePerms is the full set a home grant carries.
const homePerms = acl.Read | acl.Write | acl.Create | acl.Delete |
    acl.Rename | acl.Move | acl.Share | acl.Download
```

### EnableHomes

```go
func (c *Core) EnableHomes(ctx context.Context, host string) error
```

- Errors if homes are already enabled (a root is registered under the home
  share id).
- Creates the host directory if missing, with mode `0750`. Unlike an admin
  share, whose directory is a pre-existing location the operator points at,
  the homes host is entirely managed by this process; creating it is the
  one directory write the core does outside a share root, and the mode
  keeps other local users out of the tree that will hold every home.
- Registers the host as a share under `homeShareID` with the label `Home`
  and the default share policy, through the same registration path as any
  other share.

### ensureHome

```go
func (c *Core) ensureHome(ctx context.Context, user UserID) error
```

Creates a user's home directory and grant on first access. Called eagerly
from `Resolve`: the first thing a user does after logging in must already
see their home in the projected root, so waiting for an explicit touch is
too late. The call is best-effort at the call site: `Resolve` treats a
failure as warn-and-continue, because a home hiccup must not break access
to the user's other shares.

Behaviors, in order:

1. Homes disabled (no root under `homeShareID`): a silent no-op.
2. **Fast path.** If the user already holds a grant on the homes share
   (`userHasHome`: scan the evaluator's roots for the home share id),
   return. The grant is the marker: a grant on the homes share means the
   home directory and the grant both already exist.
3. **The once-per-user lock.** The fast path and the slow path race, so the
   slow path takes a mutex (`homeOnce`) and re-checks `userHasHome` under
   it: the classic double-checked pattern, serializing only the
   once-per-user creation, never the steady state.
4. The home subpath is the user id spelled in decimal, parsed as a safe
   path.
5. **Template seeding.** If `{homes root}/.template` exists, the new home
   is created by recursive copy of the template tree (the copy creates the
   destination directory itself). This is an admin-facing feature with no
   other trace in configuration: an operator drops files into `.template`
   and every later first login receives them. An empty-mkdir-only
   implementation loses the feature silently, which is why the spec calls
   it out. The copy runs with no cancellation gate: seeding a home is not a
   job anybody polls. The template directory is unreachable as anybody's
   own home, because homes are named by numeric user id and `.template` is
   not a number.
6. Without a template, a plain `Mkdir` of the subpath. An already-existing
   directory is not an error: it is a race won elsewhere (or a leftover
   from a crashed earlier attempt), and its existing is all that mattered.
7. **The grant.** Exactly one grant is persisted: this user, the home share,
   **subpath-scoped to the user's own directory**, `homePerms` allow, no
   deny, inherit true, label `Home`. The scoping is the security property: a
   root-scoped grant on the homes share would hand whoever received it every
   other user's home. After persisting, the evaluator is reloaded so the
   grant takes effect in the running process.

Crash ordering: the directory is created before the grant, so a crash
between the two leaves a directory with no grant. The next `ensureHome`
finds no grant, re-runs the slow path, tolerates the existing directory
(step 6; with a template, the recursive copy must equally tolerate existing
destination entries), and persists the grant. A grant is never left
pointing at a home that was not created.

### The grant persistence contract

The core does not spell the grant table's columns. Under the 3-layer
assignment (01-package-survey.md) the ACL package is a pure service-layer
evaluator and the grant table belongs to the persistence layer, so the
write surface is a grant aggregate in `engine/store/state`:

```go
// In engine/store/state, beside the other aggregates.
//
// PersistGrant validates the grant (exactly one of User and Group set, a
// share id present, a non-empty allow set), writes one grant row through
// the state database's serialized write path, stamps CreatedNs, and
// returns the stored grant's id. It does not touch the evaluator;
// reloading stays an explicit separate step so a caller creating several
// grants reloads once.
func (db *DB) PersistGrant(ctx context.Context, g GrantRow, nowNs int64) (int64, error)
```

- `GrantRow` mirrors the ACL package's `Grant` value (user or group, share,
  subpath as its string spelling, allow, deny, inherit, label); the state
  package owns the row shape, the ACL package owns the domain shape, and
  the core converts between them.
- The evaluator keeps its read path: `LoadFromState` loads grants and
  memberships from rows the store hands it. The INSERT text, the column
  order and the inherit-to-integer mapping live in `store/state` with the
  rest of the schema.
- The core's `createHomeGrant` becomes: build the row, call
  `PersistGrant`, then reload the evaluator.

### What homes deliberately do not do

- No home deletion, quota, or migration surface: out of scope, as in the
  reference.
- No lazy on-demand creation path besides the eager `Resolve` hook.
- No per-home policy; the homes share carries one policy for the tree.

## Spec: recent

What this account wrote, newest first. It reads the journal rather than
walking the filesystem, so it is exact and cheap: the rows are the writes
that actually went through this server, and there is nothing to truncate.

### Shapes

```go
type RecentHit struct {
    Vpath   vfs.Vpath
    Share   string        // the vpath's label
    Subpath vfs.SharePath
    Name    string        // the path's last component
    Size    uint64
    MTimeNs int64
    // AtNs is when the write happened, which is not the modification time
    // for a restore or a copy that preserved timestamps.
    AtNs int64
    Op   journal.Op
}

type RecentQuery struct {
    SinceNs int64  // window lower bound; zero is no window
    Limit   int
    Scope   string // one virtual subtree, as the client spells a path; empty is everywhere
}

func (c *Core) Recent(ctx context.Context, user UserID, q RecentQuery) ([]RecentHit, error)
```

`AtNs` versus `MTimeNs` is the distinction the type exists for: `AtNs` is
the journal's record of when the account performed the write; `MTimeNs` is
the file's current modification time from a fresh stat. A restore or a copy
that preserved timestamps has an old `MTimeNs` and a recent `AtNs`, and a
client sorting "what did I just do" needs the latter.

### Behaviors

1. **A nil journal answers empty.** The journal is an optional store; a
   deployment that kept no history (or lost the journal file) is not an
   error, and an empty list is the honest answer. Nil, not a synthesized
   fallback.
2. The user id is narrowed to the journal's account type; a value that does
   not fit is an error.
3. The journal is asked for the account's events since `SinceNs`, capped at
   `Limit`, newest first.
4. **Every row is re-validated before it is returned.** A row records that
   the account wrote the file, not that they may still see it. Per row, in
   order, each failure silently dropping the row:
   - **Visibility.** `VpathFor(user, share, path)` must succeed: the share
     must still be readable by this account, or the row is not theirs to
     see any more.
   - **Scope.** With a non-empty `Scope`, the vpath's string form must have
     it as a prefix.
   - **The grant at the path.** `Resolve(user, vpath, acl.Read)` re-checks
     the permission at the path, not just at the share: a grant can be
     revoked on a subtree while the share stays readable, and the resolve
     gate is the one place that judgment is made.
   - **Existence.** A stat through the resolved root; a file written once
     and gone since is dropped. The row is revalidated, not trusted.
5. Surviving rows become hits: the vpath and its label, the journal's
   share-relative path and its leaf name, the fresh stat's size and mtime,
   and the journal's timestamp and op.

Dropped rows are not backfilled: a query with `Limit` 50 of which 10 fail
revalidation returns 40. The limit bounds journal work, not the answer
size, and a second page is the client's request to make.

## Rationale

- **The grant is the existence marker.** Homes need an idempotent
  "ensure" with no dedicated bookkeeping table. The grant row is already
  durable, already loaded per user, and already the thing that makes the
  home visible; using it as the marker means there is no second record to
  drift from the first.
- **Subpath-scoped grant.** The whole tree is one share, so scoping is the
  only wall between users' homes. This line is the reason the grant's shape
  is specified rather than left to the implementation.
- **Eager creation from Resolve, best-effort.** The projected root is built
  from grants; without an eager hook the home appears only after an access
  that cannot happen because the home is not in the root yet. Best-effort
  because the failure domain of home creation (a full disk under the homes
  host, a broken template) must not take down every other share the user
  can reach.
- **Journal over walking.** A filesystem walk cannot answer "what did this
  account write" at all (files carry no writer), and mtime cannot
  distinguish this server's writes from anything else. The journal is the
  only source that knows, and revalidation keeps its rows from outliving
  the permissions and files they refer to.
- **Silent drops in Recent.** Each dropped row is a row the account must
  not see (revoked, hidden) or cannot use (gone). Reporting the drop would
  leak exactly the fact the revocation exists to hide.

## Deliberate changes

1. **Grant persistence moves to the state store.** The reference's
   `homes.go` holds `grantInsertStmt` and spells the grant table's columns
   in the domain package. In the rebuild, a grant aggregate in
   `engine/store/state` owns grant creation (`PersistGrant`, contract
   above); `engine/core/home.go` builds the row, calls `PersistGrant`, and
   reloads the evaluator. `inheritInt` and the statement text go with it.
   The ACL package stays a pure evaluator with no SQL of its own
   (01-package-survey.md).
2. **`ensureDir`/`osMkdirAll` collapse to one helper.** The reference
   splits them so a stateless gate has a single place to check the mode;
   the rebuild keeps one function with the `0750` mode and lets the gate
   check that function. No behavioral difference.
3. **`leafOf` is not duplicated.** The reference defines the leaf-name
   helper locally; the rebuild uses the path type's own name accessor if
   the `SharePath` surface offers one, keeping a local helper only if it
   does not. Either way the observable value (last component) is unchanged.

No observable behavior changes: the share id, the mode, the template rule,
the grant shape, the nil-journal answer and the revalidation order are all
as the reference.

## Tests

Homes:

- `EnableHomes` twice errors; the host directory is created with mode
  `0750`; the share registers under `999999` with the `Home` label.
- First `ensureHome` creates the directory named by the decimal user id and
  exactly one grant: right user, home share, subpath equal to the user's
  own directory, `homePerms`, inherit, label `Home`. The grant is visible
  through the evaluator after the call (reload happened).
- Second `ensureHome` is a no-op (no second grant row, no directory touch;
  observe via a counting store fake or a row count).
- N goroutines calling `ensureHome` concurrently for one user produce one
  directory and one grant.
- With `.template` populated, a new home receives the template's tree
  recursively; without it, an empty directory.
- `.template` is not reachable as a home: no user id spells it, and a user
  whose home exists cannot resolve into it (their grant scopes them to
  their own subpath).
- One user cannot resolve into another user's home (the subpath scoping
  test, the security property).
- A pre-existing home directory with no grant (simulated crash between
  mkdir and grant) is adopted: `ensureHome` succeeds and persists the
  grant.
- A failing `ensureHome` (unwritable homes host) does not fail `Resolve`
  for the user's other shares.
- Homes disabled: `ensureHome` is a no-op and no home appears in the
  projected root.
- Store side: `PersistGrant` round-trips through the evaluator's reload
  (write one, reload, evaluate); it stamps `CreatedNs`; it refuses a grant
  with neither or both of user and group.

Recent:

- Nil journal returns an empty list and no error.
- A recorded upload appears with the right vpath, leaf name, op and `AtNs`;
  `MTimeNs` comes from the current stat.
- A restore that preserved an old mtime shows old `MTimeNs` and recent
  `AtNs`.
- Newest first, and `Limit` bounds the journal read.
- `SinceNs` windows the result.
- `Scope` keeps only rows under the given virtual subtree.
- A row whose share was revoked for the account disappears.
- A row whose subtree grant was revoked (share still readable) disappears:
  the per-path re-resolve, not the share check, must catch it.
- A row whose file was deleted disappears.
- Dropped rows are not backfilled past `Limit`.
- A user id that does not fit the journal's account type errors.
