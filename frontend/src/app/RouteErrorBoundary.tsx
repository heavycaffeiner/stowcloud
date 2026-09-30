import { isRouteErrorResponse, useNavigate, useRouteError } from 'react-router-dom'
import { useI18n } from '../hooks/use-i18n'
import { Button } from '../ui/Button'

export function RouteErrorBoundary() {
  const error = useRouteError()
  const navigate = useNavigate()
  const { t } = useI18n()
  const message = isRouteErrorResponse(error)
    ? `${error.status} ${error.statusText}`
    : error instanceof Error
      ? error.message
      : t('common.could_not_load_list')

  return (
    <main className="sc-error-page">
      <section className="sc-error-page-card" role="alert">
        <h1>Stowcloud</h1>
        <p>{message}</p>
        <div className="sc-error-page-actions">
          <Button onClick={() => window.location.reload()}>{t('common.retry')}</Button>
          <Button variant="text" onClick={() => void navigate('/b/', { replace: true })}>
            {t('common.back')}
          </Button>
        </div>
      </section>
    </main>
  )
}
