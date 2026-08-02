<script lang="ts">
  import { fade } from 'svelte/transition'
  import { goto } from '$app/navigation'
  import { page } from '$app/state'
  import { api, ApiError, type Entry, type OnConflict } from '../../../../lib/api/client'
  import { baseName, joinPath, normalizePath, parentOf } from '../../../../lib/api/path-utils'
  import { authState } from '../../../../lib/state/auth.svelte'
  import { browse } from '../../../../lib/state/browse.svelte'
  import { uiState } from '../../../../lib/state/ui.svelte'
  import { t } from '../../../../lib/i18n'
  import { uploadTray } from '../../../../lib/state/upload-tray.svelte'
  import Breadcrumb from '../../../../lib/ui/Breadcrumb.svelte'
  import Button from '../../../../lib/ui/Button.svelte'
  import ConflictDialog from '../../../../lib/ui/ConflictDialog.svelte'
  import DeleteDialog from '../../../../lib/ui/DeleteDialog.svelte'
  import DestinationPickerDialog from '../../../../lib/ui/DestinationPickerDialog.svelte'
  import FileGrid from '../../../../lib/ui/FileGrid.svelte'
  import FileTable from '../../../../lib/ui/FileTable.svelte'
  import FileTree from '../../../../lib/ui/FileTree.svelte'
  import { FAB, Icon, MenuItem } from 'm3-svelte'
  import { icons } from '../../../../lib/icons'
  import IconButton from '../../../../lib/ui/IconButton.svelte'
  import Menu from '../../../../lib/ui/Menu.svelte'
  import NewFolderDialog from '../../../../lib/ui/NewFolderDialog.svelte'
  import ProgressCircular from '../../../../lib/ui/ProgressCircular.svelte'
  import RenameDialog from '../../../../lib/ui/RenameDialog.svelte'
  import ShareManageDialog from '../../../../lib/ui/ShareManageDialog.svelte'
  import Snackbar from '../../../../lib/ui/Snackbar.svelte'
  import TextField from '../../../../lib/ui/TextField.svelte'
  import {
    filesFromWebkitDirectoryInput,
    pickDirectory,
    pickedFilesFromDataTransfer,
    supportsDirectoryPicker
  } from '../../../../lib/upload/directory-picker'
  import { formatBytes } from '../../../../lib/format/bytes'
  import { triggerUrlDownload } from '../../../../lib/format/download'
  import { jobTray } from '../../../../lib/state/job-tray.svelte'
  import { JobFailedError } from '../../../../lib/state/jobs'

  const rawPath = $derived(page.params.path ?? '')
  const path = $derived(normalizePath(`/${rawPath}`))

  // `/` is not a directory.: the root a user sees is a
  // projection of their grant list, not a real path — every API path is
  // `/{label}/...`, and the labels arrive in the session. So `DESIGN-FRONTEND.md`
  // §7 says `/` redirects to the first root.
  //
  // That was never implemented because the mock answered `list('/')` with the
  // roots, so the home screen appeared to work; against the real server it is
  // a 404 rendered as "not found" on the first screen after login.
  const firstRoot = $derived(authState.session?.roots?.[0]?.label ?? null)

  // item 133: a root marked `shared_externally` is read/written by another
  // service (Jellyfin, SMB) outside this app -- true for every path under
  // it, not just the root listing itself, since `RootEntry.shared_externally`
  // is a share-wide flag (DEPLOYMENT.md §4's SELinux `:z` caveat is the same
  // sharing relationship this warns about).
  const currentRootLabel = $derived(path.split('/').filter(Boolean)[0] ?? null)
  const rootShared = $derived(
    (authState.session?.roots ?? []).find((r) => r.label === currentRootLabel)?.shared_externally ?? false
  )
  const rootTrash = $derived(
    (authState.session?.roots ?? []).find((r) => r.label === currentRootLabel)?.trash_enabled ?? false
  )

  let lastOpened = ''
  $effect(() => {
    if (path === '/' && firstRoot) {
      void goto(`/b/${encodeURIComponent(firstRoot)}`, { replaceState: true })
      return
    }
    if (path !== lastOpened) {
      lastOpened = path
      browse.open(path)
    }
  })

  uploadTray.onFileDone((dest) => {
    if (normalizePath(dest) === browse.path) void browse.refresh()
  })

  interface Crumb {
    label: string
    path: string
  }
  const crumbs = $derived.by((): Crumb[] => {
    const parts = path.split('/').filter(Boolean)
    const out: Crumb[] = [{ label: t('browse.home'), path: '/' }]
    let acc = ''
    for (const part of parts) {
      acc += `/${part}`
      out.push({ label: part, path: acc })
    }
    return out
  })

  function onNavigate(p: string): void {
    goto(`/b${p}`)
  }

  function onOpen(entry: Entry): void {
    if (entry.kind === 'dir') {
      goto(`/b${joinPath(browse.path, entry.name)}`)
    }
    // files: no preview pane wired up yet (out of scope for the initial pass)
  }

  // ── toolbar state ──
  let newFolderOpen = $state(false)
  let renameOpen = $state(false)
  let deleteOpen = $state(false)
  let conflictOpen = $state(false)
  let conflictName = $state('')
  let contextEntry = $state<Entry | null>(null)
  let menuOpen = $state(false)
  let menuX = $state(0)
  let menuY = $state(0)
  let snackbarMsg = $state<string | null>(null)
  let searchOpen = $state(false)
  let searchQuery = $state('')
  let searchResults = $state<{ path: string; entry: Entry }[]>([])
  let searchInputEl: HTMLInputElement | undefined = $state()
  let fileInputEl: HTMLInputElement | undefined = $state()
  let dirInputEl: HTMLInputElement | undefined = $state()
  let dragOver = $state(false)
  let searchCancel: (() => void) | null = null
  let shareOpen = $state(false)
  /** DESIGN-FRONTEND.md §3 component inventory: `FileTree` alongside
   *  `FileTable`/`FileGrid`. Off by default -- most of the time the
   *  breadcrumb + table is all a user needs, and a 240px side panel is a
   *  real bite out of a phone/tablet width (§3). */
  let treeOpen = $state(false)

  function openContextMenu(entry: Entry, e: MouseEvent): void {
    contextEntry = entry
    menuX = e.clientX
    menuY = e.clientY
    menuOpen = true
  }

  async function createFolder(name: string): Promise<void> {
    newFolderOpen = false
    try {
      await api.mkdir(joinPath(browse.path, name))
      await browse.refresh()
    } catch (err) {
      snackbarMsg = err instanceof Error ? err.message : t('browse.could_not_create_folder')
    }
  }

  function requestRename(): void {
    const target = contextEntry ?? browse.selected[0]
    if (!target) return
    contextEntry = target
    menuOpen = false
    renameOpen = true
  }

  async function doRename(newName: string): Promise<void> {
    if (!contextEntry) return
    renameOpen = false
    try {
      await api.rename(joinPath(browse.path, contextEntry.name), newName)
      await browse.refresh()
    } catch (err) {
      snackbarMsg = err instanceof Error ? err.message : t('common.could_not_rename')
    }
  }

  function requestDelete(): void {
    menuOpen = false
    if (browse.selected.length === 0 && contextEntry) browse.selectOnly(contextEntry)
    deleteOpen = true
  }

  // ── download (DESIGN-PREVIEW.md §2/§8) ──
  // Single file: `POST /api/fs/link` mints a signed content-origin URL, then
  // a plain navigation (not `fetch`) hands the browser the actual
  // `Content-Disposition: attachment` bytes -- no CORS/blob juggling needed,
  // since the signed URL is itself the whole authorization story.
  // Multi-selection (and any directory): `POST /api/fs/archive` is always a
  // durable job now (DESIGN-API.md §6) -- JobTray tracks progress/cancel and
  // offers a download button once the job reports bytes waiting.

  async function downloadAsArchive(entries: Entry[]): Promise<void> {
    if (entries.length === 0) return
    const paths = entries.map((e) => joinPath(browse.path, e.name))
    const filename = entries.length === 1 ? `${entries[0].name}.zip` : 'archive.zip'
    try {
      const { job } = await api.archive(paths)
      // Doesn't await the job -- same fire-and-forget shape as an upload
      // add; `jobTray` (mounted once at the app root) keeps reporting
      // progress after this component unmounts.
      jobTray.track(job, 'archive', filename).catch((err) => {
        snackbarMsg = err instanceof ApiError ? err.message : t('browse.zip_job_failed')
      })
    } catch (err) {
      if (err instanceof ApiError && err.code === 'rate.limited') {
        // DESIGN-PREVIEW.md §8: the server caps concurrent archive streams
        // and 429s over that -- a real state to show, not a silent hang.
        snackbarMsg = t('browse.too_many_zip_downloads_at')
      } else {
        snackbarMsg = err instanceof ApiError ? err.message : t('browse.zip_download_failed')
      }
    }
  }

  async function downloadEntry(entry: Entry): Promise<void> {
    if (entry.kind === 'dir') {
      await downloadAsArchive([entry])
      return
    }
    if (entry.id === undefined) {
      // `entry.id` (the fid `/fs/link` requires) is only allocated once
      // something has forced it -- today, in practice, that's this same
      // file already having a share link created for it. See `Entry.id`'s
      // doc comment in `types.ts` for the full story; this is the honest
      // consequence of it at the point a user actually clicks Download,
      // not a guess sent to an endpoint that would just 422.
      snackbarMsg = t('browse.no_download_link_can_made')
      return
    }
    try {
      const { url } = await api.link(entry.id, 'attachment')
      triggerUrlDownload(url, entry.name, true)
    } catch (err) {
      snackbarMsg = err instanceof ApiError ? err.message : t('browse.could_not_create_download_link')
    }
  }

  function downloadSelection(): void {
    menuOpen = false
    const sel = browse.selected.length > 0 ? browse.selected : contextEntry ? [contextEntry] : []
    if (sel.length === 0) return
    if (sel.length === 1 && sel[0].kind === 'file') {
      void downloadEntry(sel[0])
      return
    }
    void downloadAsArchive(sel)
  }

  // ── share links ──
  function requestShare(): void {
    const target = contextEntry ?? browse.selected[0]
    if (!target) return
    contextEntry = target
    menuOpen = false
    shareOpen = true
  }

  async function doDelete(): Promise<void> {
    deleteOpen = false
    const targets = browse.selected.length > 0 ? browse.selected : contextEntry ? [contextEntry] : []
    const paths = targets.map((e) => joinPath(browse.path, e.name))
    browse.clearSelection()
    try {
      const { job } = await api.delete(paths)
      // Every delete is a durable job now (DESIGN-API.md §6) -- JobTray
      // shows progress/cancel. The files aren't actually gone until the job
      // settles, so the refresh waits for that instead of firing now.
      jobTray
        .track(job, 'delete')
        .catch((err) => {
          snackbarMsg = err instanceof ApiError ? err.message : t('browse.delete_job_failed')
        })
        .finally(() => browse.refresh())
    } catch (err) {
      snackbarMsg = err instanceof ApiError ? err.message : t('browse.delete_failed')
    }
  }

  // ── move / copy to another folder ──
  // One code path for all three actions. "Duplicate" is a copy whose destination is
  // the folder the entry is already in, and a move differs from a copy only in
  // which endpoint it calls -- the job tracking, the conflict retry and the
  // failure messages are identical, and were worth writing once.
  let destOpen = $state(false)
  let destSources = $state<string[]>([])
  /** Set by whichever transfer hit `fs.conflict`, so ConflictDialog's three
   *  answers re-run *that* operation rather than a hardcoded one. */
  let conflictRetry: ((on: OnConflict) => void) | null = null

  function requestTransfer(): void {
    menuOpen = false
    const targets = browse.selected.length > 0 ? browse.selected : contextEntry ? [contextEntry] : []
    if (targets.length === 0) return
    destSources = targets.map((e) => joinPath(browse.path, e.name))
    destOpen = true
  }

  function onDestinationPicked(dest: string, mode: 'move' | 'copy'): void {
    destOpen = false
    void transfer(destSources, dest, mode, 'Fail')
  }

  function duplicate(onConflict: OnConflict = 'Fail'): void {
    if (!contextEntry) return
    // The one menu action that forgot this. `Menu` only dismisses on a click
    // *outside* itself, so an item's own click leaves it open -- and duplicate
    // is the one that raises a modal on the same click, so the stale menu sat
    // opaque next to the conflict dialog with its labels painted over the file
    // rows behind it.
    menuOpen = false
    // Captured now, not read back off `contextEntry` once the job settles --
    // the context menu can be pointed at a different entry by then, since
    // this doesn't await the job.
    void transfer([joinPath(browse.path, contextEntry.name)], browse.path, 'copy', onConflict)
  }

  async function transfer(paths: string[], dest: string, mode: 'move' | 'copy', onConflict: OnConflict): Promise<void> {
    if (paths.length === 0) return
    // Whatever the conflict dialog answers has to re-run *this* operation,
    // with these paths and this destination -- by the time it is answered the
    // selection and the context menu have both moved on.
    conflictRetry = (on) => void transfer(paths, dest, mode, on)
    const failMsg = mode === 'move' ? t('browse.move_job_failed') : t('browse.copy_job_failed')
    const quotaMsg =
      mode === 'move' ? t('browse.not_enough_storage_space_move') : t('browse.not_enough_storage_space_copy')
    try {
      const req = { paths, dest, on_conflict: onConflict }
      const { job } = mode === 'move' ? await api.move(req) : await api.copy(req)
      // Every move/copy is a durable job (DESIGN-API.md §6) -- a per-item
      // conflict/quota failure still ends the job in `error` state
      // (`spawn_batch_job`'s `all_ok` check), so conflict detection happens in
      // `JobFailedError`'s `status.results` once the job actually rejects,
      // not in the response to the request that started it.
      jobTray
        .track(job, mode)
        .then(() => {
          conflictOpen = false
          if (mode === 'move') browse.clearSelection()
          browse.refresh()
        })
        .catch((err) => {
          const results = err instanceof JobFailedError ? err.status.results : []
          // The first conflicting item names the dialog. The answer then
          // applies to the whole batch, because `on_conflict` is a property of
          // the request and not of an item -- asking once per name would mean
          // one dialog per file for a selection that mostly collides.
          const conflicted = results.find((r) => r.error?.code === 'fs.conflict')
          if (conflicted) {
            conflictName = baseName(conflicted.path)
            conflictOpen = true
            return
          }
          const quota = results.some((r) => r.error?.code === 'quota.exceeded')
          snackbarMsg =
            quota || (err instanceof ApiError && err.code === 'quota.exceeded')
              ? quotaMsg
              : err instanceof ApiError
                ? err.message
                : failMsg
        })
    } catch (err) {
      snackbarMsg =
        err instanceof ApiError && err.code === 'quota.exceeded'
          ? quotaMsg
          : err instanceof ApiError
            ? err.message
            : failMsg
    }
  }

  function onUploadClick(): void {
    fileInputEl?.click()
  }
  function onUploadFilesChange(e: Event): void {
    const input = e.currentTarget as HTMLInputElement
    if (input.files) uploadTray.addFiles(input.files, browse.path)
    input.value = ''
  }

  async function onUploadFolderClick(): Promise<void> {
    if (supportsDirectoryPicker()) {
      try {
        const files = await pickDirectory()
        uploadTray.addEntries(files, browse.path)
      } catch {
        /* user canceled the picker */
      }
    } else {
      dirInputEl?.click()
    }
  }
  function onDirInputChange(e: Event): void {
    const input = e.currentTarget as HTMLInputElement
    uploadTray.addEntries(filesFromWebkitDirectoryInput(input), browse.path)
    input.value = ''
  }

  async function onDrop(e: DragEvent): Promise<void> {
    e.preventDefault()
    dragOver = false
    if (!e.dataTransfer) return
    const files = await pickedFilesFromDataTransfer(e.dataTransfer)
    uploadTray.addEntries(files, browse.path)
  }

  function focusSearch(): void {
    searchOpen = true
    queueMicrotask(() => searchInputEl?.focus())
  }
  function closeSearch(): void {
    searchOpen = false
    searchQuery = ''
    searchResults = []
    searchCancel?.()
  }
  function onSearchInput(): void {
    searchCancel?.()
    searchResults = []
    if (!searchQuery.trim()) return
    searchCancel = api.searchStream(
      searchQuery,
      (hit) => {
        searchResults = [...searchResults, hit].slice(0, 100)
      },
      () => {}
    )
  }
  function onSearchResultClick(hit: { path: string; entry: Entry }): void {
    closeSearch()
    goto(`/b${parentOf(hit.path)}`)
  }

  function toggleView(): void {
    browse.view = browse.view === 'list' ? 'grid' : 'list'
  }
  // MD3 "fade through" for the list/grid view switch (§ motion): the two
  // views are structurally unrelated (table vs. grid), so a shared-axis
  // morph isn't a good fit -- fade the old one out, fade the new one in.
  // Same reduced-motion read as Menu.svelte/Snackbar.svelte/UploadTray.svelte
  // -- a JS (Svelte) transition can't consume the CSS duration tokens.
  function reduceMotion(): boolean {
    return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
  }
  function viewSwitchDuration(): number {
    return reduceMotion() ? 0 : 120
  }
  function cycleDensity(): void {
    const order = ['compact', 'comfortable', 'spacious'] as const
    browse.density = order[(order.indexOf(browse.density) + 1) % order.length]
  }
  function toggleTree(): void {
    treeOpen = !treeOpen
  }
  function densityLabel(d: 'compact' | 'comfortable' | 'spacious'): string {
    return d === 'compact' ? t('browse.compact') : d === 'spacious' ? t('browse.comfortable') : t('common.fair')
  }

  // ── overflow menu ──
  // Measured: the phone toolbar cost 217px of an 844px viewport (a quarter
  // of the screen) because eight equal-weight controls wrapped across three
  // rows. MD3's shape for this is a top app bar with at most a couple of
  // icon actions plus an overflow menu for the rest, not a row that gives up
  // and wraps -- so the six controls that aren't "search" or "the one
  // primary create action" (see the FAB below) move in here, as m3-svelte
  // `MenuItem`s -- the same component the context menu below uses.
  let overflowOpen = $state(false)
  let overflowLeft = $state(0)
  let overflowTop = $state(0)
  let overflowMenuEl: HTMLDivElement | undefined = $state()
  let overflowTriggerEl: HTMLElement | null = null

  function openOverflow(e: MouseEvent): void {
    // Without this, the same click that opens the menu keeps bubbling after
    // Svelte's synchronous DOM flush has already mounted it -- Menu.svelte's
    // own `<svelte:window onclick>` light-dismiss then sees a click outside
    // the (now-existing) menu element and closes it in the same tick it
    // opened. The existing right-click context menu below never hit this
    // because a `contextmenu` event doesn't also fire `click`.
    e.stopPropagation()
    const btn = e.currentTarget as HTMLElement
    overflowTriggerEl = btn
    const rect = btn.getBoundingClientRect()
    // `Menu.svelte` positions itself with `position: absolute; left: auto`
    // and grows *rightward* from its static position -- it has no notion of
    // right-edge anchoring, so a wrapper `right:` offset (the first attempt
    // here) doesn't constrain it at all and it overflowed straight past a
    // 390px viewport. Anchor by `left` instead, same as the context menu
    // below already does, offset so the menu's *right* edge lands under the
    // kebab button -- 200px is a hair over Menu's own 160px `min-width` so
    // a longer label than currently exists still resolves inside the
    // viewport instead of clipping past it before its real width is known.
    overflowLeft = Math.max(8, Math.min(rect.right - 200, window.innerWidth - 208))
    overflowTop = rect.bottom + 4
    overflowOpen = true
    // Move focus into the menu the same way `focusSearch` already does for
    // the search field -- a `queueMicrotask` so it runs after the `{#if}`
    // that mounts the menu has actually rendered it.
    queueMicrotask(() => overflowMenuEl?.querySelector<HTMLButtonElement>('button')?.focus())
  }
  function closeOverflow(): void {
    overflowOpen = false
    // Menu.svelte's own Escape/click-outside handling calls this same
    // function, so focus returning to the trigger has to live here rather
    // than only in a keyboard-specific path.
    overflowTriggerEl?.focus()
  }
  // ARIA menu roving-focus: Tab already reaches every item in DOM order
  // (they're plain buttons), but a `role="menu"` is conventionally also
  // arrow-key navigable, and nothing upstream (Menu.svelte) provides that --
  // it wasn't needed until items with genuine keyboard users (not just a
  // mouse-driven context menu) moved in here.
  function onOverflowKeydown(e: KeyboardEvent): void {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const container = e.currentTarget as HTMLElement
    const items = Array.from(container.querySelectorAll<HTMLButtonElement>('button'))
    if (items.length === 0) return
    const idx = items.indexOf(document.activeElement as HTMLButtonElement)
    const next = e.key === 'ArrowDown' ? (idx + 1) % items.length : (idx - 1 + items.length) % items.length
    items[next]?.focus()
  }
  function openInEditor(): void {
    if (!contextEntry || contextEntry.kind === 'dir') return
    menuOpen = false
    goto(`/edit${joinPath(browse.path, contextEntry.name)}`)
  }
