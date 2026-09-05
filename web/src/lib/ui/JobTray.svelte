<script lang="ts">
  import { t } from '../i18n'
  import { fly, slide } from 'svelte/transition'
  import { cubicOut } from 'svelte/easing'
  import { createQuery, createQueries, createMutation, type CreateQueryResult } from '@tanstack/svelte-query'
  import type { JobKindWire, JobStatus } from '../api/client'
  import { batchErrorKey } from '../api/error-text'
  import { queryClient } from '../query/client'
  import { jobListQuery, jobQuery, jobCancelMutation } from '../query/jobs'
  import { jobTray } from '../store/jobs.store'
  import IconButton from './IconButton.svelte'
  import { Icon } from 'm3-svelte'
  import { icons, type IconName } from '../icons'
  import ProgressLinear from './ProgressLinear.svelte'

  type JobKind = 'delete' | 'copy' | 'index'
  type JobItemStatus = 'running' | 'done' | 'error' | 'cancelled' | 'interrupted'

  interface JobRow {
    id: string
    kind: JobKind
    done: number
    total: number
    status: JobItemStatus
    /** A catalogue key, never text: the first server-reported failure's, or
     *  the row's own timed-out-fetch note. Resolved at render time, in the
     *  reader's language. */
    message?: string
    messageParams?: Record<string, string>
    /** Paths the server started but never recorded an outcome for -- the
     *  process died between `begin_result` and `finish_result`, so whether
     *  the file was moved/copied/deleted is genuinely unknown and only a
     *  look at the destination can settle it. At most one entry in
     *  practice. */
    attempting: string[]
    /** Paths the job was asked for but never reached. Untouched on disk, so
     *  re-running the same operation on exactly these is safe. */
    pending: string[]
  }

  // The only place the frontend's own `JobKind` (tray icon/label lookup) and
  // the backend's wire `JobKindWire` (`"index_build"` vs `"index"`) diverge.
  // A kind this build has no tray row for still has to land somewhere: an
  // archive streams and a move finishes inline, so neither creates a job
  // here, but an older server's rows are still readable and shown as a copy
  // rather than dropped.
  function frontendKind(kind: JobKindWire): JobKind {
    switch (kind) {
      case 'index_build':
        return 'index'
      case 'delete':
        return 'delete'
      default:
        return 'copy'
    }
  }

  function kindLabel(kind: JobKind): string {
    return kind === 'delete' ? t('common.delete') : kind === 'copy' ? t('common.copy') : t('job.index_build')
  }
  function kindIcon(kind: JobKind): IconName {
    return kind === 'delete' ? 'delete' : kind === 'copy' ? 'copy' : 'search'
  }

  /** Everything the job was asked to do and did not finish: the item whose
   *  outcome nobody can know (`attempting`) plus the ones it never started
   *  (`pending`). Empty for a job that ran to completion. */
  function outstanding(item: JobRow): string[] {
    return [...item.attempting, ...item.pending]
  }

  /** One tray row, built from a job's live status query. `result.isError`
   *  is what the old poll loop's timeout note covered: this row's own fetch
   *  failed (most often a 404 -- the job was already garbage-collected
   *  server-side) and there is nothing left to poll. */
  function rowFor(id: string, result: CreateQueryResult<JobStatus, Error> | undefined): JobRow {
    // `result` is momentarily undefined the instant `jobTray.state.ids`
    // gains an id but `createQueries`' own query list hasn't re-synced yet
    // -- same "nothing to show yet" placeholder as a query that is still
    // loading its first response.
    if (!result || result.isError) {
      return {
        id,
        kind: 'copy',
        done: 0,
        total: 0,
        status: result?.isError ? 'error' : 'running',
        message: result?.isError ? /* i18n */ 'job.could_not_check_job_status' : undefined,
        attempting: [],
        pending: []
      }
    }
    const status = result.data
    if (!status) {
      return { id, kind: 'copy', done: 0, total: 0, status: 'running', attempting: [], pending: [] }
    }
    const kind = frontendKind(status.kind)
    if (status.state === 'error') {
      const first = batchErrorKey(status.results.find((r) => !r.ok)?.error)
      return {
        id,
        kind,
        done: status.done,
        total: status.total,
        status: 'error',
        message: first?.key,
        messageParams: first?.params,
        attempting: status.attempting,
        pending: status.pending
      }
    }
    // `cancelled` (a user asked to stop) and `interrupted` (the process
    // hosting the runner died mid-job) are different facts from a plain
    // failure and are named apart here too, straight off the wire state.
    return { id, kind, done: status.done, total: status.total, status: status.state, attempting: status.attempting, pending: status.pending }
  }

  // Re-attaches to whatever `jobs.db` still has open -- a browser refresh or
  // a server restart (Docker cutover) both leave nothing else to go on.
  // Polls itself (`jobListQuery`'s `refetchInterval`) while anything is
  // running, so a job started in another tab shows up here too.
  const list = createQuery(() => jobListQuery())
  $effect(() => {
    jobTray.track(...(list.data?.jobs ?? []).map((job) => job.id))
  })

  // One live `JobStatus` per tray row. Each stops polling by itself
  // (`jobQuery`'s `refetchInterval`) once its own status is terminal.
  const statuses = createQueries(() => ({ queries: jobTray.state.ids.map((id) => jobQuery(id)) }))
  const items = $derived(jobTray.state.ids.map((id, i) => rowFor(id, statuses[i])))

  const totalActive = $derived(items.filter((i) => i.status === 'running').length)

  const cancelJob = createMutation(() => jobCancelMutation())

  function clearFinished(): void {
    jobTray.forget(...items.filter((i) => i.status !== 'running').map((i) => i.id))
  }

  // Same two-region pattern as UploadTray.svelte (see its comment for the
  // full reasoning) -- a separate live region because this tray, like that
  // one, is mounted once at the app root and outlives route navigation.
  let politeMsg = $state('')
  let assertiveMsg = $state('')
  const lastStatus = new Map<string, JobItemStatus>()

  function say(target: 'polite' | 'assertive', text: string): void {
    if (target === 'polite') {
      politeMsg = ''
      setTimeout(() => { politeMsg = text }, 0)
    } else {
      assertiveMsg = ''
      setTimeout(() => { assertiveMsg = text }, 0)
    }
  }

  $effect(() => {
    const seenIds = new Set<string>()
    for (const item of items) {
      seenIds.add(item.id)
      const prev = lastStatus.get(item.id)
      if (prev !== undefined && prev !== item.status) {
        // The job wrote somewhere. Its status does not say which listings it
        // touched, and a listing is only ever invalidated by an event or a
        // write, so re-checking every path read is the honest answer.
        void queryClient.invalidateQueries({ queryKey: ['path'] })
        const label = kindLabel(item.kind)
        if (item.status === 'done') {
          say('polite', t('job.job_finished_items_processed', { kind: label, count: item.total }))
        } else if (item.status === 'cancelled') {
          say('polite', t('job.job_cancelled_items_completed', { kind: label, count: item.done }))
        } else if (item.status === 'interrupted') {
          const left = outstanding(item).length
          say(
            'assertive',
            t('job.job_was_interrupted_by_server', { kind: label, count: item.done }) +
              (left > 0 ? ' ' + t('job.left', { count: left }) : '')
          )
        } else if (item.status === 'error') {
          say(
            'assertive',
            `${t('job.job_failed', { kind: label })} ${item.message ? t(item.message, item.messageParams) : ''}`.trim()
          )
        }
      }
      lastStatus.set(item.id, item.status)
    }
    for (const id of lastStatus.keys()) {
      if (!seenIds.has(id)) lastStatus.delete(id)
    }
  })

  function reduceMotion(): boolean {
    return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches
  }
  function trayDuration(): number {
    return reduceMotion() ? 0 : 200
  }
