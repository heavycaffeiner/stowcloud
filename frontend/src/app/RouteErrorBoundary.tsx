import { isRouteErrorResponse, useNavigate, useRouteError } from 'react-router-dom'
import { useI18n } from '../hooks/use-i18n'
import { Button } from '../ui/Button'

/** Stands in for a route that failed to load or render; `embedded` keeps it inside the app shell. */
export function RouteErrorBoundary({ embedded = false }: { embedded?: boolean }) {
  const error = useRouteError()
  const navigate = useNavigate()
  const { t } = useI18n()
  const notFound = isRouteErrorResponse(error) && error.status === 404

  const card = (
    <section className="sc-error-page-card" role="alert">
      <h1>{notFound ? t('error.page_not_found') : t('error.page_failed')}</h1>
      <p>{notFound ? t('error.page_not_found_hint') : t('error.page_failed_hint')}</p>
      <div className="sc-error-page-actions">
        {notFound ? null : <Button onClick={() => window.location.reload()}>{t('common.retry')}</Button>}
        <Button variant={notFound ? 'filled' : 'text'} onClick={() => void navigate('/b/', { replace: true })}>
          {t('error.go_to_files')}
        </Button>
      </div>
    </section>
  )
  return embedded ? (
    <div className="sc-error-page sc-error-page-embedded">{card}</div>
  ) : (
    <main className="sc-error-page">{card}</main>
  )
}
