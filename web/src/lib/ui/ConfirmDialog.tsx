import { Button } from './Button'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../i18n/use-i18n'
export function ConfirmDialog({ open, title, message, confirmLabel, cancelLabel, danger = true, onClose, onConfirm }: { open: boolean; title: string; message: string; confirmLabel?: string; cancelLabel?: string; danger?: boolean; onClose: () => void; onConfirm: () => void }) { const { t } = useI18n(); return <BrowseDialog open={open} title={title} onClose={onClose} actions={<><Button variant="text" onClick={onClose}>{cancelLabel ?? t('common.cancel')}</Button><Button danger={danger} onClick={onConfirm}>{confirmLabel ?? t('common.ok')}</Button></>}><p>{message}</p></BrowseDialog> }
