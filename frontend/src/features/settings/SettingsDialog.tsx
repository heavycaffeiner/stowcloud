import type { ReactNode } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { Button } from '../../ui/Button'
import { Dialog } from '../../ui/Dialog'
interface SettingsDialogProps {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  actions?: ReactNode
  dismissible?: boolean
}

export function SettingsDialog({ open, title, onClose, children, actions, dismissible = true }: SettingsDialogProps) {
  const { t } = useI18n()
  const defaultActions = (
    <Button variant="text" onClick={onClose}>
      {t('common.close')}
    </Button>
  )
  return (
    <Dialog
      open={open}
      title={title}
      onClose={onClose}
      role="dialog"
      dismissible={dismissible}
      className="sc-settings-dialog"
      actions={actions ?? defaultActions}
    >
      <div className="sc-settings-dialog-body">{children}</div>
    </Dialog>
  )
}
