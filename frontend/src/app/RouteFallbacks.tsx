// What a route shows in place of its page: a failure, an unknown address, or
// the session check that runs before the signed-in app. Kept out of the shell
// chunk because they show before it loads or after it failed to.
import type { ReactNode } from 'react'
import { useNavigate, useRouter, type ErrorComponentProps } from '@tanstack/react-router'
import { SessionUnreachableError } from '../features/auth/api'
import { useI18n } from '../hooks/use-i18n'
import { Button } from '../ui/Button'
import { ProgressCircular } from '../ui/ProgressCircular'
import * as styles from './RouteFallbacks.css'
import { cx } from '../ui/cx'

function Card({ embedded, children }: { readonly embedded: boolean; readonly children: ReactNode }) {
  const card = (
    <section className={styles.card} role="alert">
      {children}
    </section>
  )
  // Inside the shell the page area is already the main landmark.
  return embedded ? (
    <div className={cx(styles.root, styles.embedded)}>{card}</div>
  ) : (
    <main className={styles.root}>{card}</main>
  )
}

/** A page that failed to load or render, or an address with no page. The raw error is never shown. */
export function RouteProblem({ notFound = false, embedded = false }: { notFound?: boolean; embedded?: boolean }) {
  const navigate = useNavigate()
  const { t } = useI18n()
  return (
    <Card embedded={embedded}>
      <h1>{notFound ? t('error.page_not_found') : t('error.page_failed')}</h1>
      <p>{notFound ? t('error.page_not_found_hint') : t('error.page_failed_hint')}</p>
      <div>
        {notFound ? null : <Button onClick={() => window.location.reload()}>{t('common.retry')}</Button>}
        <Button
          variant={notFound ? 'filled' : 'text'}
          onClick={() => void navigate({ to: '/b/$', params: { _splat: '' }, replace: true })}
        >
          {t('error.go_to_files')}
        </Button>
      </div>
    </Card>
  )
}

/** The session check could not reach the server. Retry runs the check again. */
function SessionError() {
  const router = useRouter()
  const { t } = useI18n()
  return (
    <Card embedded={false}>
      <h1>{t('session.connection_error')}</h1>
      <p>{t('session.connection_error_hint')}</p>
      <div>
        <Button onClick={() => void router.invalidate()}>{t('common.retry')}</Button>
      </div>
    </Card>
  )
}

export function SessionPending() {
  const { t } = useI18n()
  return (
    <div className={styles.boot} role="status" aria-label={t('nav.checking_your_session')}>
      <ProgressCircular size={40} />
    </div>
  )
}

/** The signed-in app could not start: the session check failed, or the shell itself did. */
export function AppError({ error }: ErrorComponentProps) {
  return error instanceof SessionUnreachableError ? <SessionError /> : <RouteProblem />
}
