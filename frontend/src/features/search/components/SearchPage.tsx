import { useI18n } from '../../../hooks/use-i18n'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { StowIconButton } from '@/shared/ui'
import { SearchPanel } from './SearchPanel'
import { useCloseSearch } from '../state'
import * as styles from './SearchPage.css'

/** Search on a phone: it takes the page area in place of the page it was opened over. */
export function SearchPage({ scope }: { readonly scope: string }) {
  const { t } = useI18n()
  const close = useCloseSearch()
  useDocumentTitle(t('search.title'))

  return (
    <section className={styles.root}>
      <div className={styles.inner}>
        <header className={styles.header}>
          <StowIconButton label={t('common.back')} icon="chevron_left" onClick={close} />
          <h1 className={styles.title}>{t('search.title')}</h1>
        </header>
        <SearchPanel scope={scope} autoFocus />
      </div>
    </section>
  )
}
