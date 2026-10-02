import type { ReactNode } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { ErrorBoundary, StowDrawer, StowIconButton } from '@/shared/ui'
import { SearchPanel } from './SearchPanel'
import { useCloseSearch } from '../state'
import * as styles from './SearchSheet.css'

/** Search where there is room beside it: a sheet over the page, open while the URL names a scope. */
export function SearchSheet({ scope }: { readonly scope: string | undefined }) {
  const { t } = useI18n()
  const close = useCloseSearch()
  const trailing: ReactNode = <StowIconButton label={t('search.close')} onClick={close} icon="close" />
  return (
    <StowDrawer
      open={scope !== undefined}
      label={t('search.title')}
      position="top"
      className={styles.root}
      onClose={close}
    >
      <ErrorBoundary>
        <SearchPanel scope={scope ?? ''} autoFocus trailing={trailing} />
      </ErrorBoundary>
    </StowDrawer>
  )
}
