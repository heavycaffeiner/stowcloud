import type { DragEvent } from 'react'
import { useMutation } from '@tanstack/react-query'
import { ApiError, type BatchResult, type Entry, type OnConflict } from '../../../lib/api/client'
import type { CopyResult, ShareEncryption } from '../../../lib/api/types'
import { baseName, joinPath } from '../../../lib/api/path-utils'
import { batchErrorKey, describeApiError } from '../../../lib/api/error-text'
import { encryptionForLabel, shareLabelOf } from '../../../lib/crypto/encrypted-shares'
import { FileTooLargeError, isUnlocked, LockedSessionError } from '../../../lib/crypto/e2ee'
import { downloadEncryptedFile, downloadEncryptedFolder } from '../../../lib/crypto/download-sw'
import { downloadPath, triggerUrlDownload } from '../../../lib/format/download'
import { mkdirMutation, renameMutation, deleteMutation, moveMutation, copyMutation, archiveTicketMutation } from '../../../lib/query/files'
import { jobTray } from '../../../lib/store/jobs.store'
import { selection } from '../../../lib/store/selection.store'
import { addEntries, addFiles } from '../../../lib/upload/queue'
import { pickedFilesFromDataTransfer } from '../../../lib/upload/directory-picker'
import { rowActions, type RowAction } from '../../../features/files/row-actions'
import type { BrowseState } from './types'

type Patch = (patch: Partial<BrowseState> | ((state: BrowseState) => Partial<BrowseState>)) => void
type PickedEntry = { file: File; relativePath: string }

type BrowseActionContext = {
  path: string
  entries: readonly Entry[]
  selected: readonly Entry[]
  contextEntry: Entry | null
  t: (key: string, params?: Record<string, string | number>) => string
  renameTarget: Entry | null
  canCreate: boolean
  navigate: (to: string) => void
  patch: Patch
}

function uniqueUploadName(name: string, isTaken: (candidate: string) => boolean): string {
  if (!isTaken(name)) return name
  const dot = name.lastIndexOf('.')
  const stem = dot > 0 ? name.slice(0, dot) : name
  const ext = dot > 0 ? name.slice(dot) : ''
  let count = 1
  while (isTaken(`${stem} (${count})${ext}`)) count += 1
  return `${stem} (${count})${ext}`
}

