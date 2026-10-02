import type { FormEvent, ReactNode } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { StowButton, StowDialog } from '@/shared/ui'
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
    <StowButton variant="text" onClick={onClose}>
      {t('common.close')}
    </StowButton>
  )
  return (
    <StowDialog
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
    </StowDialog>
  )
}
