# Share Link Browsing and Viewer Navigation - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-11                       |
| Status     | **Implemented**                  |
| Reviewers  |                                  |

---

## 1. Summary

A share link on a folder shows one flat list of that folder's immediate
children and stops there. A subfolder in that list is a name in a `<span>`:
it cannot be opened, its contents cannot be reached, and no file in the list
can be downloaded on its own. The single action the page does offer,
"Download all", cannot work at all, and each attempt spends one of the link's
downloads before failing.

This proposal makes a shared folder browsable: a subpath, revalidated against
the link on every request, plus a per-file download and a folder download that
actually delivers bytes. Three defects found in the same code path while
tracing it are fixed here rather than filed away, because all three are in the
lines this work already touches.

The second half is unrelated to the first and is here because it is the other
half of the same complaint about looking at shared files: the file viewer's
previous and next arrows are removed from the layout at the ends of a folder,
so the picture slides sideways when the first or last file is reached.

## 2. Background & Motivation

### 2.1 A shared folder is a dead end

`GET /s/{token}` answers with the link target's metadata and, for a directory,
one array of entries. That array comes from `Core::link_list`, which lists
`link.path` and nothing else:

```rust
// crates/sc-core/src/links.rs
pub fn link_list(&self, link: &ShareLink) -> Result<Vec<Entry>, CoreError> {
    ...
    let mut names: Vec<String> = root.read_dir(&link.path)?...
```

There is no parameter for a subpath, so no caller can ask for one. The page
renders what it gets as text:

```svelte
<!-- web/src/routes/s/[token]/+page.svelte -->
{#each info.entries ?? [] as e (e.name)}
  <li class="sc-share__row">
    <span class="sc-filename sc-share__name">{e.name}</span>
```

A recipient who is sent a folder of a hundred photos in four subfolders sees
four names they cannot click and nothing else. The only way to get at the
contents is the whole-folder download, and that brings us to the next item.

### 2.2 "Download all" fails, and each attempt spends a download

`POST /s/{token}/download` mints a signed content URL for the link target:

```rust
// crates/sc-http/src/routes.rs
let claim = content::make_claim(fid, link.etag8, Disposition::Attachment, None, 0, None);
```

For a folder link, `fid` is the directory's file id. The content origin
resolves a claim to one file's bytes, and a directory has none:

```rust
// crates/sc-core/src/stream.rs
if st.kind == Kind::Dir {
    return Err(CoreError::InvalidPath("is a directory".into()));
}
```

`serve_original` maps every error other than `Gone` to `404`, so the visitor
gets a not-found on a link that exists and is live. Worse, the download
counter is consumed before the URL is minted, deliberately, so that a
half-finished transfer cannot be replayed:

```rust
// crates/sc-http/src/routes.rs, in public_link_download
if let Err(e) = state.core.share_link_note_download(id) { ... }
```

A folder link with `max_downloads = 3` is therefore dead after three clicks on
a button that never worked once. The authenticated equivalent goes through
`POST /api/fs/archive`, which builds a zip as a durable job and is served by a
session-gated route; an anonymous visitor has no session and no job.

### 2.3 The public listing rejects names that already exist on disk

`link_list` joins each name it read from the directory with `SafePath::join`,
which applies the table for names being *created*:

```rust
let p = link.path.join(&name, max_depth)?;
```

`join` refuses `CON`, `a:b`, a trailing dot or space, and every other Windows
portability rule. `join_existing` exists for exactly this case and says so in
its own doc comment: walking a directory is traversal, not creation. Because
the `?` propagates, one such name does not skip one row, it fails the entire
listing. A shared folder containing a file named `NUL` answers with an error
for every visitor.

### 2.4 A link lists what its owner may not read

`link_list` builds an entry per child and then overwrites its permissions:

```rust
e.perms = link.perms;
```

No ACL is consulted for the child. The link's own permissions are checked
against the creator once, at mint time, at the link root. A `Deny` grant
placed below that root is not evaluated, so a directory the owner themselves
cannot read is listed through the link. At one level deep this is a bounded
mistake. Making the tree traversable turns it into the whole subtree, so it
has to be closed in the same change rather than after it.

