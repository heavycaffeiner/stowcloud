import type { DragEvent, ReactNode } from 'react'
import { useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useSignalEffect } from '@preact/signals-react'
import { joinPath } from '../../../lib/path-utils'
import { batchErrorKey, describeApiError } from '../../../api/error-text'
import { ApiError } from '../../../api/fetcher'
import { t } from '../../../lib/i18n'
import { encryptionForLabel, shareLabelOf } from '../../../lib/crypto/encrypted-shares'
import { FileTooLargeError, isUnlocked, LockedSessionError } from '../../../lib/crypto/e2ee'
import { downloadEncryptedFile, downloadEncryptedFolder } from '../../../lib/crypto/download-sw'
import { downloadPath, triggerUrlDownload } from '../../../lib/format/download'
import {
  filesFromWebkitDirectoryInput,
  pickDirectory,
  pickedFilesFromDataTransfer,
  supportsDirectoryPicker
} from '../../../lib/upload/directory-picker'
import { MenuItem, MenuList, openMenu, type MenuAnchor } from '../../../ui/Menu'
import { openPreview } from '../../preview/PreviewDialog'
import { askUnlock } from '../../shares/UnlockShareDialog'
import { openShareManager } from '../../shares/ShareManageDialog'
import type { ShareEncryption } from '../../shares/api'
import { askNewFolderName } from '../NewFolderDialog'
import { askNewName } from '../RenameDialog'
import { confirmDelete } from '../DeleteDialog'
import { pickDestination } from '../DestinationPickerDialog'
import { createMenuRequest } from '../create-menu'
import { notice, operation } from '../browse-page'
import { selection } from '../selection'
import { rowActions, type RowAction, type RowMenuAnchor } from '../logic/row-actions'
import { runBrowseTransfer } from '../logic/browse-transfer'
import { uploadEntries, uploadFiles } from '../logic/browse-upload'
import { useArchiveTicket, useCopyFiles, useDeleteFiles, useMkdir, useMoveFiles, useRename, type Entry } from '../api'
import type { useBrowseListing } from './use-browse-listing'

export type BrowseListing = ReturnType<typeof useBrowseListing>
export type BrowseActions = ReturnType<typeof useBrowseActions>

const fail = (error: unknown, fallback: string): void => {
  notice.value = describeApiError(error, fallback)
}

/** True once the share's key is in this session, asking for the passphrase when it is not. */
async function ensureUnlocked(encryption: ShareEncryption | null): Promise<boolean> {
  return !encryption || isUnlocked(encryption.salt) || askUnlock(encryption)
}

/**
 * What the folder page lets the user do. Dialogs and menus open as overlays and hand back their answer,
 * so none of their state lives in the page.
 */
