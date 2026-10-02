import { useEffect, useEffectEvent, useState } from 'react'
import { overlay } from 'overlay-kit'
import { describeApiError } from '../../../api/error-text'
import { useI18n } from '../../../hooks/use-i18n'
import { useRestartServer, useSystemHealth } from '../api'
import { StowButton, StowDialog, StowProgressCircular } from '@/shared/ui'
import { nextRestartWaitStep } from '../logic/restart-wait'
import * as styles from './RestartDialog.css'
import type { ApplyOutcome } from '../api'

const POLL_INTERVAL_MS = 200
const WAIT_BUDGET_MS = 45_000
const WAIT_BUDGET_SECONDS = Math.round(WAIT_BUDGET_MS / 1000)
type Phase = 'confirm' | 'submitting' | 'waiting' | 'timeout' | 'success'
interface RestartDialogProps {
  open: boolean
  outcome: ApplyOutcome
  /** Gets whether the server came back from a restart. */
  onClose: (restarted: boolean) => void
  onClosed: () => void
}

/** Offers the restart a settings change needs. True once the server has come back from it. */
export function offerRestart(outcome: ApplyOutcome): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <RestartDialog open={isOpen} outcome={outcome} onClose={close} onClosed={unmount} />
  ))
}

function RestartDialog({ open, outcome, onClose, onClosed }: RestartDialogProps) {
  const { t } = useI18n()
  const [phase, setPhase] = useState<Phase>('confirm')
  const [waitStartedAt, setWaitStartedAt] = useState<number | null>(null)
  const [sawOutage, setSawOutage] = useState(false)
  const restart = useRestartServer()
  const health = useSystemHealth(phase === 'waiting' ? POLL_INTERVAL_MS : false)
  const close = (): void => onClose(phase === 'success')
  const finish = useEffectEvent(() => onClose(true))

  useEffect(() => {
    if (phase !== 'waiting' || waitStartedAt === null) return
    const step = nextRestartWaitStep(sawOutage, {
      isSuccess: health.isSuccess,
      succeededAt: health.dataUpdatedAt,
      isError: health.isError,
      erroredAt: health.errorUpdatedAt,
      waitStartedAt,
      now: Date.now(),
      deadline: waitStartedAt + WAIT_BUDGET_MS
    })
    setSawOutage(step.sawOutage)
    if (step.outcome === 'confirmed') setPhase('success')
    if (step.outcome === 'timed-out') setPhase('timeout')
  }, [phase, waitStartedAt, sawOutage, health.isSuccess, health.dataUpdatedAt, health.isError, health.errorUpdatedAt])

  // The good news stays up for a moment before the dialog closes on its own.
  useEffect(() => {
    if (phase !== 'success') return
    const timer = window.setTimeout(finish, 900)
    return () => window.clearTimeout(timer)
  }, [phase])

  const activeUploads = outcome.active_uploads ?? 0
  const activeJobs = outcome.active_jobs ?? 0
  function beginWaiting(): void {
    setWaitStartedAt(Date.now())
    setSawOutage(false)
    setPhase('waiting')
  }
  function confirmRestart(): void {
    restart.reset()
    setPhase('submitting')
    restart.mutate(undefined, { onSuccess: beginWaiting, onError: () => setPhase('confirm') })
  }
  const actions =
    phase === 'confirm' ? (
      <>
        <StowButton variant="text" onClick={close}>
          {t('common.cancel')}
        </StowButton>
        <StowButton danger onClick={confirmRestart}>
          {t('restart.restart_now')}
        </StowButton>
      </>
    ) : phase === 'submitting' ? (
      <>
        <StowButton variant="text" disabled>
          {t('common.cancel')}
        </StowButton>
        <StowButton loading>{t('restart.restart_now')}</StowButton>
      </>
    ) : phase === 'timeout' ? (
      <>
        <StowButton variant="text" onClick={close}>
          {t('common.close')}
        </StowButton>
        <StowButton onClick={beginWaiting}>{t('common.retry')}</StowButton>
      </>
    ) : (
      <StowButton variant="text" onClick={close}>
        {t('common.close')}
      </StowButton>
    )
  return (
    <StowDialog open={open} title={t('restart.title')} onClose={close} onClosed={onClosed} actions={actions}>
      {phase === 'confirm' || phase === 'submitting' ? (
        <p>
          {activeUploads > 0 || activeJobs > 0
            ? t('restart.will_interrupt', { uploads: activeUploads, jobs: activeJobs })
            : t('restart.no_active_work')}
        </p>
      ) : null}
      {restart.error ? (
        <p className={styles.error} role="alert">
          {describeApiError(restart.error, t('restart.could_not_start'))}
        </p>
      ) : null}
      {phase === 'waiting' ? (
        <p className={styles.status} role="status" aria-live="polite">
          <StowProgressCircular />
          {t('restart.waiting_for_server', { seconds: WAIT_BUDGET_SECONDS })}
        </p>
      ) : null}
      {phase === 'timeout' ? (
        <p className={styles.error} role="alert" aria-live="assertive">
          {t('restart.timed_out', { seconds: WAIT_BUDGET_SECONDS })}
        </p>
      ) : null}
      {phase === 'success' ? (
        <p className={styles.status} role="status" aria-live="polite">
          {t('restart.came_back')}
        </p>
      ) : null}
    </StowDialog>
  )
}
