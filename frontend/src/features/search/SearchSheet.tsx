import type { ReactNode } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { ErrorBoundary } from '../../ui/ErrorBoundary'
import { Icon } from '../../ui/Icon'
import { IconButton } from '../../ui/IconButton'
import { Modal } from '../../ui/Modal'
import { SearchPanel } from './SearchPanel'

export interface SearchSheetProps {
  readonly open: boolean
  readonly scope?: string
  readonly onClose: () => void
}

export function SearchSheet({ open, scope = '', onClose }: SearchSheetProps) {
  const { t } = useI18n()
  const trailing: ReactNode = (
    <IconButton label={t('search.close')} onClick={onClose}>
      <Icon name="close" />
    </IconButton>
  )
  return (
    <Modal
      open={open}
      className="sc-search-sheet"
      label={t('search.title')}
      onClose={onClose}
      initialFocus={(dialog) => dialog.querySelector<HTMLElement>('input[type="search"]')}
    >
      <div className="sc-search-sheet-body">
        <ErrorBoundary>
          <SearchPanel scope={scope} autoFocus onNavigated={onClose} trailing={trailing} />
        </ErrorBoundary>
      </div>
    </Modal>
  )
}
