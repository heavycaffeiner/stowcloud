<script lang="ts">
  // (app) route group: the authenticated shell (drawer/bar + upload tray).
  // Kept OUT of the true root layout so /s/[token] (outside this group)
  // never pulls this chunk in: the "separate lightweight
  // bundle" for the public share page.
  import { t } from '../../lib/i18n'
  import type { Snippet } from 'svelte'
  import { page } from '$app/state'
  import { goto } from '$app/navigation'
  import { createQuery } from '@tanstack/svelte-query'
  import { Snackbar as M3Snackbar, MenuItem } from 'm3-svelte'
  import { icons } from '../../lib/icons'
  import Menu from '../../lib/ui/Menu.svelte'
  import NavigationBar from '../../lib/ui/NavigationBar.svelte'
  import NavigationDrawer from '../../lib/ui/NavigationDrawer.svelte'
  import ProgressCircular from '../../lib/ui/ProgressCircular.svelte'
  import UploadTray from '../../lib/ui/UploadTray.svelte'
  import JobTray from '../../lib/ui/JobTray.svelte'
  import { createSession, isUnauthenticated, screenOf, setupRequiredQuery } from '../../lib/query/session'
  import { startLiveInvalidation } from '../../lib/query/live'
  import { swReady } from '../../lib/crypto/download-sw'
  import { COMPACT_MAX_PX, ui } from '../../lib/store/ui.store'
  import { openSearch, search } from '../../lib/store/search.store'
  import SearchSheet from '../../lib/ui/SearchSheet.svelte'
  interface Props {
    children: Snippet
  }
  let { children }: Props = $props()

  // The session, and the one follow-up question ("has this server ever had
  // an account?") that turns a definitive auth refusal into either "login" or
  // "first-run". Transient network and 503 failures keep the current shell
  // recoverable instead of treating an outage as a sign-out.
  const session = createSession()
  const sessionDefinitiveFailure = $derived(session.isError && isUnauthenticated(session.error))
  const setup = createQuery(() => setupRequiredQuery(sessionDefinitiveFailure))
  const screen = $derived(
    screenOf({
      hasSession: session.data !== undefined && !sessionDefinitiveFailure,
      sessionFailed: sessionDefinitiveFailure,
      setupPending: sessionDefinitiveFailure && setup.isPending,
      setupRequired: setup.data === true
    })
  )

  // Roots are contents of the same Files workspace, not peer destinations.
  // Keep the compact bar bounded to five stable meanings and expose root
  // switching through the explicitly named folder selector in the drawer.
  const rootItems = $derived(
    (session.data?.roots ?? []).map((r) => ({
      id: r.label,
      label: r.label,
      icon: icons.folder
    }))
  )

  // Compact navigation keeps the three high-frequency destinations visible.
  // Folder switching, secondary destinations, and settings live in Menu.
  const navItems = $derived([
    { id: 'files', label: t('nav.files'), icon: icons.home, href: '/b' },
    { id: 'recent', label: t('nav.recent'), icon: icons.recent, href: '/recent' },
    { id: 'trash', label: t('common.trash'), icon: icons.trash, href: '/trash' },
    { id: 'links', label: t('nav.links'), icon: icons.link, href: '/links' },
    { id: 'settings', label: t('common.settings'), icon: icons.settings, href: '/settings' },
    ...(session.data?.user.is_admin
      ? [{ id: 'admin', label: t('nav.admin'), icon: icons.admin, href: '/admin' }]
      : []),
    { id: 'more', label: t('nav.more'), icon: icons.menu }
  ])

  const compactNavItems = $derived(navItems.filter((item) => ['files', 'recent', 'trash', 'more'].includes(item.id)))

  const activeNav = $derived.by(() => {
    const p = page.url.pathname
    if (p.startsWith('/settings')) return 'settings'
    if (p.startsWith('/admin')) return 'admin'
    if (p.startsWith('/recent')) return 'recent'
    if (p.startsWith('/trash')) return 'trash'
    if (p.startsWith('/links')) return 'links'
    return 'files'
  })

  const compactActiveNav = $derived(['links', 'settings', 'admin'].includes(activeNav) ? 'more' : activeNav)

  function isBrowsePathname(pathname: string): boolean {
    return pathname.startsWith('/b') && (pathname.length === 2 || pathname[2] === '/')
  }

  const currentRoot = $derived.by(() => {
    const p = page.url.pathname
    if (!isBrowsePathname(p)) return null
    try {
      return decodeURIComponent(p.slice('/b/'.length).split('/')[0] ?? '') || null
    } catch {
      return null
    }
  })

  // Keep the last browse folder while a secondary route is open. Returning to
  // Files therefore returns to the user's actual workspace, not root one.
  let lastBrowsePath = $state<string | null>(null)
  $effect(() => {
    const p = page.url.pathname
    if (!isBrowsePathname(p)) return
    try {
      lastBrowsePath = decodeURI(p.slice('/b'.length) || '/')
    } catch {
      lastBrowsePath = p.slice('/b'.length) || '/'
    }
  })

  let moreOpen = $state(false)
  let moreX = $state(0)
  let moreY = $state(0)
  let folderSelectorOpen = $state(false)

  // MD3 window class breakpoint: standard versus compact navigation. Plain
  // `resize` listener rather than a store action, because this is a
  // property of the viewport the component is rendered in, not a user
  // choice -- nothing outside this shell needs to read it as it changes.
  $effect(() => {
    function onResize(): void {
      ui.setCompact(window.innerWidth < COMPACT_MAX_PX)
    }
    window.addEventListener('resize', onResize)
    onResize()
    return () => window.removeEventListener('resize', onResize)
  })
  // Screen redirects: `createSession()`/`setupRequiredQuery` fetch
  // themselves the moment they mount, so there is nothing left to
  // bootstrap by hand. A definitive auth refusal clears account-bound cache
  // state and makes `sessionDefinitiveFailure` hide stale session data, so the
  // shell reaches login or first-run. A transient outage leaves the existing
  // browser state visible and retryable.
  $effect(() => {
    if (screen === 'login') void goto('/login', { replaceState: true })
    else if (screen === 'first-run') void goto('/setup', { replaceState: true })
  })

  // The live socket needs the same session cookie the rest of the API does,
  // so it only makes sense while `screen` says the shell is showing the
  // file browser -- opened on entry, closed the moment that stops being
  // true (session end or the shell itself unmounting).
  $effect(() => {
    if (screen !== 'browser') return
    return startLiveInvalidation()
  })

  // Registered once the shell actually shows the file browser, same gate as
  // the live socket above: nothing here is useful before there is a session
  // to download anything under. `swReady` never throws: a browser with no
  // Service Worker support, or one where registration itself fails, just
  // resolves null and every download falls back to buffering, so this
  // fires and forgets rather than needing its own error state.
  $effect(() => {
    if (screen !== 'browser') return
    void swReady()
  })

  function pathFromBrowseUrl(): string | null {
    const p = page.url.pathname
    if (!isBrowsePathname(p)) return null
    try {
      return decodeURI(p.slice('/b'.length) || '/')
    } catch {
      return p.slice('/b'.length) || '/'
    }
  }

  function isReachableBrowsePath(candidate: string): boolean {
    const root = candidate.split('/').filter(Boolean)[0]
    return rootItems.some((item) => item.id === root)
  }

  function browseTarget(): string {
    const current = pathFromBrowseUrl()
    if (current && (current === '/' || isReachableBrowsePath(current))) return current
    if (lastBrowsePath && isReachableBrowsePath(lastBrowsePath)) return lastBrowsePath
    return rootItems.length > 0 ? `/${rootItems[0].id}` : '/'
  }

  function browseHref(path: string): string {
    return path === '/' ? '/b' : `/b${path}`
  }

  function toggleMore(): void {
    moreOpen = !moreOpen
    folderSelectorOpen = false
    if (moreOpen) {
      moreX = window.innerWidth - 16
      moreY = window.innerHeight - (ui.state.compact ? 88 : 16)
    }
  }

  function navigateTo(id: string, href?: string): void {
    if (id === 'files') {
      moreOpen = false
      folderSelectorOpen = false
      void goto(browseHref(browseTarget()))
      return
    }
    if (id === 'more') {
      toggleMore()
      return
    }
    const target = href ?? navItems.find((n) => n.id === id)?.href
    if (target) {
      moreOpen = false
      folderSelectorOpen = false
      void goto(target)
    }
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

  function selectRoot(item: { id: string }): void {
    void goto(`/b/${encodeURIComponent(item.id)}`)
    moreOpen = false
    folderSelectorOpen = false
  }

  /** The folder the shell is showing, so a search started from a keystroke
   *  ranks the same subtree the toolbar button would. Secondary routes keep
   *  the last browse context for the next global search. */
  function browseScope(): string {
    const current = pathFromBrowseUrl()
    const path = current ?? lastBrowsePath
    return path && path !== '/' ? path : ''
  }

  function openGlobalSearch(): void {
    openSearch(browseScope())
  }

  // Ctrl/Cmd+K, the combination every other file interface uses for this.
  // It reaches search from any page in the shell, which is the reason the
  // sheet is mounted here rather than on the browse page.
  function onWindowKeydown(e: KeyboardEvent): void {
    if (screen !== 'browser' || !(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== 'k') return
    e.preventDefault()
    openGlobalSearch()
  }
</script>

<svelte:window onkeydown={onWindowKeydown} />

{#if screen === 'browser'}
  <div class="sc-app-shell" class:sc-app-shell--compact={ui.state.compact}>
    {#if !ui.state.compact}
      <NavigationDrawer
        {navItems}
        {activeNav}
        items={rootItems}
        active={currentRoot ?? ''}
        onselect={selectRoot}
        onnavselect={(item) => navigateTo(item.id, item.href)}
        onsearch={openGlobalSearch}
      />
    {/if}
    <main
      class="sc-app-shell__main"
      class:sc-app-shell__main--drawer={!ui.state.compact}
    >
      {@render children()}
    </main>
    {#if ui.state.compact}
      <NavigationBar items={compactNavItems} active={compactActiveNav} onselect={navigateTo} />
      {#if folderSelectorOpen}
        <NavigationDrawer
          items={rootItems}
          active={currentRoot ?? ''}
          onselect={selectRoot}
          folderSelectorOnly
          overlay
          onclose={() => (folderSelectorOpen = false)}
        />
      {/if}
    {/if}
    {#if moreOpen}
      <Menu open={true} onclose={() => (moreOpen = false)} x={moreX} y={moreY} align="end">
        <MenuItem icon={icons.search} onclick={() => { moreOpen = false; openGlobalSearch() }}>
          {t('common.search')}
        </MenuItem>
        <MenuItem icon={icons.folder} onclick={() => { moreOpen = false; folderSelectorOpen = true }}>
          {t('nav.browse_folders')}
        </MenuItem>
        <MenuItem icon={icons.link} onclick={() => navigateTo('links', '/links')}>
          {t('nav.links')}
        </MenuItem>
        <MenuItem icon={icons.settings} onclick={() => navigateTo('settings', '/settings')}>
          {t('common.settings')}
        </MenuItem>
        {#if session.data?.user.is_admin}
          <MenuItem icon={icons.admin} onclick={() => navigateTo('admin', '/admin')}>
            {t('nav.admin')}
          </MenuItem>
        {/if}
      </Menu>
    {/if}
  </div>
  <!-- Shared fixed-position corner: `JobTray` above `UploadTray` so a job
       started on one page (and the upload tray, unrelated but the same
       bottom-right spot) never fight over the same fixed coordinates --
       see UploadTray.svelte's `.sc-upload-tray` comment. -->
  <div bind:this={trayStackEl} class="sc-tray-stack" class:sc-tray-stack--compact={ui.state.compact}>
    <JobTray />
    <UploadTray />
  </div>
  <!-- The desktop search surface. Mounted on the shell rather than on the
       browse page, so the shortcut reaches it from anywhere and the page
       underneath stays where it was: on a phone the same button navigates
       to /search instead, which is why this branch is desktop-only. -->
  <SearchSheet open={search.state.open && !ui.state.compact} scope={search.state.scope} onclose={() => search.close()} />
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
  .sc-app-shell__main--drawer {
    /* Standard (>=905px) width: Google Drive unified sidebar docked at left: 0
       with width var(--sc-nav-drawer-width), so main reserves only this width. */
    padding-left: var(--sc-nav-drawer-width);
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
