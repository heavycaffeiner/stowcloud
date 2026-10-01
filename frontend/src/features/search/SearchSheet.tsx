import type { ReactNode } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { ErrorBoundary } from '../../ui/ErrorBoundary'
import { Icon } from '../../ui/Icon'
import { IconButton } from '../../ui/IconButton'
import { Modal } from '../../ui/Modal'
import { SearchPanel } from './SearchPanel'
import { sheetScope } from './state'
import * as styles from './SearchSheet.css'

/** The desktop search surface, open while `sheetScope` names the folder it was opened from. */
export function SearchSheet() {
  const { t } = useI18n()
  const scope = sheetScope.value
  const onClose = (): void => {
    sheetScope.value = null
  }
  const trailing: ReactNode = (
    <IconButton label={t('search.close')} onClick={onClose}>
      <Icon name="close" />
    </IconButton>
  )
  return (
    <Modal
      open={scope !== null}
      className={styles.root}
      label={t('search.title')}
      onClose={onClose}
      initialFocus={(dialog) => dialog.querySelector<HTMLElement>('[role="searchbox"]')}
    >
      <div className={styles.body}>
        <ErrorBoundary>
          <SearchPanel scope={scope ?? ''} autoFocus onNavigated={onClose} trailing={trailing} />
        </ErrorBoundary>
      </div>
    </Modal>
  )
}
