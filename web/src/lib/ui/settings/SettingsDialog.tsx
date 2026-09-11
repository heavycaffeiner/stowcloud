import type { ReactNode } from 'react'
import { t } from '../../i18n'
import { Button } from '../Button'
import { Dialog } from '../Dialog'
interface SettingsDialogProps {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  actions?: ReactNode
  dismissible?: boolean
}

export function SettingsDialog({ open, title, onClose, children, actions, dismissible = true }: SettingsDialogProps) {
  const defaultActions = <Button variant="text" onClick={onClose}>{t('common.close')}</Button>
  return (
    <Dialog open={open} title={title} onClose={onClose} role="dialog" closedby={dismissible ? 'any' : 'none'} className="sc-settings-dialog" actions={actions ?? defaultActions}>
      <div className="sc-settings-dialog__body">{children}</div>
    </Dialog>
  )
}
