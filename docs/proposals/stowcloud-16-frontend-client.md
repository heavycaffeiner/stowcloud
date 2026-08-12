# Frontend API client - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

The smallest phase. `web/src/lib/api` is updated for the five REST changes in
[`stowcloud-8-http-and-api.md`](stowcloud-8-http-and-api.md) §4.4, and nothing
else in `web/` is touched.

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
- [ ] The error-text mapping updated for any new catalogue key, in every
      locale the repository carries.
- [ ] The upload worker updated if the TUS surface moved, and confirmed
      unchanged if it did not.
- [ ] `npm run build`, the i18n check and the frontend test suite green.
- [ ] The embedded build proven: a binary built with `embed_ui` serves the SPA
      that was just built, not a previous one.

### 3.2 Non-Goals

- [ ] A UI redesign. Components, routes, the design system and the virtual
      scroll are untouched.
- [ ] A framework or dependency change.
- [ ] New screens for anything the Go port adds. The two new operator-visible
      states (a hardening degradation, a weak ETag) surface through existing
      health and conflict screens.
- [ ] Removing the mock client. `VITE_API_MOCK` and the guard that throws when
      it reaches a production build both stay.

## 4. Technical Design

### 4.1 Architecture Overview

Only `web/src/lib/api` changes:

```
web/src/lib/api/
  client.ts       the fetch wrapper, the envelope, the trace header
  error-text.ts   catalogue key to rendered text
  types.ts        the wire types
  shares.ts       changed: one path vocabulary
  recent.ts       changed: ISO-8601 timestamps
  listing.ts      changed: the rollup field
  settings.ts     changed: typed values, named refusals
  archive.ts      changed: the truncation flag
  upload/         confirmed, likely unchanged
```

### 4.2 Data Model Changes

Wire types only:

| Type | Change |
|---|---|
| `SharePath` | one vocabulary; the subpath is an explicit field rather than something the client reconstructs |
| `RecentQuery` | `since` is an ISO-8601 instant, not a date |
| `Entry` | one rollup size field, with the unit documented; `etagWeak` added |
| `Setting` | a discriminated union carrying the declared range |
| `ArchiveListing` | `truncated` and `limit` fields |

`etagWeak` is the only genuinely new field, and it exists because
[`stowcloud-7`](stowcloud-7-core-domain.md) §4.3.2 can now tell the difference
between a strong and a weak change token. The conflict screen uses it to say
"this file may have changed" rather than asserting it did not.

### 4.3 Core Logic

#### 4.3.1 The error envelope is unchanged

`code`, `msg` as a catalogue key, `args`, `trace`. `error-text.ts` keeps its
shape, and the work is adding entries for new keys rather than changing how a
key is rendered.

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
dependency edge to `web/build`, and shipping a stale UI happened once: a binary
whose embedded SPA predated the routes it served, which looked like a frontend
bug for as long as it took to notice the bundle hash had not moved.

`//go:embed` has a real dependency edge, so the failure should be impossible.
"Should be" is why the check runs anyway, once, in this phase.

## 5. API Design

### 5-1. New / Modified

No server API. The client-side surface:

```ts
// Every response carries a trace id. The client attaches it to any error it
// surfaces, so a user-reported failure can be found in the server log without
// asking them to reproduce it.
export interface ApiError {
  code: string;
  msg: string;      // a catalogue key, never a sentence
  args?: unknown[];
  trace: string;
}

// etagWeak reports that the server could not derive a strong change token for
// this entry, because the filesystem carries no inode generation. A conflict
// check against a weak token is advisory, and the UI says so rather than
// promising a guarantee it does not have.
export interface Entry {
  // ...
  etag: string;
  etagWeak: boolean;
}
```

### 5-2. Error Handling

Unchanged. The client renders `msg` through `error-text.ts` and falls back to a
generic string with the trace id for a key it does not know, which is what makes
a server that added a key ahead of the frontend degrade rather than break.

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 12a | `types.ts` and the five changed modules | S | Phase 5 | heavycaffeiner |
| Phase 12b | `error-text.ts` entries for new keys, in every locale | S | 12a | heavycaffeiner |
| Phase 12c | The upload worker: confirm or adapt | S | Phase 6 | heavycaffeiner |
| Phase 12d | The embedded-build check in §4.3.3 | S | 12a, Phase 5 | heavycaffeiner |

### 6-2. Dependencies

No new npm dependency. Node 24 in the build image, unchanged, for the reason
`Dockerfile` records: `package-lock.json` was written by npm 11 and npm 10 reads
it as out of sync.

## 7. References

- `web/src/lib/api/`: the client this phase changes, and nothing else in
  `web/`.
- `web/src/lib/api/error-text.ts`: the catalogue §4.3.1 adds keys to, and the
  place the `detail.reason` incident was fixed.
- [`stowcloud-8-http-and-api.md`](stowcloud-8-http-and-api.md) §4.4: the five
  changes, and the rule that limits them to five.
- `web/tools/i18n-check.mjs`, `web/src/lib/api/error-text.ts`.
- `scripts/verify.sh`: the embed steps, and the `cargo clean -p sc-http` note
  §4.3.3 refers to.
