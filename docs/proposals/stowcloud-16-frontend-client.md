# Frontend API client - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

The API client is updated for the five REST adaptations in
[`stowcloud-8-http-and-api.md`](stowcloud-8-http-and-api.md) §4.4. The existing
edit-conflict route and dialog get the one behavioural correction needed for a
weak `If-Match`; no other UI surface changes.

## 2. Background & Motivation

The parent proposal's answer to "how much compatibility" was that both the API
and the schema are open to redesign. That permission was then narrowed on
purpose: churn without a recorded reason costs the frontend a rewrite and buys
nothing, so only the five surfaces with a defect recorded in an existing
proposal change.

That leaves this phase as an adaptation rather than a rewrite. It is stated as
its own document anyway, because "the frontend still works" is a deliverable
that otherwise belongs to nobody and gets discovered at cutover.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] The API client updated for the five changes, with its types regenerated
      or hand-updated to match.
- [ ] The error-text allowlist and locale catalogues updated for any new
      catalogue key, in every locale the repository carries.
- [ ] The upload path updated if the TUS surface moved, and confirmed unchanged
      if it did not. Note that there is no `upload/` directory: the upload calls
      are in `http.ts` with everything else, and the worker lives outside
      `lib/api`.
- [ ] `events-transport.ts` confirmed against whatever framing Phase 5f settles
      on.
- [ ] The existing edit-conflict retry omits `If-Match` only after the user
      explicitly chooses overwrite. Resending a weak validator would always
      fail strong comparison.
- [ ] `npm run build`, the i18n check and the frontend test suite green.
- [ ] The embedded build proven: a binary built with `embed_ui` serves the SPA
      that was just built, not a previous one.

### 3.2 Non-Goals

- [ ] A UI redesign. Route structure, the design system and virtual scroll are
      untouched. The existing edit route and conflict dialog are adapted, not
      replaced.
- [ ] A framework or dependency change.
- [ ] New screens for anything the Go port adds. The two new operator-visible
      states (a hardening degradation, a weak ETag) surface through existing
      health and conflict screens.
- [ ] Removing the mock client. `VITE_API_MOCK` and the guard that throws when
      it reaches a production build both stay.

## 4. Technical Design

### 4.1 Architecture Overview

The client changes are concentrated in `web/src/lib/api`:

```
web/src/lib/api/
  client.ts          the one mock-or-real selection boundary
  error-text.ts      catalogue key to rendered text
  types.ts           the wire types
  http.ts            the fetch wrapper and everything else: sharesList,
                     recentList, list,
                     stat, writeFile, archiveList, the settings calls. The five
                     surface adaptations stay in here
  share.ts           the public-link page's client, not admin shares
  oidc.ts  setup.ts  untouched by the five changes
  events-transport.ts  the WebSocket client half; see the note below
  mock.ts  mock-seed.ts  path-utils.ts  untouched
```

Two existing UI files also change:

```
web/src/routes/(app)/edit/[...path]/+page.svelte
                     explicit unconditional retry after a 412
web/src/lib/ui/EditConflictDialog.svelte
                     weak-validator wording through existing controls
```

An earlier draft of this section invented a file per surface (`shares.ts`,
`recent.ts`, `listing.ts`, `settings.ts`, `archive.ts`, an `upload/` directory)
and none of them exists. **Splitting `http.ts` is not a precondition for this
phase** and is not proposed here: the five adaptations stay inside its existing
functions, and a refactor bundled into an API migration makes the diff
unreviewable.

**`events-transport.ts` is the one file that may need more than an adaptation.**
It is the client half of the WebSocket channel, and
[`8`](stowcloud-8-http-and-api.md) §4.3.5 records that the server side was
mis-specified as push-only. If the ported server keeps the current frame
vocabulary this file is untouched; if the Phase 5f dependency decision changes
the framing, this is where it lands. Phase 12c confirms which.

### 4.2 Data Model Changes

Wire types only:

| Type | Change |
|---|---|
| `SharePath` | one vocabulary; the subpath is an explicit field rather than something the client reconstructs |
| `RecentQuery` | `since` is an ISO-8601 instant, not a date |
| `Entry` | one rollup size field, with the unit documented; `etag_weak` added |
| `Setting` | a discriminated union carrying the declared range |
| `ArchiveListing` | `truncated` and `limit` fields |

`etag_weak` is the only genuinely new field. It follows the existing wire
naming (`mtime_ns`, `dir_etag`) because `http.ts` returns JSON objects without a
camel-case mapping layer. The current Linux implementation
sets it for metadata-derived file tokens because
[`stowcloud-7`](stowcloud-7-core-domain.md) §4.3.2 cannot produce a strong
validator from `statx`. The conflict screen explains that the conditional write
was refused and makes an unconditional retry an explicit user choice.

The retry detail matters. The first save may send the weak ETag and receive
412, which is the server correctly refusing weak strong-comparison. The
dialog's overwrite action then calls `writeFile` **without** `If-Match`.
Resending either the original or returned weak token loops forever because a
weak validator can never satisfy `If-Match`. Only this explicit action may omit
the header.

