import type { Dispatch, DragEvent } from 'react'
import { joinPath } from '../../../lib/path-utils'
import { batchErrorKey, describeApiError } from '../../../api/error-text'
import { encryptionForLabel, shareLabelOf } from '../../../lib/crypto/encrypted-shares'
import { FileTooLargeError, isUnlocked, LockedSessionError } from '../../../lib/crypto/e2ee'
import { downloadEncryptedFile, downloadEncryptedFolder } from '../../../lib/crypto/download-sw'
import { downloadPath, triggerUrlDownload } from '../../../lib/format/download'
import { selection } from '../../../lib/store/selection.store'
import { pickedFilesFromDataTransfer } from '../../../lib/upload/directory-picker'
import { rowActions, type RowAction } from '../logic/row-actions'
import { browseTransferSources, runBrowseTransfer } from '../logic/browse-transfer'
import { createBrowseUploadActions } from '../logic/browse-upload'
import type { BrowseState } from '../logic/browse-types'
import type { StatePatch } from '../../../lib/merge-state'
import { ApiError } from '../../../api/fetcher'
import {
  useArchiveTicket,
  useCopyFiles,
  useDeleteFiles,
  useMkdir,
  useMoveFiles,
  useRename,
  type Entry,
  type OnConflict
} from '../api'
import type { ShareEncryption } from '../../shares/api'

type Patch = Dispatch<StatePatch<BrowseState>>

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

