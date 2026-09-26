import { isRouteErrorResponse, useNavigate, useRouteError } from 'react-router-dom'
import { useI18n } from '../lib/i18n/use-i18n'

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
      <section className="sc-error-page__card" role="alert">
        <h1>Stowcloud</h1>
        <p>{message}</p>
        <div className="sc-error-page__actions">
          <mdui-button variant="filled" onClick={() => window.location.reload()}>{t('common.retry')}</mdui-button>
          <mdui-button variant="text" onClick={() => void navigate('/b/', { replace: true })}>{t('common.back')}</mdui-button>
        </div>
      </section>
    </main>
  )
}
