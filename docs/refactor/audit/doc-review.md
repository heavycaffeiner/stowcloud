# Document consistency review

Review of the 30 rebuild documents under `docs/refactor/` on branch
`refactor/core`, plus the current route table
(`go/internal/server/routes.go`). Every finding was confirmed by reading
both sides; line numbers are as of this review. Severity is one of:
contradiction (two documents assert incompatible facts), gap (a consumed
contract or promise nobody specifies), drift (a stale or inaccurate
statement about another document), style. Nothing was fixed; this document
only records.

## 1. Cross-document contract mismatches

### 1.1 `Ident` home: core documents still say `cache.Ident` / `cache.IdentOf`

`foundation/cache.md:38-54` and `foundation/state.md:64` move the identity
tuple to `engine/store/ident` with constructor `ident.Of`; D10 in
`00-decisions.md` agrees. `core/02-domain-types.md:73,90`,
`core/04-resolution.md:189` and `core/05-listing-and-read.md:153` still
write `cache.Ident` and `cache.IdentOf`, while `core/00-overview.md:120`
already lists `store/ident` in its dependency table.
Severity: contradiction.
Resolution: rewrite the three core documents to `ident.Ident` / `ident.Of`.

### 1.2 Two incompatible `GrantRow` types under one name, conversion unowned

`foundation/acl-evaluator.md:311-322` (`User int64` zero-sentinel,
`Allow int64`) versus `foundation/state.md:412-421` (`User *int64` nil
convention, `Allow uint16`). `state.md:454-457` claims `ListGrants` feeds
`LoadFromState` directly, and `Memberships` returns `map[int64][]int64`
where `LoadFromState` takes `[]MembershipRow`; no document owns the
conversion. `00-decisions.md` D6 and `01-package-survey.md:186` claim the
contract transfers "unchanged in shape"; `state.md:698` claims "no
spelling delta".
Severity: contradiction.
Resolution: rename one of the two types, pick one convention, and name the
document that owns the store-row to evaluator-row conversion.

### 1.3 `state.NewQuota` returns `core.QuotaSink`, reversing the import direction

`foundation/state.md:331` declares `func NewQuota(db *DB) core.QuotaSink`,
which forces `store/state` to import the core; the same document at
`state.md:341-343`, `core/09-quota-and-aggregates.md:334-340`, the tier
table in `03-engine-bootstrap.md:50-57` and D6 all state the store cannot
import the core.
Severity: contradiction.
Resolution: return the package-local concrete type and let the wiring site
satisfy the interface.

### 1.4 `cache.Overrides` still forces `store/state` to import `store/cache`

The interface (`foundation/cache.md:176-180`) carries `cache.FileID` and
`cache.Assignment`; only `Ident` moved. `foundation/state.md:214-219` and
its Deliberate changes item 2 claim the `Ident` adoption "removes its
`store/cache` import", and `03-engine-bootstrap.md:69-71` forbids the
import outright.
Severity: contradiction.
Resolution: move `FileID` and `Assignment` beside `Ident`, or restate the
seam in primitive types, then correct `state.md`'s claim.

### 1.5 `Assignment` is consumed but never declared

`foundation/cache.md:148,179` uses `Assignment` in prose and in
`RecordFileIDs(ctx, assignments ...Assignment)`; no rebuild document
defines the struct. Only the old tree does, which `README.md`'s
from-scratch rule forbids relying on.
Severity: gap.
Resolution: declare `Assignment` in `foundation/cache.md` beside `FileID`.

### 1.6 `vfs.SharePolicy` and `DefaultSharePolicy` are consumed but never specified

`core/03-share-registry.md:47,264` and `core/07-transfers.md:177-178`
consume them; `foundation/vfs.md` names `SharePolicy` only in passing
(lines 80, 94, 185, 461) and specifies neither the fields nor
`DefaultSharePolicy()`.
Severity: gap.
Resolution: add both to `foundation/vfs.md`'s share-root lifecycle section.

### 1.7 Path-type methods called by core documents are absent from `vfs.md`

`SafePath.Name()` (`core/06-mutations.md:277`), `SafePath.String()`
(`core/07-transfers.md:318`), `SharePath.IsRoot()`
(`core/04-resolution.md:125`) and `Vpath.String()`
(`core/11-homes-and-recent.md:209`) are missing from
`foundation/vfs.md:264-285`'s crossings list.
Severity: gap.
Resolution: add the four methods to the path-type blocks in `vfs.md`.

### 1.8 `state` types the core writes against are unspecified in `state.md`

