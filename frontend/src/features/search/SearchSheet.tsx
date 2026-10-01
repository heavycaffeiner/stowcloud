import type { ReactNode } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { ErrorBoundary } from '../../ui/ErrorBoundary'
import { Icon } from '../../ui/Icon'
import { IconButton } from '../../ui/IconButton'
import { Modal } from '../../ui/Modal'
import { SearchPanel } from './SearchPanel'
import { useCloseSearch } from './state'
import * as styles from './SearchSheet.css'

/** Search where there is room beside it: a sheet over the page, open while the URL names a scope. */
export function SearchSheet({ scope }: { readonly scope: string | undefined }) {
  const { t } = useI18n()
  const close = useCloseSearch()
  const trailing: ReactNode = (
    <IconButton label={t('search.close')} onClick={close}>
      <Icon name="close" />
    </IconButton>
  )
  return (
    <Modal
      open={scope !== undefined}
      className={styles.root}
      label={t('search.title')}
      onClose={close}
      initialFocus={(dialog) => dialog.querySelector<HTMLElement>('[role="searchbox"]')}
    >
      <div className={styles.body}>
        <ErrorBoundary>
          <SearchPanel scope={scope ?? ''} autoFocus trailing={trailing} />
        </ErrorBoundary>
      </div>
    </Modal>
  )
}
