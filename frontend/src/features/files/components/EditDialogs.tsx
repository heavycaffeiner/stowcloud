import { overlay } from 'overlay-kit'
import { useI18n } from '../../../hooks/use-i18n'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { useKeyChange } from '../../shares/e2ee-store'

export type ConflictChoice = 'reload' | 'overwrite' | null
export type LeaveChoice = 'stay' | 'discard' | 'save'

/** Asks what to do with a draft whose file changed on the server. Null when cancelled. */
export function askEditConflict(name: string, weak: boolean): Promise<ConflictChoice> {
  return overlay.openAsync<ConflictChoice>(({ isOpen, close, unmount }) => (
    <EditConflictDialog open={isOpen} name={name} weak={weak} onChoose={close} onClosed={unmount} />
  ))
}

/** Asks what happens to unsaved changes before the editor is left. */
export function askLeaveEditor(name: string, canSave: boolean): Promise<LeaveChoice> {
  return overlay.openAsync<LeaveChoice>(({ isOpen, close, unmount }) => (
    <LeaveEditorDialog open={isOpen} name={name} canSave={canSave} onChoose={close} onClosed={unmount} />
  ))
}

interface ChoiceDialogProps<T> {
  open: boolean
  name: string
  onChoose: (choice: T) => void
  onClosed: () => void
}

function EditConflictDialog({
  open,
  name,
  weak,
  onChoose,
  onClosed
}: ChoiceDialogProps<ConflictChoice> & { weak: boolean }) {
  const { t } = useI18n()
  // A lock takes away the plaintext both answers act on.
  useKeyChange((change) => {
    if (change.kind === 'lock') onChoose(null)
  })
  return (
    <Dialog
      open={open}
      title={weak ? t('edit_conflict.file_may_have_changed') : t('edit_conflict.file_changed_elsewhere')}
      dismissible={false}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" onClick={() => onChoose(null)}>
            {t('common.cancel')}
          </Button>
          <Button variant="outlined" onClick={() => onChoose('reload')}>
            {t('edit_conflict.reload_newer_version')}
          </Button>
          <Button onClick={() => onChoose('overwrite')}>{t('edit_conflict.overwrite_with_my_changes')}</Button>
        </>
      }
    >
      <p>
        {weak
          ? t('edit_conflict.may_have_been_modified_elsewhere', { name })
          : t('edit_conflict.was_modified_elsewhere_after_you', { name })}
      </p>
      <p>{t('edit_conflict.choose_whether_keep_what_you')}</p>
      {weak ? <p>{t('edit_conflict.change_check_is_advisory')}</p> : null}
    </Dialog>
  )
}

function LeaveEditorDialog({
  open,
  name,
  canSave,
  onChoose,
  onClosed
}: ChoiceDialogProps<LeaveChoice> & { canSave: boolean }) {
  const { t } = useI18n()
  // A lock seals the draft away, so the answer waits until the share is unlocked again.
  useKeyChange((change) => {
    if (change.kind === 'lock') onChoose('stay')
  })
  return (
    <Dialog
      open={open}
      title={t('editor.unsaved_changes')}
      dismissible={false}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" onClick={() => onChoose('stay')}>
            {t('editor.stay')}
          </Button>
          <Button variant="outlined" onClick={() => onChoose('discard')}>
            {t('editor.discard_and_leave')}
          </Button>
          <Button disabled={!canSave} onClick={() => onChoose('save')}>
            {t('editor.save_and_leave')}
          </Button>
        </>
      }
    >
      <p>{t('editor.unsaved_changes_prompt', { name })}</p>
    </Dialog>
  )
}