`core/07-transfers.md:217-374` uses `state.OpKind`, `OpState`, `OpResult`,
`OpResultReason`, `ErrNoSuchOp`, the constants, and a six-argument
`state.CreateOp`; `core/03-share-registry.md:422` uses `state.ShareRow`.
`foundation/state.md:196-201` lists only method names for those aggregates.
Severity: gap.
Resolution: spell the types and signatures in `foundation/state.md`.

### 1.9 `state.md`'s `DeleteShare` amendment for `core/03` is already applied

`foundation/state.md:614-626` instructs `core/03` to name the two-argument
store call and add a cascade test; `core/03-share-registry.md:296-303` and
test 12 at `:515-518` already do both.
Severity: drift.
Resolution: rewrite the amendment as a cross-reference, not a to-do.

### 1.10 `state.md` says no core document names the grant wrappers; `core/00` does

`foundation/state.md:684-690,702-711` flags a pending amendment ("no
existing core document (`00` through `11`) currently names it");
`core/00-overview.md:112-123,213` already specifies `grants.go` with the
four wrappers, the rationale, and the build-order slot.
Severity: contradiction.
Resolution: replace the flag with a reference to `core/00-overview.md`.

### 1.11 `state.md` misdescribes the `QuotaSink` wiring pattern

`foundation/state.md:271-275` says the server wires `core.New` with the
`*state.DB` value "as `09-quota-and-aggregates.md`'s pattern for
`QuotaSink` also does"; `core/09:330-333` wires
`core.AttachQuotaSink(state.NewQuota(db))`, a different pattern.
Severity: drift.
Resolution: drop the comparison or state the quota pattern accurately.

### 1.12 The `LinkStore` wiring seam is decided outside the core documents

`core/10-share-links.md` names no attach seam for the store (only
`AttachLinkCrypto` at `:508`); `foundation/state.md:273-274` asserts "the
server wires `core.New` with the `*state.DB` value directly", a core-side
construction contract no core document states.
Severity: gap.
Resolution: name the seam in `core/10` or `core/00`; `state.md` cites it.

### 1.13 `LinkRowPatch.Perms` is `*int64` against `LinkRow.Perms uint16`

`core/10-share-links.md:421` versus `:437`; the patch mirrors `LinkPatch`
whose field is `*acl.Perms` (`uint16` per `acl-evaluator.md:37`).
`foundation/state.md` copies both by reference.
Severity: drift.
Resolution: make `LinkRowPatch.Perms` a `*uint16`.

### 1.14 `ShareLabel` takes a raw `uint32` where every other signature takes `ShareID`

`core/03-share-registry.md:385` against `core/02-domain-types.md:27` and
every other core signature.
Severity: drift.
Resolution: use `ShareID`, or state once why the projection drops the type.

### 1.15 `Link.Token` is a pointer while `CreateLink` returns a `secret.Secret` value

`core/10-share-links.md:41` versus `:113`; `foundation/kit.md:101-110`
defines `Secret` as a value type.
Severity: drift.
Resolution: pick one and document the single plaintext handoff.

Clean: the `LinkStore` interface itself (all nine methods identical between
`core/10:443-475` and `state.md:258-269`), `QuotaSink.Reserve`,
`PersistGrant`'s signature, `ScanSource` (byte-identical between `core/03`
and `foundation/search-contract.md`), the reserved id values, the VFS
errno-to-core sentinel chain names, `journal.Op` and the recent surface,
`acl.RootEntry` decoration.

## 2. Layout inconsistencies

### 2.1 Three `store/*` packages are specified to import `infra/vfs`, which the tier table forbids

`03-engine-bootstrap.md:50-57` allows `store` to import `kit` only, and the
exception list (`:59-72`) does not add `infra`. But
`foundation/cache.md:70` says `store/ident` "imports only `vfs` (for
`ShareID`)", `foundation/journal.md:76-80` types `Event.Share` as
`vfs.ShareID` and `Event.Path` as `vfs.SharePath`, and `foundation/cache.md`
spells `vfs.ShareID`/`vfs.SharePath` throughout the `store/cache` API.
`tools/layercheck` as specified would refuse the packages as specified.
Severity: contradiction.
Resolution: add a `store` over `infra/vfs` exception (or move the id and
path types to `kit`) in `03-engine-bootstrap.md`.

### 2.2 The survey's 3-layer import table disagrees with the bootstrap's 5-tier table