### 2.5 The viewer's arrows move the picture at the ends of a folder

`PreviewDialog.svelte` lays the viewer out as three flex columns: previous,
stage, next. The two arrow columns are conditional:

```svelte
{#if hasPrev}
  <div class="sc-preview__nav sc-preview__nav--prev">
```

At the first file the left column is not rendered, so the stage absorbs its
width and the image recentres by half of it. At the last file it happens in
the other direction. Stepping through a folder with the arrow keys
therefore ends with the picture jumping sideways on its own, and the toolbar
above it, which is a separate flex row, does not move with it.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] A subfolder inside a share link can be opened, and the folder above it
      can be returned to, from the public page and from the API.
- [ ] The URL names the folder being looked at, so back, forward, reload and
      pasting the address to somebody else all work.
- [ ] Every subpath is parsed and re-resolved against the link on the server,
      on every request. Nothing about where the visitor is is trusted.
- [ ] A file inside a shared folder can be downloaded on its own.
- [ ] "Download all" downloads the folder being looked at, as a zip, and
      delivers bytes.
- [ ] A name that exists on disk can be listed, whatever a Windows client
      would think of it.
- [ ] A link never lists or serves anything its owner is not allowed to read.
- [ ] Browsing a link is bounded, so a traversable tree is not a free walk of
      the owner's storage for an anonymous caller.
- [ ] The viewer's arrows never change the position of what is on screen.

### 3.2 Non-Goals

- [ ] Thumbnails or previews on the share page. The 60 KB marginal budget for
      `/s/[token]` is what keeps that bundle out of the app's dependency
      graph, and an image grid is where that budget goes. A recipient
      downloads the file to look at it, as they do today.
- [ ] Uploading into a shared folder. A link that accepts uploads is a drop
      link, it lists nothing by construction, and that stays true: a drop
      link refuses every path in this proposal exactly as it refuses the
      listing today.
- [ ] A sort control, paging or virtual scrolling on the share page. The
      listing arrives as one array in one server-chosen order, which is what
      it does today.
- [ ] Search inside a share link.
- [ ] Renaming, deleting or otherwise writing through a read link.
- [ ] A recursive folder size on the share page. The reasoning in
      `stowcloud-17-audit-gaps.md` §3.2 applies unchanged, and an anonymous
      caller is the last one who should be able to start a tree walk per row.
- [ ] Browsing through the compat share API. The phone apps mount the tree
      over DAV and do not use the public page.
- [ ] A per-entry share link. One link, one target, one subtree.

## 4. Technical Design

### 4.1 Architecture Overview

One new concept, the **subpath**: a path relative to the link's own target,
carried as a query parameter, never stored, and re-resolved from the link row
on every request. It is not a session, not a cursor and not a cookie. A
visitor who edits it gets the same answer as a visitor who was handed it.

```
GET  /s/{token}?path=2026/07     listing (or metadata) for one node under the link
POST /s/{token}/download?path=…  one-time signed URL for one file under the link
GET  /s/{token}/zip?path=…       streamed zip of one directory under the link
```

`sc-core` gains the resolution primitive and a listing that takes it;
`sc-http` gains the parameter on three routes, a narrower public entry shape,
and the zip stream. The frontend gains a breadcrumb and per-row actions.

### 4.2 Data Model Changes

None. No column, no table, no migration. A share link already stores the one
path it needs; everything here is relative to it and lives only for the length
of a request.

### 4.3 Core Logic

#### 4.3.1 Resolving a subpath

```rust
// crates/sc-core/src/links.rs
pub struct LinkNode {
    pub path: SharePath,
    pub entry: Entry,
}

pub fn link_resolve_at(&self, link: &ShareLink, rel: &str) -> Result<LinkNode, CoreError>;
pub fn link_list_at(&self, link: &ShareLink, rel: &str) -> Result<Vec<Entry>, CoreError>;
```

`link_resolve_at` does five things in this order, and the order is the
contract:

