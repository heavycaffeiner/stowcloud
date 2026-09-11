import { useInfiniteQuery, useMutation, useQueries, useQuery } from '@tanstack/react-query'
import type { DragEvent, MouseEvent as ReactMouseEvent } from 'react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { ApiError, type BatchItemResult, type BatchResult, type Entry, type OnConflict } from '../../lib/api/client'
import type { CopyResult, ShareEncryption } from '../../lib/api/types'
import { baseName, joinPath, normalizePath } from '../../lib/api/path-utils'
import { batchErrorKey, describeApiError } from '../../lib/api/error-text'
import { encryptionForLabel, shareLabelOf } from '../../lib/crypto/encrypted-shares'
import { FileTooLargeError, isUnlocked, LockedSessionError } from '../../lib/crypto/e2ee'
import { downloadEncryptedFile, downloadEncryptedFolder } from '../../lib/crypto/download-sw'
import { downloadPath, triggerUrlDownload } from '../../lib/format/download'
import { formatBytes } from '../../lib/format/bytes'
import { useI18n } from '../../lib/i18n/use-i18n'
import { dirListQuery, dirViewOf, folderSizeQuery, mkdirMutation, renameMutation, deleteMutation, moveMutation, copyMutation, archiveTicketMutation, shareEncryptionQuery } from '../../lib/query/files'
import { sessionQuery } from '../../lib/query/session'
import { useStore } from '../../lib/store/use-store'
import { selection } from '../../lib/store/selection.store'
import { ui } from '../../lib/store/ui.store'
import { view } from '../../lib/store/view.store'
import { jobTray } from '../../lib/store/jobs.store'
import { openSearch, searchTarget } from '../../lib/store/search.store'
import { addEntries, addFiles } from '../../lib/upload/queue'
import { filesFromWebkitDirectoryInput, pickedFilesFromDataTransfer, pickDirectory, supportsDirectoryPicker } from '../../lib/upload/directory-picker'
import { rowActions } from '../../lib/ui/row-actions'
import { FileTable, type FileViewHandle } from '../../lib/ui/FileTable'
import { FileGrid, type FileGridHandle } from '../../lib/ui/FileGrid'
import { FileTree } from '../../lib/ui/FileTree'
import { DetailsPanel } from '../../lib/ui/DetailsPanel'
import { NewFolderDialog } from '../../lib/ui/NewFolderDialog'
import { RenameDialog } from '../../lib/ui/RenameDialog'
import { DeleteDialog } from '../../lib/ui/DeleteDialog'
import { ConflictDialog } from '../../lib/ui/ConflictDialog'
import { DestinationPickerDialog } from '../../lib/ui/DestinationPickerDialog'
import { UnlockShareDialog } from '../../lib/ui/UnlockShareDialog'
import { PreviewDialog } from '../../lib/ui/PreviewDialog'
import { Button } from '../../lib/ui/Button'
import { icons } from '../../lib/icons'
import { Icon } from '../../lib/ui/Icon'
import { IconButton } from '../../lib/ui/IconButton'
import { ShareManageDialog } from '../../lib/ui/ShareManageDialog'
import { Menu } from '../../lib/ui/Menu'
import { Breadcrumb } from '../../lib/ui/Breadcrumb'
import { autoScrollStep, movedFar, rectBetween, type Rect } from '../../lib/ui/marquee'
import { useDocumentTitle } from '../use-document-title'
import './browse.css'
import '../../lib/ui/browse-ui.css'

type OperationKind = 'delete' | 'move' | 'copy'
type UnlockTarget = { salt: string; verifier: string; retry: () => void }
const ACTION_ICON_NAMES: Record<string, string> = { edit: 'edit-document', download: 'download', share: 'link', rename: 'rename', transfer: 'move', duplicate: 'copy', delete: 'delete' }