</script>

<div class="sc-job-tray__sr-only" role="status" aria-live="polite" aria-atomic="true">{politeMsg}</div>
<div class="sc-job-tray__sr-only" role="alert" aria-live="assertive" aria-atomic="true">{assertiveMsg}</div>

{#if items.length > 0 || list.isError}
  <div
    class="sc-job-tray"
    class:sc-job-tray--collapsed={!jobTray.state.open}
    transition:fly={{ y: 32, duration: trayDuration(), easing: cubicOut }}
  >
    <div class="sc-job-tray__header">
      <button class="sc-job-tray__title" onclick={() => jobTray.setOpen(!jobTray.state.open)}>
        <Icon icon={icons.refresh} size={18} />
        {t('job.jobs')} {totalActive > 0 ? `(${totalActive})` : t('common.done')}
      </button>
      <div class="sc-job-tray__actions">
        <IconButton label={t('common.clear_finished_items')} onclick={clearFinished}>
          <Icon icon={icons.check} size={18} />
        </IconButton>
        <IconButton label={jobTray.state.open ? t('common.collapse') : t('common.expand')} onclick={() => jobTray.setOpen(!jobTray.state.open)}>
          <Icon icon={icons[jobTray.state.open ? 'chevron-right' : 'chevron-left']} size={18} />
        </IconButton>
      </div>
    </div>
    {#if list.isError}
      <!-- `GET /api/jobs` re-attach couldn't reach the server (mid-restart,
           e.g. a Docker cutover) -- the list below is whatever was last
           confirmed, not necessarily current. Never hidden by `open`, same
           as the header, since it's a fact about the data underneath, not
           a detail worth collapsing away. -->
      <p class="sc-job-tray__stale">{t('job.server_unreachable_so_may_not')}</p>
    {/if}

    {#if jobTray.state.open}
      <ul class="sc-job-tray__list">
        {#each items as item (item.id)}
          <li class="sc-job-tray__item" transition:slide={{ duration: trayDuration(), easing: cubicOut }}>
            <div class="sc-job-tray__row">
              <span class="sc-job-tray__name">
                <Icon icon={icons[kindIcon(item.kind)]} size={16} />
                {kindLabel(item.kind)}
              </span>
              <span class="sc-job-tray__meta">{item.done} / {item.total || '?'}</span>
            </div>
            <ProgressLinear
              value={item.total > 0 ? item.done / item.total : null}
              label={t('job.job', { kind: kindLabel(item.kind) })}
              tone={item.status === 'error' || item.status === 'interrupted' ? 'weak' : item.status === 'cancelled' ? 'fair' : 'primary'}
            />
            {#if item.status === 'error' && item.message}
              <p class="sc-job-tray__message">{t(item.message, item.messageParams)}</p>
            {:else if item.status === 'cancelled'}
              <p class="sc-job-tray__message">{t('job.cancelled_completed', { count: item.done })}</p>
            {:else if item.status === 'interrupted'}
              <p class="sc-job-tray__message">{t('job.interrupted_by_server_restart_completed', { count: item.done })}</p>
            {/if}
            {#if outstanding(item).length > 0}
              <!-- A job that stopped without finishing is only "not lost" if
                   the user can see which items are still outstanding and act
                   on them. `attempting` and `pending` are different facts and
                   are labelled apart: an unstarted item is untouched and safe
                   to re-run, an attempted one has an outcome only the
                   destination can settle. -->
              <details class="sc-job-tray__outstanding">
                <summary>{t('job.items_left', { count: outstanding(item).length })}</summary>
                <ul>
                  {#each item.attempting as path (path)}
                    <li><span class="sc-job-tray__tag sc-job-tray__tag--check">{t('job.needs_checking')}</span>{path}</li>
                  {/each}
                  {#each item.pending as path (path)}
                    <li><span class="sc-job-tray__tag">{t('job.not_started')}</span>{path}</li>
                  {/each}
                </ul>
                {#if item.attempting.length > 0}
                  <p class="sc-job-tray__message">
                    {t('job.server_stopped_mid_item_anything')}
                  </p>
                {/if}
                <p class="sc-job-tray__message">{t('job.anything_marked_not_started_untouched')}</p>
              </details>
            {/if}
            <div class="sc-job-tray__controls">
              {#if item.status === 'running'}
                <IconButton label={t('job.cancel_job')} onclick={() => cancelJob.mutate(item.id)}>
                  <Icon icon={icons.close} size={16} />
                </IconButton>
              {:else}
                <IconButton label={t('common.clear')} onclick={() => jobTray.forget(item.id)}>
                  <Icon icon={icons.close} size={16} />
                </IconButton>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
{/if}

<style>
  .sc-job-tray__sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
  .sc-job-tray {
    /* Positioning lives on `.sc-tray-stack` in `(app)/+layout.svelte`, same
       as `UploadTray` -- see that component's `.sc-upload-tray` rule. */
    width: min(360px, calc(100vw - 32px));
    max-height: 60vh;
    display: flex;
    flex-direction: column;
    border-radius: var(--m3-shape-large);
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
    box-shadow: var(--m3-elevation-3);
    overflow: hidden;
  }
  .sc-job-tray__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 48px;
    padding-inline: 16px;
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-job-tray--collapsed .sc-job-tray__header {
    border-bottom: none;
  }
  .sc-job-tray__stale {
    margin: 0;
    padding: 8px 16px;
    @apply --m3-body-small;
    color: var(--m3c-tertiary);
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-job-tray__title {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    border: none;
    background: transparent;
    color: inherit;
    font-weight: 600;
    cursor: pointer;
  }
  .sc-job-tray__actions {
    display: flex;
    gap: 4px;
  }
  .sc-job-tray__list {
    list-style: none;
    margin: 0;
    padding: 8px 16px;
    overflow-y: auto;
  }
  .sc-job-tray__item {
    padding-block: 12px;
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-job-tray__item:last-child {
    border-bottom: none;
  }
  .sc-job-tray__row {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 4px;
  }
  .sc-job-tray__name {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    @apply --m3-body-medium;
  }
  .sc-job-tray__meta {
    flex-shrink: 0;
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-job-tray__message {
    margin: 4px 0 0;
    @apply --m3-body-small;
    color: var(--m3c-tertiary);
  }
  .sc-job-tray__outstanding {
    margin-top: 4px;
    @apply --m3-body-small;
  }
  .sc-job-tray__outstanding summary {
    cursor: pointer;
    color: var(--m3c-on-surface-variant);
  }
  .sc-job-tray__outstanding ul {
    list-style: none;
    margin: 4px 0 0;
    padding: 0;
    max-height: 12rem;
    overflow-y: auto;
  }
  .sc-job-tray__outstanding li {
    display: flex;
    align-items: baseline;
    gap: 4px;
    /* Paths are long and arbitrary; wrapping beats a horizontal scrollbar in
       a 360px tray. */
    overflow-wrap: anywhere;
  }
  .sc-job-tray__tag {
    flex-shrink: 0;
    padding-inline: 4px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-on-surface-variant);
    @apply --m3-label-small;
  }
  .sc-job-tray__tag--check {
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
  }
  .sc-job-tray__controls {
    display: flex;
    justify-content: flex-end;
    gap: 4px;
    margin-top: 4px;
  }
</style>
