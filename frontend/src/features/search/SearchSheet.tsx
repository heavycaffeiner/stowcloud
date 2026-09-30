import type { ReactNode } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { Icon } from '../../ui/Icon'
import { IconButton } from '../../ui/IconButton'
import { Modal } from '../../ui/Modal'
import { SearchPanel } from './SearchPanel'

export interface SearchSheetProps {
  readonly open: boolean
  readonly scope?: string
  readonly onclose: () => void
}

export function SearchSheet({ open, scope = '', onclose }: SearchSheetProps) {
  const { t } = useI18n()
  const trailing: ReactNode = (
    <IconButton label={t('search.close')} onClick={onclose}>
      <Icon name="close" />
    </IconButton>
  )
  return (
    <Modal
      open={open}
      className="sc-search-sheet"
      label={t('search.title')}
      onClose={onclose}
      initialFocus={(dialog) => dialog.querySelector<HTMLElement>('input[type="search"]')}
    >
      <div className="sc-search-sheet-body">
        <SearchPanel scope={scope} autofocus onnavigated={onclose} trailing={trailing} />
      </div>
    </Modal>
  )
}