1. **Liveness first.** `link_target` runs before anything else touches the
   subpath, so expiry, the download cap and the path-plus-fileid cross-check
   still answer `Gone` for a dead link no matter what path was asked for.
2. **Drop links are refused.** `is_drop()` or a link without `READ` answers
   `Denied` for every subpath, as `link_list` does today. This is a refusal
   inside `sc-core`; the handler never asks in the first place, because a
   pathless `GET /s/{token}` on a drop link answers exactly as it does now
   (the target's metadata, `drop: true`, no entries) and only a request that
   names a path reaches the resolver at all.
3. **Parse.** `SafePath::parse(rel, max_depth)` is the whole of the input
   validation, and it is enough: it rejects an absolute path, a `.` or `..`
   component, an embedded NUL, a component over 255 bytes, a path over 4096
   bytes, anything deeper than the share's policy, and the reserved prefixes
   that name this server's own control files. There is no normalisation step
   and no string concatenation anywhere in this path.
4. **Join.** Each parsed component is pushed onto a clone of `link.path` with
   `join_existing`, so the combined depth is checked against the share's
   `max_depth` rather than the relative depth alone.
5. **Authorize, then stat.** See below.

The empty subpath resolves to the link target itself, which is what makes
every existing caller keep working without passing anything.

#### 4.3.2 The owner's ACL is evaluated at every level

```rust
if !self.acl.effective(link.owner, link.share, &path).contains(Perms::READ) {
    return Err(CoreError::NotFound);
}
```

`NotFound`, not `Denied`: to an anonymous visitor, "you may not see this" and
"there is nothing here" must be the same answer, for the same reason the
password endpoint refuses to distinguish an unknown token from a wrong
password.

The same check filters each child in `link_list_at`, and a child that fails it
is omitted rather than reported. This is the fix for §2.4 and it applies to
the link root as well, so a link whose creator has since been denied below the
root stops listing what the deny covers.

`link.perms` and the owner's access are both upper bounds, and what a visitor
gets is their intersection. The first was fixed when the link was minted and
can never exceed what its creator held then; the second is re-read on every
request. Today only the first is consulted at access time. `update_link`
already re-checks the second, so revoking a grant stops the owner widening a
link they hold; what it does not do is stop that link from serving what it
already points at, and that is what this closes.

#### 4.3.3 Listing a name that already exists

`link_list_at` uses `join_existing`, not `join`. One word, and it is the
difference between listing a folder and returning an error for it (§2.3). A
name that even `join_existing` rejects (a reserved control prefix, which
`read_dir` already hides) skips that one row and the listing continues.

`archive.rs`'s `walk_rec` has the same `join` on a name it read from disk. It
skips rather than fails there, so the symptom is quieter and the result would
be worse here: the listing would show a file the zip silently left out. It
gets `join_existing` in the same change, which also stops the authenticated
archive from dropping those names.

Entries are ordered directories first, then by name, matching what the app's
own listing does. Today the public listing is a flat lexicographic sort, so a
folder can appear between two files.

#### 4.3.4 Downloading one file

`POST /s/{token}/download?path=…` resolves the subpath, refuses a directory
with `422`, requires `can_download`, counts one download, and mints the same
`sub = 0` claim the whole-target download mints today. The content origin
needs no share-link awareness, exactly as before.

**Every minted URL counts one download.** A folder link with
`max_downloads = 5` is spent after five files. That is the honest reading of a
cap whose purpose is to bound what leaves the server, and it is recorded in
§7 because it is a behaviour change an owner can be surprised by: before this
proposal, the same cap on a folder link bounded a button that did not work.

#### 4.3.5 Downloading a folder

`GET /s/{token}/zip?path=…` streams a ZIP64/STORE archive of one directory
under the link. It reuses `archive_zip::ZipStreamWriter` over a
channel-backed `Write`, so the zip is produced by a blocking task and consumed
by the response body as it is written: nothing is buffered whole, in memory or
on disk. The walk is `Core`'s existing recursion with the owner's ACL as the
per-entry filter.

Three deliberate differences from the authenticated archive:

- **No `_skipped.txt`.** The authenticated zip lists the paths it could not
  read, which is useful to their owner and is a list of file names to an
  anonymous visitor. The public zip omits unreadable entries silently.
- **It is a stream, not a job.** A job is owned by a user and its artifact is
  fetched from a session-gated route. There is no session here.
- **It holds an `archive_concurrency` permit** for the life of the stream and
  answers `429` with `Retry-After: 1` when the cap is full, which is what
  `POST /api/fs/archive` already does. An anonymous tree walk is precisely the
  thing that cap exists for.

It is served from the app origin with `Content-Disposition: attachment` and
`application/zip`, matching `GET /api/jobs/{id}/download`, which serves the
authenticated archive the same way. The `nosniff`, `no-referrer`,
`same-site` and `noindex` headers are applied by `middleware::security_headers`
to every response already. One download is counted per stream started.

#### 4.3.6 What a public listing puts on the wire

Today the handler serialises the internal `Entry` straight out:

```rust
body["entries"] = serde_json::to_value(entries).unwrap_or_default()
```

That ships the file id, the ETag, the permission set, the preview flag and the
confusable-name flag to an anonymous caller, of which the page reads three
fields. A public document's shape should be chosen rather than inherited, so
`sc-http` gains a projection carrying those three and nothing else:

```rust
struct PublicEntry {
    name: String,
    kind: Kind,
    size: u64,
}
```

No `mtime_ns`, deliberately: the page has never shown a date per row and this
proposal does not add one, so shipping the field would be the same mistake in
smaller print. The link target's own `mtime_ns` stays on the document, where
it already is.

None of the dropped fields is a secret on its own, and this is not presented
as a vulnerability fix. It is the same rule `PublicLink` already follows: the
public surface carries what the public page needs and nothing that happens to
be in scope.

#### 4.3.7 Bounding the walk

Traversal is the one thing here that widens an existing exposure: a flat
listing of the link root is one `read_dir` per request, and a browsable tree
is one per directory in it. The general limiter (a 60-request burst refilling
at one per second, per IP) is the wrong axis for a link, for the reason
`KeyedTokenBucket`'s doc comment already gives: one office behind a NAT shares
a bucket, and a botnet gets one bucket per host against a single link.

So listing joins the password check on the per-token axis, with its own
budget: a second `KeyedTokenBucket` in `AppState`, `new(60, 1s)`, so sixty
folder openings back to back and one a second after that, checked by
`GET /s/{token}` and `GET /s/{token}/zip`. That is more than a person opening
folders and it caps a crawler at one directory a second per link. This is the
only new server state in this proposal.

### 4.4 Frontend, the share page

The subpath lives in the query string: `/s/{token}?path=2026/07`. Navigation
is `goto`, so each folder is a history entry and the browser's own back button
is the way back out. A reload works because the server serves the SPA document
for any `Accept: text/html` request to `/s/{token}` regardless of the query,
and the page reads the path back from `page.url`.

- A **breadcrumb** replaces the bare `<h1>`: the link's label as the root,
  then one button per segment, marked up as a `<nav>` with an ordered list and
  `aria-current="page"` on the last item.
- A **directory row** becomes a button carrying the folder's name. A file row
  is unchanged except for its size cell.
- A **file row** gains a download control when `can_download` is set. It uses
  the `Button` component the page already imports rather than `IconButton`, so
  the bundle gains markup and no new module.
- **"Download all"** downloads the folder currently being shown, and its label
  says so.
- A `path` the server refuses clears back to the link root and says the folder
  is no longer there, rather than showing an empty list that looks like an
  empty folder.
- The password form and the drop-link screen are untouched. A drop link never
  reaches this code: it has no entries and no path.

New i18n keys, added to `en.json` and `ko.json` in the same commit, which is
what `lint:i18n` enforces: `public_share.location`,
`public_share.open_folder`, `public_share.folder_gone`,
`public_share.download_folder`.

The marginal bundle cost is a breadcrumb's worth of markup plus `goto`, which
is part of the SvelteKit client runtime the shell already loads and therefore
not marginal. `check:bundle-size` is the gate that decides this, not this
paragraph.

### 4.5 Frontend, the viewer's arrows

Both navigation columns are always rendered. The one with no neighbour is
hidden with `visibility`:

```svelte
<div class="sc-preview__nav sc-preview__nav--prev" class:sc-preview__nav--empty={!hasPrev}>
```

```css
.sc-preview__nav--empty {
  visibility: hidden;
}
```

`visibility: hidden` keeps the box, so the stage's width and the image's
centre never depend on where in the folder the viewer is. It also takes the
button out of the tab order and out of the accessibility tree, and it receives
no pointer events, so `IconButton`'s hover tooltip cannot fire on an arrow
that is not there.

Two alternatives were rejected:

- **`{#if}`**, which is the current code and the defect.
- **A disabled button.** m3-svelte draws a disabled button as
  `--m3c-on-surface` at 38% on a 12% background of the same token. The viewer
  draws on a scrim that is 93% black in both themes, and picks its own
  foreground with `light-dark(...)` precisely because `--m3c-on-surface` is
  the dark half of its pair in the light theme. A disabled arrow there would
  be a dark smudge on black. Overriding the disabled palette through
  `:global()` to fix that adds a rule whose only job is to make a permanently
  dead control visible, which is not worth having.

Nothing about the keyboard changes: `onKeydown` already refuses `ArrowLeft` at
the first file and `ArrowRight` at the last.

## 5. API Design

### 5-1. New / Modified

```
GET   /s/{token}            path=<rel>   listing or metadata for a node under the link
POST  /s/{token}/download   path=<rel>   one-time signed content URL for one file
GET   /s/{token}/zip        path=<rel>   streamed ZIP64/STORE of one directory
POST  /s/{token}/auth                    unchanged
POST  /s/{token}/drop       name=<name>  unchanged
```

`path` is optional everywhere and defaults to the link's own target. A client
written against the current API therefore keeps working, with one deliberate
exception: a pathless `POST /s/{token}/download` on a **folder** link now
answers `422` without touching the counter, where today it spends one download
and then answers `404` (§2.2). That request has never produced bytes, so
nothing that worked stops working; what changes is that failing at it is no
longer expensive.

`GET /s/{token}?path=2026/07`:

```json
{
  "protected": false,
  "name": "07",
  "is_dir": true,
  "size": 0,
  "mtime_ns": "1786000000000000000",
  "label": "Holiday photos",
  "drop": false,
  "can_download": true,
  "path": "2026/07",
  "entries": [
    { "name": "beach", "kind": "dir", "size": 0 },
    { "name": "IMG_0001.jpg", "kind": "file", "size": 4213665 }
  ]
}
```

`name` describes the resolved node, not the link target; `label` still
describes the link, so the page can show both the link's title and where in it
the visitor is. `path` echoes what the server resolved, which is how a client
tells that its request was honoured. `entries` is present only when the
resolved node is a directory, and is still absent (not empty) for a file, a
drop link and a locked link.

`sc-core` and the `CoreApi` trait:

```rust
// sc-core
pub fn link_resolve_at(&self, link: &ShareLink, rel: &str) -> Result<LinkNode, CoreError>;
pub fn link_list_at(&self, link: &ShareLink, rel: &str) -> Result<Vec<Entry>, CoreError>;
pub fn link_archive_walk(&self, link: &ShareLink, rel: &str, visit: &mut …) -> Result<(), CoreError>;

// sc-http::core_api::CoreApi, replacing share_link_entries
fn share_link_node(&self, id: i64, path: &str) -> Result<PublicNode, CoreError>;
fn share_link_entries_at(&self, id: i64, path: &str) -> Result<Vec<PublicEntry>, CoreError>;
fn share_link_archive(&self, id: i64, path: &str, out: &mut (dyn Write + Send)) -> Result<(), CoreError>;
```

`Send` on that writer is load-bearing: the walk runs in `spawn_blocking` and
the writer is the sending half of the response body's channel.

`Core::link_list` is **removed**, not wrapped. Its only non-test caller is
`bridge.rs::share_link_entries`, which this proposal rewrites anyway, and the
two cases in `tests_links.rs` move to `link_list_at(link, "")`. Keeping a
one-line forwarder alive for two tests is how a second way to do the same
thing gets established.

### 5-2. Error Handling

| Condition | Result |
|---|---|
| `path` is absolute, contains `.`/`..`, a NUL, an over-long component, or a reserved control prefix | 404 |
| `path` is deeper than the share's `max_depth` once joined to the link | 404 |
| `path` names nothing | 404 |
| `path` names something the link's owner may not read | 404, identical to the line above |
| link expired, exhausted, or its target replaced | 410 |
| password set and not cleared, on `GET` | `{"protected": true}`, as today |
| password set and not cleared, on download or zip | 403 |
| drop link with a non-empty `path` | 403 |
| drop link, pathless `GET` | unchanged: metadata, `drop: true`, no entries |
| `download` names a directory | 422 |
| `zip` names a file | 422 |
| `max_downloads` reached | 410 |
| archive concurrency cap full | 429, `Retry-After: 1` |
| listing or zip requests past the per-token budget | 429, `Retry-After` |

The table is not evaluated top to bottom. §4.3.1's order is what decides which
row applies: liveness answers first, so a malformed path on a dead link is
`410`, not `404`; the drop-link refusal comes next; the path is only looked at
after both.

The four `404` rows answer identically on purpose. Malformed, absent and
forbidden are one answer, so a refusal tells a visitor nothing they could
have used to map what surrounds the subtree they were given.

Every request continues to be audited as `share.link_accessed` with the link
id, never the token. The subpath is **not** written to the audit log: the log
already identifies the link, and the point of not logging the token is that
log access must not become link access.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Estimated Duration | Owner |
|---|---|---|---|
| Phase 1 | `sc-core`: `link_resolve_at`, `link_list_at`, `link_archive_walk`, the owner-ACL filter, `join_existing` in both the listing and `walk_rec`, `link_list` removed. Tests: `..` and an absolute path refused, a denied child omitted, a `CON` name both listed and archived, a drop link refused for every path, a dead link answering `Gone` before the path is looked at | 1.5 d | heavycaffeiner |
| Phase 2 | `sc-http`: `path` on `GET /s/{token}` and on the download route, `PublicEntry`, the per-token listing budget, `testutil` and `CoreApi` updates | 1 d | heavycaffeiner |
| Phase 3 | `sc-http`: `GET /s/{token}/zip`, the channel-backed writer, the concurrency permit, headers, counter | 1 d | heavycaffeiner |
| Phase 4 | `web`: breadcrumb, folder rows, per-file download, folder download, the refused-path state, i18n in both locales | 1.5 d | heavycaffeiner |
| Phase 5 | `web`: the viewer's arrow columns, plus a `vitest` case asserting both columns are present at the first and last file | 0.5 d | heavycaffeiner |
| Phase 6 | Documentation: the route list in `stowcloud-6-preview-sharing.md` §5-1 and the error table in its §5-2, and the reading-order row in `docs/README.md`. §4.7 needs no edit: nothing this changes contradicts it | 0.5 d | heavycaffeiner |

Phase 1's tests are the load-bearing ones. Every defect in §2 survived because
the public path was exercised only at the link root, where a traversal test has
nothing to traverse.

Phase 3 lands after Phase 2 rather than beside it: the zip is the only part
that streams an unbounded walk to an anonymous caller, and it should not be
merged before the resolution and authorization it depends on are settled.

### 6-2. Dependencies

- No new crate. `ZipStreamWriter`, `archive_concurrency`, `KeyedTokenBucket`
  and the signed-URL machinery all exist and are used as they are.
- No schema change, so no migration and nothing to roll back.
- The web phases are verified in CI. `lint:i18n`, `check:bundle-size` and
  `check:design` need node, which is not installed on the workstation this is
  written on, and `verify.sh` reports PASS without them. A web change that has
  not been through CI is not verified, whatever the local script says.

## 7. Known limitations

- **Every minted download counts, including per-file ones.** A folder link
  with a small `max_downloads` is spent faster than its owner expects. The cap
  counts at mint time so that a broken transfer cannot be replayed for free.
- **An exhausted link stops browsing, not just downloading.** `link_target`
  treats the spent cap as `Gone`, which is existing behaviour and is what the
  whole public surface is built on, so a visitor who downloads the last
  permitted file loses the listing on their next click. Before this proposal
  a folder link could not spend its cap on files at all, so the two were never
  seen together. An owner who wants a folder browsed should leave the cap
  unset; one who sets it is asking for the link to stop.
- **A large folder is still one array.** A shared directory with a hundred
  thousand entries is serialised whole and rendered whole; the share page has
  no virtual scroll, because that is in the app bundle this one deliberately
  does not import. The per-token budget bounds how often that can be asked for,
  not how big one answer is.
- **The zip has no size up front**, so a browser shows an indeterminate
  download. That is inherent to ZIP64 with data descriptors and is the same
  for the authenticated archive.
- **A public zip is silent about what it skipped.** An entry the owner cannot
  read is omitted with no marker, unlike the authenticated archive's
  `_skipped.txt`. The recipient cannot tell a folder that was empty from one
  that was filtered.
- **Compression stays STORE**, so a shared folder of text does not compress.
  The reasoning in `stowcloud-6-preview-sharing.md` §4.8 is unchanged.
- **No previews and no dates.** The share page shows names and sizes, as it
  does today. Looking at a shared photo still means downloading it.
- **Links created before this change browse identically**, because nothing
  about a link row changes. There is no migration and no re-minting.
- **The compat apps are untouched.** They reach shared content over DAV and
  never load the public page.
- **The viewer's arrow fix is layout only.** At the ends of a folder the
  arrow is invisible and unreachable, exactly as it is today; what changes is
  that the picture no longer moves.
- **Phase 5's `vitest` case is not written.** It would be the first test in
  this repository that renders a Svelte component, and the workstation this
  was built on has no node, so it could not be run once before being made a
  blocking gate (`stowcloud-17-audit-gaps.md` §4.3.8 put `npm run test` in
  CI). A test nobody has seen pass is a worse gate than no test. The two
  columns are asserted by reading the component instead: both are rendered
  unconditionally and `sc-preview__nav--empty` sets `visibility: hidden`.

## 8. References

- `crates/sc-core/src/links.rs` (`link_list`, `link_target`, `link_drop`, the
  four load-bearing properties in the module doc),
  `crates/sc-core/src/stream.rs` (`open_stream_in`, the directory refusal),
  `crates/sc-core/src/archive.rs` (`archive_walk`, `walk_rec`),
  `crates/sc-vfs/src/safe_path.rs` (`parse`, `join`, `join_existing`, and the
  comment that distinguishes naming from creating),
  `crates/sc-http/src/routes.rs` (`public_link_get`, `public_link_download`,
  `public_link_drop`, `serve_original`, `job_download`),
  `crates/sc-http/src/{content,archive_zip,rate_limit,middleware}.rs`,
  `crates/sc-server/src/bridge.rs` (`share_link_public`, `share_link_entries`),
  `web/src/routes/s/[token]/+page.svelte`, `web/src/lib/api/share.ts`,
  `web/src/lib/ui/PreviewDialog.svelte`, `web/src/lib/ui/IconButton.svelte`
- `stowcloud-6-preview-sharing.md` §4.7 (the link's liveness rules and the
  drop-link contract), §4.8 (the streamed zip and why STORE), §5-1 (the route
  list this extends), §5-2 (the error table this extends)
- `stowcloud-2-core-vfs.md` §4.5 (ACL evaluation and inherited denies), §4.7
  (why a recursive size is not on a listing row)
- `stowcloud-15-sharing.md` (the two path vocabularies; the subpath here is a
  third and is deliberately relative to the link, never a vpath)
- `stowcloud-3-frontend.md` §6-2 (the byte budgets and the gates that hold
  them)
- `stowcloud-17-audit-gaps.md` §3.2 (folder size as a non-goal, and the CI
  gates that do and do not run)
