<script lang="ts">
  // (app) route group: the authenticated shell (nav rail/bar + upload tray).
  // Kept OUT of the true root layout so /s/[token] (outside this group)
  // never pulls this chunk in — the "separate lightweight
  // bundle" for the public share page.
  import { t } from '../../lib/i18n'
  import type { Snippet } from 'svelte'
  import { page } from '$app/state'
  import { goto } from '$app/navigation'
  import { Snackbar as M3Snackbar } from 'm3-svelte'
  import { icons } from '../../lib/icons'
  import NavigationRail from '../../lib/ui/NavigationRail.svelte'
  import NavigationBar from '../../lib/ui/NavigationBar.svelte'
  import NavigationDrawer from '../../lib/ui/NavigationDrawer.svelte'
  import ProgressCircular from '../../lib/ui/ProgressCircular.svelte'
  import UploadTray from '../../lib/ui/UploadTray.svelte'
  import JobTray from '../../lib/ui/JobTray.svelte'
  import { setDrawer, uiState, watchViewport } from '../../lib/state/ui.svelte'
  import { authState } from '../../lib/state/auth.svelte'
  import { bootstrapAuth } from '../../lib/state/auth-bootstrap'

  interface Props {
    children: Snippet
  }
  let { children }: Props = $props()

  // Used to list one nav entry per root (": what a user
  // sees as their root is a projection of their grant list") -- fixed a real
  // bug (a hardcoded single "home" item made every root after the first
  // unreachable) by creating a worse one: a NavigationBar/Rail is a fixed
  // *small* set of destinations (MD3: 3-5 for the bar), not one item per
  // grant. Per-user grants make a dozen roots the expected case, not the
  // extreme, so that list only grows.
  //
  // The roots aren't peer destinations to Settings/Admin anyway -- they're all
  // contents of the *same* place ("Files"). So the fixed nav is now Files +
  // [Admin] + Settings (2-3 items, independent of grant count), and "Files"
  // doesn't navigate anywhere itself -- it opens `NavigationDrawer`
  // ( names this component; §3 already pairs it
  // with NavigationRail for ≥905px, which is a strong hint this is where
  // root-switching belongs), which lists the actual roots. That component is
  // where the fix for "every root must stay reachable" lives now -- see its
  // own doc comment for the modal (phone) vs. standard (rail) split.
  const rootItems = $derived(
    (authState.session?.roots ?? []).map((r) => ({
      id: r.label,
      label: r.label,
      icon: icons.folder
    }))
  )

  // Admin nav item only for an administrator — a hidden nav entry is not
  // access control (the server's `require_admin` still gates the API), but
  // there is no reason to draw a link into a screen a non-admin would only
  // get an "Only administrators can see this screen" message from.
  //
  // Trash is here rather than only behind the browser's overflow menu: it is
  // a destination (its own route, its own list), not an action on the current
  // folder, and a restore path nobody can find is the same as no restore path.
  // Still 3-4 items, inside MD3's 3-5 for a bottom bar.
  const navItems = $derived([
    { id: 'files', label: t('nav.files'), icon: icons.home },
    { id: 'trash', label: t('common.trash'), icon: icons.trash, href: '/trash' },
    ...(authState.session?.user.is_admin
      // `nav.admin`, not `common.administrator`: the rail gives a label a
      // 56px box, and "Administrator" needs 83 of them.
      ? [{ id: 'admin', label: t('nav.admin'), icon: icons.admin, href: '/admin' }]
      : []),
    { id: 'settings', label: t('common.settings'), icon: icons.settings, href: '/settings' }
  ])

  // "Files" stays highlighted for every root, not just the first one — it
  // represents the section, not any single grant.
  const activeNav = $derived.by(() => {
    const p = page.url.pathname
    if (p.startsWith('/settings')) return 'settings'
    if (p.startsWith('/admin')) return 'admin'
    if (p.startsWith('/trash')) return 'trash'
    return 'files'
  })

  const currentRoot = $derived.by(() => {
    const p = page.url.pathname
    if (!p.startsWith('/b/')) return null
    return decodeURIComponent(p.slice('/b/'.length).split('/')[0] ?? '') || null
  })

  // Desktop: the root list used to be visible in the rail with no click at
  // all, so it defaults open there to cost the same single click switching
  // roots always did (see NavigationDrawer.svelte doc comment). Phone:
  // opening it always costs a tap either way (bar item -> drawer), so it
  // defaults closed to spend the least vertical/visual budget when a user
  // isn't switching roots.
  //
  // `drawerDefaultApplied` makes that a *default*, applied once, rather
  // than a rule re-enforced on every render: without it, this effect's
  // dependency on `uiState.compact` meant crossing the compact/standard
  // breakpoint in either direction re-ran it, and re-entering standard
  // width forced `drawerOpen` back to `true` even after a user had
  // deliberately collapsed it -- resize the window (or rotate a tablet)
  // past 905px and a closed drawer sprang back open with no click at all.
  // Now the forced-open only ever fires the first time standard width is
  // seen with a session, exactly the "cost one click, same as before"
  // default the comment above describes -- after that, only the "Files"
  // rail item (`navigateTo`) or `selectRoot` change it. Entering compact
  // still closes it unconditionally: the overlay variant is a modal, and
  // leaving one open across a resize would pop a dialog over whatever the
  // user is now looking at on the narrower layout.
  //
  // `drawerDefaultApplied` is a plain `let`, so it only survives a resize --
  // a refresh reset it and the default fired again, which is why a drawer
  // closed on one load was open on the next. `uiState.drawer` (localStorage,
  // same shape as `sc.theme`) carries the choice across loads; `null` means
  // there is no choice yet and the width default still applies.
  let drawerOpen = $state(false)
  let drawerDefaultApplied = false
  $effect(() => {
    if (uiState.compact) {
      drawerOpen = false
    } else if (!drawerDefaultApplied && authState.session) {
      drawerOpen = uiState.drawer ?? true
      drawerDefaultApplied = true
    }
  })

  // Only standard width records a preference. Compact's drawer is a modal:
  // opening it is a step in "switch root", and closing it is dismissing a
  // dialog -- neither says anything about how the desktop layout should
  // start, and `uiState.compact` closes it on entry anyway.
  function setDrawerOpen(open: boolean) {
    drawerOpen = open
    if (!uiState.compact) setDrawer(open)
  }

  $effect(() => watchViewport())

  // Session bootstrapping (task #4): consult GET /api/auth/session exactly
  // once on load to decide file browser vs. login vs. first-run. Every
  // subsequent 401 (task #3) flips the same `authState.screen` from
  // http.ts's request() — the effect below reacts to that too, so a session
  // dying mid-browse bounces here just like a fresh unauthenticated load.
  let bootstrapped = false
  $effect(() => {
    if (authState.screen === 'loading' && !bootstrapped) {
      bootstrapped = true
      void bootstrapAuth()
    }
  })
  $effect(() => {
    if (authState.screen === 'login') void goto('/login', { replaceState: true })
    else if (authState.screen === 'first-run') void goto('/setup', { replaceState: true })
  })

  function navigateTo(id: string) {
    if (id === 'files') {
      // Not a route -- "Files" reveals the root list rather than jumping to
      // one root, since there's no single canonical destination it could
      // navigate to that wouldn't strand the other grants again.
      setDrawerOpen(!drawerOpen)
      return
    }
    const item = navItems.find((n) => n.id === id)
    if (item && 'href' in item && item.href) goto(item.href)
  }

  // The tray stack and the browse page's FAB are both fixed to the bottom
  // right corner, and the stack (z 30) wins over the FAB (z 10) -- so on a
  // phone, any running job or upload buried the upload FAB completely (its
  // 56px box sat 708-764, the job tray 596-756: 8px of it left showing).
  // The stack's height is content-dependent (one row per job), so no static
  // `bottom` can clear it.
  //
  // What gets published is the stack's *top edge*, as a distance up from the
  // bottom of the viewport, plus the stack's own 12px `gap` -- not its
  // height. A height would have to be added to the FAB's own baseline, and
  // the two baselines differ (the stack clears the bottom bar by 24px, the
  // FAB by 16px), so that arithmetic silently lost 8px of the gap. A top edge
  // is absolute: the FAB just takes `max()` of it and its own resting place,
  // which also makes the empty case fall out for free (0px never wins).
  let trayStackEl: HTMLDivElement | undefined = $state()
  $effect(() => {
    const el = trayStackEl
    if (!el) return
    const publish = () => {
      const top = el.offsetHeight > 0 ? `${window.innerHeight - el.getBoundingClientRect().top + 12}px` : '0px'
      document.documentElement.style.setProperty('--sc-tray-stack-top', top)
    }
    const ro = new ResizeObserver(publish)
    ro.observe(el)
    publish()
    return () => {
      ro.disconnect()
      document.documentElement.style.removeProperty('--sc-tray-stack-top')
    }
  })

  function selectRoot(item: { id: string }) {
    goto(`/b/${encodeURIComponent(item.id)}`)
    // Modal drawer: picking a root is the drawer's whole purpose, so close
    // it same as any other modal on selection. Standard (rail) drawer: it
    // stays open, matching how the rail always kept every root visible
    // before this change -- switching again shouldn't cost a re-open.
    if (uiState.compact) drawerOpen = false
  }