export function useBrowseActions(path: string, listing: BrowseListing) {
  const navigate = useNavigate()
  const mkdir = useMkdir()
  const rename = useRename()
  const remove = useDeleteFiles()
  const move = useMoveFiles()
  const copy = useCopyFiles()
  const archive = useArchiveTicket()
  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)
  const { entries, selected, selectedNames, root, canCreate } = listing
  const pathOf = (entry: Entry): string => joinPath(path, entry.name)

  const openEditor = async (entry: Entry): Promise<void> => {
    if (entry.kind === 'dir') return
    try {
      if (await ensureUnlocked(await encryptionForLabel(shareLabelOf(entry.path))))
        void navigate(`/edit${pathOf(entry)}`)
    } catch (error) {
      fail(error, t('common.could_not_load_list'))
    }
  }

  const download = async (targets: readonly Entry[]): Promise<void> => {
    if (!targets.length) return
    let encryption: ShareEncryption | null = null
    try {
      encryption = await encryptionForLabel(shareLabelOf(targets[0].path))
      if (encryption) {
        for (const entry of targets)
          await (entry.kind === 'dir' ? downloadEncryptedFolder(entry.path) : downloadEncryptedFile(entry))
      } else if (targets.length === 1 && targets[0].kind !== 'dir') await downloadPath(targets[0].path)
      else {
        const ticket = await archive.mutateAsync({
          paths: targets.map((entry) => entry.path),
          name: targets.length === 1 ? `${targets[0].name}.zip` : 'archive.zip'
        })
        triggerUrlDownload(ticket.url, ticket.name)
      }
    } catch (error) {
      if (error instanceof LockedSessionError && encryption) {
        if (await askUnlock(encryption)) await download(targets)
      } else if (error instanceof FileTooLargeError) notice.value = t('browse.download_too_large_to_buffer')
      else if (error instanceof ApiError && error.code === 'rate.limited')
        notice.value = t('browse.too_many_zip_downloads_at')
      else fail(error, encryption ? t('browse.zip_download_failed') : t('browse.download_failed'))
    }
  }

  const share = (entry: Entry): void =>
    void openShareManager({ path: pathOf(entry), targetName: entry.name, targetIsDir: entry.kind === 'dir' })

  const renameEntry = async (entry: Entry): Promise<void> => {
    const newName = await askNewName(entry.name)
    if (newName === null) return
    try {
      await rename.mutateAsync({ path: pathOf(entry), newName })
    } catch (error) {
      fail(error, t('common.could_not_rename'))
    }
  }

  const transfer = async (targets: readonly Entry[]): Promise<void> => {
    const sources = targets.map(pathOf)
    const choice = await pickDestination({
      sources,
      canCopy: targets.every((entry) => entry.perms.read && entry.perms.download),
      canMove: targets.every((entry) => entry.perms.read && entry.perms.move)
    })
    if (choice) await runBrowseTransfer(sources, choice.dest, choice.kind, 'fail', choice.kind === 'move' ? move : copy)
  }

  const removeEntries = async (targets: readonly Entry[]): Promise<void> => {
    const confirmed = await confirmDelete({
      count: targets.length,
      externalShare: Boolean(root?.shared_externally),
      trashEnabled: Boolean(root?.trash_enabled)
    })
    if (!confirmed) return
    try {
      const result = await remove.mutateAsync(targets.map(pathOf))
      operation.value = { kind: 'delete', results: result.results, jobs: [] }
      selection.clear()
      const failed = result.results.find((item) => !item.ok)
      const key = failed ? batchErrorKey(failed.error) : null
      notice.value = !failed ? t('common.done') : key ? t(key.key, key.params) : t('browse.delete_failed')
    } catch (error) {
      fail(error, t('browse.delete_failed'))
    }
  }

  const actionsFor = (targets: readonly Entry[]): RowAction[] =>
    rowActions(
      targets,
      {
        openInEditor: ([entry]) => void openEditor(entry),
        download: (items) => void download(items),
        share: ([entry]) => share(entry),
        rename: ([entry]) => void renameEntry(entry),
        transfer: (items) => void transfer(items),
        duplicate: (items) => void runBrowseTransfer(items.map(pathOf), path, 'copy', 'rename', copy),
        remove: (items) => void removeEntries(items)
      },
      canCreate
    )

  /** Opens the row menu for `entry`. A row outside the selection becomes the selection first. */
  const openRowMenu = (entry: Entry, anchor: RowMenuAnchor): void => {
    const inSelection = selectedNames.has(entry.name)
    if (!inSelection) selection.only(entry.name, entries.indexOf(entry))
    const actions = actionsFor(inSelection ? selected : [entry])
    const trigger = anchor.currentTarget instanceof HTMLElement ? anchor.currentTarget : null
    openMenu({ x: anchor.clientX, y: anchor.clientY, trigger }, (close) => (
      <MenuList>
        {actions.map((action) => (
          <MenuItem
            key={action.key}
            onClick={() => {
              close()
              action.run()
            }}
          >
            {action.label}
          </MenuItem>
        ))}
      </MenuList>
    ))
  }

  /** Runs a row action on the selection, if the row menu would offer it there. */
  const runOnSelection = (key: string): void =>
    actionsFor(selected)
      .find((action) => action.key === key)
      ?.run()

  const createFolder = async (): Promise<void> => {
    const name = await askNewFolderName()
    if (name === null) return
    try {
      await mkdir.mutateAsync({ parent: path, name })
    } catch (error) {
      fail(error, t('browse.could_not_create_folder'))
    }
  }

  const chooseFolder = async (): Promise<void> => {
    if (!supportsDirectoryPicker()) {
      folderInput.current?.click()
      return
    }
    // A cancelled picker rejects; there is nothing to report.
    const picked = await pickDirectory().catch(() => null)
    if (picked) await uploadEntries(picked, path, entries)
  }

  const createMenu = (close: () => void): ReactNode => (
    <MenuList>
      <MenuItem
        onClick={() => {
          close()
          void createFolder()
        }}
      >
        {t('common.new_folder')}
      </MenuItem>
      <MenuItem
        onClick={() => {
          close()
          fileInput.current?.click()
        }}
      >
        {t('common.upload')}
      </MenuItem>
      <MenuItem
        onClick={() => {
          close()
          void chooseFolder()
        }}
      >
        {t('browse.upload_folder')}
      </MenuItem>
    </MenuList>
  )

  const openCreateMenu = (at: MenuAnchor): void => {
    if (canCreate) openMenu(at, createMenu)
  }

  // The sidebar's New button asks for the create menu from outside the page.
  useSignalEffect(() => {
    const at = createMenuRequest.value
    if (at === null) return
    createMenuRequest.value = null
    if (canCreate) openMenu(at, createMenu)
    else notice.value = t('error.acl_denied')
  })

  const drop = async (event: DragEvent): Promise<void> => {
    event.preventDefault()
    if (!canCreate) {
      notice.value = t('error.acl_denied')
      return
    }
    const picked = await pickedFilesFromDataTransfer(event.dataTransfer).catch(() => null)
    if (picked) await uploadEntries(picked, path, entries)
    else notice.value = t('upload.could_not_start_upload')
  }

  const open = (entry: Entry): void => {
    if (entry.kind === 'dir') {
      void navigate(`/b${pathOf(entry)}`)
      return
    }
    openPreview({
      entries,
      index: entries.indexOf(entry),
      folder: path,
      onDownload: (item) => void download([item]),
      onEdit: (item) => void openEditor(item)
    })
  }

  const unlockFolder = (): void => {
    if (listing.encryption.data) void askUnlock(listing.encryption.data)
  }

  // Hidden inputs that the create menu clicks to open the browser's file and folder pickers.
  const uploadInputs = (
    <>
      <input
        ref={fileInput}
        type="file"
        hidden
        multiple
        aria-label={t('browse.choose_files_upload')}
        onChange={(event) => {
          const files = Array.from(event.currentTarget.files ?? [])
          event.currentTarget.value = ''
          void uploadFiles(files, path, entries)
        }}
      />
      <input
        ref={folderInput}
        type="file"
        hidden
        aria-label={t('browse.choose_folder_upload')}
        {...{ webkitdirectory: '' }}
        onChange={(event) => {
          const picked = filesFromWebkitDirectoryInput(event.currentTarget)
          event.currentTarget.value = ''
          void uploadEntries(picked, path, entries)
        }}
      />
    </>
  )

  return {
    open,
    openRowMenu,
    runOnSelection,
    actionsFor,
    download,
    share,
    createMenu,
    openCreateMenu,
    unlockFolder,
    drop,
    uploadInputs
  }
}