export function useBrowseActions(context: BrowseActionContext) {
  const { path, entries, selected, contextEntry, renameTarget, canCreate, navigate, t, patch } = context
  const mkdir = useMutation(mkdirMutation())
  const rename = useMutation(renameMutation())
  const remove = useMutation(deleteMutation())
  const move = useMutation(moveMutation())
  const copy = useMutation(copyMutation())
  const archive = useMutation(archiveTicketMutation())

  const openUnlockFor = (target: { salt: string; verifier: string }, retry: () => void) => patch({ unlockTarget: { salt: target.salt, verifier: target.verifier, retry } })
  const openEditor = async (entry: Entry) => {
    if (entry.kind === 'dir') return
    const href = `/edit${joinPath(path, entry.name)}`
    try {
      const entryEncryption = await encryptionForLabel(shareLabelOf(entry.path))
      if (entryEncryption && !isUnlocked(entryEncryption.salt)) { openUnlockFor(entryEncryption, () => { navigate(href) }); return }
      navigate(href)
    } catch (error) { patch({ snackbar: describeApiError(error, t('common.could_not_load_list')) }) }
  }
  const actionTarget = () => selected[0] ?? contextEntry
  const requestTransfer = () => {
    const targets = selected.length ? selected : contextEntry ? [contextEntry] : []
    if (!targets.length) return
    patch({ destSources: targets.map((entry) => joinPath(path, entry.name)), destCanCopy: targets.every((entry) => entry.perms.read && entry.perms.download), destCanMove: targets.every((entry) => entry.perms.read && entry.perms.move), destOpen: true })
  }
  const transfer = async (paths: string[], dest: string, kind: 'move' | 'copy', onConflict: OnConflict): Promise<void> => {
    if (!paths.length) return
    try {
      const result: BatchResult | CopyResult = kind === 'move' ? await move.mutateAsync({ paths, dest, onConflict }) : await copy.mutateAsync({ paths, dest, onConflict })
      const jobs = 'jobs' in result ? result.jobs ?? [] : []
      patch({ operation: { kind, results: result.results, jobs } })
      if (jobs.length) jobTray.track(...jobs)
      const conflicts = result.results.filter((item) => item.error?.code === 'fs.conflict')
      if (conflicts.length) {
        const first = conflicts[0]
        const detailPath = first.error?.detail?.path
        const namedPath = typeof detailPath === 'string' ? detailPath : first.destination ?? first.path
        patch({ conflictName: baseName(namedPath), conflictRetry: () => (next: OnConflict) => { void transfer(conflicts.map((item) => item.path), dest, kind, next) }, conflictOpen: true })
        return
      }
      const failed = result.results.find((item) => !item.ok)
      if (failed) {
        if (failed.error?.code === 'quota.exceeded') patch({ snackbar: kind === 'move' ? t('browse.not_enough_storage_space_move') : t('browse.not_enough_storage_space_copy') })
        else { const key = batchErrorKey(failed.error); patch({ snackbar: key ? t(key.key, key.params) : t(kind === 'move' ? 'browse.move_job_failed' : 'browse.copy_job_failed') }) }
      } else { selection.clear(); patch({ snackbar: t('common.done') }) }
    } catch (error) {
      if (error instanceof ApiError && error.code === 'fs.conflict') patch({ conflictName: baseName(typeof error.detail?.path === 'string' ? error.detail.path : paths[0] ?? ''), conflictRetry: () => (next: OnConflict) => { void transfer(paths, dest, kind, next) }, conflictOpen: true })
      else if (error instanceof ApiError && error.code === 'quota.exceeded') patch({ snackbar: kind === 'move' ? t('browse.not_enough_storage_space_move') : t('browse.not_enough_storage_space_copy') })
      else patch({ snackbar: describeApiError(error, kind === 'move' ? t('browse.move_job_failed') : t('browse.copy_job_failed')) })
    }
  }
  const downloadTargets = async (targets: readonly Entry[]): Promise<void> => {
    if (!targets.length) return
    let entryEncryption: ShareEncryption | null = null
    try {
      entryEncryption = await encryptionForLabel(shareLabelOf(targets[0].path))
      if (entryEncryption) {
        for (const entry of targets) entry.kind === 'dir' ? await downloadEncryptedFolder(entry.path) : await downloadEncryptedFile(entry)
      } else if (targets.length === 1 && targets[0].kind !== 'dir') await downloadPath(targets[0].path)
      else { const ticket = await archive.mutateAsync({ paths: targets.map((entry) => entry.path), name: targets.length === 1 ? `${targets[0].name}.zip` : 'archive.zip' }); triggerUrlDownload(ticket.url, ticket.name) }
    } catch (error) {
      if (error instanceof LockedSessionError && entryEncryption) openUnlockFor(entryEncryption, () => { void downloadTargets(targets) })
      else if (error instanceof FileTooLargeError) patch({ snackbar: t('browse.download_too_large_to_buffer') })
      else if (error instanceof ApiError && error.code === 'rate.limited') patch({ snackbar: t('browse.too_many_zip_downloads_at') })
      else patch({ snackbar: describeApiError(error, entryEncryption ? t('browse.zip_download_failed') : t('browse.download_failed')) })
    }
  }
  const downloadSelection = () => { void downloadTargets(selected.length ? selected : contextEntry ? [contextEntry] : []) }
  const downloadEntry = (entry: Entry) => { void downloadTargets([entry]) }
  const createFolder = async (name: string) => { patch({ newFolderOpen: false }); try { await mkdir.mutateAsync({ parent: path, name }) } catch (error) { patch({ snackbar: describeApiError(error, t('browse.could_not_create_folder')) }) } }
  const doRename = async (name: string) => { const target = renameTarget; patch({ renameTarget: null }); if (!target) return; try { await rename.mutateAsync({ path: joinPath(path, target.name), newName: name }) } catch (error) { patch({ snackbar: describeApiError(error, t('common.could_not_rename')) }) } }
  const doDelete = async () => {
    patch({ deleteOpen: false })
    const targets = selected.length ? selected : contextEntry ? [contextEntry] : []
    const paths = targets.map((entry) => joinPath(path, entry.name))
    if (!paths.length) return
    try { const result = await remove.mutateAsync(paths); patch({ operation: { kind: 'delete', results: result.results, jobs: [] } }); selection.clear(); const failed = result.results.find((item) => !item.ok); patch({ snackbar: failed ? (batchErrorKey(failed.error)?.key ? t(batchErrorKey(failed.error)!.key, batchErrorKey(failed.error)!.params) : t('browse.delete_failed')) : t('common.done') }) } catch (error) { patch({ snackbar: describeApiError(error, t('browse.delete_failed')) }) }
  }
  const showUploadConflict = (name: string, retry: (policy: OnConflict) => void, count: number) => patch({ conflictName: count === 1 ? name : `${name} (+${count - 1})`, conflictRetry: () => retry, conflictOpen: true })
  const handleUploadFiles = (files: FileList | File[]) => {
    const incoming = Array.from(files); if (!incoming.length) return
    const conflicts = incoming.filter((file) => entries.some((entry) => entry.name === file.name))
    if (!conflicts.length) { void addFiles(incoming, path); return }
    showUploadConflict(conflicts[0].name, (policy) => {
      patch({ conflictOpen: false })
      if (policy === 'overwrite') { void addFiles(incoming, path); return }
      if (policy === 'skip') { const conflictNames = new Set(conflicts.map((file) => file.name)); const remaining = incoming.filter((file) => !conflictNames.has(file.name)); if (remaining.length) void addFiles(remaining, path); return }
      const handedOut = new Set<string>(); const renamed = incoming.map((file) => { if (!entries.some((entry) => entry.name === file.name)) return file; const name = uniqueUploadName(file.name, (candidate) => entries.some((entry) => entry.name === candidate) || handedOut.has(candidate)); handedOut.add(name); return new File([file], name, { type: file.type, lastModified: file.lastModified }) }); void addFiles(renamed, path)
    }, conflicts.length)
  }
  const handleUploadEntries = (picked: readonly PickedEntry[]) => {
    if (!picked.length) return
    const conflicts = picked.filter((entry) => !entry.relativePath && entries.some((listed) => listed.name === entry.file.name))
    if (!conflicts.length) { void addEntries(picked, path); return }
    showUploadConflict(conflicts[0].file.name, (policy) => {
      patch({ conflictOpen: false })
      if (policy === 'overwrite') { void addEntries(picked, path); return }
      if (policy === 'skip') { const conflictNames = new Set(conflicts.map((entry) => entry.file.name)); const remaining = picked.filter((entry) => entry.relativePath || !conflictNames.has(entry.file.name)); if (remaining.length) void addEntries(remaining, path); return }
      const handedOut = new Set<string>(); const renamed = picked.map((entry) => { if (entry.relativePath || !entries.some((listed) => listed.name === entry.file.name)) return entry; const name = uniqueUploadName(entry.file.name, (candidate) => entries.some((listed) => listed.name === candidate) || handedOut.has(candidate)); handedOut.add(name); return { file: new File([entry.file], name, { type: entry.file.type, lastModified: entry.file.lastModified }), relativePath: entry.relativePath } }); void addEntries(renamed, path)
    }, conflicts.length)
  }
  const onDrop = async (event: DragEvent) => { event.preventDefault(); patch({ dragOver: false }); if (!event.dataTransfer) return; if (!canCreate) { patch({ snackbar: t('error.acl_denied') }); return }; try { handleUploadEntries(await pickedFilesFromDataTransfer(event.dataTransfer)) } catch { patch({ snackbar: t('upload.could_not_start_upload') }) } }
  const actions: RowAction[] = rowActions([...selected], { openInEditor: () => { const entry = actionTarget(); if (entry) void openEditor(entry) }, download: downloadSelection, share: () => { const entry = actionTarget(); if (entry) patch({ shareTarget: entry }) }, rename: () => { const entry = actionTarget(); if (entry) patch({ renameTarget: entry }) }, transfer: requestTransfer, duplicate: () => { void transfer(selected.map((entry) => joinPath(path, entry.name)), path, 'copy', 'rename') }, remove: () => patch({ deleteOpen: true }) }, canCreate)
  const onOpen = (entry: Entry) => { if (entry.kind === 'dir') { navigate(`/b${joinPath(path, entry.name)}`); return }; patch({ previewIndex: entries.indexOf(entry), previewOpen: true }) }
  return { openEditor, requestTransfer, transfer, downloadSelection, downloadEntry, createFolder, doRename, doDelete, handleUploadFiles, handleUploadEntries, onDrop, actions, onOpen, openUnlockFor }
}