</script>

{#if authState.screen === 'browser'}
  <div class="sc-app-shell" class:sc-app-shell--compact={uiState.compact}>
    {#if !uiState.compact}
      <NavigationRail items={navItems} active={activeNav} onselect={navigateTo} />
      {#if drawerOpen}
        <NavigationDrawer items={rootItems} active={currentRoot ?? ''} onselect={selectRoot} onclose={() => setDrawerOpen(false)} />
      {/if}
    {/if}
    <main
      class="sc-app-shell__main"
      class:sc-app-shell__main--rail={!uiState.compact}
      class:sc-app-shell__main--drawer-open={!uiState.compact && drawerOpen}
    >
      {@render children()}
    </main>
    {#if uiState.compact}
      <NavigationBar items={navItems} active={activeNav} onselect={navigateTo} />
      {#if drawerOpen}
        <NavigationDrawer items={rootItems} active={currentRoot ?? ''} onselect={selectRoot} overlay onclose={() => (drawerOpen = false)} />
      {/if}
    {/if}
  </div>
  <!-- Shared fixed-position corner: `JobTray` above `UploadTray` so a job
       started on one page (and the upload tray, unrelated but the same
       bottom-right spot) never fight over the same fixed coordinates --
       see UploadTray.svelte's `.sc-upload-tray` comment. -->
  <div bind:this={trayStackEl} class="sc-tray-stack" class:sc-tray-stack--compact={uiState.compact}>
    <JobTray />
    <UploadTray />
  </div>
{:else}
  <div class="sc-app-shell__boot" role="status" aria-label={t('nav.checking_your_session')}>
    <ProgressCircular />
  </div>
{/if}

<!-- m3-svelte's snackbar is a singleton: one host here, and every page's
     `snackbar(...)` call (via `lib/ui/Snackbar.svelte`) queues into it.
     Outside the auth branch so a message raised while the session is still
     being checked isn't swallowed. -->
<M3Snackbar closeTitle={t('common.close')} />

<style>
  .sc-app-shell {
    display: flex;
    /* Stays a hard `height: 100dvh` -- exactly one screen, same as before.
       An early version of this fix tried `min-height: 100dvh` so the shell
       would grow tall enough to contain a long list, on the theory that
       `NavigationBar` could then `position: sticky; bottom: 0` against it.
       That doesn't work and isn't needed: (1) `height: 100%` chains inside
       (`.sc-browse`, machinery) resolve against a
       *definite* ancestor height -- once the shell's own height became
       content-dependent, every percentage-height descendant lost its
       definite size too (the classic "flex `flex: 1` can't grow into
       nothing" circularity), collapsing the whole browse pane back down to
       near zero instead of growing it. (2) Even fixed, a `sticky` bottom
       bar's "stuck" range is bounded by *its own containing block's* box,
       which is exactly one screen tall here -- it would release and scroll
       away the moment the document's real (content-driven) scrollable
       height exceeds that one screen, which is precisely the case this
       whole change makes common. Neither problem needs solving, because
       overflow doesn't need a tall ancestor to reach the document: a fixed-
       height box with the *default* `overflow: visible` still lets its
       children's content paint past its own edges, and that painted
       overflow still counts toward `document.documentElement.scrollHeight`
       regardless of how tall any single ancestor's own box reports itself
       (see `.sc-app-shell__main` below, the one box in this chain that
       used to override that default). So the shell stays exactly one
       viewport tall, `.sc-browse`'s `height: 100%` chain keeps resolving
       against a definite number exactly as it always did, and
       `NavigationBar` gets pinned a different way -- see its own comment. */
    height: 100vh;
    height: 100dvh;
  }
  .sc-app-shell--compact {
    flex-direction: column;
  }
  .sc-app-shell__main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    /* Was `overflow: hidden` -- the shell's clip point, and the reason
       nothing below it ever reached the document no matter how tall it
       wanted to be: a browser only collapses its address-bar chrome when
       *the document* scrolls, and `document.scrollHeight` can never exceed
       `innerHeight` while something upstream throws the overflow away.
       Removing it doesn't, by itself, make anything look different for a
       short list (there's nothing to clip yet); for a long one, it's what
       lets FileTable/FileGrid's virtualized spacer (now real document flow
       -- see their own `.sc-file-table`/`.sc-file-grid` rules) actually
       push `document.scrollHeight` taller instead of being cut off at one
       screen, even though this element's own box (via `flex: 1` against
       the shell's still-fixed height, see `.sc-app-shell` above) stays
       exactly the same size it always was. */
  }
  .sc-app-shell--compact .sc-app-shell__main {
    /* `NavigationBar` is `position: fixed` now (see its own comment for why
       `sticky` doesn't work here), which takes it out of flow -- so without
       this, nothing reserves its ~64px anymore and the last field on a
       page like `/settings` renders *underneath* it instead of above it.
       This works for `/settings`/`/admin` because their pane
       (`.sc-settings`) has no explicit height of its own -- it's the sole
       child of this flex column, so it's *shrunk to fit* whatever space
       main actually has and scrolls its own overflow internally; shrinking
       main's available content-box height by this padding is what gives it
       less room, which is exactly what reserves the bar's strip.
       It does NOT reach FileTable/FileGrid the same way: their real height
       deliberately escapes past this element's own box instead of being
       shrunk to fit it (that's the whole point of the document-scroll
       change), so padding added *here* never reaches their actual tail end
       -- see their own `.sc-file-table--reserve-bar`/
       `.sc-file-grid--reserve-bar` rules for that half of the fix.
       `--sc-nav-bar-height` (app.css) is the bar's own item height, shared so
       the reservation can't drift from it. */
    padding-bottom: calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px));
  }
  .sc-app-shell__main--rail {
    /* Standard (≥905px) width: `NavigationRail` is `position: fixed` for
       the exact same reason `NavigationBar` is (see that component's own
       comment) -- once the document can scroll past one screen, a rail
       that's merely a flex sibling scrolls away with everything else
       instead of staying put, which a real device has no way to reveal
       again short of scrolling all the way back up. Fixed takes it out of
       flow, so this reserves its `--sc-nav-rail-width` the same way the
       compact rule above reserves the bottom bar's height. */
    padding-left: var(--sc-nav-rail-width);
  }
  .sc-app-shell__main--drawer-open {
    /* `NavigationDrawer`'s standard (docked) variant is `position: fixed`
       too, immediately right of the rail -- same reasoning, same fix. Its
       width replaces (not adds to) the rail's own reservation above since
       the drawer already starts past the rail's right edge. */
    padding-left: calc(var(--sc-nav-rail-width) + var(--sc-nav-drawer-width));
  }
  .sc-tray-stack {
    /* Carries the fixed/right/bottom/z-index that used to live directly on
       `.sc-upload-tray` (UploadTray.svelte) -- moved here so `JobTray` can
       stack above it in the same corner instead of each tray claiming its
       own fixed spot (which would overlap once both had items). z-index 30
       is the same value `.sc-upload-tray` always used (see FileTree.svelte's
       110/Menu.svelte's 88/Snackbar.svelte's 40 for the rest of this app's
       fixed-position stacking order). */
    position: fixed;
    right: 24px;
    bottom: 24px;
    z-index: 30;
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 12px;
  }
  .sc-tray-stack--compact {
    /* On a phone the bottom bar owns this corner, and both are `fixed` --
       the trays were landing on top of Settings/Admin. Same reservation
       formula as everywhere else the bar's strip is cleared; the 24px above
       is the stack's own margin, kept. */
    bottom: calc(24px + var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px));
    /* Both trays are `min(360px, 100vw - 32px)` wide, so at 360px they
       measured 328px and `right: 24px` left 8px on the left and 24px on the
       right -- visibly off-centre. 16px makes the two margins agree. */
    right: 16px;
  }
  .sc-app-shell__boot {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100vh;
    height: 100dvh;
  }
</style>
