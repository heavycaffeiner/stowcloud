import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQueries, useQuery } from '@tanstack/react-query'
import type { JobKindWire, JobState, JobStatus } from '../api/client'
import { batchErrorKey } from '../api/error-text'
import { useI18n } from '../i18n/use-i18n'
import { queryClient } from '../query/client'
import { jobCancelMutation, jobListQuery, jobQuery } from '../query/jobs'
import { jobTray } from '../store/jobs.store'
import { useStore } from '../store/use-store'
import { Icon } from './Icon'
import { IconButton } from './IconButton'
import { VirtualList } from './VirtualList'

interface JobRow {
  id: string
  kind: 'delete' | 'copy' | 'index'
  done: number
  total: number
  status: JobState
  message?: string
  messageParams?: Record<string, string>
  attempting: string[]
  pending: string[]
}

function frontendKind(kind: JobKindWire): JobRow['kind'] {
  return kind === 'index_build' ? 'index' : kind === 'delete' ? 'delete' : 'copy'
}

function kindLabel(kind: JobRow['kind'], t: (key: string, params?: Record<string, string | number>) => string): string {
  return kind === 'delete' ? t('common.delete') : kind === 'copy' ? t('common.copy') : t('job.index_build')
}

function rowFor(id: string, status: JobStatus | undefined, error: boolean): JobRow {
  if (!status || error) {
    return { id, kind: 'copy', done: 0, total: 0, status: error ? 'error' : 'running', message: error ? /* i18n */ 'job.could_not_check_job_status' : undefined, attempting: [], pending: [] }
  }
  const kind = frontendKind(status.kind)
  if (status.state === 'error') {
    const first = batchErrorKey(status.results.find((result) => !result.ok)?.error)
    return { id, kind, done: status.done, total: status.total, status: 'error', message: first?.key, messageParams: first?.params, attempting: status.attempting, pending: status.pending }
  }
  return { id, kind, done: status.done, total: status.total, status: status.state, attempting: status.attempting, pending: status.pending }
}

