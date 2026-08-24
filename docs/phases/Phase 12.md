# Phase 12: frontend API client

## Before anything else

Read [`Ground rules.md`](Ground%20rules.md) in full, then
`docs/proposals/stowcloud-16-frontend-client.md`.

## Scope

`web/src/lib/api` is updated for the five REST adaptations. The existing edit
route and conflict dialog get the narrow weak-validator retry correction; no
other UI surface changes.

Depends on Phases 5 and 6. Blocks Phase 13.

## Milestones

- **12a**: `types.ts` and the five changed surfaces.
- **12b**: `error-text.ts`, the edit route and `EditConflictDialog`: weak retry
  and entries for new keys in every locale.
- **12c**: the upload path and `events-transport.ts`: confirm or adapt.
- **12d**: the embedded-build check.

## Traps

- **Do not restructure `web/src/lib/api`.** The five changed surfaces are five
  adaptations inside existing `http.ts` functions, and splitting that file
  inside an API migration makes the diff unreviewable. There is no `upload/`
  directory and there are no per-surface modules; an earlier draft of the
  proposal invented them.
- **`client.ts`, `error-text.ts` and `types.ts` exist as the document
  describes.** The five REST adaptations remain in `http.ts`.
  `share.ts` exists but is the public-link page's client, not admin shares.
- **The error envelope does not transition.** Both backends return
  `{error:{code,message,detail?}}`; localized refusals keep
  `detail.reason_key` and `detail.reason_params`. `Sc-Trace` stays a response
  header and is not duplicated in the body. Phase 13 uses the existing client
  shape against both backends.
- **Every new catalogue key needs an entry in every locale before this phase is
  done.** The i18n check is a gate, not a report, and "English only for now" is
  not a state it permits. A key the server can send and the frontend cannot
  render is a defect on this side of the wire.
- **`etag_weak` is the one genuinely new field.** Keep the wire's existing
  snake-case convention; `http.ts` has no camel-case mapping layer. The
  conflict screen says "this
  file may have changed" rather than asserting it did not.
- **The overwrite retry omits `If-Match`.** The first conditional save with a
  weak validator correctly receives 412. Sending that weak validator again can
  never pass RFC strong comparison, so only the user's explicit overwrite
  action makes an unconditional request.
- **Do not add a MIME type to a listing entry, on either side.** The icon comes
  from the name and the directory flag. A guessed type invites rendering
  something that should have been downloaded, which is the failure the separate
  content origin exists to prevent.
- **`events-transport.ts` depends on whatever Phase 5f settled about framing.**
  Confirm before assuming it is untouched.
- **Node and npm do not run on the Windows host.** This phase is verified in the
  guest, and `verify.sh` reports SKIP there rather than failing.
- **The embedded-build check is the last thing.** Build the frontend, build the
  binary with `embed_ui`, serve it, and confirm the bundle hash the browser
  loads is the one just built. `//go:embed` has a real dependency edge so this
  should be impossible to get wrong, which is why it is checked once rather
  than never.

## Done when

- `npm run build`, `web/tools/i18n-check.mjs` and the frontend test suite are
  green in the guest.
- Every new catalogue key has an entry in every locale.
- A binary built with `embed_ui` serves the bundle that was just built.