`01-package-survey.md:15-22` folds `vfs` and `jail` into "a foundation tier
that any layer may use"; `03-engine-bootstrap.md:50-57` makes `infra` its
own tier that `store` may not import. Under one wording finding 2.1 is
legal, under the other it is not.
Severity: drift.
Resolution: replace the survey's table with a pointer to the bootstrap's.

### 2.3 Five core documents spell file paths as bare `core/x.go`

`core/00:130`, `core/01:10`, `core/02:16`, `core/05:13,15`, `core/06:10`
use `core/errors.go`-style paths, colliding with the document directory
`docs/refactor/core/`; `core/03`, `04`, `09`, `10`, `11` use the full
`engine/service/core/...` spelling, and `03-engine-bootstrap.md:88-91`
claims every phase 0 and 1 document already uses the layout.
Severity: drift.
Resolution: normalize to `engine/service/core/<file>.go`, or state the
shorthand once in `core/00-overview.md`.

Clean otherwise: every `engine/` path in the tree fits the five-tier
layout; the four tree diagrams (README, survey, decisions, bootstrap) agree
on package membership, the bootstrap adding only `unixprobe` detail and
phase annotations; no `utils`, `common` or `helpers` path exists.

## 3. Decision record drift

### 3.1 D6 cites `core/11` for `grants.go`, which `core/11` never mentions

`00-decisions.md:98-102` credits the wrapper file to `core/11`;
`core/11-homes-and-recent.md` contains no `grants.go`, `CreateGrant`,
`UpdateGrant` or `DeleteGrant`; the wrapper lives in
`core/00-overview.md:112-123`.
Severity: drift.
Resolution: re-cite D6 at `core/00`.

### 3.2 "D14" has two live meanings

`foundation/state.md:153-155` (and `audit/foundation-persistence.md:486`)
cite "the project's D14 rule that nothing here is ever built from parts",
a constant-SQL rule; `00-decisions.md`'s D14 is "Gates from the first
commit". The intended reference is the old tree's separate decision
numbering (`go/internal/acl/sql.go`), which the rebuild's record now
shadows.
Severity: contradiction.
Resolution: spell the constant-statement rule out in `state.md` instead of
citing a number.

### 3.3 "65 defect citations" is not supported by the audits

`00-decisions.md:141` and `02-document-plan.md:11` both state 65; the
audits carry 311 numbered findings, 92 non-`none` tagged, 21 tagged
`defect`. No count reaches 65.
Severity: drift.
Resolution: recount and restate, or use a wording the audit tags support.

### 3.4 D11 cites `core/11`, which says nothing about the cascade

`00-decisions.md:118-127` cites `foundation/state.md`, `core/03`, and
`core/11` for the no-FK cascade decision; `core/11` mentions neither
"cascade" nor `DeleteShare`, only the reserved id.
Severity: gap.
Resolution: narrow D11's `core/11` citation to the reserved id.

### 3.5 `PublishNew` is simultaneously moved and dropped

`foundation/fsatomic.md:138` decides "drop `PublishNew`; it does not move";
`foundation/vfs.md:948` Deliberate changes item 1 still headlines
"`ReplaceFileDurable` and `PublishNew` move to `store/fsatomic`";
`01-package-survey.md:88,174-176` says both move; `00-decisions.md` D8 has
both outcomes in one sentence.
Severity: drift.
Resolution: restate the vfs item, the survey rows and D8 as "moves
`ReplaceFileDurable`; drops `PublishNew`".

### 3.6 Line counts disagree between the survey, the audits, and `core/00`

`foundation/dbfile.md` and `foundation/journal.md` correct two survey
counts; the audit records three more the survey never corrects
(`store/state` ~2,600 vs 3,062, `acl` ~1,080 vs 782), and
`01-package-survey.md:131`'s 5,190 for `core` against
`core/00-overview.md:34`'s "roughly 6,800" count different things (without
and with tests) with neither saying which.
Severity: drift.
Resolution: correct the survey table or add the same correction notes, and
state in `core/00` that its number includes tests.

## 4. Plan drift

### 4.1 The plan and the survey disagree on when the core may start

`02-document-plan.md:52` says "when 0.2 and 0.3 are done";
`01-package-survey.md:202` says "when 0.1 through 0.5 are done". The
survey is right: `core/00-overview.md:172-178` lists `acl` and four `kit`
packages as core dependencies.
Severity: contradiction.
Resolution: change the plan to "0.1 through 0.5".

### 4.2 The bootstrap cites the plan for a violation catalogue the plan does not hold

