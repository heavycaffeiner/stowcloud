import type { ReactNode } from 'react'
import { StowDialog } from '@/shared/ui'
import * as styles from './browse-dialog.css'

export interface BrowseDialogProps {
  open: boolean
  title: string
  onClose: () => void
  onClosed?: () => void
  children: ReactNode
  actions?: ReactNode
}

export function BrowseDialog({ open, title, onClose, onClosed, children, actions }: BrowseDialogProps) {
  return (
    <StowDialog open={open} title={title} onClose={onClose} onClosed={onClosed} actions={actions}>
      <div className={styles.body}>{children}</div>
    </StowDialog>
  )
}