export function JobTray() {
  const { t, tp } = useI18n()
  const ids = useStore(jobTray, (state) => state.ids)
  const open = useStore(jobTray, (state) => state.open)
  const [expandedJobs, setExpandedJobs] = useState<ReadonlySet<string>>(() => new Set())
  const list = useQuery(jobListQuery())
  const statuses = useQueries({ queries: ids.map((id) => jobQuery(id)) })
  const cancel = useMutation(jobCancelMutation())
  const previous = useRef(new Map<string, JobState>())
  const politeRef = useRef<HTMLDivElement | null>(null)
  const assertiveRef = useRef<HTMLDivElement | null>(null)
  const rows = useMemo(() => ids.map((id, index) => rowFor(id, statuses[index]?.data, statuses[index]?.isError === true)), [ids, statuses])

  useEffect(() => {
    jobTray.track(...(list.data?.jobs ?? []).map((job) => job.id))
  }, [list.data?.jobs])

  useEffect(() => {
    setExpandedJobs((current) => {
      const tracked = new Set(ids)
      const retained = new Set([...current].filter((id) => tracked.has(id)))
      return retained.size === current.size ? current : retained
    })
  }, [ids])

  const announce = (target: 'polite' | 'assertive', text: string): void => {
    const element = (target === 'polite' ? politeRef : assertiveRef).current
    if (!element) return
    element.textContent = ''
    window.setTimeout(() => { element.textContent = text }, 0)
  }

  useEffect(() => {
    const seen = new Set<string>()
    for (const item of rows) {
      seen.add(item.id)
      const prior = previous.current.get(item.id)
      if (prior !== undefined && prior !== item.status) {
        void queryClient.invalidateQueries({ queryKey: ['path'] })
        const label = kindLabel(item.kind, t)
        if (item.status === 'done') announce('polite', tp('job.job_finished_items_processed', item.total > 0 ? item.total : item.done, { kind: label }))
        else if (item.status === 'cancelled') announce('polite', tp('job.job_cancelled_items_completed', item.done, { kind: label }))
        else if (item.status === 'interrupted') {
          const left = item.attempting.length + item.pending.length
          announce('assertive', `${tp('job.job_was_interrupted_by_server', item.done, { kind: label })}${left > 0 ? ` ${t('job.left', { count: left })}` : ''}`)
        } else if (item.status === 'error') {
          announce('assertive', `${t('job.job_failed', { kind: label })} ${item.message ? t(item.message, item.messageParams) : ''}`.trim())
        }
      }
      previous.current.set(item.id, item.status)
    }
    for (const id of previous.current.keys()) if (!seen.has(id)) previous.current.delete(id)
  }, [rows, t, tp])

  if (rows.length === 0 && !list.isError) {
    return <><div ref={politeRef} className="sc-job-tray__sr-only" role="status" aria-live="polite" aria-atomic="true"></div><div ref={assertiveRef} className="sc-job-tray__sr-only" role="alert" aria-live="assertive" aria-atomic="true"></div></>
  }

  const activeCount = rows.filter((row) => row.status === 'running').length
  const clearFinished = (): void => jobTray.forget(...rows.filter((row) => row.status !== 'running').map((row) => row.id))

  return (
    <>
      <div ref={politeRef} className="sc-job-tray__sr-only" role="status" aria-live="polite" aria-atomic="true"></div>
      <div ref={assertiveRef} className="sc-job-tray__sr-only" role="alert" aria-live="assertive" aria-atomic="true"></div>
      <section className={open ? 'sc-job-tray' : 'sc-job-tray sc-job-tray--collapsed'} aria-label={t('job.jobs')}>
        <header className="sc-job-tray__header">
          <button className="sc-job-tray__title" type="button" onClick={() => jobTray.setOpen(!open)} aria-expanded={open}>
            <Icon name="refresh" />
            <span>{t('job.jobs')}</span>
            <span>{activeCount > 0 ? `(${activeCount})` : t('common.done')}</span>
          </button>
          <div className="sc-job-tray__actions">
            <IconButton label={t('common.clear_finished_items')} onClick={clearFinished}><Icon name="check" /></IconButton>
            <IconButton label={open ? t('common.collapse') : t('common.expand')} expanded={open} onClick={() => jobTray.setOpen(!open)}><Icon name={open ? 'chevron_right' : 'chevron_left'} /></IconButton>
          </div>
        </header>
        {list.isError ? <p className="sc-job-tray__stale">{t('job.server_unreachable_so_may_not')}</p> : null}
        {open ? (
          <div className="sc-job-tray__scroll">
            <VirtualList
              className="sc-job-tray__list"
              items={rows}
              itemKey={(item) => item.id}
              estimateSize={128}
              itemProps={() => ({ className: 'sc-job-tray__item' })}
              renderItem={(item) => {
              const label = kindLabel(item.kind, t)
              const outstandingCount = item.attempting.length + item.pending.length
              return (
                <>
                  <div className="sc-job-tray__row"><span className="sc-job-tray__name"><Icon name={item.kind === 'delete' ? 'delete' : item.kind === 'copy' ? 'content_copy' : 'search'} />{label}</span><span className="sc-job-tray__meta">{item.done} / {item.total || '?'}</span></div>
                  <mdui-linear-progress value={item.total > 0 ? Math.min(Math.max(item.done / item.total, 0), 1) : undefined} aria-label={t('job.job', { kind: label })}></mdui-linear-progress>
                  {item.status === 'error' && item.message ? <p className="sc-job-tray__message">{t(item.message, item.messageParams)}</p> : null}
                  {item.status === 'cancelled' ? <p className="sc-job-tray__message">{t('job.cancelled_completed', { count: item.done })}</p> : null}
                  {item.status === 'interrupted' ? <p className="sc-job-tray__message">{t('job.interrupted_by_server_restart_completed', { count: item.done })}</p> : null}
                  {outstandingCount > 0 ? (
                    <details
                      className="sc-job-tray__outstanding"
                      open={expandedJobs.has(item.id)}
                      onToggle={(event) => {
                        const expanded = event.currentTarget.open
                        setExpandedJobs((current) => {
                          if (current.has(item.id) === expanded) return current
                          const next = new Set(current)
                          if (expanded) next.add(item.id)
                          else next.delete(item.id)
                          return next
                        })
                      }}
                    >
                      <summary>{t('job.items_left', { count: outstandingCount })}</summary>
                      {expandedJobs.has(item.id) ? (
                        <div className="sc-job-tray__outstanding-scroll">
                          <VirtualList
                            items={[...item.attempting, ...item.pending]}
                            itemKey={(path, index) => `${index < item.attempting.length ? 'attempting' : 'pending'}-${path}`}
                            estimateSize={32}
                            renderItem={(path, index) => (
                              <>
                                {index < item.attempting.length
                                  ? <span className="sc-job-tray__tag sc-job-tray__tag--check">{t('job.needs_checking')}</span>
                                  : <span className="sc-job-tray__tag">{t('job.not_started')}</span>}
                                {path}
                              </>
                            )}
                          />
                        </div>
                      ) : null}
                      {item.attempting.length > 0 ? <p className="sc-job-tray__message">{t('job.server_stopped_mid_item_anything')}</p> : null}
                      <p className="sc-job-tray__message">{t('job.anything_marked_not_started_untouched')}</p>
                    </details>
                  ) : null}
                  <div className="sc-job-tray__controls">
                    {item.status === 'running' ? <IconButton label={t('job.cancel_job')} onClick={() => cancel.mutate(item.id)}><Icon name="close" /></IconButton> : <IconButton label={t('common.clear')} onClick={() => jobTray.forget(item.id)}><Icon name="close" /></IconButton>}
                  </div>
                </>
              )
              }}
            />
          </div>
        ) : null}
      </section>
    </>
  )
}
