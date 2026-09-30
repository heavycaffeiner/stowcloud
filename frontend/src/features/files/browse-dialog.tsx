import type { ReactNode } from 'react'
import { Dialog } from '../../ui/Dialog'
import * as styles from './browse-dialog.css'

export interface BrowseDialogProps {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  actions?: ReactNode
}

export function BrowseDialog({ open, title, onClose, children, actions }: BrowseDialogProps) {
  return (
    <Dialog open={open} title={title} onClose={onClose} actions={actions} className={styles.root}>
      <div className={styles.body}>{children}</div>
    </Dialog>
  )
}
