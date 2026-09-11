import { useEffect, useRef } from 'react'
import { useI18n } from '../i18n/use-i18n'
import { Button } from './Button'

export interface EditConflictDialogProps {
  open: boolean
  name: string
  weak?: boolean
  onClose: () => void
  onReload: () => void
  onOverwrite: () => void
}

export function EditConflictDialog({ open, name, weak = false, onClose, onReload, onOverwrite }: EditConflictDialogProps) {
  const { t } = useI18n()
  const ref = useRef<HTMLElement>(null)
  useEffect(() => {
    const dialog = ref.current as unknown as { open?: boolean; addEventListener: typeof window.addEventListener; removeEventListener: typeof window.removeEventListener } | null
    if (!dialog) return
    dialog.open = open
    const close = () => { if (open) onClose() }
    dialog.addEventListener('close', close)
    return () => dialog.removeEventListener('close', close)
  }, [open, onClose])
  return (
    <mdui-dialog ref={ref} headline={weak ? t('edit_conflict.file_may_have_changed') : t('edit_conflict.file_changed_elsewhere')}>
      <p>{weak ? t('edit_conflict.may_have_been_modified_elsewhere', { name }) : t('edit_conflict.was_modified_elsewhere_after_you', { name })}</p>
      <p>{t('edit_conflict.choose_whether_keep_what_you')}</p>
      {weak ? <p>{t('edit_conflict.change_check_is_advisory')}</p> : null}
      <Button variant="text" onClick={onClose}>{t('common.cancel')}</Button>
      <Button variant="outlined" onClick={onReload}>{t('edit_conflict.reload_newer_version')}</Button>
      <Button variant="filled" onClick={onOverwrite}>{t('edit_conflict.overwrite_with_my_changes')}</Button>
    </mdui-dialog>
  )
}
