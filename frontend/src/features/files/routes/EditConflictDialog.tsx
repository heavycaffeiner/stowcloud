import { useI18n } from '../../../hooks/use-i18n'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'

export interface EditConflictDialogProps {
  open: boolean
  name: string
  weak?: boolean
  onClose: () => void
  onReload: () => void
  onOverwrite: () => void
}

export function EditConflictDialog({
  open,
  name,
  weak = false,
  onClose,
  onReload,
  onOverwrite
}: EditConflictDialogProps) {
  const { t } = useI18n()
  return (
    <Dialog
      open={open}
      title={weak ? t('edit_conflict.file_may_have_changed') : t('edit_conflict.file_changed_elsewhere')}
      dismissible={false}
      actions={
        <>
          <Button variant="text" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="outlined" onClick={onReload}>
            {t('edit_conflict.reload_newer_version')}
          </Button>
          <Button onClick={onOverwrite}>{t('edit_conflict.overwrite_with_my_changes')}</Button>
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
