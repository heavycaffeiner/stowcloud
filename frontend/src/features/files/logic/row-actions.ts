// What can be done to a set of rows. The row menu and the selection bar both render this list.
import type { IconName } from '../../../ui/icons'
import { t } from '../../../i18n'
import { isEditableFileName } from './editable-files'
import type { Entry } from '../api'

/** Where a row menu opens and which element opened it; a mouse event fits as is. */
export interface RowMenuAnchor {
  readonly clientX: number
  readonly clientY: number
  readonly currentTarget: EventTarget | null
}

export interface RowAction {
  key: string
  label: string
  icon: IconName
  run: () => void
}

export type RowActionHandler = (targets: readonly Entry[]) => void

export interface RowActionHandlers {
  openInEditor: RowActionHandler
  download: RowActionHandler
  share: RowActionHandler
  rename: RowActionHandler
  transfer: RowActionHandler
  duplicate: RowActionHandler
  remove: RowActionHandler
}

/**
 * The actions that apply to `targets`, in display order. A single-item action needs exactly one target,
 * and a permission gate needs every target to carry the permission.
 */
export function rowActions(targets: readonly Entry[], h: RowActionHandlers, canCreateHere: boolean): RowAction[] {
  const any = targets.length > 0
  const one = targets.length === 1
  const everyCan = (p: (e: Entry) => boolean) => any && targets.every(p)
  const canCopy = everyCan((e) => e.perms.read && e.perms.download)
  const canMove = everyCan((e) => e.perms.read && e.perms.move)
  const all: (Omit<RowAction, 'run'> & { show: boolean; run: RowActionHandler })[] = [
    // Only known text formats: saving a decoded binary file would corrupt it.
    {
      key: 'edit',
      label: t('browse.open_text_editor'),
      icon: 'edit-document',
      show: one && targets[0].kind !== 'dir' && targets[0].perms.read && isEditableFileName(targets[0].name),
      run: h.openInEditor
    },
    {
      key: 'download',
      label: t('common.download'),
      icon: 'download',
      show: canCopy,
      run: h.download
    },
    {
      key: 'share',
      label: t('browse.manage_share_links'),
      icon: 'link',
      show: one && everyCan((e) => e.perms.share),
      run: h.share
    },
    {
      key: 'rename',
      label: t('common.rename'),
      icon: 'rename',
      show: one && everyCan((e) => e.perms.rename),
      run: h.rename
    },
    // The destination picker gates each mode on its own, so either mode is enough here.
    { key: 'transfer', label: t('dest.move_or_copy'), icon: 'move', show: canCopy || canMove, run: h.transfer },
    // A duplicate is a copy into the folder on screen, so it also needs Create there.
    {
      key: 'duplicate',
      label: t('browse.duplicate'),
      icon: 'copy',
      show: canCreateHere && canCopy,
      run: h.duplicate
    },
    {
      key: 'delete',
      label: t('common.delete'),
      icon: 'delete',
      show: everyCan((e) => e.perms.delete),
      run: h.remove
    }
  ]
  return all.filter((a) => a.show).map(({ key, label, icon, run }) => ({ key, label, icon, run: () => run(targets) }))
}