</script>

<svelte:head>
  <title>{crumbs.at(-1)?.label ?? 'Stowcloud'} · Stowcloud</title>
</svelte:head>

<div
  class="sc-browse"
  role="region"
  aria-label={t('browse.file_browser')}
  ondragover={(e) => {
    e.preventDefault()
    dragOver = true
  }}
  ondragleave={() => (dragOver = false)}
  ondrop={onDrop}
>
  <!--
    MD3 top app bar: a title (the breadcrumb) plus at most a couple of icon
    actions, everything else behind overflow -- not a row of equal-weight
    controls that wraps when it runs out of room (measured before this
    change: 217px of an 844px phone viewport, three rows deep).

    Search stays inline on both breakpoints -- it's used constantly and a
    second tap to reach it would be a regression. Refresh and the folder tree
    stay inline on desktop (room for them, and they're used often enough to be
    worth a direct icon) but fold into More on a phone, alongside the viewing
    preferences (grid/list, density) that fold in on *both* breakpoints --
    density and view mode are preferences, not actions taken per-visit, so they
    don't deserve a persistent icon on either width. Upload folder and New
    folder stay as visible buttons on desktop (there's room, and hiding a
    familiar control there would be a regression, not a simplification); on a
    phone they fold into More too, because the one create action a phone layout
    gets a dedicated control for is Upload -- see the FAB below.
  -->
  <div class="sc-browse__bar-stack">
  <header
    class="sc-browse__toolbar"
    class:sc-browse__toolbar--compact={uiState.compact}
    inert={browse.selection.size > 0}
  >
    <div class="sc-browse__title">
      <Breadcrumb {crumbs} onnavigate={onNavigate} />
      {#if rootShared}
        <span class="sc-browse__external-badge">
          <Icon icon={icons.warning} size={14} />
          {t('common.shared_with_other_services')}
        </span>
      {/if}
    </div>
    <div class="sc-browse__toolbar-actions">
      {#if searchOpen}
        <div class="sc-browse__search">
          <TextField bind:value={searchQuery} type="search" placeholder={t('browse.search')} autofocus onkeydown={(e) => {
            if (e.key === 'Enter') onSearchInput()
            if (e.key === 'Escape') closeSearch()
          }} />
          <IconButton label={t('browse.close_search')} onclick={closeSearch}><Icon icon={icons.close} /></IconButton>
        </div>
      {:else}
        <IconButton label={t('common.search')} onclick={focusSearch}><Icon icon={icons.search} /></IconButton>
        {#if !uiState.compact}
          <IconButton label={t('common.refresh')} onclick={() => browse.refresh()}><Icon icon={icons.refresh} /></IconButton>
          <IconButton label={treeOpen ? t('browse.hide_folder_tree') : t('browse.show_folder_tree')} selected={treeOpen} onclick={toggleTree}>
            <Icon icon={icons['folder-tree']} />
          </IconButton>
        {/if}
        <IconButton label={t('browse.more')} selected={overflowOpen} onclick={openOverflow}><Icon icon={icons['more-vert']} /></IconButton>
        {#if !uiState.compact}
          <Button variant="text" onclick={onUploadFolderClick}>
            {#snippet icon()}<Icon icon={icons['upload-folder']} size={18} />{/snippet}
            {t('browse.upload_folder')}
          </Button>
          <Button variant="tonal" onclick={onUploadClick}>
            {#snippet icon()}<Icon icon={icons.upload} size={18} />{/snippet}
            {t('common.upload')}
          </Button>
          <Button variant="filled" onclick={() => (newFolderOpen = true)}>
            {#snippet icon()}<Icon icon={icons.add} size={18} />{/snippet}
            {t('common.new_folder')}
          </Button>
        {/if}
      {/if}
    </div>
  </header>

  <!--
    MD3 contextual top app bar: while rows are selected this *replaces* the
    toolbar rather than stacking below it. It used to be a sibling with a
    permanently reserved 48px height, which bought layout stability at the
    price of an always-empty strip under the toolbar on every folder -- 48px
    of an 844px phone screen, for nothing, all the time. Overlaying the
    toolbar's own box costs no space and still can't reflow the table (it is
    out of flow entirely), so the dblclick-lands-on-another-row hazard the
    reservation existed to prevent is gone either way.
    The toolbar underneath is `inert` while this is up: its controls are
    covered, and a covered control must not stay tabbable.
  -->
  <div
    class="sc-browse__selection-bar"
    class:sc-browse__selection-bar--visible={browse.selection.size > 0}
  >
    <div class="sc-browse__selection-bar-inner">
      <!-- Ctrl/Cmd+A already covers Select all (FileTable.svelte's onKeydown),
           but that needs a physical keyboard — a phone has none, so it has to
           be reachable here too.
           Compact width gets icons, not the five text buttons: measured, they
           needed 642px of a 390px bar, so three of the five sat off-screen
           behind a horizontal scroll nothing announced. Icons are MD3's own
           answer for the contextual bar at this width, and every action stays
           one tap. Clear selection leads as the close affordance, matching how
           every other modal-ish surface in this app dismisses. -->
      {#if uiState.compact}
        <IconButton label={t('browse.clear_selection')} onclick={() => browse.clearSelection()}><Icon icon={icons.close} /></IconButton>
        <span class="sc-browse__selection-count">{t('common.item_count', { count: browse.selection.size })}</span>
        <span class="sc-browse__selection-gap"></span>
        <IconButton label={t('browse.select_all')} onclick={() => browse.selectAll()}><Icon icon={icons.check} /></IconButton>
        <IconButton label={t('common.download')} onclick={downloadSelection}><Icon icon={icons.download} /></IconButton>
        <IconButton label={t('dest.move_or_copy')} onclick={requestTransfer}><Icon icon={icons.move} /></IconButton>
        <IconButton label={t('common.rename')} onclick={requestRename}><Icon icon={icons.rename} /></IconButton>
        <IconButton label={t('common.delete')} onclick={requestDelete}><Icon icon={icons.delete} /></IconButton>
      {:else}
        <span class="sc-browse__selection-count">
          {t('browse.selected', { count: browse.selection.size, size: formatBytes(browse.totalSelectedSize) })}
        </span>
        <Button variant="text" onclick={() => browse.selectAll()}>{t('browse.select_all')}</Button>
        <Button variant="text" onclick={downloadSelection}>{t('common.download')}</Button>
        <Button variant="text" onclick={requestTransfer}>{t('dest.move_or_copy')}</Button>
        <Button variant="text" onclick={requestRename}>{t('common.rename')}</Button>
        <Button variant="text" onclick={requestDelete}>{t('common.delete')}</Button>
        <Button variant="text" onclick={() => browse.clearSelection()}>{t('browse.clear_selection')}</Button>
      {/if}
    </div>
  </div>
  </div>

  <Menu open={overflowOpen} onclose={closeOverflow} x={overflowLeft} y={overflowTop}>
      <div bind:this={overflowMenuEl} role="none" onkeydown={onOverflowKeydown}>
        {#if uiState.compact}
          <MenuItem onclick={() => { browse.refresh(); closeOverflow() }}>{t('common.refresh')}</MenuItem>
          <MenuItem onclick={() => { toggleTree(); closeOverflow() }}>
            {treeOpen ? t('browse.hide_folder_tree') : t('browse.show_folder_tree')}
          </MenuItem>
        {/if}
        <MenuItem onclick={() => { toggleView(); closeOverflow() }}>
          {browse.view === 'list' ? t('browse.grid_view') : t('browse.list_view')}
        </MenuItem>
        <MenuItem onclick={() => { cycleDensity(); closeOverflow() }}>
          {t('browse.density', { density: densityLabel(browse.density) })}
        </MenuItem>
        {#if uiState.compact}
          <MenuItem onclick={() => { onUploadFolderClick(); closeOverflow() }}>{t('browse.upload_folder')}</MenuItem>
          <MenuItem onclick={() => { newFolderOpen = true; closeOverflow() }}>{t('common.new_folder')}</MenuItem>
        {/if}
        <MenuItem onclick={() => { closeOverflow(); goto('/trash') }}>{t('browse.open_trash')}</MenuItem>
    </div>
  </Menu>

  {#if searchOpen && searchQuery.trim()}
    <ul class="sc-browse__search-results">
      {#each searchResults as hit (hit.path)}
        <li>
          <button class="sc-browse__search-result" onclick={() => onSearchResultClick(hit)}>
            <Icon icon={icons[hit.entry.kind === 'dir' ? 'folder' : 'file']} size={16} />
            {hit.path}
          </button>
        </li>
      {:else}
        <li class="sc-browse__search-empty">{t('browse.no_results')}</li>
      {/each}
    </ul>
  {/if}

  <div class="sc-browse__content">
    {#if treeOpen}
      <FileTree
        currentPath={path}
        onnavigate={(p) => goto(`/b${p}`)}
        overlay={uiState.compact}
        onclose={() => (treeOpen = false)}
      />
    {/if}
    <div class="sc-browse__table-wrap" class:sc-browse__table-wrap--dragover={dragOver}>
      {#if browse.loading}
        <div class="sc-browse__loading"><ProgressCircular /></div>
      {:else if browse.error}
        <p class="sc-browse__error" role="alert">{browse.error.message}</p>
      {:else if browse.view === 'grid'}
        <div
          class="sc-browse__view"
          in:fade={{ duration: viewSwitchDuration() }}
          out:fade={{ duration: viewSwitchDuration() }}
        >
          <FileGrid
            {browse}
            onopen={onOpen}
            oncontextmenu={openContextMenu}
            onrename={requestRename}
            ondelete={requestDelete}
            onsearchfocus={focusSearch}
          />
        </div>
      {:else}
        <div
          class="sc-browse__view"
          in:fade={{ duration: viewSwitchDuration() }}
          out:fade={{ duration: viewSwitchDuration() }}
        >
          <FileTable
            {browse}
            onopen={onOpen}
            oncontextmenu={openContextMenu}
            onrename={requestRename}
            ondelete={requestDelete}
            onsearchfocus={focusSearch}
          />
        </div>
      {/if}
      {#if dragOver}
        <div class="sc-browse__drop-overlay" aria-hidden="true">{t('browse.drop_here_upload')}</div>
      {/if}
    </div>
  </div>

  {#if uiState.compact}
    <!--
      The one primary action a phone-width toolbar gets a dedicated control
      for. Upload over New folder: a folder is created rarely,
      once per project or session; bringing in new files (not least straight
      from the camera roll) is the action a phone visit to a file browser is
      most often *for*. Upload folder was never a contender here -- directory
      picker support on a mobile browser is patchy at best, so it's exactly the
      kind of secondary action More exists for.
    -->
    <div class="sc-browse__fab">
      <FAB icon={icons.upload} aria-label={t('common.upload')} onclick={onUploadClick} />
    </div>
  {/if}
</div>

<input
  bind:this={fileInputEl}
  type="file"
  id="sc-upload-file-input"
  name="sc-upload-file-input"
  aria-label={t('browse.choose_files_upload')}
  multiple
  hidden
  onchange={onUploadFilesChange}
/>
<input
  bind:this={dirInputEl}
  type="file"
  id="sc-upload-dir-input"
  name="sc-upload-dir-input"
  aria-label={t('browse.choose_folder_upload')}
  webkitdirectory
  hidden
  onchange={onDirInputChange}
/>

<Menu open={menuOpen} onclose={() => (menuOpen = false)} x={menuX} y={menuY}>
    {#if contextEntry && contextEntry.kind !== 'dir'}
      <MenuItem onclick={openInEditor}>{t('browse.open_text_editor')}</MenuItem>
    {/if}
    <MenuItem onclick={downloadSelection}>{t('common.download')}</MenuItem>
    {#if contextEntry?.perms.share}
      <MenuItem onclick={requestShare}>{t('browse.manage_share_links')}</MenuItem>
    {/if}
    <MenuItem onclick={requestRename}>{t('browse.rename_f2')}</MenuItem>
    <MenuItem onclick={requestTransfer}>{t('dest.move_or_copy')}</MenuItem>
    <MenuItem onclick={() => duplicate('Fail')}>{t('browse.duplicate')}</MenuItem>
    <MenuItem onclick={requestDelete}>{t('browse.delete_del')}</MenuItem>
</Menu>

<NewFolderDialog open={newFolderOpen} onclose={() => (newFolderOpen = false)} oncreate={createFolder} />
<RenameDialog
  open={renameOpen}
  currentName={contextEntry?.name ?? ''}
  onclose={() => (renameOpen = false)}
  onrename={doRename}
/>
<DeleteDialog
  open={deleteOpen}
  count={browse.selected.length || (contextEntry ? 1 : 0)}
  externalShare={rootShared}
  trashEnabled={rootTrash}
  onclose={() => (deleteOpen = false)}
  onconfirm={doDelete}
/>
<DestinationPickerDialog
  open={destOpen}
  sources={destSources}
  onclose={() => (destOpen = false)}
  onpick={onDestinationPicked}
/>
<ConflictDialog
  open={conflictOpen}
  name={conflictName}
  onclose={() => (conflictOpen = false)}
  onkeepboth={() => conflictRetry?.('Rename')}
  onoverwrite={() => conflictRetry?.('Overwrite')}
  onskip={() => conflictRetry?.('Skip')}
/>
{#if contextEntry}
  <ShareManageDialog
    open={shareOpen}
    path={joinPath(browse.path, contextEntry.name)}
    targetName={contextEntry.name}
    targetIsDir={contextEntry.kind === 'dir'}
    onclose={() => (shareOpen = false)}
  />
{/if}
<Snackbar message={snackbarMsg} ondismiss={() => (snackbarMsg = null)} />

<style>
  .sc-browse {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    /* Anchors `.sc-browse__drop-overlay` (absolute) to the browse pane
       itself. `.sc-browse__fab` used to rely on this too -- floating over
       the file list "for free" by being absolutely positioned inside a
       pane that was itself always exactly the visible area, back when
       `.sc-app-shell__main` clipped everything to one screen. Once the
       document became the real scroller (+layout.svelte), this pane is no
       longer clipped to the viewport -- it scrolls with the page like
       everything else -- so an absolutely-positioned FAB anchored to it
       scrolled away too (measured: `top` went from 707 at the top of the
       page to -4443 near the bottom). Same mistake already caught and
       fixed twice over for `NavigationBar`/`NavigationRail`: an element
       that only looked pinned because nothing used to scroll. The FAB is
       `position: fixed` now (see its own rule) instead of relying on this
       element's `position: relative`. */
    position: relative;
  }
  .sc-browse__toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 16px;
    /* `box-shadow`, not `border-bottom`: a border is inside the border box, so
       the 40px action row plus 16px of padding each side measured 73px, one
       off the 4px grid, and pushed everything below it half a pixel out of
       step. A shadow paints outside the box and costs no layout height.
       NavigationDrawer/FileTree's overlay headers do the same. */
    box-shadow: 0 1px 0 var(--m3c-outline-variant);
    /* Defensive fallback only, not the normal case anymore: an unusually
       deep breadcrumb chain can still outgrow one row (Breadcrumb.svelte
       wraps its own crumbs rather than truncating, and isn't a file this
       change owns). At the two-icon compact width and the handful of
       controls left on desktop, this shouldn't trigger in practice -- see
       `--compact` below for the width it's tuned against. */
    flex-wrap: wrap;
  }
  .sc-browse__toolbar--compact {
    /* Down from 16px/16px: at 360px (the
       narrowest width this project tests against) the breadcrumb plus
       search plus More needed 345px against 328px available at
       the wider spacing and wrapped to a second row -- exactly the failure this
       toolbar redesign exists to remove. Tightening compact-only spacing
       buys back the ~17px that cost; desktop keeps the wider spacing since
       it was never the problem. */
    padding-inline: 8px;
    gap: 8px;
  }
  .sc-browse__toolbar-actions {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 8px;
  }
  .sc-browse__title {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    min-width: 0;
  }
  /* item 133: marks a root shared with another service (Jellyfin, SMB) --
     same tonal-container chip shape as `SessionsSection.svelte`'s "Current
     session" badge, tertiary rather than primary since this is a heads-up, not
     a status. */
  .sc-browse__external-badge {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 24px;
    padding-inline: 8px;
    border-radius: var(--m3-shape-full);
    background: var(--m3c-tertiary-container);
    color: var(--m3c-on-tertiary-container);
    @apply --m3-label-small;
    white-space: nowrap;
  }
  .sc-browse__search {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 240px;
  }
  /* `TextField.svelte` sizes the framework's box off its own wrapper
     (`width: 100%`), which is right in the column layouts every other caller
     uses and circular in this row: the wrapper's automatic width comes from
     content that is measured as a percentage of the wrapper, so it resolved to
     zero and the field disappeared, leaving the input to spill 32px of itself
     over the breadcrumb. This is the one caller that has to say how much of
     the row the field takes. */
  .sc-browse__search :global(.field) {
    flex: 1;
  }
  .sc-browse__bar-stack {
    /* Containing block for the selection bar, which covers the toolbar
       exactly instead of guessing at its height (it varies with breakpoint,
       and wraps to two rows on a deep breadcrumb chain). */
    position: relative;
  }
  .sc-browse__selection-bar {
    position: absolute;
    inset: 0;
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
    /* Was a `.sc-fade-toggle` utility from the hand-rolled `tokens.css`.
       That file is gone (m3-svelte owns the design system now) and the
       utility went with it, which left the bar permanently visible -- an
       always-on green strip announcing "0 selected" above every folder. It
       was the only user of that utility, so the two rules live here now.
       `visibility` is delayed rather than transitioned so the bar stays
       hit-testable for the length of the fade out, then stops. */
    opacity: 0;
    visibility: hidden;
    transition:
      opacity var(--m3-easing),
      visibility 0s var(--m3-duration);
  }
  .sc-browse__selection-bar--visible {
    opacity: 1;
    visibility: visible;
    transition:
      opacity var(--m3-easing),
      visibility 0s;
  }
  .sc-browse__selection-bar-inner {
    display: flex;
    align-items: center;
    gap: 16px;
    height: 100%;
    padding-inline: 16px;
    /* Nothing here shrinks or wraps (a half-rendered destructive button is
       worse than a scroll), so the row scrolls if a long enough locale ever
       outgrows it. At compact width the icon variant above fits without it. */
    overflow-x: auto;
  }
  .sc-browse__selection-bar-inner:has(.sc-browse__selection-gap) {
    /* Icon variant: the icons are already 48px touch targets, so 16px between
       them reads as gaps rather than a group. */
    gap: 4px;
    padding-inline: 4px;
  }
  .sc-browse__selection-gap {
    /* Pushes the actions to the trailing edge, leading close + count to the
       start — MD3's contextual top app bar layout. */
    flex: 1 1 auto;
  }
  .sc-browse__selection-count {
    flex-shrink: 0;
    white-space: nowrap;
  }
  .sc-browse__selection-bar-inner :global(button) {
    flex-shrink: 0;
  }
  .sc-browse__search-results {
    list-style: none;
    margin: 0;
    padding: 8px 16px;
    max-height: 240px;
    overflow-y: auto;
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-browse__search-result {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    border: none;
    background: transparent;
    color: var(--m3c-on-surface);
    text-align: left;
    padding-block: 8px;
    cursor: pointer;
  }
  .sc-browse__search-empty {
    color: var(--m3c-on-surface-variant);
    padding-block: 8px;
  }
  .sc-browse__content {
    display: flex;
    flex: 1;
    min-height: 0;
    /* FileTree.svelte queries this container's own inline size (§3), not
       the viewport, to shrink itself on a phone/tablet-width browse pane. */
    container-type: inline-size;
    container-name: sc-browse-content;
  }
  .sc-browse__table-wrap {
    position: relative;
    flex: 1;
    display: flex;
    min-width: 0;
    min-height: 0;
  }
  .sc-browse__table-wrap--dragover {
    outline: 2px dashed var(--m3c-primary);
    outline-offset: -2px;
  }
  /* Wraps FileGrid/FileTable so switching between them can `fade` (MD3
     "fade through" for unrelated view swaps -- see `viewSwitchDuration()`).
     Svelte runs the outgoing view's `out:` and the incoming view's `in:`
     concurrently, so both are briefly in the DOM together; `position:
     absolute` here keeps them stacked on top of each other instead of
     splitting `.sc-browse__table-wrap`'s flex space two ways for that
     instant (which would otherwise squeeze both to half-width/height for
     one frame). Still `display: flex; min-width: 0` so FileTable's own
     `flex: 1; min-width: 0` (load-bearing for the no-horizontal-overflow
     fix at 360px) resolves against *this* box exactly like it used to
     resolve against `.sc-browse__table-wrap` directly. */
  .sc-browse__view {
    position: absolute;
    inset: 0;
    display: flex;
    min-width: 0;
    min-height: 0;
  }
  .sc-browse__loading {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: 1;
  }
  .sc-browse__error {
    padding: 24px;
    color: var(--m3c-error);
  }
  .sc-browse__drop-overlay {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: color-mix(in srgb, var(--m3c-primary) 12%, transparent);
    color: var(--m3c-primary);
    @apply --m3-title-medium;
    pointer-events: none;
  }
  .sc-browse__fab {
    /* `position: fixed` (was `absolute`, anchored to `.sc-browse` -- see
       that rule's comment for why that stopped being pinned once the
       document became the real scroller). Fixed pins to the viewport
       itself, same fix already applied to `NavigationBar`/`NavigationRail`/
       `NavigationDrawer` (+layout.svelte and those components' own
       comments) for the identical reason. */
    position: fixed;
    right: 16px;
    /* Clears `NavigationBar`'s full height (the same
       `--sc-nav-bar-height + env(safe-area-inset-bottom, 0px)` formula
       `NavigationDrawer.svelte`'s overlay variant already shares with it)
       plus this FAB's own 16px gap above it, so the two never collide.
       This does NOT try to also clear the last row of a long list -- MD3
       floating action buttons are expected to float over list content (the
       "floating" is the point; every reference MD3 file-manager mock has
       the FAB sitting on top of the last row, not making room for it), so
       the bottom reservation FileTable.svelte/FileGrid.svelte already add
       for the bar stays exactly what it is. Only the bar itself gets a
       dedicated no-overlap guarantee; the FAB gets MD3's normal floating
       behavior.
       `--sc-tray-stack-top` is the one thing that does move it: the
       job/upload tray stack is fixed to this same corner at z 30 and wins,
       so a running job buried this FAB outright (8px of 56 left showing,
       measured at 390x844) rather than merely floating over it. A snackbar
       covering the FAB for a few seconds is MD3-normal; a tray that stays up
       for the length of a copy is not. +layout.svelte publishes the stack's
       measured top edge as a distance up from the bottom of the viewport,
       already including the 12px gap the FAB should keep above it, and 0px
       when the stack is empty -- so `max()` picks the resting place below
       whenever there is nothing there, with no second baseline to reconcile
       (see that effect's comment for why a published *height* lost 8px). */
    bottom: max(
      calc(16px + var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px)),
      var(--sc-tray-stack-top, 0px)
    );
    /* `NavigationDrawer`'s overlay variant is a native `<dialog>`
       (`showModal()`), which the UA always paints in the top layer above
       every ordinary stacking context regardless of z-index -- so the FAB
       sits under its scrim for free; this z-index only has to beat this
       page's own regular content (below `Menu`/`Snackbar`/`UploadTray`'s
       20/40/30 -- see their own files -- which is correct, a snackbar or
       menu should still cover the FAB, not the other way round). */
    z-index: 10;
  }
</style>
