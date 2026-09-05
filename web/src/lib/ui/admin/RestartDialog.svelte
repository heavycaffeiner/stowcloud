<script lang="ts">
  // The confirm-then-wait dialog for `POST /api/v1/admin/system/restart`
  // (`go/engine/http/server`'s `syscall.Exec` swap, same PID, container
  // survives). A restart interrupts in-flight uploads and running jobs, so
  // this never fires on its own: it always asks first, and the caller
  // decides when to show it by setting `open` from a save's own
  // `ApplyOutcome.restart_required`.
  //
  // After confirming, the socket is expected to drop while the process
  // re-execs itself, a failed `GET /api/v1/system/health` mid-wait is the
  // signal to keep polling, not a failure to report. Only the poll running
  // past its own bounded budget is worth surfacing.
  import { t } from '../../i18n'
  import { createMutation, createQuery } from '@tanstack/svelte-query'
  import { adminRestartMutation, systemHealthQuery } from '../../query/admin'
  import { describeApiError } from '../../api/error-text'
  import type { ApplyOutcome } from '../../api/types'
  import Button from '../Button.svelte'
  import Dialog from '../Dialog.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'

  interface Props {
    open: boolean
    /** The save that triggered this. `null`, or an outcome that did not need
     *  a restart, both render nothing, the caller is not trusted to gate
     *  `open` correctly on its own. */
    outcome: ApplyOutcome | null
    /** Cancel, Escape, backdrop, or the dialog dismissing itself once the
     *  server is confirmed back. */
    onclose: () => void
    /** Fires once, the moment the health probe answers again after a
     *  confirmed restart. The caller reloads whatever it was showing; this
     *  component has no view of that state to refresh itself. */
    onrestarted: () => void
  }
  let { open, outcome, onclose, onrestarted }: Props = $props()

  type Phase = 'confirm' | 'submitting' | 'waiting' | 'timeout' | 'success'
  let phase = $state<Phase>('confirm')

  const POLL_INTERVAL_MS = 1_500
  const WAIT_BUDGET_MS = 45_000
  const WAIT_BUDGET_SECONDS = Math.round(WAIT_BUDGET_MS / 1000)

  const activeUploads = $derived(outcome?.active_uploads ?? 0)
  const activeJobs = $derived(outcome?.active_jobs ?? 0)

  const restartMutation = createMutation(() => adminRestartMutation())
  const submitError = $derived(
    restartMutation.error ? describeApiError(restartMutation.error, t('restart.could_not_start')) : null
  )

  // The wait budget this poll is bounded by. `pollMs` is what actually
  // drives the query's `refetchInterval`; it goes false the moment the
  // server answers or the budget runs out, which is what stops the polling.
  // `waitStartedAt` guards against a stale success: this query already ran
  // once on mount (before any restart was ever asked for), so a plain
  // `isSuccess` check would treat that leftover healthy answer as the
  // server having already come back, without ever actually re-checking it.
  let deadline = $state<number | null>(null)
  let waitStartedAt = $state<number | null>(null)
  let pollMs = $state<number | false>(false)
  const health = createQuery(() => systemHealthQuery(pollMs))

  function beginWaiting(): void {
    waitStartedAt = Date.now()
    deadline = Date.now() + WAIT_BUDGET_MS
    phase = 'waiting'
    pollMs = POLL_INTERVAL_MS
    void health.refetch()
  }

  $effect(() => {
    if (phase !== 'waiting' || !open) return
    // Both timestamps are read unconditionally so this effect re-runs on
    // every poll tick, success or failure, not only the first of either.
    const succeededAt = health.dataUpdatedAt
    void health.errorUpdatedAt
    if (health.isSuccess && waitStartedAt !== null && succeededAt >= waitStartedAt) {
      pollMs = false
      phase = 'success'
      onrestarted()
      const timer = setTimeout(() => {
        if (phase === 'success') onclose()
      }, 900)
      return () => clearTimeout(timer)
    }
    if (deadline !== null && Date.now() >= deadline) {
      pollMs = false
      phase = 'timeout'
      return
    }
    pollMs = POLL_INTERVAL_MS
  })

  // A fresh open resets to the confirm step; closing (by any route) stops
  // whatever polling is running rather than leaving it ticking in the
  // background for a dialog nobody is looking at.
  let wasOpen = $state(false)
  $effect(() => {
    if (open && !wasOpen) {
      phase = 'confirm'
      restartMutation.reset()
    }
    if (!open) pollMs = false
    wasOpen = open
  })

  function confirmRestart(): void {
    restartMutation.reset()
    phase = 'submitting'
    restartMutation.mutate(undefined, {
      onSuccess: () => {
        // The restart is already underway server-side by the time this
        // resolves: closing here cannot undo it. It only decides whether
        // this component keeps watching for it, which a dismissed dialog
        // should not.
        if (!open) return
        beginWaiting()
      },
      onError: () => {
        if (!open) return
        phase = 'confirm'
      }
    })
  }

  function retryWaiting(): void {
    beginWaiting()
  }
</script>

<Dialog open={open && !!outcome?.restart_required} title={t('restart.title')} {onclose}>
  {#if phase === 'confirm' || phase === 'submitting'}
    <p>
      {#if activeUploads > 0 || activeJobs > 0}
        {t('restart.will_interrupt', { uploads: activeUploads, jobs: activeJobs })}
      {:else}
        {t('restart.no_active_work')}
      {/if}
    </p>
    {#if submitError}
      <p class="sc-restart__error" role="alert">{submitError}</p>
    {/if}
  {:else if phase === 'waiting'}
    <p class="sc-restart__status" role="status" aria-live="polite">
      <ProgressCircular size={20} label={t('restart.waiting_for_server', { seconds: WAIT_BUDGET_SECONDS })} />
      {t('restart.waiting_for_server', { seconds: WAIT_BUDGET_SECONDS })}
    </p>
  {:else if phase === 'timeout'}
    <p class="sc-restart__error" role="alert" aria-live="assertive">
      {t('restart.timed_out', { seconds: WAIT_BUDGET_SECONDS })}
    </p>
  {:else if phase === 'success'}
    <p class="sc-restart__status" role="status" aria-live="polite">{t('restart.came_back')}</p>
  {/if}

  {#snippet actions()}
    {#if phase === 'confirm'}
      <Button variant="text" onclick={onclose}>{t('common.cancel')}</Button>
      <Button variant="filled" danger onclick={confirmRestart}>{t('restart.restart_now')}</Button>
    {:else if phase === 'submitting'}
      <Button variant="text" onclick={onclose} disabled>{t('common.cancel')}</Button>
      <Button variant="filled" danger loading>{t('restart.restart_now')}</Button>
    {:else if phase === 'waiting'}
      <Button variant="text" onclick={onclose}>{t('common.close')}</Button>
    {:else if phase === 'timeout'}
      <Button variant="text" onclick={onclose}>{t('common.close')}</Button>
      <Button variant="filled" onclick={retryWaiting}>{t('common.retry')}</Button>
    {:else if phase === 'success'}
      <Button variant="text" onclick={onclose}>{t('common.close')}</Button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .sc-restart__status {
    display: inline-flex;
    align-items: center;
    gap: 12px;
  }
  .sc-restart__error {
    color: var(--m3c-error);
  }
</style>
