# Frontend design detail

`web/`. SvelteKit 2 / Svelte 5 (runes) / `adapter-static` SPA. The build output is embedded into the binary via `rust-embed`.

---

## 1. Stack decisions

| Choice | Why |
|---|---|
| Svelte 5 runes | Small runtime, no virtual DOM — reactivity overhead stays measurably low on a 100k-row list |
| `adapter-static` + SPA | The server is one Rust binary; no reason to run a second SSR runtime |
| **m3-svelte** for the design system | Was "MD3 tokens are adopted; components are hand-built". The hand-built half is gone: a token generator, a static token sheet, a state-layer class and ~30 primitives were all re-implementations of things a maintained Svelte-native MD3 library already ships, and each one drifted from the spec at its own pace. `@material/web` is still rejected for the original reason (web-component interop friction — form participation, SSR, style leakage); m3-svelte has none of it because it compiles to the same Svelte components everything else here is |
| CSS custom properties | Theme switching happens without a re-render. m3-svelte emits every colour as `light-dark(...)`, so the toggle is one `color-scheme` declaration rather than a second copy of the token table |
| CodeMirror 6 | Dynamic import — never in the initial bundle |

---

## 2. Tokens and styling

### 2.1 Where the tokens come from

The framework, at build time, from its own package — there is no token pipeline in this repo any more. `web/src/app.css` is **the entire stylesheet**: it imports `m3-svelte/etc/styles.css` and `m3-svelte/etc/recommended-styles.css`, pins the brand palette, sets the font stack, and adds the handful of rules the framework has no opinion about.

The palette is a static `--m3c-*` table generated **once**, by hand, from seed `#3F6C4F` with m3-svelte's own `genCSS` (`SchemeTonalSpot`, contrast 0) — the framework's documented snippet, not a build step. It lives in an `@layer tokens` block in `app.css`; changing the brand colour means regenerating and pasting it. `genCSS` emits every role as `light-dark(...)`, which is why the theme toggle is two rules (`:root[data-theme='light'|'dark'] { color-scheme }`) rather than a second copy of the table, and why `data-theme` still wins over `prefers-color-scheme` in both directions.

