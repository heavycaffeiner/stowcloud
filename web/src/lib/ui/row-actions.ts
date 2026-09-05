// row-actions.ts — the one definition of "what can be done to the selected
// rows", shared by the right-click menu and the selection bar.
//
// It lives in its own module rather than inside the browse page so that the
// agreement between those two surfaces is testable without mounting anything.
// Both of them render the *same array value*, so an action cannot be added to
// one and missed on the other; this module is where adding, removing or gating
// one is allowed to happen, and the only place.
import type { Entry } from '../api/client'
import { icons } from '../icons'
import { t } from '../i18n'

export interface RowAction {
  key: string
  label: string
  icon: (typeof icons)[keyof typeof icons]
  run: () => void
}

/** What the browse page does when an action is picked. Passed in rather than
 *  imported so this module stays free of page state. */
export interface RowActionHandlers {
  openInEditor: () => void
  download: () => void
  share: () => void
  rename: () => void
  transfer: () => void
  duplicate: () => void
  remove: () => void
}

/**
 * The actions that apply to `targets`, in display order.
 *
 * Three rules decide `show`, and they are the whole of it:
 *
 * 1. An action that can only mean one thing at a time needs exactly one
 *    target. "Open in the text editor" and "Manage share links" both open a
 *    screen about a single file; with three rows selected there is no answer
 *    to which one.
 * 2. An action gated on a permission needs *every* target to carry it, never
 *    just the first. Offering "share" because the first of five rows allows it
 *    is an offer the other four will refuse.
 * 3. Every action the server can refuse on permission is gated. Only `share`
 *    used to be, so an account holding read alone was offered rename, move,
 *    duplicate and delete, clicked one, and got a snackbar. The rows already
 *    carry the caller's rights at each path; this reads them.
 *
 * A duplicate lands in the folder on screen, so it needs the create right
 * there, which the listing reports and the rows do not. A move or copy picks
 * its destination afterwards, and the picker gates itself on the rights of
 * whatever folder was chosen.
 *
 * This used to take a single `Entry` and each caller picked its own: the menu
 * passed the right-clicked row, the bar passed `selected[0]`. Same function,
 * different answers, so a mixed file-and-folder selection showed "open in the
 * text editor" whenever the *first* row happened to be a file.
 */
export function rowActions(targets: Entry[], h: RowActionHandlers, canCreateHere: boolean): RowAction[] {
  const any = targets.length > 0
  const one = targets.length === 1
  const everyCan = (p: (e: Entry) => boolean) => any && targets.every(p)

  return [
    // `edit-document`, not the plain file glyph it used to carry. These render
    // icon-only in the selection bar now, and "a document" said nothing about
    // opening one for editing.
    { key: 'edit', label: t('browse.open_text_editor'), icon: icons['edit-document'], show: one && targets[0].kind !== 'dir' && targets[0].perms.read, run: h.openInEditor },
    { key: 'download', label: t('common.download'), icon: icons.download, show: everyCan((e) => e.perms.download), run: h.download },
    { key: 'share', label: t('browse.manage_share_links'), icon: icons.link, show: one && everyCan((e) => e.perms.share), run: h.share },
    { key: 'rename', label: t('common.rename'), icon: icons.rename, show: one && everyCan((e) => e.perms.rename), run: h.rename },
    { key: 'transfer', label: t('dest.move_or_copy'), icon: icons.move, show: everyCan((e) => e.perms.move || e.perms.read), run: h.transfer },
    { key: 'duplicate', label: t('browse.duplicate'), icon: icons.copy, show: canCreateHere && everyCan((e) => e.perms.read), run: h.duplicate },
    { key: 'delete', label: t('common.delete'), icon: icons.delete, show: everyCan((e) => e.perms.delete), run: h.remove }
  ]
    .filter((a) => a.show)
    .map(({ key, label, icon, run }) => ({ key, label, icon, run }))
}
