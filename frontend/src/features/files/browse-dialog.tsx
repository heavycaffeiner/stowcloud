import type { ReactNode } from 'react'
import { Dialog } from '../../lib/ui/Dialog'
import '../../styles/features/files/browse-ui.css.ts'

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
      <div className="sc-browse-dialog-body">{children}</div>
    </Dialog>
  )
}