What this replaced, and what went with it: `web/tools/gen-tokens.mjs`, `web/theme.config.json`, `web/src/lib/styles/tokens.css` and `tokens.generated.css` are **deleted**, along with the `npm run gen-tokens` script and the `@material/material-color-utilities` dependency. (`@ktibow/material-color-utilities-nightly` in `devDependencies` looks like a leftover of that and is not — it is m3-svelte's own **peer dependency**. Nothing in `web/src` imports it; removing it breaks the install.) The old pipeline also generated a **high-contrast** pair (`contrastLevel` 1) behind `@media (prefers-contrast: more)`; that is gone and is now an explicit non-goal (`FEATURES.md` #135), not a regression to fix.

### 2.2 What `app.css` still owns

Everything here is either a browser default overridden once or something m3-svelte leaves to the app:

- **`--m3-font`** — one override reaches the whole type scale, because every m3-svelte type mixin reads it. Google Sans Flex carries no Hangul; Pretendard/Noto Sans KR follow it in the stack.
- **`@function --m3-density`, as identity.** An upstream omission, not a knob: twelve m3-svelte components size themselves with `height: --m3-density(3.5rem)` and v7.2.0 ships no such function, so the declaration is invalid and the browser drops the height — buttons collapse to their content, text fields lose their 56dp box. MD3's density scale is not wired up because nothing asks for it.
- **Reduced motion** — m3-svelte honours `prefers-reduced-motion` in its ripple engine only; its CSS transitions all read `--m3-duration*`, so collapsing those four here reaches the framework's components and ours at once.
- **Layout variables the framework has no token for:** `--sc-row-height` (file-list density), `--sc-nav-bar-height`/`--sc-nav-rail-width`/`--sc-nav-drawer-width` (the nav components are `position: fixed`, so their space is reserved by hand — the values mirror m3-svelte's own, in the framework's own units, so a browser font-size change moves the bar and its reservation together), and `--sc-page-pad` (MD3 window-class margins: 16/24/32px at 0/600/905px).
- **Utility classes:** `.sc-focus-ring`/`.sc-focus-ring-within` (m3-svelte exposes its focus indicator as a mixin and ships no class), `.sc-touch-target`, `.sc-sr-only`, `.sc-filename`, `.sc-danger`.

The 4px spacing scale is no longer a set of `--sc-space-*` variables. It is enforced on literal px instead — §2.4 — which is what the variables were only ever a convention for.

Density stays `[data-density="compact"|"spacious"]`, not `:root[data-density=...]` — `FileTable`/`FileGrid` set the attribute on their own container element, not on `<html>`, so a `:root`-scoped selector never matched and `--sc-row-height` silently stayed at 48px regardless of the toggle.

Elevation is m3-svelte's now, both halves of it (the shadow tokens and the `surface-container-*` tint roles). The five hand-built `--md-sys-elevation-*` tokens that replaced `Dialog`/`Menu`/`Snackbar`/`UploadTray`/`FileTree`'s individually hand-rolled `rgb(0 0 0 / 0.3)` shadows are gone with the rest of `tokens.css`; those components use framework containers that carry their own elevation.

### 2.3 Typography

Full MD3 type scale as tokens. Line-heights are all multiples of 4px — grid conformance lives there, not in font-size, because line-height is what actually affects layout (a 57px display size is fine as-is).

`word-break: keep-all` on Korean prose (`global.css`): a Korean syllable is its own UAX#14 break opportunity, so without this the browser breaks mid-word wherever a line happens to end, which reads as a rendering fault rather than a line wrap. Filenames get `overflow-wrap: anywhere` and truncate from the **front** (`direction: rtl`) so the extension — the part that actually disambiguates — survives instead of the start of the name.

### 2.4 Grid enforcement

`web/tools/stylelint-four-px.cjs` is a real stylelint plugin (`sc/four-px-grid`), not a convention: it walks every `margin`/`padding`/`gap`/`width`/`height`/`inset`/`border-radius`/`border`/… declaration, parses out px literals (skipping the contents of `calc()`/`var()`/`min()`/`max()`/`clamp()` entirely — mixed units make static validation unreliable there), and rejects anything that isn't a multiple of 4 outside the allowlist (`0`, `1`–`3px` for hairlines/borders, `9999px` for pills). `npm run lint:css` runs it against `src/**/*.{css,svelte}`; `npm run build` runs `lint:css` before `vite build`, and `.github/workflows/verify.yml` builds the frontend on both CI platforms before running the gate — a violation fails the build, not just a local lint pass.

### 2.5 State layers

m3-svelte's, via the `.m3-layer` class — `.sc-state-layer` and its `::after` opacity steps are deleted with the rest of `tokens.css`.

One non-obvious wiring detail, recorded because getting it wrong is silent: the framework ships `.m3-layer` **only** as `m3-svelte/etc/layer`, a JS module that pulls its own stylesheet in alongside the ripple listeners — not as part of `etc/styles.css`. `routes/+layout.svelte` imports it. Its components (and our compositions built on them, e.g. `ListItem`) apply the class but ship no rule for it, so without that one import every control renders correctly and has no hover, focus or press feedback at all.

A second, sharper one: that stylesheet gives `.m3-layer` a `::before` as well as the `::after` tint — square, `inset: 0`, pointer events on, commented "hitbox for rounded elements". It exists so a click in the corner of a rounded control still counts, which is right for the leaf controls m3-svelte ships and wrong for the four places we put the class on a *container*. An absolutely-positioned pseudo-element paints over statically-positioned children, so the hitbox silently ate their clicks: `FileTreeItem`'s twisty and label buttons stopped responding entirely (the folder tree would not expand and would not select), and `FileRow`/`FileGrid`'s 48px checkbox touch targets shrank to the 18px box m3-svelte positions for itself — the one touch-reachable way to build a multi-selection, per §9. `app.css` turns `pointer-events` off for those four `::before`s and nothing else; the hosts keep their own click and their hover tint. Put `.m3-layer` on anything with interactive children and this needs adding to that list.

---

## 3. Component inventory

Nothing here is a design system any more. `web/src/lib/ui/` is now two kinds of file, and the distinction is the point:

- **Adapters** — a few lines around an m3-svelte component, carrying no visual styling of their own. They exist only for what the framework has no opinion about (`Button`'s `loading` state that keeps the label in the layout so the box can't change width mid-click; `TextField`'s filled/outlined choice behind one prop). Deleting the adapter and calling the framework directly would work; it would just repeat those few lines at every call site.
- **App compositions** — components the framework has no equivalent for, built *out of* its state layer, focus ring, colour roles and type mixins rather than its own. `ListItem` is the clearest case: m3-svelte's takes `headline`/`supporting` as plain strings, and every call site here puts markup in them.

| Group | Components | Framework component behind it |
|---|---|---|
| Action | `Button`, `IconButton` | `Button` |
| Input | `TextField`, `Checkbox`, `Switch` | `TextField`/`TextFieldOutlined`, `Checkbox`, `Switch` |
| Container | `Dialog`, `Menu`, `Snackbar` | `Dialog`, `Menu`, `snackbar` |
| Navigation | `NavigationRail`, `NavigationDrawer`, `NavigationBar` | `NavigationRail`/`NavigationRailItem`, `NavCMLXItem` |
| Display | `Chip`, `ProgressLinear`, `ProgressCircular`, `Divider` | `Chip`, `LinearProgress`, `CircularProgress`, `Divider` |
| Compositions | `ListItem`, `Breadcrumb`, `JobTray`, `UploadTray`, `CodeEditor` | — (state layer / icons only) |
| App-specific | `FileTable` (virtual scroll), `FileGrid`, `FileTree`/`FileTreeItem`, `FileRow`/`FileRowSkeleton` | `Checkbox`, `Icon` |
| Task dialogs | `ConfirmDialog`, `DeleteDialog`, `RenameDialog`, `NewFolderDialog`, `ConflictDialog`, `EditConflictDialog`, `ShareManageDialog`, `DestinationPickerDialog` | via `Dialog` |
| Settings screens | `settings/{PasswordSection, TotpSection, SessionsSection, SmbSection, AppPasswordsSection}` | — |
| Admin screens | `admin/{UserManagementSection, GroupManagementSection, GrantManagementSection, ShareManagementSection, StorageIndexSection, UploadSettingsSection, ServerSettingsSection, AuditLogSection}` | — |

Used straight from `m3-svelte`, with no adapter at all: `Tabs`/`VariableTabs` (§7's tabbed `/settings` and `/admin`), `ConnectedButtons` (the theme segmented control), `SelectOutlined`, `Icon`, `LoadingIndicator`, `NavCMLXItem`.

Two entries this table used to claim and which have **never** existed: `Fab` and `Tooltip`. The browse page's upload FAB is markup in the page, not a component; nothing in the app has a tooltip. An earlier plan also listed `BottomSheet`, `RadioGroup`, `Select`, `Slider`, `SegmentedButton`, `Card`, `Tabs`, `Avatar`, `Badge` and a generic `List` — the framework now supplies the ones that turned out to be needed, which is the whole argument for adopting it.

**Fixed nav vs. drawer.** The nav rail/bar lists a fixed 3–4 destinations regardless of grant count — Files / Trash / [Admin, admin accounts only] / Settings, still inside MD3's 3–5 for a bottom bar. Trash is a destination rather than an overflow-menu action because it has its own route and its own list, and a restore path nobody can find is the same as no restore path. It used to put one entry per granted root directly in the rail, which is correct at two grants and wrong by construction past a handful: a `NavigationBar`/`NavigationRail` is MD3's fixed *small* set of destinations (3–5 items for the bar), not one item per grant, and per-user grants make a dozen roots the normal case here, not the extreme. The granted roots now live in `NavigationDrawer` — modal (native `<dialog>`) below 905px, opened by tapping "Files"; docked as a flex-adjacent panel beside the rail at ≥905px, open by default so switching roots still costs the one click it always did before this change (a phone-width drawer defaults closed instead, since opening it costs a tap either way).

**Responsive.** A single `uiState.compact` flag (`window.innerWidth < 905`, recomputed on resize) switches `NavigationRail` for `NavigationBar` + `NavigationDrawer`. Two of MD3's four window-class boundaries are implemented as CSS container queries against the browse pane's own inline size, not the viewport: `max-width: 599.98px` (`FileRow`/`FileRowSkeleton`/`UserManagementSection` collapse to a denser row) and `max-width: 839.98px` (`FileTree` collapses to overlay). Expanded/large (840–1239 / ≥1240) are not distinguished anywhere today.

---

## 4. State management

Svelte 5 runes classes. No global store library. `BrowseState` (`web/src/lib/state/browse.svelte.ts`) is the central one:

- Rows are addressed by **absolute index** in the server's sorted listing and fetched in on-demand windows (whatever `FileTable`/`FileGrid` is actually rendering), not as an ever-growing "infinite scroll" prefix. A sparse cache (`Map<index, Entry>`) holds what has loaded, capped at 2000 rows via LRU eviction — selected rows are exempt from eviction, which is what keeps "selection survives scrolling far away and back" true.
- **Selection is kept by name**, not index — resolved through a `name → index` side map back into the row cache. A list refresh does not relocate a selection onto the wrong file.
- `refresh()` re-lists the same path and preserves scroll position and selection. `open()` subscribes the directory to the live-invalidation channel (`state/events.ts`) and re-subscribes on every call, including navigating to the same path again (a sort change re-lists via the same code path) — the previous subscription is always torn down first so a directory the user has left can't keep triggering refreshes. This used to be inert — nothing server-side called `sc_watch::Watcher::subscribe`/`touch`, so no invalidation was ever published. It is live now: `sc-server`'s `watch_subscribe`/`watch_unsubscribe` (`app.rs`) register the OS-level watch off the client's own WebSocket subscribe/unsubscribe, `bridge.rs` touches the LRU on list/stat, and a watcher that fails to start degrades to lazy revalidation rather than erroring.
- No optimistic UI. Renaming or deleting is not reflected until the server confirms; a `412` conflict is common enough in a shared folder that undoing an optimistic change would be the common case, not the exception. Delay is hidden with inline progress state instead.

---

## 5. Virtual scroll

**The document is the scroll container**, not `FileTable`'s/`FileGrid`'s own box. A browser only collapses its address-bar chrome when *the document* scrolls; the shell used to clip at `100dvh` (`.sc-app-shell__main { overflow: hidden }`) with every route scrolling inside its own `overflow: auto` box, so `document.scrollHeight` could never exceed one screen and `window.scrollY` stayed at 0 through thousands of pixels of inner scrolling — the address bar never collapsed on a real phone. Fixing that meant deleting the inner scroller, which turns virtualization's inputs into a call-site problem, not an algorithm problem: `computeWindow` (`web/src/lib/virtual/windowing.ts`) only ever needed "how far scrolled" and "how tall the visible area is" — it did not change. What changed is where those two numbers come from:

- `documentScrollTop(windowScrollY, viewportDocumentTop)` — the viewport element's own top edge in document coordinates (`getBoundingClientRect().top + window.scrollY`, not `offsetTop`, which is relative to the nearest positioned ancestor and would silently break the day something upstream gets `position: relative`), clamped at 0.
- `effectiveViewportHeight(visualViewport?.height, innerHeight)` — `visualViewport.height` is preferred because `innerHeight` historically lags (or never updates on some engines) while the address bar is animating; `innerHeight` is the fallback where `visualViewport` doesn't exist (older engines, the jsdom test environment).

Consequence for layout: `NavigationBar`, `NavigationRail`, `NavigationDrawer`'s docked variant, and the FAB are all `position: fixed`. `position: sticky` pins to the *containing block's* edge, and that containing block (`.sc-app-shell`) is deliberately still exactly one viewport tall (`height: 100dvh`) — a `sticky` bottom bar would release and scroll away the instant the document's real height exceeds one screen, which a long directory now does routinely. `fixed` pins to the viewport itself regardless of document scroll, at the cost of no longer reserving its own space in flow — `.sc-app-shell__main`'s `padding-bottom`/`padding-left` (and `FileTable`/`FileGrid`'s own `--reserve-bar` modifier, since their real height escapes past that ancestor's box) do that reservation explicitly now. Bottom-bar height is reserved with one shared formula, `calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px))`, rather than repeating the constant — and the variable is `4rem`, m3-svelte's own `NavCMLXItem` height in the framework's own unit, so a browser font-size change moves the bar and its reservation together instead of splitting them apart. `contain: strict` (size+layout+style+paint) on `FileTable`/`FileGrid` also had to become `contain: content` (layout+style+paint, no `size`) — `strict` requires a definite size, which is exactly what these elements no longer have once their height is content-driven instead of handed down by a clipping ancestor.

Windowing itself:

- **Fixed row height** is assumed — measuring variable heights doesn't scale to 100k rows. `FileGrid` uses the same technique with fixed cell size.
- The spacer is sized from the directory's **total** row count (`browse.total`), not how many rows happen to be loaded, so the scrollbar tells the truth (a drag to 50% lands near row 50,000) from the first render — not-yet-loaded indices render `FileRowSkeleton` placeholders.
- Keyed `{#each slice as row (row.entry?.name ?? placeholder-id)}` reuses DOM nodes.
- **Browser scroll-height ceilings**: Chrome ≈33.5M px, Firefox ≈17.8M px. Above `SCALE_MAPPING_THRESHOLD_PX` (15M px — comfortably below the tighter Firefox ceiling even for unmeasured engines), the real spacer height is capped there and scroll position is remapped to row index through a linear scale factor (`computeScaleMapping`) instead of pretending `scrollTop` can address `itemCount * rowHeight` px of real scrollable space. At 48px rows this triggers past roughly 310k rows.
- Scroll-driven row fetches are debounced 80ms (`WINDOW_DEBOUNCE_MS` in `browse.svelte.ts`) so a fast scroll doesn't fire a request per frame. Thumbnails follow the same idea at a 150ms idle threshold — no thumbnail requests while actively scrolling.

Accessibility adaptation for a virtualized 100k-row grid: `role="grid"` + `aria-multiselectable="true"` on the scroll container, `tabindex="0"` on the container itself (one tab stop, not 100k), a `focusedIndex` cursor tracked in `BrowseState`, and `aria-activedescendant` pointing at the focused row's DOM id once it's actually rendered. See §9.

---

## 6. Upload worker

Slicing and hashing on the main thread would jank the virtual-scrolled table, so every byte-pushing step happens in a dedicated Worker (`web/src/lib/upload/worker.ts`).

- One `POST /api/uploads` per file (`Sc-Random-Access: 1`), then `file.slice(off, off + chunkSize)` → `PATCH`. Chunk size: 5 MB floor (server-enforced), 10 MB default, no upper bound (`chunk-planner.ts`).
- Concurrency is **4 in-flight chunks globally**, not per file (`MAX_INFLIGHT`) — a single large file still gets parallelism instead of waiting on one request at a time.
- Progress is throttled to ≤10 Hz (`PROGRESS_HZ_MS = 100`) before `postMessage`; posting on every chunk completion would stall the main thread processing the flood.
- Retry: network errors and 5xx back off at 1s/2s/4s/8s/8s (`BACKOFF_MS`, 5 attempts — the array repeats 8s for the last attempt rather than continuing to double) then gives up. A `413` halves the chunk size (`shrinkChunkSize`, floored at 5 MB) and re-plans the remaining bytes rather than aborting — the chunk size is nominally fixed, but a misconfigured intermediary (e.g. a proxy's own body-size limit) can still produce one.
- **Resume**: session id keyed by `(name, size, lastModified)` in IndexedDB (`idb.ts`). On reload, `HEAD /api/uploads/<id>` returns the true offset and upload continues from there; the local record is deleted once the transport confirms completion.
- Directory upload: `showDirectoryPicker()` (File System Access API) first, `<input webkitdirectory>` fallback; drag-and-drop uses `DataTransferItem.getAsFileSystemHandle()` falling back to `webkitGetAsEntry()`. Relative paths ride `Upload-Metadata`'s `relativePath`; the server creates intermediate directories.

`UploadTray` is a fixed bottom panel (own `position: fixed`, same reasoning as §5) showing per-file progress, rate, ETA, and pause/resume/cancel. It survives route changes because the Worker is not owned by any route.

---

## 7. Routing

| Route | Chunk |
|---|---|
| `/` | Redirects to the first accessible root |
| `/login`, `/setup` | First-run / auth screens, outside the `(app)` shell |
| `/b/[...path]` | Main file browser. Initial bundle |
| `/s/[token]` | Public share link. **Separate lightweight bundle** — the true root layout (`routes/+layout.svelte`) is minimal on purpose (global styles + theme init only); nav rail, upload tray, and admin/auth code live in `(app)/+layout.svelte`, which `/s/[token]` never imports |
| `/settings/*` | Four tabs (Account / Security / [Connections, when SMB is on] / Appearance), dynamic `import()` **per tab**, not just per section. Three of these sections fetch on mount, so a flat scroll fired three API calls on anyone who opened the page to change the theme |
| `/admin/*` | Five tabs (Users / Shares / Storage / Server / Audit log), same per-tab `import()`. Five is a ceiling, not a coincidence: m3-svelte's `Tabs` positions its indicator with `nth-of-type(-n + 5)` rules, so a sixth tab renders with no indicator — another section means regrouping, not appending. The thin route entry loads for anyone who navigates there (SvelteKit can't prevent route entry client-side), but every data-fetching subcomponent is behind `{#await import(...)}`, so neither their code nor their first API call happens unless the session is actually an admin. Server-side `require_admin` is the real access control either way — this is UX, not a security boundary |
| `/trash` | Lists, restores, and permanently purges trashed items. A `NavigationBar`/`NavigationRail` destination since the discoverability fix (§3), not only a file-browser overflow-menu item |
| `/edit/[...path]` | CodeMirror 6 behind its own dynamic `import()`, on top of the route-level split |

Splitting the public-link page out is not just a size optimization — it keeps admin/auth/upload code out of what an unauthenticated visitor's browser ever fetches.

Both tabbed screens keep the open tab in the URL hash (`#security`, `#audit`) via `replaceState`, so a reload — or a link pasted to a colleague — lands on the same tab. `replaceState` rather than `pushState`: switching tabs is not a navigation a Back press should have to undo one step at a time.

---

## 8. Performance targets

The two byte budgets are CI gates. The frame-drop target is not, and deliberately so.

| Metric | Target | Enforced |
|---|---|---|
| Initial JS (gzip) | < 150 KB | **yes** — `web/tools/check-bundle-size.mjs` |
| Public share-link page JS (gzip, marginal) | < 60 KB | **yes** — same script |
| Scroll frame drops (100k rows) | 0 | no — manual, see below |

`npm run check:bundle-size` runs the script, and `.github/workflows/verify.yml` runs it on **both** the `windows-latest` and `ubuntu-latest` jobs, right after `npm run build`. A non-zero exit fails the job; the script prints a `PASS`/`FAIL` line with the measured KB next to the budget, so a regression says which budget and by how much rather than just "too big".

It measures Vite's own build output rather than re-implementing bundling, so it cannot drift from what a browser actually downloads:

- **Initial JS** — the `<link rel="modulepreload">` list in `build/index.html`, gzipped. This is a single-fallback SPA (one `build/*.html`, no per-route prerender), so those are the same bytes on every URL.
- **Share-link page JS** — the module graph of `/s/[token]`'s leaf node, walked through `.svelte-kit/output/client/.vite/manifest.json`, **minus** anything the initial shell already paid for. Marginal cost, not total, because that is what §7's route split is actually buying.

Both lookups fail loudly (exit 1, naming what it looked for) if the SvelteKit output shape changes or the `/s/[token]` route is renamed — a gate that silently measures nothing is worse than no gate.

**Why frame drops are not gated.** It is a runtime Core Web Vitals measurement, not a build artifact: producing it at all needs a real browser trace (Lighthouse/Playwright), and a GitHub-hosted runner's CPU and IO throughput vary enough run to run that a frame-timing threshold would go red on noise. A gate everyone learns to re-run until it passes is worse than an honest manual target, so this row stays verified by hand in DevTools. Lighthouse CI was considered and rejected for the same reason.

---

## 9. Accessibility

- File list: `role="grid"` + `aria-multiselectable="true"`, rows `role="row"`, cells `role="gridcell"`. **Roving tabindex** — the whole grid is one tab stop; arrow keys move a `focusedIndex` cursor and `aria-activedescendant` follows it (see §5 — real per-row DOM focus can't work when most rows aren't mounted).
- Keyboard, as actually wired in `FileTable.svelte`'s `onKeydown`: `↑↓` move, `Shift+↑↓` extends the range, `Space` toggles the focused row, `Ctrl/Cmd+A` selects all, `Enter` opens, `F2` renames, `Delete` deletes, `/` focuses search. **There is no keyboard copy/paste** (`Ctrl+C`/`Ctrl+V`) — the only `navigator.clipboard` calls in the app copy a share link, an app-password token, or a TOTP secret to the OS clipboard, not files between folders.
- Focus indicator: m3-svelte's own, which it exposes as the `--m3-focused-outward` mixin and ships no class for. `app.css` turns it into `.sc-focus-ring`/`.sc-focus-ring-within` so app compositions can reach it; the `-within` variant exists because a `TextField`'s focusable `<input>` sits inside a bordered box the ring needs to wrap, not the input itself.
- Touch targets: 48×48 minimum via `.sc-touch-target`'s `::before` hit-area expansion (`app.css`), used by `Checkbox`, `IconButton`, `Switch`, and the file list's own selection checkboxes — the visual control can stay smaller (e.g. 40px in compact density) while the tappable area doesn't shrink.
- Tab groups: the `aria-label` naming the group goes on a wrapper with `role="radiogroup"`, never on m3-svelte's `Tabs`/`VariableTabs`. The component spreads its extra attributes onto **every** `<input type="radio">` it renders, so a label passed to it overrode each tab's own `<label>` and a screen reader announced three radios all called "Settings sections".
- Toggle state: a filled-vs-tonal variant swap conveys "selected" to sighted users only, which is why `Button` carries a `pressed` prop (`aria-pressed`). Without it the theme segmented control read as three identical buttons with no indication of which one was on.
- Live regions: `Snackbar` (`role="status"`/`aria-live="polite"`) for transient confirmation/error toasts elsewhere in the app. `UploadTray` owns two more of its own — not Snackbar's, since Snackbar is mounted per-route while UploadTray is mounted once and outlives navigation (§6); routing through whichever page's Snackbar happens to be current would make announcements go silent on navigation. One is `role="status"`/`aria-live="polite"` for a batched "upload started" and a per-file "done"; the other is `role="alert"`/`aria-live="assertive"` for a per-file failure (naming the file — "upload failed" alone is useless with several in flight). Per-chunk progress (up to 10 Hz, §6) is deliberately never announced — only the queued/done/error transitions are, or the region would out-talk itself.
- Contrast: the palette (§2.1) is a `SchemeTonalSpot` at contrast 0, which targets WCAG-oriented ratios by construction, but nothing in this repo automates checking it — no `axe-core`, no CI contrast check. A high-contrast variant is an explicit non-goal (`FEATURES.md` #135), so `prefers-contrast: more` changes nothing.

---

## 10. Internationalization

- **Korean and English, switchable at `/settings#appearance`.** Two catalogues of the same 604 keys (`lib/i18n/{ko,en}.json`, plain JSON, both imported statically), selected by `i18nState.locale` (`lib/i18n/state.svelte.ts`, persisted per-browser in `localStorage`). The root layout writes the matching BCP 47 tag into `<html lang>` so the document agrees with what is on screen.
- **A call site names a key, never its own text**: `t('nav.files')`, not `t('Files')`. The source-string-as-key variant was built and then removed — it makes `t()` a substitution table over one hard-coded language, so a Korean copy edit silently orphans its English and the source language can never be swapped. `t(key, params)` fills `{name}` holes; the fallback chain (locale → ko → the key) only ever reaches its last hop for a key that arrives through a variable — a server-sent `reason_key` this build predates, say, where rendering the key is the honest answer. The server itself never sends prose for a screen: a refusal travels as a stable code plus `detail.reason_key`/`reason_params`, and `lib/api/error-text.ts` is the single place that turns one into a sentence.
- Strings that cross a thread or module boundary as data are keys too, marked `/* i18n */` at the literal: the upload Worker has no locale state, so it posts a key and `UploadTray.svelte` resolves it at the render site.
- **`web/tools/i18n-check.mjs` is the gate** (`npm run lint:i18n`, wired into `npm run build`, so both CI jobs run it). It fails on a key missing from either catalogue, a catalogue entry no call site uses, `{placeholder}` sets that disagree between languages, and a `t()` argument that is not a dotted key — the last of which is what stops the source-string design from creeping back in one call site at a time.
- This section claimed the opposite for a long time ("there is no locale switch"), and that was accurate then: 56 keys against ~995 hardcoded strings. The switch is back because the catalogues are complete and CI now keeps them that way.
- The two catalogues cost ~24 KB gzip in the initial chunk (34.4 → 58.8 KB), measured against the 150 KB budget in §8. Loading the non-default language dynamically would save half of that and is not worth the mid-session flash of untranslated UI; revisit only if that budget is actually threatened.
- Dates/relative time/numbers go through `Intl.DateTimeFormat`/`Intl.RelativeTimeFormat`/`Intl.NumberFormat` (`lib/i18n/index.ts`) — never hand-formatted.
- File sizes use **one vocabulary everywhere: KB/MB/GB, each 1024 of the one below** (`format/bytes.ts`). That is the consumer convention, and — more to the point — it is the same definition every size *input* uses: `BYTES_PER_MB` is the single constant behind the chunk-size, quota and DB-guard fields, so "5 MB" typed into a field is exactly the 5 MB `formatBytes()` prints. IEC suffixes would disagree with those labels by 4.9%; true SI would render the server's binary constants as non-round numbers (the 5 MB chunk floor as "5.24 MB"). `Intl` has no unit for this scheme, so only the numeric magnitude goes through `Intl.NumberFormat` and the suffix is chosen by hand. There is no SI mode.

---

## 11. Testing

What actually exists, run via `vitest run` (`web/package.json`'s `test` script):

| Area | Coverage |
|---|---|
| Pure logic | Vitest unit tests for state classes (`selection`, `jobs`, `events`, `mock`), formatting (`bytes`, `password-strength`, `user-agent`), upload planning (`chunk-planner`), auth flows, and the virtual-scroll math (`windowing.test.ts`) |
| Grid enforcement | The stylelint plugin itself is tested (`stylelint-four-px.test.ts`); `npm run lint:css` runs it against every `.svelte`/`.css` file (§2.4) |
| Bundle size | `npm run check:bundle-size` enforces §8's two byte budgets against the real build output, on both CI jobs |

What the earlier version of this section claimed and does not exist: **no component rendering tests** (`@testing-library/svelte` is a `devDependency` but zero test files import it), **no Playwright** (no visual regression, no keyboard-workflow end-to-end tests, no upload-under-network-throttle tests), **no `axe-core`** or any other automated accessibility check, and **no Lighthouse CI** (§8 — a frame-timing gate on a shared runner would be flaky, so that one §8 row stays manual). If any of these are wanted, they need to be built, not just described.
