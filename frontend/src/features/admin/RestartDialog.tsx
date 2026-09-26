import { useEffect, useRef } from 'react'
import { useComponentState } from '../../hooks/use-component-state'
import { useMutation, useQuery } from '@tanstack/react-query'
import { describeApiError } from '../../lib/api/error-text'
import type { ApplyOutcome } from '../../lib/api/types'
import { useI18n } from '../../hooks/use-i18n'
import { adminRestartMutation, systemHealthQuery } from '../../lib/query/admin'
import { Button } from '../../lib/ui/Button'
import { Dialog } from '../../lib/ui/Dialog'
import { ProgressCircular } from '../../lib/ui/ProgressCircular'
import { nextRestartWaitStep } from './logic/restart-wait'
import '../../styles/features/admin/restart-dialog.css.ts'

const POLL_INTERVAL_MS = 200
const WAIT_BUDGET_MS = 45_000
const WAIT_BUDGET_SECONDS = Math.round(WAIT_BUDGET_MS / 1000)
type Phase = 'confirm' | 'submitting' | 'waiting' | 'timeout' | 'success'
interface RestartDialogProps { open: boolean; outcome: ApplyOutcome | null; onclose: () => void; onrestarted: () => void }

export function RestartDialog({ open, outcome, onclose, onrestarted }: RestartDialogProps) {
  const { t } = useI18n()
  type RestartState = { phase: Phase; deadline: number | null; waitStartedAt: number | null; sawOutage: boolean }
  const [state, setState] = useComponentState<RestartState>({ phase: 'confirm', deadline: null, waitStartedAt: null, sawOutage: false })
  const { phase, deadline, waitStartedAt, sawOutage } = state
  const patchState = (patch: Partial<RestartState>): void => setState((current) => ({ ...current, ...patch }))
  const setPhase = (value: Phase): void => patchState({ phase: value })
  const setDeadline = (value: number | null): void => patchState({ deadline: value })
  const setWaitStartedAt = (value: number | null): void => patchState({ waitStartedAt: value })
  const setSawOutage = (value: boolean): void => patchState({ sawOutage: value })
  const wasOpen = useRef(false)
  const restart = useMutation(adminRestartMutation())
  const health = useQuery(systemHealthQuery(phase === 'waiting' ? POLL_INTERVAL_MS : false))
  useEffect(() => {
    if (open && !wasOpen.current) { setPhase('confirm'); setDeadline(null); setWaitStartedAt(null); setSawOutage(false); restart.reset() }
    if (!open && phase === 'waiting') setPhase('confirm')
    wasOpen.current = open
  }, [open, phase, restart])
  useEffect(() => {
    if (!open || phase !== 'waiting' || waitStartedAt === null || deadline === null) return
    const step = nextRestartWaitStep(sawOutage, { isSuccess: health.isSuccess, succeededAt: health.dataUpdatedAt, isError: health.isError, erroredAt: health.errorUpdatedAt, waitStartedAt, now: Date.now(), deadline })
    setSawOutage(step.sawOutage)
    if (step.outcome === 'confirmed') { setPhase('success'); onrestarted(); const timer = window.setTimeout(onclose, 900); return () => window.clearTimeout(timer) }
    if (step.outcome === 'timed-out') setPhase('timeout')
  }, [open, phase, waitStartedAt, deadline, sawOutage, health.isSuccess, health.dataUpdatedAt, health.isError, health.errorUpdatedAt, onclose, onrestarted])
  if (!open || !outcome?.restart_required) return null
  const activeUploads = outcome.active_uploads ?? 0
  const activeJobs = outcome.active_jobs ?? 0
  function beginWaiting(): void { const started = Date.now(); setWaitStartedAt(started); setDeadline(started + WAIT_BUDGET_MS); setSawOutage(false); setPhase('waiting') }
  function confirmRestart(): void { restart.reset(); setPhase('submitting'); restart.mutate(undefined, { onSuccess: () => { if (open) beginWaiting() }, onError: () => { if (open) setPhase('confirm') } }) }
  const actions = phase === 'confirm' ? <><Button variant="text" onClick={onclose}>{t('common.cancel')}</Button><Button danger onClick={confirmRestart}>{t('restart.restart_now')}</Button></> : phase === 'submitting' ? <><Button variant="text" disabled>{t('common.cancel')}</Button><Button loading>{t('restart.restart_now')}</Button></> : phase === 'timeout' ? <><Button variant="text" onClick={onclose}>{t('common.close')}</Button><Button onClick={beginWaiting}>{t('common.retry')}</Button></> : <Button variant="text" onClick={onclose}>{t('common.close')}</Button>
  return <Dialog open={open} title={t('restart.title')} onClose={onclose} actions={actions}>{phase === 'confirm' || phase === 'submitting' ? <p>{activeUploads > 0 || activeJobs > 0 ? t('restart.will_interrupt', { uploads: activeUploads, jobs: activeJobs }) : t('restart.no_active_work')}</p> : null}{restart.error ? <p className="sc-restart-error" role="alert">{describeApiError(restart.error, t('restart.could_not_start'))}</p> : null}{phase === 'waiting' ? <p className="sc-restart-status" role="status" aria-live="polite"><ProgressCircular />{t('restart.waiting_for_server', { seconds: WAIT_BUDGET_SECONDS })}</p> : null}{phase === 'timeout' ? <p className="sc-restart-error" role="alert" aria-live="assertive">{t('restart.timed_out', { seconds: WAIT_BUDGET_SECONDS })}</p> : null}{phase === 'success' ? <p className="sc-restart-status" role="status" aria-live="polite">{t('restart.came_back')}</p> : null}</Dialog>
}
