import { useNavigate, useSearchParams } from 'react-router-dom'
import { useI18n } from '../../../hooks/use-i18n'
import { SearchPanel } from '../../../features/search/SearchPanel'
import { useDocumentTitle } from '../../hooks/use-document-title'
import { Icon } from '../../../ui/Icon'
import * as styles from './SearchPage.css'

export function SearchPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const scope = searchParams.get('path') ?? ''
  useDocumentTitle(t('search.title'))

  return (
    <section className={styles.root}>
      <div className={styles.inner}>
        <header className={styles.header}>
          <button
            type="button"
            className={styles.routeBack}
            aria-label={t('common.back')}
            onClick={() => void navigate(scope ? `/b${scope}` : '/b/')}
          >
            <Icon name="chevron_left" />
          </button>
          <h1 className={styles.title}>{t('search.title')}</h1>
        </header>
        <SearchPanel scope={scope} autoFocus />
      </div>
    </section>
  )
}
