import { useI18n } from '../../../hooks/use-i18n'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { Icon } from '../../../ui/Icon'
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
          <button type="button" className={styles.routeBack} aria-label={t('common.back')} onClick={close}>
            <Icon name="chevron_left" />
          </button>
          <h1 className={styles.title}>{t('search.title')}</h1>
        </header>
        <SearchPanel scope={scope} autoFocus />
      </div>
    </section>
  )
}