`03-engine-bootstrap.md:130` names `02-document-plan.md` as layercheck's
initial test fixture source; the catalogue is
`01-package-survey.md:155-178` ("Cross-layer violations found"), and the
plan itself points at the audits (`02-document-plan.md:115-118`).
Severity: contradiction.
Resolution: repoint the bootstrap at the survey's violations section.

### 4.3 The plan still awaits a `LinkStore` amendment that 0.3b retired

`02-document-plan.md:70-72` says an amendment "lands when 0.3b is written";
`foundation/state.md:276-278` concludes "No amendment to `core/10` is
required", verified method for method. Meanwhile the amendment 0.3b did
flag (the grant wrapper, finding 1.10) is not in the plan.
Severity: drift.
Resolution: record the outcome in the plan and name the actual amendment.

### 4.4 Four audit-required documents were redistributed without a rename note

`audit/foundation-persistence.md:904-913` requires
`foundation/search-family.md`, `search-index-format.md`,
`search-service.md`, and `jail.md`; the plan places them at
`search/00-02` and `preview/01-jail.md` with no note, unlike the note the
audit carries for `watch` (`:916`). `foundation/search-contract.md`, the
search document that does exist, is absent from the audit's list.
Severity: gap.
Resolution: add the rename notes and list `search-contract.md` as
satisfying the core-facing slice.

### 4.5 `audit/service.md` requires a document under a directory the plan never creates

`audit/service.md:939-941` names `docs/refactor/store/upload-aggregate.md`;
the plan has no `store/` document directory, and `foundation/state.md`'s
uploads bullet ("carried forward unchanged") does not answer the row-shape
question the audit raises.
Severity: drift.
Resolution: fold the question into the plan's upload phase row or answer it
in `state.md`'s uploads bullet.

### 4.6 Preview document names split differently in the audit and the plan

`audit/service.md:942-951` wants `01-decode-limits.md` and
`02-jail-and-worker-pool.md`; `02-document-plan.md:85` plans `01-jail.md`
and `02-worker-protocol.md`, leaving decode limits unassigned.
Severity: drift.
Resolution: reconcile the lists and say where decode limits land.

### 4.7 The grant write surface is implicitly assigned to phase 3

`audit/presentation.md:1358-1364` item 8 ("Grant write surface spec") is
answered by `foundation/state.md:405-500` (phase 0) and
`core/00-overview.md:112` (phase 1), but `02-document-plan.md:99`
attributes it only to `http/03-handlers.md`, where the callers live.
Severity: gap.
Resolution: note the phase 0 and phase 1 halves in the plan.

### 4.8 Stray table delimiter in the survey's violation list

`01-package-survey.md:178` ends violation 6 with a trailing ` |` left over
from the table above.
Severity: drift.
Resolution: delete the trailing pipe.

Clean on existence: all 30 planned or promised documents exist; no missing
planned document, no unplanned document.

## 5. Behavioral spec conflicts

### 5.1 `chargeQuota`'s sign convention is inverted between `core/09` and its callers

`core/09-quota-and-aggregates.md:148-156` says "Positive delta credits
freed bytes through `Release`" with no negative-delta clause;
`core/06-mutations.md:211-213` and `core/08-trash.md:200-201` credit with
`int64Minus(freed)`, an explicitly negative delta, and
`core/06:309-311`'s `PublishPart` passes a positive `deltaOf` when a file
grows. As written, deletes credit nothing and a growing write refunds the
user its own bytes.
Severity: contradiction.
Resolution: fix `core/09`'s bullet: negative delta credits through
`Release`, positive delta books.

### 5.2 `QuotaSink.Release` is documented unsigned and typed signed

`core/09:86-90` says "an unsigned magnitude" one line above
`delta int64`, then `:128-131` and `foundation/state.md:383-388` make a
negative delta an error, which only a signed type can carry.
Severity: contradiction.
Resolution: strike "unsigned magnitude"; the contract is a non-negative
`int64`, credit only.

### 5.3 A revoked link gets two different errors

`core/01-errors.md:35` puts "revoked" under `ErrLinkExpired`;
`core/10-share-links.md:31-33` makes revocation a row delete, and
`:242-247` requires an unknown token to answer `ErrNotFound` as an
information-leak defence. `core/01` mandates the answer `core/10` forbids.
Severity: contradiction.
Resolution: drop "revoked" from `core/01`'s `ErrLinkExpired` row.

### 5.4 `vfs.ErrNotADirectory` carries two opposite meanings

`foundation/vfs.md:207` produces it when the path is not a directory;
`vfs.md:899` maps `EISDIR` (the target is a directory) to the same
sentinel. A caller cannot branch on it.
Severity: contradiction.
Resolution: give `EISDIR` its own sentinel.