### 4.3 Core Logic

#### 4.3.1 The error envelope is unchanged

Both backends use the existing
`{error:{code,message,detail?}}` envelope. Localized validation data stays in
`detail.reason_key` and `detail.reason_params`; `Sc-Trace` stays a response
header and is not duplicated in the body. Phase 12 does not add a dual-read
transition because Phase 13 can exercise both backends with the current client
shape.

The rule that produced this design holds on the Go side (D15): the server sends
a key and placeholders, never a sentence. The settings screen printing
`detail.reason` raw, in Korean, whatever locale the reader had picked, is what
the rule exists to prevent and it is the reason the i18n check exists.

The stance is that **the server never decides what language a reader wants**,
and it has a consequence this phase owns: a key the server can send and the
frontend cannot render is a defect on this side of the wire, not the other.
That is why the i18n check is a gate rather than a report, and why §4.3.2 does
not permit an English-only placeholder to land "for now".

The same stance governs one thing this phase must **not** add: the listing has
no MIME type field and the client does not invent one. The icon is chosen from
the name and the directory flag. A type guessed on either side invites
rendering something that should have been downloaded, which is the failure the
separate content origin exists to prevent.

#### 4.3.2 The i18n check

`web/tools/i18n-check.mjs` runs unchanged and it is the gate that catches a new
key with no translation. Every key added in this phase needs an entry in every
locale before the phase is done, and "English only for now" is not a state this
gate permits.

Note the operational limitation recorded in the repository: the Windows
development box has no node, so neither the frontend build nor the i18n check
runs there and `verify.sh` reports SKIP. The Linux VM is where this phase is
actually verified, which matches where the rest of the port is verified anyway.

#### 4.3.3 The embedded build

The check that matters at the end of this phase: build the frontend, build the
binary with `embed_ui`, serve it, and confirm the bundle hash the browser loads
is the one just built.

The Rust tree needed `cargo clean -p sc-http` for this, because cargo had no
dependency edge to the bundle, and shipping a stale UI happened once: a binary
whose embedded SPA predated the routes it served, which looked like a frontend
bug for as long as it took to notice the bundle hash had not moved.

`//go:embed` has a real dependency edge, so the failure should be impossible.
"Should be" is why the check runs anyway, once, in this phase.

**The bundle is built into the embedding package.** `//go:embed` rejects a
pattern that leaves the package directory (`invalid pattern syntax`) and
rejects a symlink pointing out of it (`cannot embed irregular file`), so the
frontend's output directory is `go/internal/httpapi/spa/build/` and not
`web/build/`. That is what makes the dependency edge exist at all: an embed
that cannot name the files has no edge to them.

## 5. API Design

### 5-1. New / Modified

No server API. The client-side surface:

```ts
export interface ApiErrorBody {
  error: {
    code: string;
    message: string; // stable fallback, not rendered localized copy
    detail?: Record<string, unknown>;
  };
}

// etag_weak reports that Linux statx exposes no inode change version from which
// this server can derive a strong change token. A conflict check against a weak
// token is advisory. A weak If-Match is refused, and the UI requires an
// explicit unconditional retry rather than promising a guarantee it does not
// have.
export interface Entry {
  // ...
  etag: string;
  etag_weak: boolean;
}
```

### 5-2. Error Handling

Unchanged. `error-text.ts` renders a recognized `detail.reason_key`, then a
recognized `code`, and otherwise uses the caller's translated action-specific
fallback. It never renders `message` or `detail.reason` as localized UI copy.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 12a | `types.ts` and the five surface adaptations in `http.ts` | S | Phase 5 | heavycaffeiner |
| Phase 12b | `error-text.ts`, the edit route and `EditConflictDialog`: weak retry and locale copy | S | 12a | heavycaffeiner |
| Phase 12c | The upload path and `events-transport.ts`: confirm or adapt | S | Phase 6, Phase 5f | heavycaffeiner |
| Phase 12d | The embedded-build check in §4.3.3 | S | 12a, Phase 5 | heavycaffeiner |

### 6-2. Dependencies

No new npm dependency. Node 24 in the build image, unchanged, for the reason
`Dockerfile` records: `package-lock.json` was written by npm 11 and npm 10 reads
it as out of sync.

## 7. References

- `web/src/lib/api/`: the client this phase changes.
- `web/src/routes/(app)/edit/[...path]/+page.svelte` and
  `web/src/lib/ui/EditConflictDialog.svelte`: the narrow weak-retry correction.
- `web/src/lib/api/error-text.ts`: the catalogue §4.3.1 adds keys to, and the
  place the `detail.reason` incident was fixed.
- [`stowcloud-8-http-and-api.md`](stowcloud-8-http-and-api.md) §4.4: the five
  changes, and the rule that limits them to five.
- `web/tools/i18n-check.mjs`, `web/src/lib/api/error-text.ts`.
- `scripts/verify.sh`: the embed steps, and the `cargo clean -p sc-http` note
  §4.3.3 refers to.
