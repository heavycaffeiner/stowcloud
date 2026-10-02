import type { FormEvent, ReactNode } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import * as styles from './SettingsDialog.css'
interface SettingsDialogProps {
  open: boolean
  title: string
  onClose?: () => void
  onClosed?: () => void
  /** Makes the body a form, so Enter in a field submits it. */
  onSubmit?: (event: FormEvent<HTMLFormElement>) => void
  children: ReactNode
  actions?: ReactNode
  dismissible?: boolean
}

export function SettingsDialog({
  open,
  title,
  onClose,
  onClosed,
  onSubmit,
  children,
  actions,
  dismissible = true
}: SettingsDialogProps) {
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
      onClosed={onClosed}
      role="dialog"
      dismissible={dismissible}
      actions={actions ?? defaultActions}
    >
      {onSubmit ? (
        <form className={styles.body} onSubmit={onSubmit}>
          {children}
        </form>
      ) : (
        <div className={styles.body}>{children}</div>
      )}
    </Dialog>
  )
}