### 5.5 `core/01`'s `mapVFSErr` table omits `ErrNotADirectory`

`core/01-errors.md:88-108` passes unnamed errors through as infrastructure
failures; `vfs.md:899` makes `ErrNotADirectory` a first-class sentinel
from ordinary syscall wrappers, and `vfs.md:917-921` warns each sentinel
addition is a hand-maintained cross-package contract.
Severity: gap.
Resolution: add the row (and decide `ErrInvalidName`'s scope) in `core/01`.

### 5.6 Reserved id spelling drift

`999_999` versus `999999` (`core/11:27,278` against everywhere else) and
`1_000_000` versus `1,000,000` (`core/03:233,507`). Values and meanings
agree at every mention.
Severity: style.
Resolution: use the underscore spelling outside prose totals.

Clean otherwise: page sizes and clamps, cursor semantics, etag derivation
and the inode-versus-aggregate token split, permission bits per operation,
trash layout and retention, link liveness and identity pin, journal op set.

## 6. The API map versus the route table

Route coverage is exact: all 101 routes in
`go/internal/server/routes.go` appear exactly once in
`http/09-api-consistency.md`'s tables or its out-of-scope list
(`/s/{token}*`, emergency); nothing missed, nothing invented, no
duplicate or misspelled method or path. The findings are in the
surrounding prose:

### 6.1 "Nine categories" enumerates ten

`http/09-api-consistency.md:70`, `00-decisions.md:161` and
`02-document-plan.md:105` all say nine, then list auth, account, files,
links, trash, jobs, uploads, search, admin, system.
Severity: contradiction.
Resolution: change "nine" to "ten" in all three places.

### 6.2 Rule 8 makes `jobs/*` permission-scoped without a bit to require

`http/09:61-64` versus `routes.go:151-161`, where all four job routes are
`AccessAny`, and `go/internal/httpapi/route/route.go:34-36` names job
status as the canonical `AccessAny` case.
Severity: gap.
Resolution: move `jobs/*` to the any-authenticated class or name the bit.

### 6.3 Rule 8 contradicts the document's own server-only list for TUS discovery

`http/09:61-64` scopes `uploads/*` by permission; `http/09:230-232` keeps
TUS discovery server-only, and both `OPTIONS` routes in `routes.go:54-55`
are `any` by design.
Severity: gap.
Resolution: name the two `OPTIONS` routes as rule 8 exceptions.

### 6.4 `links/*` merges two families with different permission bits and names neither

`http/09:131-137` folds `/api/fs/link` (`acl.Read`, `routes.go:126-131`)
and `/api/shares` (`acl.Share`, `routes.go:136-140`) into one resource;
`core/10:117` requires `acl.Share`, and the document never says which bit
`links/*` requires.
Severity: gap.
Resolution: state `acl.Share` in the links table and record the tightening
of the `acl.Read` half as deliberate.

### 6.5 Rule 8 silently moves logout off `AccessAny`

`http/09:59-61` excepts only login and OIDC entry points from
session-only; `POST /api/auth/logout` is `any` today (`routes.go:70`,
named in `route.go:34-36`), and the table row carries no note.
Severity: drift.
Resolution: add logout to the exception list or note the tightening.

### 6.6 The admin table lists `GET /api/v1/admin/settings` twice against its own fixture rule

`http/09:190` and `:193` are two rows with one method and pattern (a
deliberate fold of two old routes), while the Tests section (`:266-271`)
declares the tables a fixture with no duplicate method plus pattern.
Severity: gap.
Resolution: make the second row a note on the first, or state that the
fixture deduplicates.

## 7. Style

Clean. Zero em dashes, en dashes, middle dots, or Hangul across all 30
documents; double hyphens appear only as table separators and CLI flags
inside code blocks; no marketing adjectives (the one "first-class" hit in
`audit/service.md:302` is a technical use).

## Totals

| Severity | Count |
| --- | --- |
| contradiction | 14 |
| gap | 13 |
| drift | 16 |
| style | 1 |

44 findings. The most consequential: 2.1 (the layer gate as specified
refuses three foundation packages as specified), 5.1 (the quota ledger as
specified never credits a delete and refunds a growing write), 1.2 (two
incompatible `GrantRow` types at the store-evaluator seam), and 1.10 with
4.3 (a flagged amendment that is already applied and an applied amendment
the plan still lists as pending, so an implementer reading either document
alone gets the wrong picture of what is left).
