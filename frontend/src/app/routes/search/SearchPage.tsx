import { useNavigate, useSearchParams } from 'react-router-dom'
import { useI18n } from '../../../hooks/use-i18n'
import { SearchPanel } from '../../../features/search/SearchPanel'
import { useDocumentTitle } from '../../hooks/use-document-title'
import '../../../styles/app/routes/simple-pages.css.ts'
import { Icon } from '../../../lib/ui/Icon'

export function SearchPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const scope = searchParams.get('path') ?? ''
  useDocumentTitle(t('search.title'))

  return (
    <section className="sc-search-page">
      <div className="sc-search-page-inner">
        <header className="sc-search-page-header">
          <button type="button" className="sc-route-back" aria-label={t('common.back')} onClick={() => void navigate(scope ? `/b${scope}` : '/b/')}>
            <Icon name="chevron_left" />
          </button>
          <h1>{t('search.title')}</h1>
        </header>
        <SearchPanel scope={scope} autofocus />
      </div>
    </section>
  )
}
