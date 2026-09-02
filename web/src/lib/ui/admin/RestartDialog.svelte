<script lang="ts">
  // The confirm-then-wait dialog for `POST /api/v1/admin/system/restart`
  // (`go/engine/http/server`'s `syscall.Exec` swap, same PID, container
  // survives). A restart interrupts in-flight uploads and running jobs, so
  // this never fires on its own: it always asks first, and the caller
  // decides when to show it by setting `open` from a save's own
  // `ApplyOutcome.restart_required`.
  //
  // After confirming, the socket is expected to drop while the process
  // re-execs itself — a failed `GET /api/v1/system/health` mid-wait is the
  // signal to keep polling, not a failure to report. Only the poll running
  // past its own bounded budget is worth surfacing.
  import { t } from '../../i18n'
  import { api } from '../../api/client'
  import { describeApiError } from '../../api/error-text'
  import type { ApplyOutcome } from '../../api/types'
  import Button from '../Button.svelte'
  import Dialog from '../Dialog.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'

  interface Props {
    open: boolean
    /** The save that triggered this. `null`, or an outcome that did not need
     *  a restart, both render nothing — the caller is not trusted to gate
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
  let submitError = $state<string | null>(null)

  const POLL_INTERVAL_MS = 1_500
  const WAIT_BUDGET_MS = 45_000
  const WAIT_BUDGET_SECONDS = Math.round(WAIT_BUDGET_MS / 1000)

  const activeUploads = $derived(outcome?.active_uploads ?? 0)
  const activeJobs = $derived(outcome?.active_jobs ?? 0)

  // A fresh open resets to the confirm step; closing (by any route) stops
  // whatever poll loop is running rather than leaving it ticking in the
  // background for a dialog nobody is looking at.
  let wasOpen = $state(false)
  $effect(() => {
    if (open && !wasOpen) {
      phase = 'confirm'
      submitError = null
    }
    if (!open) stopPolling()
    wasOpen = open
  })
  $effect(() => () => stopPolling())

  // Incremented every time polling should stop, current or future: a loop
  // reads its own snapshot back before each step and quits the moment it no
  // longer matches, which is what lets `stopPolling` cancel a loop already
  // in flight without a separate cancellation flag to thread through it.
  let pollToken = 0
  function stopPolling(): void {
    pollToken++
  }

  async function startWaiting(): Promise<void> {
    phase = 'waiting'
    const token = ++pollToken
    const startedAt = Date.now()
    while (token === pollToken) {
      try {
        await api.systemHealth()
        if (token === pollToken) {
          phase = 'success'
          onrestarted()
          setTimeout(() => {
            if (token === pollToken) onclose()
          }, 900)
        }
        return
      } catch {
        // Expected while the process is mid-drain or mid-exec: the socket is
        // down for a stretch on purpose. Only running out of budget below is
        // worth telling the operator about.
      }
      if (Date.now() - startedAt >= WAIT_BUDGET_MS) {
        if (token === pollToken) phase = 'timeout'
        return
      }
      await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
    }
  }

  async function confirmRestart(): Promise<void> {
    submitError = null
    phase = 'submitting'
    try {
      await api.adminSystemRestart()
      // The restart is already underway server-side by the time this
      // resolves: closing here cannot undo it. It only decides whether this
      // component keeps watching for it, which a dismissed dialog should not.
      if (!open) return
      void startWaiting()
    } catch (err) {
      if (!open) return
      phase = 'confirm'
      submitError = describeApiError(err, t('restart.could_not_start'))
    }
  }

  function retryWaiting(): void {
    void startWaiting()
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
