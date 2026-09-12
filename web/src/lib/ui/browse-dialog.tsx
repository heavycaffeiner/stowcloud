import type { ReactNode } from 'react'
import { Dialog } from './Dialog'
import './browse-ui.css'

export interface BrowseDialogProps {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
  actions?: ReactNode
}

export function BrowseDialog({ open, title, onClose, children, actions }: BrowseDialogProps) {
  return (
    <Dialog
      open={open}
      title={title}
      onClose={onClose}
      actions={actions}
      className="sc-browse-dialog"
    >
      <div className="sc-browse-dialog__body">{children}</div>
    </Dialog>
  )
}
