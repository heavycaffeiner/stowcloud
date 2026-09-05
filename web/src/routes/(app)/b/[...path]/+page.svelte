<script lang="ts">
  import { fade } from 'svelte/transition'
  import { goto } from '$app/navigation'
  import { page } from '$app/state'
  import { createInfiniteQuery, createMutation, createQueries } from '@tanstack/svelte-query'
  import { ApiError, type BatchItemResult, type Entry, type OnConflict } from '../../../../lib/api/types'
  import { api } from '../../../../lib/api/client'
  import { baseName, joinPath, normalizePath, parentOf } from '../../../../lib/api/path-utils'
  import { batchErrorKey, describeApiError } from '../../../../lib/api/error-text'
  import {
    archiveTicketMutation,
    copyMutation,
    deleteMutation,
    dirListQuery,
    dirViewOf,
    folderSizeQuery,
    invalidateDirs,
    mkdirMutation,
    moveMutation,
    renameMutation
  } from '../../../../lib/query/files'
  import { createSession } from '../../../../lib/query/session'
  import { jobTray } from '../../../../lib/store/jobs.store'
  import { selection } from '../../../../lib/store/selection.store'
  import { ui } from '../../../../lib/store/ui.store'
  import { view } from '../../../../lib/store/view.store'
  import { addEntries, addFiles } from '../../../../lib/upload/queue'
  import { t } from '../../../../lib/i18n'
  import Breadcrumb from '../../../../lib/ui/Breadcrumb.svelte'
  import Button from '../../../../lib/ui/Button.svelte'
  import ConflictDialog from '../../../../lib/ui/ConflictDialog.svelte'
  import DeleteDialog from '../../../../lib/ui/DeleteDialog.svelte'
  import DetailsPanel from '../../../../lib/ui/DetailsPanel.svelte'
  import DestinationPickerDialog from '../../../../lib/ui/DestinationPickerDialog.svelte'
  import FileGrid from '../../../../lib/ui/FileGrid.svelte'
  import FileTable from '../../../../lib/ui/FileTable.svelte'
  import FileTree from '../../../../lib/ui/FileTree.svelte'
  import { FAB, Icon, MenuItem } from 'm3-svelte'
  import { icons } from '../../../../lib/icons'
  import type { Order, SortKey } from '../../../../lib/api/types'
  import IconButton from '../../../../lib/ui/IconButton.svelte'
  import Menu from '../../../../lib/ui/Menu.svelte'
  import NewFolderDialog from '../../../../lib/ui/NewFolderDialog.svelte'
  import PreviewDialog from '../../../../lib/ui/PreviewDialog.svelte'
  import ProgressCircular from '../../../../lib/ui/ProgressCircular.svelte'
  import RenameDialog from '../../../../lib/ui/RenameDialog.svelte'
  import { autoScrollStep, movedFar, rectBetween, type Rect } from '../../../../lib/ui/marquee'
  import { rowActions } from '../../../../lib/ui/row-actions'
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
  import { downloadPath, triggerUrlDownload } from '../../../../lib/format/download'
  import { downloadEncryptedFile, downloadEncryptedFolder } from '../../../../lib/crypto/download-sw'
  import { encryptionForLabel, shareLabelOf } from '../../../../lib/crypto/encrypted-shares'
  import { FileTooLargeError, LockedSessionError } from '../../../../lib/crypto/e2ee'
  import UnlockShareDialog from '../../../../lib/ui/UnlockShareDialog.svelte'

  const session = createSession()
  const rawPath = $derived(page.params.path ?? '')
  const path = $derived(normalizePath(`/${rawPath}`))

  // `/` is not a directory: the root a user sees is a
  // projection of their grant list, not a real path: every API path is
  // `/{label}/...`, and the labels arrive in the session.
  //
  // That was never implemented because the mock answered `list('/')` with the
  // roots, so the home screen appeared to work; against the real server it is
  // a 404 rendered as "not found" on the first screen after login.
  const firstRoot = $derived(session.data?.roots?.[0]?.label ?? null)

  // No root at all is a deployment nobody has given this account a folder in,
  // which on a fresh install is every account including the first
  // administrator. The redirect above has nowhere to go, so this screen is
  // where they stay and it has to say something useful.
  const noShares = $derived((session.data?.roots ?? []).length === 0)

  const currentRootLabel = $derived(path.split('/').filter(Boolean)[0] ?? null)
  const currentRoot = $derived((session.data?.roots ?? []).find((r) => r.label === currentRootLabel))

  // A folder that is listed and cannot be opened, because the disk under it is
  // not there right now. It is in the root list on purpose: dropping it made a
  // drive that did not come back look exactly like a share somebody deleted.
  const rootBroken = $derived(currentRoot?.broken_reason ?? '')

  // item 133: a root marked `shared_externally` is read/written by another
  // service (Jellyfin, SMB) outside this app -- true for every path under
  // it, not just the root listing itself, since `RootEntry.shared_externally`
  // is a share-wide flag ('s SELinux `:z` caveat is the same
  // sharing relationship this warns about).
  const rootShared = $derived(currentRoot?.shared_externally ?? false)
  const rootTrash = $derived(currentRoot?.trash_enabled ?? false)

  // ── the listing ──
  //
  // One infinite query per path and sort order. Both are in the key, so
  // navigating or re-sorting is a different query rather than a re-fetch this
  // page has to sequence, and a change reported over the WebSocket
  // (`query/live.ts`) invalidates it without anybody subscribing here.
  const sort = $derived({ key: view.state.sortKey, order: view.state.sortOrder })
  const listing = createInfiniteQuery(() => dirListQuery(path, sort))
  const dir = $derived(dirViewOf(listing.data?.pages))
  const entries = $derived(dir.entries)
  const names = $derived(new Set(entries.map((e) => e.name)))

  /** The selection resolved against what is listed. A name the selection
   *  still holds for a row that has since gone simply matches nothing, which
   *  is why nothing has to prune it. */
  const selected = $derived(entries.filter((e) => selection.state.names.has(e.name)))
  const selectedFolders = $derived(selected.filter((e) => e.kind === 'dir'))
  /** Files only. A directory entry's own size is the bytes its inode spends on
   *  the listing, a number that says nothing about what is inside, so adding
   *  it to a total reads as a wrong answer rather than a partial one. */
  const selectedFileBytes = $derived(selected.reduce((a, e) => (e.kind === 'dir' ? a : a + e.size), 0))

  $effect(() => {
    if (path === '/' && firstRoot) {
      void goto(`/b/${encodeURIComponent(firstRoot)}`, { replaceState: true })
    }
  })

  // A selection is a set of names in one directory, and those names mean
  // something else in the next one.
  $effect(() => {
    void path
    selection.reset()
  })

  function refresh(): void {
    invalidateDirs([path])
  }

  /** The rendered window has reached past what is loaded. Listings are a
   *  forward-only cursor walk, so the answer is always "ask for the next
   *  page"; the query itself drops a duplicate request. */
  function requestMore(): void {
    if (listing.hasNextPage && !listing.isFetchingNextPage) void listing.fetchNextPage()
  }

  // ── what a selection holds ──
  //
  // One query per folder, so re-selecting a folder is instant and a walk is
  // never repeated. Nothing selected measures the folder being looked at,
  // which is what the details panel reports for a directory with no selection
  // in it. The virtual root is above every share and is not a folder to walk.
  const measured = $derived(
    selected.length === 0
      ? path === '/'
        ? []
        : [path]
      : selectedFolders.map((e) => joinPath(path, e.name))
  )
  const folderSizes = createQueries(() => ({
    queries: measured.map((p) => folderSizeQuery(p)),
    combine: (results) => ({
      pending: results.some((r) => r.isPending),
      failed: results.some((r) => r.isError),
      bytes: results.reduce((sum, r) => sum + (r.data?.bytes ?? 0), 0),
      files: results.reduce((sum, r) => sum + (r.data?.files ?? 0), 0)
    })
  }))
  const selectionBytes = $derived(selectedFileBytes + folderSizes.bytes)

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
      goto(`/b${joinPath(path, entry.name)}`)
      return
    }
    // Looking, not committing. A click used to download, which writes to the
    // user's disk to answer "what is this"; the viewer answers it without
    // touching anything, and keeps Download a button away.
    previewIndex = entries.indexOf(entry)
    previewOpen = true
  }

  // ── preview ──
  let previewOpen = $state(false)
  /** Absolute index into the directory listing, not the entry itself, so the
   *  arrows can walk the folder without the dialog holding a stale copy of a
   *  row that has since been re-fetched. */
  let previewIndex = $state(-1)
  const previewEntry = $derived(previewIndex >= 0 ? (entries[previewIndex] ?? null) : null)
  const previewPath = $derived(previewEntry ? joinPath(path, previewEntry.name) : '')

  /** Step to the next/previous file, skipping directories: the viewer has
   *  nothing to show for one, and stopping the arrows dead at every folder in
   *  a mixed listing would make them useless. Folders sort ahead of files
   *  (`sc-core`'s listing), so in practice this skips the run at the top once. */
  function stepPreview(delta: number): void {
    let i = previewIndex + delta
    while (i >= 0 && i < dir.total) {
      const candidate = entries[i]
      // An unloaded row is a window that has not arrived yet. Stop rather than
      // skipping past it, or a fast arrow key would walk over files silently.
      if (!candidate) return
      if (candidate.kind !== 'dir') {
        previewIndex = i
        return
      }
      i += delta
    }
  }
  const hasPreviewNeighbour = (delta: number): boolean => {
    let i = previewIndex + delta
    while (i >= 0 && i < dir.total) {
      const candidate = entries[i]
      if (!candidate) return false
      if (candidate.kind !== 'dir') return true
      i += delta
    }
    return false
  }

  // ── rubber-band selection ──
  //
  // Drag across the listing to select what the rectangle covers. Mouse only:
  // on a touch screen the same gesture is how the page scrolls, and taking it
  // would leave no way to scroll at all.
  //
  // The rectangle is kept in document coordinates so that auto-scrolling near
  // the edge of the window extends it instead of dragging it along. Which rows
  // it covers is arithmetic, not hit-testing, because the listing is
  // virtualized: see `lib/ui/marquee.ts`.
  let gridView = $state<{ entriesInRect: (r: Rect) => Entry[] }>()
  let tableView = $state<{ entriesInRect: (r: Rect) => Entry[] }>()
  let marqueeRect = $state<Rect | null>(null)
  let scrollXNow = $state(0)
  let scrollYNow = $state(0)

  /** Anchor corner, in document coordinates. */
  let dragOrigin: { x: number; y: number } | null = null
  /** Last pointer position, in viewport coordinates, for the auto-scroll loop. */
  let dragPointer = { x: 0, y: 0 }
  /** Whatever was selected when the drag began, kept only for an additive drag. */
  let dragBase: string[] = []
  let dragFrame = 0

  const activeView = $derived(view.state.mode === 'grid' ? gridView : tableView)

  /** Controls own their own gestures. Rows and cards are not on this list: a
   *  drag may start on one, because it only becomes a marquee once it has
   *  moved too far to have been a click. */
  const CONTROL_SELECTOR = 'button, input, a, [role="menuitem"], [role="menu"]'
  /** ...and for deciding whether a click landed on the blank area, rows and
   *  cards are, because a click on one is a click on it. */
  const CONTENT_SELECTOR = `.sc-row, .sc-file-grid__card, ${CONTROL_SELECTOR}`

  function onMarqueePointerDown(e: PointerEvent): void {
    if (e.pointerType !== 'mouse' || e.button !== 0) return
    if ((e.target as HTMLElement).closest(CONTROL_SELECTOR)) return

    dragOrigin = { x: e.clientX + window.scrollX, y: e.clientY + window.scrollY }
    dragPointer = { x: e.clientX, y: e.clientY }
    dragBase = e.shiftKey || e.ctrlKey || e.metaKey ? [...selection.state.names] : []
    window.addEventListener('pointermove', onMarqueePointerMove)
    window.addEventListener('pointerup', endMarquee)
    window.addEventListener('keydown', onMarqueeKeydown)
  }

  function onMarqueePointerMove(e: PointerEvent): void {
    if (!dragOrigin) return
    dragPointer = { x: e.clientX, y: e.clientY }
    if (!marqueeRect && !movedFar(dragOrigin.x - window.scrollX, dragOrigin.y - window.scrollY, e.clientX, e.clientY)) {
      return
    }
    if (!marqueeRect) {
      // Crossing the threshold is what turns a click into a drag. Text
      // selection has to stop here rather than at pointerdown, or an ordinary
      // click would stop selecting text everywhere in the listing.
      window.getSelection()?.removeAllRanges()
      dragFrame = requestAnimationFrame(autoScrollTick)
    }
    updateMarquee()
  }

  function updateMarquee(): void {
    if (!dragOrigin) return
    scrollXNow = window.scrollX
    scrollYNow = window.scrollY
    const rect = rectBetween(
      dragOrigin.x,
      dragOrigin.y,
      dragPointer.x + window.scrollX,
      dragPointer.y + window.scrollY
    )
    marqueeRect = rect
    const hits = activeView?.entriesInRect(rect) ?? []
    selection.replace([...dragBase, ...hits.map((entry) => entry.name)])
  }

  function autoScrollTick(): void {
    if (!dragOrigin) return
    const step = autoScrollStep(dragPointer.y, window.innerHeight)
    if (step !== 0) {
      window.scrollBy(0, step)
      updateMarquee()
    }
    dragFrame = requestAnimationFrame(autoScrollTick)
  }

  function onMarqueeKeydown(e: KeyboardEvent): void {
    if (e.key !== 'Escape') return
    selection.replace(dragBase)
    endMarquee()
  }

  function endMarquee(): void {
    const wasDragging = marqueeRect !== null
    dragOrigin = null
    marqueeRect = null
    cancelAnimationFrame(dragFrame)
    window.removeEventListener('pointermove', onMarqueePointerMove)
    window.removeEventListener('pointerup', endMarquee)
    window.removeEventListener('keydown', onMarqueeKeydown)
    if (!wasDragging) return
    // The click that closes this gesture is still coming, and a row's own
    // handler would answer it by replacing everything the drag just selected.
    window.addEventListener('click', swallowClick, { capture: true, once: true })
  }

  function swallowClick(e: MouseEvent): void {
    e.stopPropagation()
    e.preventDefault()
  }

  /**
   * Clicking past the end of the listing, or in the gaps between cards, drops
   * the selection. Same rule as the blank-area right-click, and what every
   * file manager does.
   *
   * A click that reached here through a row or a card is that row's, which is
   * why this checks where it started rather than trusting that it arrived.
   * Nothing stops propagation on the way up, so without the check this would
   * undo every selection the moment it was made. A click that closes a
   * marquee drag never gets here at all: `swallowClick` takes it during the
   * capture phase.
   */
  function onEmptyAreaClick(e: MouseEvent): void {
    if ((e.target as HTMLElement).closest(CONTENT_SELECTOR)) return
    selection.clear()
  }

  // ── toolbar state ──
  let newFolderOpen = $state(false)
  let renameOpen = $state(false)
  let deleteOpen = $state(false)
  let conflictOpen = $state(false)
  let conflictName = $state('')
  let contextEntry = $state<Entry | null>(null)
  let menuOpen = $state(false)
  let emptyMenuOpen = $state(false)
  let menuX = $state(0)
  let menuY = $state(0)
  let snackbarMsg = $state<string | null>(null)
  let searchOpen = $state(false)
  let searchQuery = $state('')
  let searchResults = $state<{ path: string; entry: Entry }[]>([])
  /** A query has actually been submitted (Enter), so an empty
   *  `searchResults` means "nothing matched" rather than "not asked yet". */
  let searchRan = $state(false)
  // The server stopped the walk at its deadline, so what is listed is a
  // prefix of the matches. Saying so beats a short list that reads complete.
  let searchTruncated = $state(false)
  let searchInputEl: HTMLInputElement | undefined = $state()
  let fileInputEl: HTMLInputElement | undefined = $state()
  let dirInputEl: HTMLInputElement | undefined = $state()
  let dragOver = $state(false)
  let searchCancel: (() => void) | null = null
  let shareOpen = $state(false)
  /** The entries the share and rename dialogs are pointed at, captured when
   *  the action starts. They are separate from `contextEntry` because that one
   *  belongs to the right-click menu and outlives it, which is how both
   *  dialogs used to open on the previously right-clicked row. */
  let shareTarget = $state<Entry | null>(null)
  let renameTarget = $state<Entry | null>(null)
  /** component inventory: `FileTree` alongside
   *  `FileTable`/`FileGrid`. Off by default -- most of the time the
   *  breadcrumb + table is all a user needs, and a 240px side panel is a
   *  real bite out of a phone/tablet width (§3). */
  let treeOpen = $state(false)

  $effect(() => {
    const onNewFolder = () => { newFolderOpen = true }
    const onUploadFile = () => { onUploadClick() }
    const onUploadDir = () => { onUploadFolderClick() }
    window.addEventListener('sc:folder', onNewFolder)
    window.addEventListener('sc:file', onUploadFile)
    window.addEventListener('sc:upload-folder', onUploadDir)
    return () => {
      window.removeEventListener('sc:folder', onNewFolder)
      window.removeEventListener('sc:file', onUploadFile)
      window.removeEventListener('sc:upload-folder', onUploadDir)
    }
  })

  function openContextMenu(entry: Entry, e: MouseEvent): void {
    // Right-clicking a row that is not in the selection makes it the selection,
    // which is what every file manager does and what keeps this menu and the
    // selection bar aimed at the same rows. Without it the two could be open
    // against different targets at once, showing different actions for the same
    // gesture. A right-click *inside* the selection leaves it alone, so
    // right-clicking one of five selected files still acts on all five.
    if (!selection.state.names.has(entry.name)) selection.only(entry.name, entries.indexOf(entry))
    contextEntry = entry
    emptyMenuOpen = false
    menuX = e.clientX
    menuY = e.clientY
    menuOpen = true
  }

  /** Right-click on blank space. No target rows, so this is deliberately not
   *  `rowActions`: it offers what you can do *to the folder* instead. Clearing
   *  the selection matches every file manager, and keeps the selection bar from
   *  sitting there describing rows this menu cannot act on. */
  function openEmptyMenu(e: MouseEvent): void {
    e.preventDefault()
    selection.clear()
    contextEntry = null
    menuOpen = false
    menuX = e.clientX
    menuY = e.clientY
    emptyMenuOpen = true
  }

  /** The actions that apply to whatever rows are being acted on, in one place.
   *
   *  Right-clicking a row and ticking its checkbox are two ways of saying the
   *  same thing, and they used to offer different menus: the selection bar had
   *  no "open in editor", no share-link management and no duplicate, so which
   *  gesture you happened to use decided what the app could do. Both surfaces
   *  render this list now, so they cannot drift apart again.
   *
   *  `target` is what the per-item predicates read. The handlers themselves
   *  already resolve `contextEntry ?? selected[0]`, so they work from
   *  either surface unchanged. "Select all" and "clear selection" are
   *  deliberately not here: they manage the selection rather than act on it,
   *  and there is nothing for them to do in a right-click menu. */
  /** Whether anything may be put into the directory on screen.
   *
   *  The listing says so (`dir_perms`), and nothing in the rows does: a folder
   *  of read-only files says nothing about creating one beside them. Upload,
   *  folder upload, "new folder" and the drop target all hang off this, so an
   *  account holding read alone is not offered four ways to be refused. The
   *  virtual root answers no perms at all, which is right: a share is not
   *  created by uploading into the list of them. */
  const canCreateHere = $derived(dir.perms.create)

  /** One list, one target set, rendered by both the right-click menu and the
   *  selection bar. They cannot show different actions because there is only
   *  one array; `openContextMenu` makes the right-clicked row the selection so
   *  that "the target set" is unambiguous for both. See `row-actions.ts` for
   *  the rules deciding what appears. */
  const actions = $derived(
    rowActions(
      selected,
      {
        openInEditor,
        download: downloadSelection,
        share: requestShare,
        rename: requestRename,
        transfer: requestTransfer,
        duplicate: () => duplicate(),
        remove: requestDelete
      },
      canCreateHere
    )
  )

  /** The entry a single-item action applies to.
   *
   *  The selection decides: `openContextMenu` makes the right-clicked row the
   *  selection, so the menu and the selection bar always aim at the same rows.
   *  `contextEntry` outlives the menu that set it, because the dialogs it
   *  opens keep reading it, so consulting it first meant an action started
   *  from the toolbar acted on whatever row had been right-clicked earlier:
   *  select another file, open its share links, and the previous file's links
   *  came up and a link created there was minted over the wrong path. It stays
   *  as the fallback for the one case the selection cannot answer, a menu
   *  raised while nothing is selected.
   */
  function actionTarget(): Entry | null {
    return selected[0] ?? contextEntry
  }

  // ── writes ──
  //
  // Each mutation invalidates what it changed, so none of these re-lists by
  // hand. What is on screen updates because the listing query was invalidated,
  // not because this page asked for it again.
  const mkdir = createMutation(() => mkdirMutation())
  const rename = createMutation(() => renameMutation())
  const remove = createMutation(() => deleteMutation())
  const move = createMutation(() => moveMutation())
  const copy = createMutation(() => copyMutation())
  const archive = createMutation(() => archiveTicketMutation())

  async function createFolder(name: string): Promise<void> {
    newFolderOpen = false
    try {
      await mkdir.mutateAsync({ parent: path, name })
    } catch (err) {
      snackbarMsg = describeApiError(err, t('browse.could_not_create_folder'))
    }
  }

  function requestRename(): void {
    const target = actionTarget()
    if (!target) return
    renameTarget = target
    menuOpen = false
    renameOpen = true
  }

  async function doRename(newName: string): Promise<void> {
    if (!renameTarget) return
    renameOpen = false
    try {
      await rename.mutateAsync({ path: joinPath(path, renameTarget.name), newName })
    } catch (err) {
      snackbarMsg = describeApiError(err, t('common.could_not_rename'))
    }
  }

  function requestDelete(): void {
    menuOpen = false
    if (selected.length === 0 && contextEntry) selection.only(contextEntry.name, entries.indexOf(contextEntry))
    deleteOpen = true
  }

  // ── download ──
  // Single file, plain share: `downloadPath` (format/download.ts) mints a
  // ticket (`POST /api/v1/files/download`) and navigates the browser to its
  // `url`. Multi-selection or a folder, plain share: `POST /api/fs/archive`
  // mints its own multi-path ticket the same way. Either way nothing is
  // fetched here: the browser's own download manager owns the transfer once
  // it is pointed at the ticket's URL, and no blob is ever held in the tab.
  //
  // An encrypted share has no server-built ticket to ask for at all (the
  // server holds no key), so both cases instead route through
  // `downloadEncryptedFile`/`downloadEncryptedFolder`
  // (crypto/download-sw.ts), which fetch the ciphertext and decrypt it in
  // the browser on the way to the same Service Worker download. A
  // `LockedSessionError` from either opens `UnlockShareDialog` and retries
  // the same action once the passphrase checks out.
  let unlockTarget = $state<{ salt: string; verifier: string; retry: () => void } | null>(null)

  function openUnlockFor(encryption: { salt: string; verifier: string }, retry: () => void): void {
    unlockTarget = { salt: encryption.salt, verifier: encryption.verifier, retry }
  }

  function encryptedDownloadFailureMessage(err: unknown, fallback: string): string {
    if (err instanceof FileTooLargeError) return t('browse.download_too_large_to_buffer')
    return describeApiError(err, fallback)
  }

  async function downloadAsArchive(targets: Entry[]): Promise<void> {
    if (targets.length === 0) return
    const encryption = await encryptionForLabel(shareLabelOf(path))
    if (encryption) {
      try {
        for (const target of targets) {
          if (target.kind === 'dir') {
            await downloadEncryptedFolder(target.path)
          } else {
            await downloadEncryptedFile(target)
          }
        }
      } catch (err) {
        if (err instanceof LockedSessionError) {
          openUnlockFor(encryption, () => void downloadAsArchive(targets))
        } else {
          snackbarMsg = encryptedDownloadFailureMessage(err, t('browse.zip_download_failed'))
        }
      }
      return
    }
    const paths = targets.map((e) => joinPath(path, e.name))
    const filename = targets.length === 1 ? `${targets[0].name}.zip` : 'archive.zip'
    try {
      const ticket = await archive.mutateAsync({ paths, name: filename })
      triggerUrlDownload(ticket.url, ticket.name)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'rate.limited') {
        // the server caps concurrent archive streams
        // and 429s over that -- a real state to show, not a silent hang.
        snackbarMsg = t('browse.too_many_zip_downloads_at')
      } else {
        snackbarMsg = describeApiError(err, t('browse.zip_download_failed'))
      }
    }
  }

  async function downloadEntry(entry: Entry): Promise<void> {
    if (entry.kind === 'dir') {
      await downloadAsArchive([entry])
      return
    }
    try {
      await downloadPath(entry.path)
    } catch (err) {
      if (err instanceof LockedSessionError) {
        const encryption = await encryptionForLabel(shareLabelOf(entry.path))
        if (encryption) {
          openUnlockFor(encryption, () => void downloadEntry(entry))
          return
        }
      }
      snackbarMsg = encryptedDownloadFailureMessage(err, t('browse.download_failed'))
    }
  }

  function downloadSelection(): void {
    menuOpen = false
    const sel = selected.length > 0 ? selected : contextEntry ? [contextEntry] : []
    if (sel.length === 0) return
    if (sel.length === 1 && sel[0].kind === 'file') {
      void downloadEntry(sel[0])
      return
    }
    void downloadAsArchive(sel)
  }

  // ── share links ──
  function requestShare(): void {
    const target = actionTarget()
    if (!target) return
    shareTarget = target
    menuOpen = false
    shareOpen = true
  }

  async function doDelete(): Promise<void> {
    deleteOpen = false
    const targets = selected.length > 0 ? selected : contextEntry ? [contextEntry] : []
    const paths = targets.map((e) => joinPath(path, e.name))
    selection.clear()
    try {
      // The server answers with one result per path, not a job.
      const { results } = await remove.mutateAsync(paths)
      const failed = results.filter((r) => !r.ok)
      if (failed.length > 0) {
        const bKey = batchErrorKey(failed[0].error)
        snackbarMsg = bKey ? t(bKey.key, bKey.params) : t('browse.delete_failed')
      }
    } catch (err) {
      snackbarMsg = describeApiError(err, t('browse.delete_failed'))
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
    const targets = selected.length > 0 ? selected : contextEntry ? [contextEntry] : []
    if (targets.length === 0) return
    destSources = targets.map((e) => joinPath(path, e.name))
    destOpen = true
  }

  function onDestinationPicked(dest: string, mode: 'move' | 'copy'): void {
    destOpen = false
    void transfer(destSources, dest, mode, 'fail')
  }

  /** Duplicate is a copy into the folder the entry is already in, so it always
   *  collides with itself. `rename` rather than `fail`: asking "this name is
   *  taken, what now" about a name the user did not choose is a dialog with
   *  one sensible answer, and the point of the action is a second copy. */
  function duplicate(onConflict: OnConflict = 'rename'): void {
    // The same target rule every other action here follows: the right-clicked
    // row when there is one, the selection otherwise. Reading only
    // `contextEntry` meant the selection bar's own button did nothing on the
    // menu path and sent an empty name on the bar path, which the server took
    // as the folder itself and duplicated into a file called " (2)".
    const targets = selected.length > 0 ? selected : contextEntry ? [contextEntry] : []
    if (targets.length === 0) return
    // The one menu action that forgot this. `Menu` only dismisses on a click
    // *outside* itself, so an item's own click leaves it open -- and duplicate
    // is the one that raises a modal on the same click, so the stale menu sat
    // opaque next to the conflict dialog with its labels painted over the file
    // rows behind it.
    menuOpen = false
    // Captured now, not read back off `contextEntry` once the job settles --
    // the context menu can be pointed at a different entry by then, since
    // this doesn't await the job.
    const paths = targets.map((e) => joinPath(path, e.name))
    void transfer(paths, path, 'copy', onConflict)
  }

  /** Says how many items were left alone because their destination was taken.
   *  A skip is a success with nothing written, so without this the operation
   *  reports as done and the files are silently not there. */
  function noteSkipped(results: BatchItemResult[]): void {
    const skipped = results.filter((r) => r.skipped).length
    if (skipped > 0) snackbarMsg = t('browse.items_skipped_name_taken', { count: skipped })
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

    /** The first refusal in a batch decides the message. A conflict opens the
     *  dialog instead, because it has three answers rather than one. */
    function reportBatch(results: BatchItemResult[]): 'conflict' | 'failed' | 'ok' {
      const conflicted = results.find((r) => r.error?.code === 'fs.conflict')
      if (conflicted) {
        // The first conflicting item names the dialog. The answer then applies
        // to the whole batch, because `on_conflict` is a property of the
        // request and not of an item: asking once per name would mean one
        // dialog per file for a selection that mostly collides.
        conflictName = baseName(conflicted.path)
        conflictOpen = true
        return 'conflict'
      }
      const refused = results.find((r) => !r.ok)
      if (refused) {
        const bKey = batchErrorKey(refused.error)
        snackbarMsg =
          refused.error?.code === 'quota.exceeded' ? quotaMsg : bKey ? t(bKey.key, bKey.params) : failMsg
        return 'failed'
      }
      return 'ok'
    }

    try {
      const vars = { paths, dest, onConflict }
      // A move finishes in the request: it is a rename, and only the
      // cross-device case rewrites bytes, which the server reports per item.
      // A copy large enough becomes a durable job, because it always rewrites
      // them; the destination is checked before that job exists, so a
      // conflict, a denial or a quota refusal is in this response rather than
      // minutes later.
      const { results, job } =
        mode === 'move' ? { ...(await move.mutateAsync(vars)), job: undefined } : await copy.mutateAsync(vars)
      if (reportBatch(results) !== 'ok') return
      conflictOpen = false
      if (mode === 'move') selection.clear()
      noteSkipped(results)
      // A job reports its own progress and its own failure in the tray. What
      // it writes arrives here as an invalidation over the WebSocket, so
      // there is nothing to wait for on this screen.
      if (job !== undefined) jobTray.track(job)
    } catch (err) {
      snackbarMsg =
        err instanceof ApiError && err.code === 'quota.exceeded' ? quotaMsg : describeApiError(err, failMsg)
    }
  }

  function onUploadClick(): void {
    fileInputEl?.click()
  }
  function getUniqueUploadName(name: string, isTaken: (candidate: string) => boolean): string {
    if (!isTaken(name)) return name
    const dot = name.lastIndexOf('.')
    const base = dot > 0 ? name.slice(0, dot) : name
    const ext = dot > 0 ? name.slice(dot) : ''
    let count = 1
    while (isTaken(`${base} (${count})${ext}`)) {
      count++
    }
    return `${base} (${count})${ext}`
  }

  function handleUploadFiles(fileList: FileList | File[]): void {
    const rawFiles = Array.from(fileList)
    if (rawFiles.length === 0) return

    const conflicts = rawFiles.filter((f) => names.has(f.name))
    if (conflicts.length === 0) {
      addFiles(rawFiles, path)
      return
    }

    conflictName = conflicts.length === 1 ? conflicts[0].name : `${conflicts[0].name} (+${conflicts.length - 1})`
    conflictRetry = (action: OnConflict) => {
      conflictOpen = false
      if (action === 'overwrite') {
        addFiles(rawFiles, path)
      } else if (action === 'skip') {
        const conflictNames = new Set(conflicts.map((c) => c.name))
        const remaining = rawFiles.filter((f) => !conflictNames.has(f.name))
        if (remaining.length > 0) addFiles(remaining, path)
      } else if (action === 'rename') {
        const taken = new Set<string>()
        const renamed = rawFiles.map((f) => {
          if (!names.has(f.name)) return f
          const newName = getUniqueUploadName(f.name, (n) => names.has(n) || taken.has(n))
          taken.add(newName)
          return new File([f], newName, { type: f.type, lastModified: f.lastModified })
        })
        addFiles(renamed, path)
      }
    }
    conflictOpen = true
  }

  function handleUploadEntries(picked: { file: File; relativePath: string }[]): void {
    if (picked.length === 0) return
    const conflicts = picked.filter((e) => !e.relativePath && names.has(e.file.name))
    if (conflicts.length === 0) {
      addEntries(picked, path)
      return
    }

    conflictName = conflicts.length === 1 ? conflicts[0].file.name : `${conflicts[0].file.name} (+${conflicts.length - 1})`
    conflictRetry = (action: OnConflict) => {
      conflictOpen = false
      if (action === 'overwrite') {
        addEntries(picked, path)
      } else if (action === 'skip') {
        const conflictNames = new Set(conflicts.map((c) => c.file.name))
        const remaining = picked.filter((e) => e.relativePath || !conflictNames.has(e.file.name))
        if (remaining.length > 0) addEntries(remaining, path)
      } else if (action === 'rename') {
        const taken = new Set<string>()
        const renamed = picked.map((e) => {
          if (e.relativePath || !names.has(e.file.name)) return e
          const newName = getUniqueUploadName(e.file.name, (n) => names.has(n) || taken.has(n))
          taken.add(newName)
          return {
            file: new File([e.file], newName, { type: e.file.type, lastModified: e.file.lastModified }),
            relativePath: e.relativePath
          }
        })
        addEntries(renamed, path)
      }
    }
    conflictOpen = true
  }

  function onUploadFilesChange(e: Event): void {
    const input = e.currentTarget as HTMLInputElement
    if (input.files) handleUploadFiles(input.files)
    input.value = ''
  }

  async function onUploadFolderClick(): Promise<void> {
    if (supportsDirectoryPicker()) {
      try {
        const files = await pickDirectory()
        handleUploadEntries(files)
      } catch {
        /* user canceled the picker */
      }
    } else {
      dirInputEl?.click()
    }
  }
  function onDirInputChange(e: Event): void {
    const input = e.currentTarget as HTMLInputElement
    handleUploadEntries(filesFromWebkitDirectoryInput(input))
    input.value = ''
  }

  async function onDrop(e: DragEvent): Promise<void> {
    e.preventDefault()
    dragOver = false
    if (!e.dataTransfer) return
    // A drop is an upload, so it needs the same right the upload button does.
    // Without this the one way in stayed open on a folder the account may
    // only read, and every dropped file failed in the tray.
    if (!canCreateHere) {
      snackbarMsg = t('error.acl_denied')
      return
    }
    try {
      const files = await pickedFilesFromDataTransfer(e.dataTransfer)
      handleUploadEntries(files)
    } catch {
      snackbarMsg = t('upload.could_not_start_upload')
    }
  }

  function focusSearch(): void {
    searchOpen = true
    queueMicrotask(() => searchInputEl?.focus())
  }
  function closeSearch(): void {
    searchOpen = false
    searchQuery = ''
    searchResults = []
    searchRan = false
    searchTruncated = false
    searchCancel?.()
  }
  function onSearchInput(): void {
    searchCancel?.()
    searchResults = []
    searchRan = false
    searchTruncated = false
    if (!searchQuery.trim()) return
    searchRan = true
    searchTruncated = false
    searchCancel = api.searchStream(
      searchQuery,
      (hit) => {
        searchResults = [...searchResults, hit].slice(0, 100)
      },
      (done) => {
        searchTruncated = done.truncated
      }
    )
  }
  function onSearchResultClick(hit: { path: string; entry: Entry }): void {
    closeSearch()
    // A folder opens; a file opens the folder holding it, since there is
    // nowhere else to land. `hit.path` is a full `/{label}/sub/path` virtual
    // path: the wire shape carries the share separately and `toSearchHit`
    // rejoins the two, which is what a result for `Share/Movie/01` needs to
    // stop navigating to `/Movie/01` and 404ing.
    goto(`/b${hit.entry.kind === 'dir' ? normalizePath(hit.path) : parentOf(hit.path)}`)
  }

  function toggleView(): void {
    view.setMode(view.state.mode === 'list' ? 'grid' : 'list')
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
    view.setDensity(order[(order.indexOf(view.state.density) + 1) % order.length])
  }
  function toggleTree(): void {
    treeOpen = !treeOpen
  }
  // One key per density, each named after the density it labels. The three
  // used to be spelled `browse.compact` / `browse.comfortable` /
  // `common.fair`, shifted by one: `spacious` rendered "Comfortable" and
  // `comfortable` borrowed the password-strength word and rendered "Fair".
  // Korean hid it (좁게 / 보통 / 넓게 happen to line up), English did not.
  function densityLabel(d: 'compact' | 'comfortable' | 'spacious'): string {
    return d === 'compact'
      ? t('browse.compact')
      : d === 'spacious'
        ? t('browse.spacious')
        : t('browse.comfortable')
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

  // The sort menu. Its own anchor rather than a submenu of the overflow one,
  // because it is a thing people change often enough to want one click away.
  let sortOpen = $state(false)
  let sortLeft = $state(0)
  let sortTop = $state(0)
  let sortMenuEl: HTMLDivElement | undefined = $state()
  let sortTriggerEl: HTMLElement | null = null

  const sortKeys: { key: SortKey; label: () => string }[] = [
    { key: 'name', label: () => t('browse.sort_by_name') },
    { key: 'size', label: () => t('browse.sort_by_size') },
    { key: 'mtime', label: () => t('browse.sort_by_modified') },
    { key: 'kind', label: () => t('browse.sort_by_kind') }
  ]

  function openSort(e: MouseEvent): void {
    if (sortOpen) {
      closeSort()
      return
    }
    e.stopPropagation()
    const btn = e.currentTarget as HTMLElement
    sortTriggerEl = btn
    const rect = btn.getBoundingClientRect()
    sortLeft = rect.right
    sortTop = rect.bottom + 4
    sortOpen = true
    queueMicrotask(() => sortMenuEl?.querySelector<HTMLButtonElement>('button')?.focus())
  }

  function closeSort(): void {
    sortOpen = false
    sortTriggerEl?.focus()
    sortTriggerEl = null
  }

  function sortKeyLabel(key: SortKey): string {
    return sortKeys.find((s) => s.key === key)?.label() ?? key
  }

  // Picking the key already in use flips the direction, which is what a file
  // manager's column header does and what makes one menu enough for both.
  function chooseSort(key: SortKey): void {
    const order: Order = sort.key === key && sort.order === 'asc' ? 'desc' : 'asc'
    view.setSort(key, order)
    closeSort()
  }

  function openOverflow(e: MouseEvent): void {
    if (overflowOpen) {
      closeOverflow()
      return
    }
    e.stopPropagation()
    const btn = e.currentTarget as HTMLElement
    overflowTriggerEl = btn
    const rect = btn.getBoundingClientRect()
    // Pass button rect.right with align="end" so Menu right-aligns directly to this coordinate.
    overflowLeft = rect.right
    overflowTop = rect.bottom + 4
    overflowOpen = true
    // Move focus into the menu the same way `focusSearch` already does for
    // the search field: a `queueMicrotask` so it runs after the `{#if}`
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
  // arrow-key navigable, and nothing upstream (Menu.svelte) provides that:
  // it was not needed until items with genuine keyboard users (not just a
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
    // The selection decides, the right-clicked row is the fallback: the same
    // rule `actionTarget` states once for every single-item action here.
    const target = actionTarget()
    if (!target || target.kind === 'dir') return
    menuOpen = false
    goto(`/edit${joinPath(path, target.name)}`)
  }
</script>

<svelte:head>
  <title>{crumbs.at(-1)?.label ?? 'Stowcloud'} - Stowcloud</title>
</svelte:head>

<div
  class="sc-browse"
  role="region"
  aria-label={t('browse.file_browser')}
  ondragover={(e) => {
    e.preventDefault()
    // No highlight where nothing may be created: the outline and its "drop to
    // upload" overlay are a promise the server would break.
    dragOver = canCreateHere
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
  <!-- No `inert` here any more. It was correct while the selection bar covered
       this element (a covered control must not stay tabbable) and became the
       opposite of correct once the bar moved to its own row: visible controls
       that cannot be clicked or tabbed to are worse than hidden ones. -->
  <header
    class="sc-browse__toolbar"
    class:sc-browse__toolbar--compact={ui.state.compact}
  >
    <div class="sc-browse__title">
      <Breadcrumb {crumbs} onnavigate={onNavigate} />
      {#if rootShared}
        <span class="sc-browse__external-badge">
          <Icon icon={icons.warning} size={14} />
          {t('common.shared_with_other_services')}
        </span>
      {/if}
      {#if rootBroken}
        <!-- The badge is on the folder rather than only in the failed
             listing, because the folder is still navigable from the root
             list and the reason has to travel with it. -->
        <span class="sc-browse__broken-badge" role="status">
          <Icon icon={icons.warning} size={14} />
          {t('browse.this_folder_is_unavailable')}
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
        {#if !ui.state.compact}
          <IconButton label={t('common.refresh')} onclick={refresh}><Icon icon={icons.refresh} /></IconButton>
          <IconButton label={treeOpen ? t('browse.hide_folder_tree') : t('browse.show_folder_tree')} selected={treeOpen} onclick={toggleTree}>
            <Icon icon={icons['folder-tree']} />
          </IconButton>
        {/if}
        <!-- Its own control, not an overflow-menu row: switching between grid
             and list is a frequent, reversible view preference, and burying it
             two clicks deep next to destructive one-way actions read as if it
             were one of them. The icon shows what you would switch *to*, which
             is what the label says too. -->
        <IconButton
          label={view.state.mode === 'list' ? t('browse.grid_view') : t('browse.list_view')}
          onclick={toggleView}
        >
          <Icon icon={view.state.mode === 'list' ? icons.grid : icons.list} />
        </IconButton>
        <IconButton
          label={ui.state.details ? t('details.hide') : t('details.show')}
          expanded={ui.state.details}
          onclick={() => ui.setDetails(!ui.state.details)}
        >
          <Icon icon={icons.info} />
        </IconButton>
        <IconButton
          label={t('browse.sort_by', { key: sortKeyLabel(sort.key) })}
          selected={sortOpen}
          expanded={sortOpen}
          onclick={openSort}
        >
          <Icon icon={icons.sort} />
        </IconButton>
        <IconButton label={t('browse.more')} selected={overflowOpen} expanded={overflowOpen} onclick={openOverflow}><Icon icon={icons['more-vert']} /></IconButton>
        {#if !ui.state.compact && canCreateHere}
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
    Floating, over the bottom of the viewport, in no layout flow at all.

    Two earlier placements each broke something. Absolutely positioned over the
    toolbar, it took the breadcrumb and the New folder / Upload buttons with
    it, so selecting one file hid where you were. As its own row under the
    toolbar, it pushed the list down by its own height the moment the first
    click landed. The second click of a double click then arrived one row
    higher than the first, so `dblclick` fired on their common ancestor instead
    of on a row and opening quietly did nothing. Compensating with `scrollBy`
    could not fix that: at the top of the page, where a first click most often
    lands, there is no scroll to give back.

    Taking it out of flow removes the shift instead of correcting it. Nothing
    above or below moves, at any scroll position. The list reserves room at its
    own bottom so the last rows can still be scrolled clear of the bar.
  -->
  {#if selected.length > 0}
  <div class="sc-browse__selection-bar">
    <div class="sc-browse__selection-bar-inner">
      <!-- Ctrl/Cmd+A already covers Select all (FileTable.svelte's onKeydown),
           but that needs a physical keyboard, and a phone has none, so it has
           to be reachable here too.
           Icons at every width, one layout. The text version needed 642px of
           a 390px bar, so at compact width three of the five buttons sat
           off-screen behind a horizontal scroll nothing announced; that is
           what first split this in two. Keeping the split meant two orders,
           two sets of affordances and one of them exercised only at a width
           nobody develops at. `IconButton` shows the same label on hover and
           on keyboard focus, so nothing is lost by dropping the text.
           Clear selection leads as the close affordance, matching how every
           other modal-ish surface in this app dismisses. -->
      <IconButton label={t('browse.clear_selection')} onclick={() => selection.clear()}>
        <Icon icon={icons.close} />
      </IconButton>
      <span class="sc-browse__selection-count">
        {#if ui.state.compact}
          {t('common.item_count', { count: selected.length })}
        {:else if folderSizes.pending}
          <!-- A folder's total needs a walk, and a cold one takes long enough
               that a bare count would read as the answer. -->
          {t('common.item_count', { count: selected.length })}
          <span class="sc-browse__selection-measuring" role="status">{t('details.measuring')}</span>
        {:else if folderSizes.failed}
          {t('common.item_count', { count: selected.length })}
        {:else}
          {t('browse.selected', { count: selected.length, size: formatBytes(selectionBytes) })}
        {/if}
      </span>
      <span class="sc-browse__selection-gap"></span>
      <IconButton label={t('browse.select_all')} onclick={() => selection.all(entries.map((e) => e.name))}>
        <Icon icon={icons.check} />
      </IconButton>
      {#each actions as action (action.key)}
        <IconButton label={action.label} onclick={action.run}><Icon icon={action.icon} /></IconButton>
      {/each}
    </div>
  </div>
  {/if}
  </div>

  <Menu open={sortOpen} onclose={closeSort} x={sortLeft} y={sortTop} align="end">
    <div bind:this={sortMenuEl} role="none">
      {#each sortKeys as s (s.key)}
        <MenuItem onclick={() => chooseSort(s.key)}>
          {sort.key === s.key
            ? t('browse.sort_selected', {
                label: s.label(),
                direction: sort.order === 'asc' ? t('browse.sort_ascending') : t('browse.sort_descending')
              })
            : s.label()}
        </MenuItem>
      {/each}
    </div>
  </Menu>

  <Menu open={overflowOpen} onclose={closeOverflow} x={overflowLeft} y={overflowTop} align="end">
      <div bind:this={overflowMenuEl} role="none" onkeydown={onOverflowKeydown}>
        {#if ui.state.compact}
          <MenuItem onclick={() => { refresh(); closeOverflow() }}>{t('common.refresh')}</MenuItem>
          <MenuItem onclick={() => { toggleTree(); closeOverflow() }}>
            {treeOpen ? t('browse.hide_folder_tree') : t('browse.show_folder_tree')}
          </MenuItem>
        {/if}
        <MenuItem onclick={() => { cycleDensity(); closeOverflow() }}>
          {t('browse.density', { density: densityLabel(view.state.density) })}
        </MenuItem>
        {#if ui.state.compact && canCreateHere}
          <MenuItem onclick={() => { onUploadFolderClick(); closeOverflow() }}>{t('browse.upload_folder')}</MenuItem>
          <MenuItem onclick={() => { newFolderOpen = true; closeOverflow() }}>{t('common.new_folder')}</MenuItem>
        {/if}
        <MenuItem onclick={() => { closeOverflow(); goto('/trash') }}>{t('browse.open_trash')}</MenuItem>
    </div>
  </Menu>

  <!-- `searchRan`, not just a non-empty box: the query runs on Enter, so
       between the first keystroke and that Enter there is a typed word and
       an empty result list, which rendered as "No results" for a search
       nobody had asked for yet. It said the opposite of the truth about
       files that were about to match. -->
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
        <li class="sc-browse__search-empty">
          {searchRan ? t('browse.no_results') : t('browse.press_enter_to_search')}
        </li>
      {/each}
      {#if searchTruncated && searchResults.length > 0}
        <li class="sc-browse__search-empty">{t('browse.search_stopped_early')}</li>
      {/if}
    </ul>
  {/if}

  <div class="sc-browse__content">
    {#if treeOpen}
      <FileTree
        currentPath={path}
        onnavigate={(p) => goto(`/b${p}`)}
        overlay={true}
        onclose={() => (treeOpen = false)}
      />
    {/if}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- The click here only clears the selection, and the keyboard already has
         that: Escape on the list or the grid, which is where a keyboard user's
         focus is. A keydown handler on this div would never fire, since it is
         not focusable and never will be. -->

    <!-- The blank-space menu hangs here, not on the table/grid itself: this
         element is `flex: 1`, so it covers both the gaps between rows and the
         empty area left when a short listing does not fill the pane. Rows stop
         the event before it reaches this (`FileTable`/`FileGrid`), so a
         right-click is answered by exactly one of the two menus. -->
    <div
      class="sc-browse__table-wrap"
      class:sc-browse__table-wrap--dragover={dragOver}
      class:sc-browse__table-wrap--marquee={marqueeRect !== null}
      oncontextmenu={openEmptyMenu}
      onpointerdown={onMarqueePointerDown}
      onclick={onEmptyAreaClick}
    >
      {#if noShares}
        <!-- Not an empty folder: there is no folder. A deployment serves
             nothing until somebody says what to serve, and this is the only
             screen the person who can say it lands on. -->
        <div class="sc-browse__nothing">
          <h2 class="sc-browse__nothing-title">{t('browse.nothing_here')}</h2>
          {#if session.data?.user.is_admin}
            <p class="sc-browse__nothing-hint">{t('browse.press_this_button_to_set_up_your_first_folder')}</p>
            <Button variant="filled" onclick={() => goto('/admin#shares')}>
              {#snippet icon()}<Icon icon={icons.add} size={18} />{/snippet}
              {t('common.add_folder')}
            </Button>
          {:else}
            <!-- Nothing is granted to this account, which an administrator
                 fixes. Offering the button would be offering a screen this
                 account is refused from. -->
            <p class="sc-browse__nothing-hint">{t('browse.ask_an_administrator_for_a_folder')}</p>
          {/if}
        </div>
      {:else if listing.isPending}
        <div class="sc-browse__loading"><ProgressCircular /></div>
      {:else if listing.error}
        <!-- The server's own answer in the reader's language, not the wire
             message. A folder whose disk did not come back says so and names
             the folder, which is a different thing to act on from a path that
             is genuinely not there. -->
        <p class="sc-browse__error" role="alert">
          {describeApiError(listing.error, t('browse.this_folder_could_not_be_opened'))}
        </p>
      {:else if view.state.mode === 'grid'}
        <div
          class="sc-browse__view"
          in:fade={{ duration: viewSwitchDuration() }}
          out:fade={{ duration: viewSwitchDuration() }}
        >
          <FileGrid
            bind:this={gridView}
            {entries}
            total={dir.total}
            dirs={dir.dirs}
            loading={listing.isPending}
            loadingMore={listing.isFetchingNextPage}
            perms={dir.perms}
            {requestMore}
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
            bind:this={tableView}
            {entries}
            total={dir.total}
            dirs={dir.dirs}
            loading={listing.isPending}
            loadingMore={listing.isFetchingNextPage}
            perms={dir.perms}
            {requestMore}
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
    <!-- Outside the wrap and `position: fixed`, so it is drawn in viewport
         coordinates and cannot be clipped by anything the drag passes over. -->
    {#if marqueeRect}
      <div
        class="sc-browse__marquee"
        aria-hidden="true"
        style:left="{marqueeRect.left - scrollXNow}px"
        style:top="{marqueeRect.top - scrollYNow}px"
        style:width="{marqueeRect.right - marqueeRect.left}px"
        style:height="{marqueeRect.bottom - marqueeRect.top}px"
      ></div>
    {/if}
    {#if ui.state.details}
      <DetailsPanel {path} {selected} total={dir.total} dirs={dir.dirs} onclose={() => ui.setDetails(false)} />
    {/if}
  </div>

  {#if ui.state.compact && selected.length === 0 && canCreateHere}
    <!--
      The one primary action a phone-width toolbar gets a dedicated control
      for. Upload over New folder: a folder is created rarely,
      once per project or session; bringing in new files (not least straight
      from the camera roll) is the action a phone visit to a file browser is
      most often *for*. Upload folder was never a contender here -- directory
      picker support on a mobile browser is patchy at best, so it's exactly the
      kind of secondary action More exists for.

      Gone while something is selected: the selection bar now floats over the
      same corner, and what you do to the files you have picked is on it.
      Uploading is not part of that, and half a button behind a bar is worse
      than no button.
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
    {#each actions as action (action.key)}
      <MenuItem onclick={action.run}>{action.label}</MenuItem>
    {/each}
</Menu>

<!-- Blank-space menu. Same component and the same `menuX`/`menuY` the row menu
     positions with; only the contents differ, because there is no target row to
     act on. The three handlers and the three labels are the toolbar's own, not
     copies. -->
<Menu open={emptyMenuOpen} onclose={() => (emptyMenuOpen = false)} x={menuX} y={menuY}>
    {#if canCreateHere}
      <MenuItem onclick={() => { emptyMenuOpen = false; newFolderOpen = true }}>{t('common.new_folder')}</MenuItem>
      <MenuItem onclick={() => { emptyMenuOpen = false; onUploadClick() }}>{t('common.upload')}</MenuItem>
      <MenuItem onclick={() => { emptyMenuOpen = false; onUploadFolderClick() }}>{t('browse.upload_folder')}</MenuItem>
    {/if}
</Menu>

<NewFolderDialog open={newFolderOpen} onclose={() => (newFolderOpen = false)} oncreate={createFolder} />
<RenameDialog
  open={renameOpen}
  currentName={renameTarget?.name ?? ''}
  onclose={() => (renameOpen = false)}
  onrename={doRename}
/>
<DeleteDialog
  open={deleteOpen}
  count={selected.length || (contextEntry ? 1 : 0)}
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
  onkeepboth={() => conflictRetry?.('rename')}
  onoverwrite={() => conflictRetry?.('overwrite')}
  onskip={() => conflictRetry?.('skip')}
/>
<UnlockShareDialog
  open={unlockTarget !== null}
  salt={unlockTarget?.salt ?? ''}
  verifier={unlockTarget?.verifier ?? ''}
  onunlock={() => {
    const retry = unlockTarget?.retry
    unlockTarget = null
    retry?.()
  }}
  onclose={() => (unlockTarget = null)}
/>
<PreviewDialog
  open={previewOpen && previewEntry !== null}
  entry={previewEntry}
  path={previewPath}
  hasPrev={hasPreviewNeighbour(-1)}
  hasNext={hasPreviewNeighbour(1)}
  onclose={() => (previewOpen = false)}
  onprev={() => stepPreview(-1)}
  onnext={() => stepPreview(1)}
  ondownload={(e) => void downloadEntry(e)}
  onedit={(e) => goto(`/edit${joinPath(path, e.name)}`)}
/>

{#if shareTarget}
  <ShareManageDialog
    open={shareOpen}
    path={joinPath(path, shareTarget.name)}
    targetName={shareTarget.name}
    targetIsDir={shareTarget.kind === 'dir'}
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
  /* The error container, plus the sentence: a badge distinguished only by
     colour says nothing to a reader who cannot see the difference. */
  .sc-browse__broken-badge {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 24px;
    padding-inline: 8px;
    border-radius: var(--m3-shape-full);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
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
  .sc-browse__table-wrap--marquee {
    /* Only while a drag is running: dragging over text would otherwise
       highlight it, and the browser's own drag-select fights the rectangle. */
    user-select: none;
    cursor: crosshair;
  }
  .sc-browse__marquee {
    position: fixed;
    z-index: 15;
    pointer-events: none;
    border: 1px solid var(--m3c-primary);
    background: color-mix(in srgb, var(--m3c-primary) 18%, transparent);
    border-radius: 2px;
  }
  .sc-browse__selection-bar {
    /* Fixed, centred on the bottom edge, so it occupies no layout space and
       nothing reflows when it appears. See the markup for why that matters
       more than where it sits.

       Clear of the `NavigationBar`, which is itself fixed to the bottom at
       compact width, and of the home indicator below that. `--sc-selection-
       bar-gap` is the same number the list reserves, kept here so the two
       cannot drift apart. */
    position: fixed;
    left: 50%;
    transform: translateX(-50%);
    bottom: calc(16px + var(--sc-selection-bar-offset, 0px) + env(safe-area-inset-bottom, 0px));
    z-index: 20;
    max-width: calc(100vw - 32px);
    border-radius: var(--m3-shape-large);
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
    box-shadow: var(--m3-util-elevation-3, 0 4px 12px rgb(0 0 0 / 0.3));
  }
  :global(.sc-app-shell--compact) .sc-browse__selection-bar {
    --sc-selection-bar-offset: var(--sc-nav-bar-height);
  }
  .sc-browse__selection-bar-inner {
    display: flex;
    align-items: center;
    /* The icons are already 48px touch targets, so anything wider than this
       reads as gaps rather than as a group. */
    gap: 4px;
    min-height: 48px;
    padding-inline: 8px;
    /* Nothing here shrinks or wraps (a half-rendered destructive button is
       worse than a scroll), so the row scrolls if a long enough count string
       ever outgrows it. */
    overflow-x: auto;
    /* ...and the tooltips are `position: fixed`, so scrolling this box clips
       them only if it is also the containing block. It is not. */
  }
  .sc-browse__selection-gap {
    /* Separates the count from the actions. It no longer stretches: the bar is
       only as wide as its contents now, so a growing gap would just push the
       two ends apart across an empty middle. */
    flex: 0 0 8px;
  }
  .sc-browse__selection-count {
    flex-shrink: 0;
    white-space: nowrap;
  }
  .sc-browse__selection-measuring {
    margin-inline-start: 0.5ch;
    opacity: 0.7;
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
  .sc-browse__nothing {
    display: flex;
    flex: 1;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 16px;
    padding: 24px;
    text-align: center;
  }
  .sc-browse__nothing-title {
    margin: 0;
    @apply --m3-headline-small;
  }
  .sc-browse__nothing-hint {
    max-width: 360px;
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-medium;
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