export function BrowsePage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const location = useLocation()
  const params = useParams()
  const path = normalizePath(`/${params['*'] ?? ''}`)
  const session = useQuery(sessionQuery())
  const compact = useStore(ui, (state) => state.compact)
  const details = useStore(ui, (state) => state.details)
  const mode = useStore(view, (state) => state.mode)
  const density = useStore(view, (state) => state.density)
  const sortKey = useStore(view, (state) => state.sortKey)
  const sortOrder = useStore(view, (state) => state.sortOrder)
  const selectedNames = useStore(selection, (state) => state.names)
  const listing = useInfiniteQuery(dirListQuery(path, { key: sortKey, order: sortOrder }))
  const directory = useMemo(() => dirViewOf(listing.data?.pages), [listing.data?.pages])
  const entries = directory.entries
  const selected = entries.filter((entry) => selectedNames.has(entry.name))
  const encryption = useQuery(shareEncryptionQuery(path))
  const encrypted = encryption.data != null || encryption.isError
  const unlocked = encryption.data != null && isUnlocked(encryption.data.salt)
  const rootLabel = path.split('/').filter(Boolean)[0]
  const root = session.data?.roots.find((entry) => entry.label === rootLabel)
  const noShares = (session.data?.roots.length ?? 0) === 0
  const focusName = new URLSearchParams(location.search).get('focus')
  const tableRef = useRef<FileViewHandle>(null)
  const gridRef = useRef<FileGridHandle>(null)
  const [newFolderOpen, setNewFolderOpen] = useState(false)
  const [renameTarget, setRenameTarget] = useState<Entry | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [destOpen, setDestOpen] = useState(false)
  const [conflictOpen, setConflictOpen] = useState(false)
  const [conflictName, setConflictName] = useState('')
  const [contextEntry, setContextEntry] = useState<Entry | null>(null)
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number } | null>(null)
  const [blankMenu, setBlankMenu] = useState<{ x: number; y: number } | null>(null)
  const [menuTrigger, setMenuTrigger] = useState<HTMLElement | null>(null)
  const [shareTarget, setShareTarget] = useState<Entry | null>(null)
  const [snackbar, setSnackbar] = useState<string | null>(null)
  const [operation, setOperation] = useState<{ kind: OperationKind; results: BatchItemResult[]; jobs: string[] } | null>(null)
  const [destSources, setDestSources] = useState<string[]>([])
  const [destCanCopy, setDestCanCopy] = useState(false)
  const [destCanMove, setDestCanMove] = useState(false)
  const [conflictRetry, setConflictRetry] = useState<((kind: OnConflict) => void) | null>(null)
  const [dragOver, setDragOver] = useState(false)
  const [newMenuOpen, setNewMenuOpen] = useState(false)
  const [newMenuPosition, setNewMenuPosition] = useState({ x: 0, y: 0 })
  const [newMenuTrigger, setNewMenuTrigger] = useState<HTMLElement | null>(null)
  const [overflowOpen, setOverflowOpen] = useState(false)
  const [overflowPosition, setOverflowPosition] = useState({ x: 0, y: 0 })
  const [overflowTrigger, setOverflowTrigger] = useState<HTMLElement | null>(null)
  const [sortMenuOpen, setSortMenuOpen] = useState(false)
  const [sortMenuPosition, setSortMenuPosition] = useState({ x: 0, y: 0 })
  const [sortMenuTrigger, setSortMenuTrigger] = useState<HTMLElement | null>(null)
  const [marqueeRect, setMarqueeRect] = useState<Rect | null>(null)
  const [marqueeScroll, setMarqueeScroll] = useState({ x: 0, y: 0 })
  const dragOrigin = useRef<{ x: number; y: number } | null>(null)
  const dragPointer = useRef({ x: 0, y: 0 })
  const dragBase = useRef<string[]>([])
  const dragFrame = useRef<number | null>(null)
  const [unlockTarget, setUnlockTarget] = useState<UnlockTarget | null>(null)
  const [previewOpen, setPreviewOpen] = useState(false)
  const [previewIndex, setPreviewIndex] = useState(-1)
  const [fileInput, setFileInput] = useState<HTMLInputElement | null>(null)
  const [dirInput, setDirInput] = useState<HTMLInputElement | null>(null)
  const mkdir = useMutation(mkdirMutation())
  const rename = useMutation(renameMutation())
  const remove = useMutation(deleteMutation())
  const move = useMutation(moveMutation())
  const copy = useMutation(copyMutation())
  const archive = useMutation(archiveTicketMutation())
  const measured = useQueries({ queries: (selected.length ? selected.filter((entry) => entry.kind === 'dir').map((entry) => joinPath(path, entry.name)) : path === '/' ? [] : [path]).map((p) => folderSizeQuery(p)) })
  const selectionBytes = selected.reduce((sum, entry) => sum + (entry.kind === 'dir' ? 0 : entry.size), 0) + measured.reduce((sum, query) => sum + (query.data?.bytes ?? 0), 0)
  const canCreate = directory.perms.create
  const densityLabel = density === 'compact' ? t('browse.compact') : density === 'spacious' ? t('browse.spacious') : t('browse.comfortable')
  const sortLabel = sortKey === 'name' ? t('browse.sort_by_name') : sortKey === 'size' ? t('browse.sort_by_size') : sortKey === 'mtime' ? t('browse.sort_by_modified') : t('browse.sort_by_kind')

  const controlSelector = 'button, input, a, [role="menuitem"], [role="menu"]'
  const contentSelector = `.sc-row, .sc-file-grid__card, ${controlSelector}`

  const updateMarquee = () => {
    const origin = dragOrigin.current
    if (!origin) return
    const pointer = dragPointer.current
    const rect = rectBetween(origin.x, origin.y, pointer.x + window.scrollX, pointer.y + window.scrollY)
    setMarqueeScroll({ x: window.scrollX, y: window.scrollY })
    setMarqueeRect(rect)
    const activeView = mode === 'grid' ? gridRef.current : tableRef.current
    const hits = activeView?.entriesInRect(rect) ?? []
    selection.replace([...new Set([...dragBase.current, ...hits.map((entry) => entry.name)])])
  }

  const autoScrollTick = () => {
    if (!dragOrigin.current) return
    const step = autoScrollStep(dragPointer.current.y, window.innerHeight)
    if (step !== 0) {
      window.scrollBy(0, step)
      updateMarquee()
    }
    dragFrame.current = requestAnimationFrame(autoScrollTick)
  }

  const swallowMarqueeClick = (event: MouseEvent) => {
    event.preventDefault()
    event.stopPropagation()
  }

  const endMarquee = () => {
    const wasDragging = marqueeRect !== null
    dragOrigin.current = null
    setMarqueeRect(null)
    if (dragFrame.current !== null) cancelAnimationFrame(dragFrame.current)
    dragFrame.current = null
    window.removeEventListener('pointermove', onMarqueePointerMove)
    window.removeEventListener('pointerup', endMarquee)
    window.removeEventListener('keydown', onMarqueeKeyDown)
    if (wasDragging) window.addEventListener('click', swallowMarqueeClick, { capture: true, once: true })
  }

  const onMarqueeKeyDown = (event: KeyboardEvent) => {
    if (event.key !== 'Escape') return
    event.preventDefault()
    selection.replace(dragBase.current)
    endMarquee()
  }

  const onMarqueePointerMove = (event: PointerEvent) => {
    const origin = dragOrigin.current
    if (!origin) return
    dragPointer.current = { x: event.clientX, y: event.clientY }
    if (marqueeRect === null && !movedFar(origin.x - window.scrollX, origin.y - window.scrollY, event.clientX, event.clientY)) return
    if (marqueeRect === null) {
      window.getSelection()?.removeAllRanges()
      dragFrame.current = requestAnimationFrame(autoScrollTick)
    }
    updateMarquee()
  }

  const onMarqueePointerDown = (event: React.PointerEvent) => {
    if (event.pointerType !== 'mouse' || event.button !== 0) return
    if ((event.target as HTMLElement).closest(controlSelector)) return
    dragOrigin.current = { x: event.clientX + window.scrollX, y: event.clientY + window.scrollY }
    dragPointer.current = { x: event.clientX, y: event.clientY }
    dragBase.current = event.shiftKey || event.ctrlKey || event.metaKey ? [...selectedNames] : []
    window.addEventListener('pointermove', onMarqueePointerMove)
    window.addEventListener('pointerup', endMarquee)
    window.addEventListener('keydown', onMarqueeKeyDown)
  }

  const onEmptyAreaClick = (event: React.MouseEvent) => {
    if ((event.target as HTMLElement).closest(contentSelector)) return
    selection.clear()
  }

  const openBlankMenu = (event: React.MouseEvent) => {
    event.preventDefault()
    selection.clear()
    setContextEntry(null)
    setContextMenu(null)
    setMenuTrigger(event.currentTarget as HTMLElement)
    setBlankMenu({ x: event.clientX, y: event.clientY })
  }

  const crumbs = useMemo(() => {
    const parts = path.split('/').filter(Boolean)
    const result = [{ label: t('browse.home'), path: '/' }]
    let current = ''
    for (const part of parts) {
      current += `/${part}`
      result.push({ label: part, path: current })
    }
    return result
  }, [path, t])

  const openSort = (event: ReactMouseEvent<HTMLElement>) => {
    event.stopPropagation()
    const trigger = event.currentTarget
    const rect = trigger.getBoundingClientRect()
    setSortMenuTrigger(trigger)
    setSortMenuPosition({ x: rect.right, y: rect.bottom + 4 })
    setSortMenuOpen(true)
  }
  const closeSort = () => {
    setSortMenuOpen(false)
    sortMenuTrigger?.focus()
    setSortMenuTrigger(null)
  }
  const chooseSort = (key: typeof sortKey) => {
    view.setSort(key, sortKey === key && sortOrder === 'asc' ? 'desc' : 'asc')
    closeSort()
  }

  useDocumentTitle(`${path.split('/').filter(Boolean).at(-1) ?? 'Stowcloud'} - Stowcloud`)
  useEffect(() => { selection.reset() }, [path])
  useEffect(() => {
    if (!focusName || listing.isPending) return
    const target = mode === 'grid' ? gridRef.current : tableRef.current
    if (target?.focusEntry(focusName)) {
      const next = new URLSearchParams(location.search)
      next.delete('focus')
      void navigate({ pathname: location.pathname, search: next.toString() ? `?${next}` : '' }, { replace: true })
    } else if (listing.hasNextPage && !listing.isFetchingNextPage) void listing.fetchNextPage()
  }, [focusName, listing.isPending, listing.hasNextPage, listing.isFetchingNextPage, mode, location.pathname, location.search, navigate])

  const cycleDensity = () => {
    const order = ['compact', 'comfortable', 'spacious'] as const
    view.setDensity(order[(order.indexOf(density) + 1) % order.length])
  }
  const refresh = () => { void listing.refetch() }
  const [treeOpen, setTreeOpen] = useState(false)
  const toggleView = () => { view.setMode(mode === 'list' ? 'grid' : 'list') }
  const toggleTree = () => { setTreeOpen((open) => !open) }
  const startSearch = () => {
    const target = searchTarget(path)
    if (target) void navigate(target)
    else openSearch(path)
  }
  const openUnlockFor = (target: { salt: string; verifier: string }, retry: () => void) => {
    setUnlockTarget({ salt: target.salt, verifier: target.verifier, retry })
  }
  const closeNewMenu = () => {
    setNewMenuOpen(false)
    newMenuTrigger?.focus()
    setNewMenuTrigger(null)
  }
  const openNewMenu = (event: ReactMouseEvent<HTMLElement>) => {
    if (newMenuOpen) { closeNewMenu(); return }
    event.stopPropagation()
    const trigger = event.currentTarget
    const rect = trigger.getBoundingClientRect()
    setNewMenuTrigger(trigger)
    setNewMenuPosition({ x: rect.right, y: rect.bottom + 4 })
    setNewMenuOpen(true)
  }
  const closeOverflow = () => {
    setOverflowOpen(false)
    overflowTrigger?.focus()
    setOverflowTrigger(null)
  }
  const openOverflow = (event: ReactMouseEvent<HTMLElement>) => {
    if (overflowOpen) { closeOverflow(); return }
    event.stopPropagation()
    const trigger = event.currentTarget
    const rect = trigger.getBoundingClientRect()
    setOverflowTrigger(trigger)
    setOverflowPosition({ x: rect.right, y: rect.bottom + 4 })
    setOverflowOpen(true)
  }
  useEffect(() => {
    if (!newMenuOpen && !overflowOpen) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      if (newMenuOpen) closeNewMenu()
      else closeOverflow()
    }
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Element | null
      if (target?.closest('.sc-browse-new-menu, [aria-expanded="true"]')) return
      if (newMenuOpen) closeNewMenu()
      if (overflowOpen) closeOverflow()
    }
    window.addEventListener('keydown', onKeyDown)
    window.addEventListener('pointerdown', onPointerDown, true)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('pointerdown', onPointerDown, true)
    }
  }, [newMenuOpen, overflowOpen, newMenuTrigger, overflowTrigger])
  useEffect(() => {
    if (!newMenuOpen && !overflowOpen) return
    queueMicrotask(() => document.querySelector<HTMLElement>('.sc-browse-new-menu button')?.focus())
  }, [newMenuOpen, overflowOpen])
  useEffect(() => {
    if (contextMenu === null && blankMenu === null) return
    queueMicrotask(() => document.querySelector<HTMLElement>('.sc-browse-new-menu button')?.focus())
  }, [contextMenu, blankMenu])
  const openEditor = async (entry: Entry) => {
    if (entry.kind === 'dir') return
    const href = `/edit${joinPath(path, entry.name)}`
    try {
      const entryEncryption = await encryptionForLabel(shareLabelOf(entry.path))
      if (entryEncryption && !isUnlocked(entryEncryption.salt)) {
        openUnlockFor(entryEncryption, () => { void navigate(href) })
        return
      }
      void navigate(href)
    } catch (error) {
      setSnackbar(describeApiError(error, t('common.could_not_load_list')))
    }
  }
  const actionTarget = () => selected[0] ?? contextEntry
  const requestTransfer = () => {
    const targets = selected.length ? selected : contextEntry ? [contextEntry] : []
    if (!targets.length) return
    setDestSources(targets.map((entry) => joinPath(path, entry.name)))
    setDestCanCopy(targets.every((entry) => entry.perms.read && entry.perms.download))
    setDestCanMove(targets.every((entry) => entry.perms.read && entry.perms.move))
    setDestOpen(true)
  }
  const transfer = async (paths: string[], dest: string, kind: 'move' | 'copy', onConflict: OnConflict): Promise<void> => {
    if (!paths.length) return
    try {
      const result: BatchResult | CopyResult = kind === 'move' ? await move.mutateAsync({ paths, dest, onConflict }) : await copy.mutateAsync({ paths, dest, onConflict })
      const jobs = 'jobs' in result ? result.jobs ?? [] : []
      setOperation({ kind, results: result.results, jobs })
      if (jobs.length) jobTray.track(...jobs)
      const conflicts = result.results.filter((item) => item.error?.code === 'fs.conflict')
      if (conflicts.length) {
        const conflictPaths = conflicts.map((item) => item.path)
        const first = conflicts[0]
        const detailPath = first.error?.detail?.path
        const namedPath = typeof detailPath === 'string' ? detailPath : first.destination ?? first.path
        setConflictName(baseName(namedPath))
        setConflictRetry(() => (next: OnConflict) => { void transfer(conflictPaths, dest, kind, next) })
        setConflictOpen(true)
        return
      }
      const failed = result.results.find((item) => !item.ok)
      if (failed) {
        if (failed.error?.code === 'quota.exceeded') setSnackbar(kind === 'move' ? t('browse.not_enough_storage_space_move') : t('browse.not_enough_storage_space_copy'))
        else { const key = batchErrorKey(failed.error); setSnackbar(key ? t(key.key, key.params) : t(kind === 'move' ? 'browse.move_job_failed' : 'browse.copy_job_failed')) }
      } else { selection.clear(); setSnackbar(t('common.done')) }
    } catch (error) {
      if (error instanceof ApiError && error.code === 'fs.conflict') {
        const namedPath = typeof error.detail?.path === 'string' ? error.detail.path : paths[0] ?? ''
        setConflictName(baseName(namedPath))
        setConflictRetry(() => (next: OnConflict) => { void transfer(paths, dest, kind, next) })
        setConflictOpen(true)
      } else if (error instanceof ApiError && error.code === 'quota.exceeded') setSnackbar(kind === 'move' ? t('browse.not_enough_storage_space_move') : t('browse.not_enough_storage_space_copy'))
      else setSnackbar(describeApiError(error, kind === 'move' ? t('browse.move_job_failed') : t('browse.copy_job_failed')))
    }
  }
  const downloadTargets = async (targets: Entry[]): Promise<void> => {
    if (!targets.length) return
    let entryEncryption: ShareEncryption | null = null
    try {
      entryEncryption = await encryptionForLabel(shareLabelOf(targets[0].path))
      if (entryEncryption) {
        for (const entry of targets) entry.kind === 'dir' ? await downloadEncryptedFolder(entry.path) : await downloadEncryptedFile(entry)
      } else if (targets.length === 1 && targets[0].kind !== 'dir') {
        await downloadPath(targets[0].path)
      } else {
        const ticket = await archive.mutateAsync({ paths: targets.map((entry) => entry.path), name: targets.length === 1 ? `${targets[0].name}.zip` : 'archive.zip' })
        triggerUrlDownload(ticket.url, ticket.name)
      }
    } catch (error) {
      if (error instanceof LockedSessionError && entryEncryption) {
        openUnlockFor(entryEncryption, () => { void downloadTargets(targets) })
      } else if (error instanceof FileTooLargeError) setSnackbar(t('browse.download_too_large_to_buffer'))
      else if (error instanceof ApiError && error.code === 'rate.limited') setSnackbar(t('browse.too_many_zip_downloads_at'))
      else setSnackbar(describeApiError(error, entryEncryption ? t('browse.zip_download_failed') : t('browse.download_failed')))
    }
  }
  const downloadSelection = () => {
    const targets = selected.length ? selected : contextEntry ? [contextEntry] : []
    void downloadTargets(targets)
  }
  const downloadEntry = (entry: Entry) => { void downloadTargets([entry]) }
  const createFolder = async (name: string) => {
    setNewFolderOpen(false)
    try { await mkdir.mutateAsync({ parent: path, name }) }
    catch (error) { setSnackbar(describeApiError(error, t('browse.could_not_create_folder'))) }
  }
  const doRename = async (name: string) => {
    const target = renameTarget
    setRenameTarget(null)
    if (!target) return
    try { await rename.mutateAsync({ path: joinPath(path, target.name), newName: name }) }
    catch (error) { setSnackbar(describeApiError(error, t('common.could_not_rename'))) }
  }
  const doDelete = async () => {
    setDeleteOpen(false)
    const targets = selected.length ? selected : contextEntry ? [contextEntry] : []
    const paths = targets.map((entry) => joinPath(path, entry.name))
    if (!paths.length) return
    try {
      const result = await remove.mutateAsync(paths)
      setOperation({ kind: 'delete', results: result.results, jobs: [] })
      selection.clear()
      const failed = result.results.find((item) => !item.ok)
      setSnackbar(failed ? (batchErrorKey(failed.error)?.key ? t(batchErrorKey(failed.error)!.key, batchErrorKey(failed.error)!.params) : t('browse.delete_failed')) : t('common.done'))
    } catch (error) { setSnackbar(describeApiError(error, t('browse.delete_failed'))) }
  }
  const getUniqueUploadName = (name: string, isTaken: (candidate: string) => boolean): string => {
    if (!isTaken(name)) return name
    const dot = name.lastIndexOf('.')
    const stem = dot > 0 ? name.slice(0, dot) : name
    const ext = dot > 0 ? name.slice(dot) : ''
    let count = 1
    while (isTaken(`${stem} (${count})${ext}`)) count++
    return `${stem} (${count})${ext}`
  }
  const showUploadConflict = (name: string, retry: (policy: OnConflict) => void, count: number) => {
    setConflictName(count === 1 ? name : `${name} (+${count - 1})`)
    setConflictRetry(() => retry)
    setConflictOpen(true)
  }
  const handleUploadFiles = (files: FileList | File[]) => {
    const incoming = Array.from(files)
    if (!incoming.length) return
    const conflicts = incoming.filter((file) => entries.some((entry) => entry.name === file.name))
    if (!conflicts.length) { void addFiles(incoming, path); return }
    showUploadConflict(conflicts[0].name, (policy) => {
      setConflictOpen(false)
      if (policy === 'overwrite') { void addFiles(incoming, path); return }
      if (policy === 'skip') {
        const conflictNames = new Set(conflicts.map((file) => file.name))
        const remaining = incoming.filter((file) => !conflictNames.has(file.name))
        if (remaining.length) void addFiles(remaining, path)
        return
      }
      const handedOut = new Set<string>()
      const renamed = incoming.map((file) => {
        if (!entries.some((entry) => entry.name === file.name)) return file
        const name = getUniqueUploadName(file.name, (candidate) => entries.some((entry) => entry.name === candidate) || handedOut.has(candidate))
        handedOut.add(name)
        return new File([file], name, { type: file.type, lastModified: file.lastModified })
      })
      void addFiles(renamed, path)
    }, conflicts.length)
  }
  const handleUploadEntries = (picked: readonly { file: File; relativePath: string }[]) => {
    if (!picked.length) return
    const conflicts = picked.filter((entry) => !entry.relativePath && entries.some((listed) => listed.name === entry.file.name))
    if (!conflicts.length) { void addEntries(picked, path); return }
    showUploadConflict(conflicts[0].file.name, (policy) => {
      setConflictOpen(false)
      if (policy === 'overwrite') { void addEntries(picked, path); return }
      if (policy === 'skip') {
        const conflictNames = new Set(conflicts.map((entry) => entry.file.name))
        const remaining = picked.filter((entry) => entry.relativePath || !conflictNames.has(entry.file.name))
        if (remaining.length) void addEntries(remaining, path)
        return
      }
      const handedOut = new Set<string>()
      const renamed = picked.map((entry) => {
        if (entry.relativePath || !entries.some((listed) => listed.name === entry.file.name)) return entry
        const name = getUniqueUploadName(entry.file.name, (candidate) => entries.some((listed) => listed.name === candidate) || handedOut.has(candidate))
        handedOut.add(name)
        return { file: new File([entry.file], name, { type: entry.file.type, lastModified: entry.file.lastModified }), relativePath: entry.relativePath }
      })
      void addEntries(renamed, path)
    }, conflicts.length)
  }
  const uploadFiles = (files: FileList | File[]) => { handleUploadFiles(files) }
  const onDrop = async (event: DragEvent) => {
    event.preventDefault()
    setDragOver(false)
    if (!event.dataTransfer) return
    if (!canCreate) { setSnackbar(t('error.acl_denied')); return }
    try { handleUploadEntries(await pickedFilesFromDataTransfer(event.dataTransfer)) }
    catch { setSnackbar(t('upload.could_not_start_upload')) }
  }
  const actions = rowActions(selected, {
    openInEditor: () => { const entry = actionTarget(); if (entry) void openEditor(entry) },
    download: downloadSelection,
    share: () => { const entry = actionTarget(); if (entry) setShareTarget(entry) },
    rename: () => { const entry = actionTarget(); if (entry) setRenameTarget(entry) },
    transfer: requestTransfer,
    duplicate: () => { void transfer(selected.map((entry) => joinPath(path, entry.name)), path, 'copy', 'rename') },
    remove: () => setDeleteOpen(true)
  }, canCreate)
  const onOpen = (entry: Entry) => {
    if (entry.kind === 'dir') { void navigate(`/b${joinPath(path, entry.name)}`); return }
    const index = entries.indexOf(entry)
    setPreviewIndex(index)
    setPreviewOpen(true)
  }
  const previewEntry = previewIndex >= 0 ? entries[previewIndex] ?? null : null
  const previewPath = previewEntry ? joinPath(path, previewEntry.name) : ''
  const stepPreview = (delta: number) => {
    let index = previewIndex + delta
    while (index >= 0 && index < entries.length) {
      if (entries[index].kind !== 'dir') { setPreviewIndex(index); return }
      index += delta
    }
  }
  const hasPreviewNeighbour = (delta: number) => {
    let index = previewIndex + delta
    while (index >= 0 && index < entries.length) {
      if (entries[index].kind !== 'dir') return true
      index += delta
    }
    return false
  }
  if (path === '/' && session.data?.roots[0]) return <NavigateToRoot path={session.data.roots[0].label} search={location.search} />
  return (
    <div className="sc-browse" role="region" aria-label={t('browse.file_browser')} onDragOver={(event) => { event.preventDefault(); setDragOver(canCreate) }} onDragLeave={() => setDragOver(false)} onDrop={onDrop}>
      <div className="sc-browse__bar-stack">
        <div className="sc-browse__folder-context">
          <div className="sc-browse__folder-heading">
            <Breadcrumb crumbs={crumbs} onNavigate={(next) => void navigate(`/b${next}`)} />
            {root?.shared_externally ? <span className="sc-browse__external-badge">⚠ {t('common.shared_with_other_services')}</span> : null}
            {encrypted ? unlocked ? <span className="sc-browse__encrypted-badge">🔒 {t('browse.encrypted_badge')}</span> : <button type="button" className="sc-browse__encrypted-badge sc-browse__encrypted-badge--locked" onClick={() => { if (encryption.data) openUnlockFor(encryption.data, () => {}) }}>🔒 {t('browse.encrypted_locked_badge')}</button> : null}
            {root?.broken_reason ? <span className="sc-browse__broken-badge">⚠ {t('browse.this_folder_is_unavailable')}</span> : null}
          </div>
          <div className="sc-browse__folder-state"><span>{mode === 'list' ? t('browse.list_view') : t('browse.grid_view')}</span><span>{t('browse.sort_selected', { label: sortLabel, direction: sortOrder === 'asc' ? t('browse.sort_ascending') : t('browse.sort_descending') })}</span></div>
        </div>
        <header className={`sc-browse__toolbar${compact ? ' sc-browse__toolbar--compact' : ''}`}>
          <div className="sc-browse__toolbar-actions">
            <IconButton label={t('common.search')} onClick={startSearch}><Icon name="search" /></IconButton>
            {canCreate ? <Button variant="filled" onClick={openNewMenu} icon={<Icon name="add" />}>{t('browse.new')}</Button> : null}
            {!compact ? <><IconButton label={t('common.refresh')} onClick={refresh}><Icon name="refresh" /></IconButton><IconButton label={treeOpen ? t('browse.hide_folder_tree') : t('browse.show_folder_tree')} selected={treeOpen} onClick={toggleTree}><Icon name="folder-tree" /></IconButton><IconButton label={mode === 'list' ? t('browse.grid_view') : t('browse.list_view')} onClick={toggleView}><Icon name={mode === 'list' ? 'grid' : 'list'} /></IconButton><IconButton label={details ? t('details.hide') : t('details.show')} expanded={details} onClick={() => ui.setDetails(!details)}><Icon name="info" /></IconButton></> : null}
            <IconButton label={t('browse.sort_by', { key: sortLabel })} selected={sortMenuOpen} expanded={sortMenuOpen} onClick={openSort}><Icon name="sort" /></IconButton>
            <IconButton label={t('browse.more')} selected={overflowOpen} expanded={overflowOpen} onClick={openOverflow}><Icon name="more-vert" /></IconButton>
          </div>
        </header>
      </div>
      {selected.length ? <div className="sc-browse__selection-bar"><div className="sc-browse__selection-bar-inner"><IconButton label={t('browse.clear_selection')} onClick={() => selection.clear()}><Icon name="close" /></IconButton><IconButton label={t('browse.select_all')} onClick={() => selection.all(entries.map((entry) => entry.name))}><Icon name="check" /></IconButton><span className="sc-browse__selection-count">{compact ? t('common.item_count', { count: selected.length }) : t('browse.selected', { count: selected.length, size: formatBytes(selectionBytes) })}</span>{actions.map((action) => <IconButton key={action.key} label={action.label} onClick={action.run}><Icon name={ACTION_ICON_NAMES[action.key] ?? 'more-vert'} /></IconButton>)}</div></div> : null}
      {operation ? <section className="sc-browse__operation" role="status" aria-live="polite"><div className="sc-browse__operation-heading"><h2>{operation.kind === 'delete' ? t('common.delete') : operation.kind === 'move' ? t('common.move') : t('common.copy')}</h2><button type="button" className="sc-browse__operation-close" onClick={() => setOperation(null)}>{t('common.close')}</button></div><ul>{operation.results.map((result) => <li key={result.path} className={!result.ok ? 'sc-browse__operation-error' : undefined}><span>{result.destination ? `${result.path} to ${result.destination}` : result.path}</span><span>{result.ok ? result.skipped ? t('browse.items_skipped_name_taken', { count: 1 }) : t('common.done') : batchErrorKey(result.error)?.key ? t(batchErrorKey(result.error)!.key, batchErrorKey(result.error)!.params) : t('error.internal')}</span></li>)}</ul>{operation.results.some((result) => result.skipped) ? <p>{t('browse.items_skipped_name_taken', { count: operation.results.filter((result) => result.skipped).length })}</p> : null}</section> : null}
      <div className="sc-browse__content">
        {treeOpen ? <FileTree currentPath={path} onNavigate={(next) => { setTreeOpen(false); void navigate(`/b${next}`) }} overlay={compact} onClose={() => setTreeOpen(false)} /> : null}
        <div className={`sc-browse__table-wrap${dragOver ? ' sc-browse__table-wrap--dragover' : ''}${marqueeRect ? ' sc-browse__table-wrap--marquee' : ''}`} onPointerDown={onMarqueePointerDown} onContextMenu={openBlankMenu} onClick={onEmptyAreaClick}>
          {noShares ? <div className="sc-browse__nothing"><h2 className="sc-browse__nothing-title">{t('browse.nothing_here')}</h2><p className="sc-browse__nothing-hint">{session.data?.user.is_admin ? t('browse.press_this_button_to_set_up_your_first_folder') : t('browse.ask_an_administrator_for_a_folder')}</p>{session.data?.user.is_admin ? <Button onClick={() => void navigate('/admin#shares')}>{t('common.add_folder')}</Button> : null}</div> : listing.isPending ? <div className="sc-browse__loading"><mdui-circular-progress /></div> : listing.error ? <p className="sc-browse__error" role="alert">{describeApiError(listing.error, t('browse.this_folder_could_not_be_opened'))}</p> : <div className="sc-browse__view">{mode === 'grid' ? <FileGrid ref={gridRef} entries={entries} total={directory.total} dirs={directory.dirs} loading={listing.isPending} loadingMore={listing.isFetchingNextPage} requestMore={() => { if (listing.hasNextPage && !listing.isFetchingNextPage) void listing.fetchNextPage() }} perms={directory.perms} onOpen={onOpen} onContextMenu={(entry, event) => { if (!selectedNames.has(entry.name)) selection.only(entry.name, entries.indexOf(entry)); setContextEntry(entry); setMenuTrigger(event.currentTarget as HTMLElement); setContextMenu({ x: event.clientX, y: event.clientY }); setBlankMenu(null) }} onRename={() => { const entry = actionTarget(); if (entry) setRenameTarget(entry) }} onDelete={() => setDeleteOpen(true)} onSearchFocus={startSearch} encrypted={encrypted} /> : <FileTable ref={tableRef} entries={entries} total={directory.total} dirs={directory.dirs} loading={listing.isPending} loadingMore={listing.isFetchingNextPage} requestMore={() => { if (listing.hasNextPage && !listing.isFetchingNextPage) void listing.fetchNextPage() }} perms={directory.perms} onOpen={onOpen} onContextMenu={(entry, event) => { if (!selectedNames.has(entry.name)) selection.only(entry.name, entries.indexOf(entry)); setContextEntry(entry); setMenuTrigger(event.currentTarget as HTMLElement); setContextMenu({ x: event.clientX, y: event.clientY }); setBlankMenu(null) }} onRename={() => { const entry = actionTarget(); if (entry) setRenameTarget(entry) }} onDelete={() => setDeleteOpen(true)} onSearchFocus={startSearch} encrypted={encrypted} />}</div>}
          {dragOver ? <div className="sc-browse__drop-overlay">{t('browse.drop_here_upload')}</div> : null}
        </div>
        {marqueeRect ? <div className="sc-browse__marquee" aria-hidden="true" style={{ left: marqueeRect.left - marqueeScroll.x, top: marqueeRect.top - marqueeScroll.y, width: marqueeRect.right - marqueeRect.left, height: marqueeRect.bottom - marqueeRect.top }} /> : null}
        {details ? <DetailsPanel path={path} selected={selected} total={directory.total} dirs={directory.dirs} encrypted={encrypted} onClose={() => ui.setDetails(false)} /> : null}
      </div>
      <Menu open={contextMenu !== null} onClose={() => { setContextMenu(null); menuTrigger?.focus(); setMenuTrigger(null) }} x={contextMenu?.x} y={contextMenu?.y}><div className="sc-browse-new-menu" role="menu">{actions.map((action) => <button key={action.key} type="button" role="menuitem" onClick={() => { setContextMenu(null); action.run() }}>{action.label}</button>)}</div></Menu>
      <Menu open={blankMenu !== null} onClose={() => { setBlankMenu(null); menuTrigger?.focus(); setMenuTrigger(null) }} x={blankMenu?.x} y={blankMenu?.y}><div className="sc-browse-new-menu" role="menu">{canCreate ? <><button type="button" role="menuitem" onClick={() => { setBlankMenu(null); setNewFolderOpen(true) }}>{t('common.new_folder')}</button><button type="button" role="menuitem" onClick={() => { setBlankMenu(null); fileInput?.click() }}>{t('common.upload')}</button><button type="button" role="menuitem" onClick={async () => { setBlankMenu(null); if (supportsDirectoryPicker()) { try { handleUploadEntries(await pickDirectory()) } catch { /* canceled */ } } else dirInput?.click() }}>{t('browse.upload_folder')}</button></> : null}</div></Menu>
      <Menu open={sortMenuOpen} onClose={closeSort} x={sortMenuPosition.x} y={sortMenuPosition.y} align="end"><div className="sc-browse-new-menu" role="menu">{(['name', 'size', 'mtime', 'kind'] as const).map((key) => <button type="button" role="menuitem" key={key} onClick={() => chooseSort(key)}>{sortKey === key ? t('browse.sort_selected', { label: key === 'name' ? t('browse.sort_by_name') : key === 'size' ? t('browse.sort_by_size') : key === 'mtime' ? t('browse.sort_by_modified') : t('browse.sort_by_kind'), direction: sortOrder === 'asc' ? t('browse.sort_ascending') : t('browse.sort_descending') }) : key === 'name' ? t('browse.sort_by_name') : key === 'size' ? t('browse.sort_by_size') : key === 'mtime' ? t('browse.sort_by_modified') : t('browse.sort_by_kind')}</button>)}</div></Menu>
      {canCreate ? <Menu open={newMenuOpen} onClose={closeNewMenu} x={newMenuPosition.x} y={newMenuPosition.y} align="end"><div className="sc-browse-new-menu" role="menu"><button type="button" role="menuitem" onClick={() => { closeNewMenu(); setNewFolderOpen(true) }}>{t('common.new_folder')}</button><button type="button" role="menuitem" onClick={() => { closeNewMenu(); fileInput?.click() }}>{t('common.upload')}</button><button type="button" role="menuitem" onClick={async () => { closeNewMenu(); if (supportsDirectoryPicker()) { try { handleUploadEntries(await pickDirectory()) } catch { /* canceled */ } } else dirInput?.click() }}>{t('browse.upload_folder')}</button></div></Menu> : null}
      <Menu open={overflowOpen} onClose={closeOverflow} x={overflowPosition.x} y={overflowPosition.y} align="end"><div className="sc-browse-new-menu" role="menu">{compact ? <><button type="button" role="menuitem" onClick={() => { toggleView(); closeOverflow() }}>{mode === 'list' ? t('browse.grid_view') : t('browse.list_view')}</button><button type="button" role="menuitem" onClick={() => { ui.setDetails(!details); closeOverflow() }}>{details ? t('details.hide') : t('details.show')}</button><button type="button" role="menuitem" onClick={() => { closeOverflow(); const target = overflowTrigger; if (target) openSort({ stopPropagation: () => undefined, currentTarget: target } as unknown as ReactMouseEvent<HTMLElement>) }}>{t('browse.sort_by', { key: sortLabel })}</button><button type="button" role="menuitem" onClick={() => { refresh(); closeOverflow() }}>{t('common.refresh')}</button><button type="button" role="menuitem" onClick={() => { toggleTree(); closeOverflow() }}>{treeOpen ? t('browse.hide_folder_tree') : t('browse.show_folder_tree')}</button></> : null}<button type="button" role="menuitem" onClick={() => { cycleDensity(); closeOverflow() }}>{t('browse.density', { density: densityLabel })}</button><button type="button" role="menuitem" onClick={() => { closeOverflow(); void navigate('/trash') }}>{t('browse.open_trash')}</button></div></Menu>
      <input ref={setFileInput} type="file" hidden multiple aria-label={t('browse.choose_files_upload')} onChange={(event) => { if (event.currentTarget.files) uploadFiles(event.currentTarget.files); event.currentTarget.value = '' }} />
      <input ref={setDirInput} type="file" hidden aria-label={t('browse.choose_folder_upload')} {...{ webkitdirectory: '', directory: '' }} onChange={(event) => { handleUploadEntries(filesFromWebkitDirectoryInput(event.currentTarget)); event.currentTarget.value = '' }} />
      <NewFolderDialog open={newFolderOpen} onClose={() => setNewFolderOpen(false)} onCreate={createFolder} />
      <RenameDialog open={renameTarget !== null} currentName={renameTarget?.name ?? ''} onClose={() => setRenameTarget(null)} onRename={doRename} />
      <DeleteDialog open={deleteOpen} count={selected.length || (contextEntry ? 1 : 0)} externalShare={root?.shared_externally} trashEnabled={root?.trash_enabled} onClose={() => setDeleteOpen(false)} onConfirm={() => void doDelete()} />
      <DestinationPickerDialog open={destOpen} sources={destSources} canCopy={destCanCopy} canMove={destCanMove} onClose={() => setDestOpen(false)} onPick={(dest, kind) => { setDestOpen(false); void transfer(destSources, dest, kind, 'fail') }} />
      <ConflictDialog open={conflictOpen} name={conflictName} onClose={() => setConflictOpen(false)} onKeepBoth={() => { setConflictOpen(false); conflictRetry?.('rename') }} onOverwrite={() => { setConflictOpen(false); conflictRetry?.('overwrite') }} onSkip={() => { setConflictOpen(false); conflictRetry?.('skip') }} />
      <UnlockShareDialog open={unlockTarget !== null} salt={unlockTarget?.salt ?? ''} verifier={unlockTarget?.verifier ?? ''} onUnlock={() => { const retry = unlockTarget?.retry; setUnlockTarget(null); retry?.() }} onClose={() => setUnlockTarget(null)} />
      <PreviewDialog open={previewOpen && previewEntry !== null} entry={previewEntry} path={previewPath} hasPrev={hasPreviewNeighbour(-1)} hasNext={hasPreviewNeighbour(1)} onClose={() => setPreviewOpen(false)} onPrev={() => stepPreview(-1)} onNext={() => stepPreview(1)} onDownload={downloadEntry} onEdit={(entry) => { void openEditor(entry) }} />
      {shareTarget ? <ShareManageDialog open path={joinPath(path, shareTarget.name)} targetName={shareTarget.name} targetIsDir={shareTarget.kind === 'dir'} onClose={() => setShareTarget(null)} /> : null}
      {snackbar ? <div role="status" className="sc-snackbar" onClick={() => setSnackbar(null)}>{snackbar}</div> : null}
    </div>
  )
}

function NavigateToRoot({ path, search }: { path: string; search: string }) {
  const navigate = useNavigate()
  useEffect(() => { void navigate(`/b/${encodeURIComponent(path)}${search}`, { replace: true }) }, [navigate, path, search])
  return null
}