export function useBrowseActions(context: BrowseActionContext) {
  const { path, entries, selected, contextEntry, renameTarget, canCreate, navigate, t, patch } = context
  const mkdir = useMkdir()
  const rename = useRename()
  const remove = useDeleteFiles()
  const move = useMoveFiles()
  const copy = useCopyFiles()
  const archive = useArchiveTicket()
  const uploads = createBrowseUploadActions({ entries, path, patch })

  const openUnlockFor = (target: { salt: string; verifier: string }, retry: () => void) =>
    patch({ unlockTarget: { salt: target.salt, verifier: target.verifier, retry } })
  const openEditor = async (entry: Entry) => {
    if (entry.kind === 'dir') return
    const href = `/edit${joinPath(path, entry.name)}`
    try {
      const entryEncryption = await encryptionForLabel(shareLabelOf(entry.path))
      if (entryEncryption && !isUnlocked(entryEncryption.salt)) {
        openUnlockFor(entryEncryption, () => {
          navigate(href)
        })
        return
      }
      navigate(href)
    } catch (error) {
      patch({ snackbar: describeApiError(error, t('common.could_not_load_list')) })
    }
  }
  const actionTarget = () => selected[0] ?? contextEntry
  const requestTransfer = () => {
    const targets = selected.length ? selected : contextEntry ? [contextEntry] : []
    if (!targets.length) return
    const sources = browseTransferSources(path, selected, contextEntry)
    patch({
      destSources: sources,
      destCanCopy: targets.every((entry) => entry.perms.read && entry.perms.download),
      destCanMove: targets.every((entry) => entry.perms.read && entry.perms.move),
      destOpen: true
    })
  }
  const transfer = async (
    paths: string[],
    dest: string,
    kind: 'move' | 'copy',
    onConflict: OnConflict
  ): Promise<void> => {
    await runBrowseTransfer(
      paths,
      dest,
      kind,
      onConflict,
      { move, copy },
      patch,
      t,
      (retryPaths, retryDest, retryKind, retryPolicy) => {
        void transfer(retryPaths, retryDest, retryKind, retryPolicy)
      }
    )
  }
  const downloadTargets = async (targets: readonly Entry[]): Promise<void> => {
    if (!targets.length) return
    let entryEncryption: ShareEncryption | null = null
    try {
      entryEncryption = await encryptionForLabel(shareLabelOf(targets[0].path))
      if (entryEncryption) {
        for (const entry of targets)
          entry.kind === 'dir' ? await downloadEncryptedFolder(entry.path) : await downloadEncryptedFile(entry)
      } else if (targets.length === 1 && targets[0].kind !== 'dir') await downloadPath(targets[0].path)
      else {
        const ticket = await archive.mutateAsync({
          paths: targets.map((entry) => entry.path),
          name: targets.length === 1 ? `${targets[0].name}.zip` : 'archive.zip'
        })
        triggerUrlDownload(ticket.url, ticket.name)
      }
    } catch (error) {
      if (error instanceof LockedSessionError && entryEncryption)
        openUnlockFor(entryEncryption, () => {
          void downloadTargets(targets)
        })
      else if (error instanceof FileTooLargeError) patch({ snackbar: t('browse.download_too_large_to_buffer') })
      else if (error instanceof ApiError && error.code === 'rate.limited')
        patch({ snackbar: t('browse.too_many_zip_downloads_at') })
      else
        patch({
          snackbar: describeApiError(
            error,
            entryEncryption ? t('browse.zip_download_failed') : t('browse.download_failed')
          )
        })
    }
  }
  const downloadSelection = () => {
    void downloadTargets(selected.length ? selected : contextEntry ? [contextEntry] : [])
  }
  const downloadEntry = (entry: Entry) => {
    void downloadTargets([entry])
  }
  const createFolder = async (name: string) => {
    patch({ newFolderOpen: false })
    try {
      await mkdir.mutateAsync({ parent: path, name })
    } catch (error) {
      patch({ snackbar: describeApiError(error, t('browse.could_not_create_folder')) })
    }
  }
  const doRename = async (name: string) => {
    const target = renameTarget
    patch({ renameTarget: null })
    if (!target) return
    try {
      await rename.mutateAsync({ path: joinPath(path, target.name), newName: name })
    } catch (error) {
      patch({ snackbar: describeApiError(error, t('common.could_not_rename')) })
    }
  }
  const doDelete = async () => {
    patch({ deleteOpen: false })
    const targets = selected.length ? selected : contextEntry ? [contextEntry] : []
    const paths = targets.map((entry) => joinPath(path, entry.name))
    if (!paths.length) return
    try {
      const result = await remove.mutateAsync(paths)
      patch({ operation: { kind: 'delete', results: result.results, jobs: [] } })
      selection.clear()
      const failed = result.results.find((item) => !item.ok)
      patch({
        snackbar: failed
          ? batchErrorKey(failed.error)?.key
            ? t(batchErrorKey(failed.error)!.key, batchErrorKey(failed.error)!.params)
            : t('browse.delete_failed')
          : t('common.done')
      })
    } catch (error) {
      patch({ snackbar: describeApiError(error, t('browse.delete_failed')) })
    }
  }
  const onDrop = async (event: DragEvent) => {
    event.preventDefault()
    patch({ dragOver: false })
    if (!event.dataTransfer) return
    if (!canCreate) {
      patch({ snackbar: t('error.acl_denied') })
      return
    }
    try {
      uploads.handleEntries(await pickedFilesFromDataTransfer(event.dataTransfer))
    } catch {
      patch({ snackbar: t('upload.could_not_start_upload') })
    }
  }
  const actions: RowAction[] = rowActions(
    [...selected],
    {
      openInEditor: () => {
        const entry = actionTarget()
        if (entry) void openEditor(entry)
      },
      download: downloadSelection,
      share: () => {
        const entry = actionTarget()
        if (entry) patch({ shareTarget: entry })
      },
      rename: () => {
        const entry = actionTarget()
        if (entry) patch({ renameTarget: entry })
      },
      transfer: requestTransfer,
      duplicate: () => {
        void transfer(
          selected.map((entry) => joinPath(path, entry.name)),
          path,
          'copy',
          'rename'
        )
      },
      remove: () => patch({ deleteOpen: true })
    },
    canCreate
  )
  const onOpen = (entry: Entry) => {
    if (entry.kind === 'dir') {
      navigate(`/b${joinPath(path, entry.name)}`)
      return
    }
    patch({ previewIndex: entries.indexOf(entry), previewOpen: true })
  }
  return {
    openEditor,
    requestTransfer,
    transfer,
    downloadSelection,
    downloadEntry,
    createFolder,
    doRename,
    doDelete,
    handleUploadFiles: uploads.handleFiles,
    handleUploadEntries: uploads.handleEntries,
    onDrop,
    actions,
    onOpen,
    openUnlockFor
  }
}
