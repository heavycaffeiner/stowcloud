import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef } from 'react'
import type { KeyboardEvent } from 'react'
import { ALL_LOG_LEVELS, type AdminLogRecord, type AuditRow } from '../../api/types'
import { formatDateNs, formatDuration, formatNumber } from '../../i18n'
import { useI18n } from '../../i18n/use-i18n'
import { DEBOUNCE_MS, MAX_RECORDS, pureActorLabel, pureIncludesAudit, pureIncludesServer, pureInterleave, pureKnownSubsystems, pureServerOnlyFiltersActive, pureTimelineView, type LogSourceMode, type TimelineBar, type TimelineSeries, type UnifiedLogItem } from '../../admin/log-view'
import { adminAuditQuery, adminLogsQuery, adminTimelineQuery } from '../../query/logs'
import { adminUsersQuery } from '../../query/admin'
import { useDebounced } from '../../query/use-debounced'
import { logsForm } from '../../store/logs.store'
import { useStore } from '../../store/use-store'
import { Button } from '../Button'
import { TextField } from '../TextField'
import { ProgressCircular } from '../ProgressCircular'
import './LogsSection.css'

const SOURCE_MODES: readonly { mode: LogSourceMode; key: string }[] = [{ mode: 'all', key: 'logs.source_all' }, { mode: 'server', key: 'logs.server_log' }, { mode: 'audit', key: 'common.audit_log' }]
const LEVEL_KEY: Record<string, string> = { DEBUG: 'logs.level_debug', INFO: 'logs.level_info', WARN: 'logs.level_warn', ERROR: 'logs.level_error' }
const OUTCOME_KEY: Record<string, string> = { ok: 'audit.success', failed: 'audit.failure' }

/* i18n */ 'logs.arrow_keys_move_between_buckets'
/* i18n */ 'logs.level_debug'
/* i18n */ 'logs.level_error'
/* i18n */ 'logs.level_info'
/* i18n */ 'logs.level_warn'
/* i18n */ 'logs.loading_timeline'
/* i18n */ 'logs.show_the_numbers'
/* i18n */ 'logs.source_all'
/* i18n */ 'logs.time'
/* i18n */ 'logs.timeline_table_caption'
/* i18n */ 'logs.total'
export function LogsSection() {
  const { t, tp } = useI18n()
  const filters = useStore(logsForm, (state) => state.filters)
  const expanded = useStore(logsForm, (state) => state.expanded)
  const focusedBucket = useStore(logsForm, (state) => state.focusedBucket)
  const settled = useDebounced(filters, DEBOUNCE_MS)
  const includesServer = pureIncludesServer(settled.sourceMode)
  const includesAudit = pureIncludesAudit(settled.sourceMode)
  const logs = useInfiniteQuery(adminLogsQuery(settled, includesServer))
  const audit = useInfiniteQuery(adminAuditQuery(settled, includesAudit))
  const timeline = useQuery(adminTimelineQuery(settled, true))
  const users = useQuery({ ...adminUsersQuery(), enabled: includesAudit })
  const records: AdminLogRecord[] = includesServer ? logs.data?.pages.flatMap((page) => page.records) ?? [] : []
  const auditRows: AuditRow[] = includesAudit ? audit.data?.pages.flatMap((page) => page.rows) ?? [] : []
  const items = useMemo(() => pureInterleave(records, auditRows, MAX_RECORDS), [records, auditRows])
  const view = useMemo(() => pureTimelineView(timeline.data ?? null, settled.sourceMode), [timeline.data, settled.sourceMode])
  const serverOnlyActive = pureServerOnlyFiltersActive(filters)
  const knownSubsystems = pureKnownSubsystems(records)
  const loading = (includesServer && logs.isPending) || (includesAudit && audit.isPending)
  const loadingMore = (includesServer && logs.isFetchingNextPage) || (includesAudit && audit.isFetchingNextPage)
  const failed = (includesServer && logs.isError) || (includesAudit && audit.isError)
  const hasMore = (includesServer && logs.hasNextPage) || (includesAudit && audit.hasNextPage)
  const truncated = items.length >= MAX_RECORDS && hasMore
  const newestPage = logs.data?.pages.at(-1)
  const storedBytes = BigInt(newestPage?.stored_bytes ?? '0')
  const storedLabel = storedBytes / 1_048_576n === 0n ? t('logs.under_one_mb') : t('logs.megabytes', { size: formatNumber(Number(storedBytes / 1_048_576n)) })
  const barCount = view?.bars.length ?? 0
  const activeBucket = barCount === 0 ? -1 : Math.min(focusedBucket ?? barCount - 1, barCount - 1)
  const activeBar = activeBucket >= 0 ? view?.bars[activeBucket] ?? null : null
  const barRefs = useRef<Array<HTMLButtonElement | null>>([])
  tp('logs.attribute_count', 0)
  tp('logs.showing_records', 0)
  tp('logs.reached_the_cap', 0)
  function loadMore(): void { if (includesServer && logs.hasNextPage && !logs.isFetchingNextPage) void logs.fetchNextPage(); if (includesAudit && audit.hasNextPage && !audit.isFetchingNextPage) void audit.fetchNextPage() }
  function itemKey(item: UnifiedLogItem): string { return item.key }
  function moveFocus(index: number): void { if (!barCount) return; const next = Math.max(0, Math.min(index, barCount - 1)); logsForm.focusBucket(next); barRefs.current[next]?.focus() }
  function onPlotKeyDown(event: KeyboardEvent, index: number): void { const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : event.key === 'Home' ? -barCount : event.key === 'End' ? barCount : 0; if (!step) return; event.preventDefault(); moveFocus(index + step) }
  function seriesLabel(series: TimelineSeries): string { const key = series.source === 'server' ? LEVEL_KEY[series.name] : OUTCOME_KEY[series.name]; return key ? t(key) : series.name }
  function barName(bar: TimelineBar): string { const range = t('logs.bucket_range', { start: formatDateNs(bar.startNs), end: formatDateNs(bar.endNs) }); const total = tp('logs.event_count', bar.total, { count: formatNumber(bar.total) }); const parts = bar.segments.map((segment) => t('logs.series_count', { name: seriesLabel(segment), count: formatNumber(segment.count) })); return `${range}. ${total}${parts.length ? `. ${parts.join(', ')}` : ''}` }
  function countIn(bar: TimelineBar, key: string): number { return bar.segments.find((segment) => segment.key === key)?.count ?? 0 }
  function actorText(row: AuditRow): string { const actor = pureActorLabel(row, users.data ?? []); return actor.kind === 'system' ? t('common.system') : actor.kind === 'name' ? actor.name : t('common.user', { id: actor.id }) }
  function auditMeta(row: AuditRow): string[] { const result = [formatDateNs(row.ts_ns), actorText(row)]; if (row.target) result.push(row.target); if (row.ip) result.push(row.ip); if (row.detail) result.push(row.detail); return result }
  return <section className="sc-logs"><h2>{t('common.logs')}</h2><p className="sc-logs__hint">{t('logs.what_the_logs_are')}</p><dl className="sc-logs__figures"><div><dt>{t('logs.stored_size')}</dt><dd>{storedLabel}</dd></div><div><dt>{t('logs.segments')}</dt><dd>{formatNumber(newestPage?.segments ?? 0)}</dd></div></dl><form className="sc-logs__filters" onSubmit={(event) => event.preventDefault()}><div className="sc-logs__sources"><span className="sc-logs__group-label" id="sc-logs-source-label">{t('logs.source')}</span><div role="group" aria-labelledby="sc-logs-source-label">{SOURCE_MODES.map((option) => <button key={option.mode} type="button" className={filters.sourceMode === option.mode ? 'sc-logs__source-button sc-logs__source-button--active' : 'sc-logs__source-button'} aria-pressed={filters.sourceMode === option.mode} onClick={() => logsForm.patch({ sourceMode: option.mode })}>{t(option.key)}</button>)}</div></div><fieldset className="sc-logs__levels"><legend>{t('logs.level')}</legend><div className="sc-logs__level-boxes">{ALL_LOG_LEVELS.map((level) => <label key={level} className="sc-logs__check"><input type="checkbox" checked={filters.levels.has(level)} onChange={() => logsForm.toggleLevel(level)} />{LEVEL_KEY[level] ? t(LEVEL_KEY[level]) : level}</label>)}</div></fieldset><div className="sc-logs__fields"><TextField label={t('logs.search_text')} placeholder={t('logs.e_g_refused')} type="search" value={filters.text} onValueChange={(value) => logsForm.patch({ text: value })} autoComplete="off" /><TextField label={t('logs.subsystem')} placeholder={t('logs.e_g_dav')} list="sc-logs-subsystems" value={filters.subsystem} onValueChange={(value) => logsForm.patch({ subsystem: value })} autoComplete="off" /><datalist id="sc-logs-subsystems">{knownSubsystems.map((value) => <option key={value} value={value} />)}</datalist><TextField label={t('logs.request_id')} placeholder={t('logs.e_g_request_id')} value={filters.requestId} onValueChange={(value) => logsForm.patch({ requestId: value })} autoComplete="off" /><TextField label={t('logs.from')} type="datetime-local" value={filters.since} onValueChange={(value) => logsForm.patch({ since: value })} /><TextField label={t('logs.to')} type="datetime-local" value={filters.until} onValueChange={(value) => logsForm.patch({ until: value })} /></div>{serverOnlyActive && filters.sourceMode !== 'server' ? <p className="sc-logs__scope" role="status">{t('logs.server_only_filters_note')}</p> : null}<p className="sc-logs__auto-note" role="status">{t('logs.filters_update_automatically')}</p></form><section className="sc-logs__chart" aria-labelledby="sc-logs-chart-title"><div className="sc-logs__chart-head"><h3 id="sc-logs-chart-title">{t('logs.timeline')}</h3><p className="sc-logs__hint">{t('logs.timeline_description')}</p></div>{timeline.isPending && !view ? <ProgressCircular label={t('logs.loading_timeline')} /> : timeline.isError && !view ? <p className="sc-logs__note" role="status">{t('logs.could_not_load_timeline')}</p> : view ? <>{view.truncated ? <p className="sc-logs__warn" role="status">{t('logs.timeline_truncated')}</p> : null}{view.total === 0 ? <div className="sc-logs__empty"><p>{t('logs.no_events_in_window')}</p><p className="sc-logs__empty-hint">{t('logs.widen_the_filters')}</p></div> : <><ul className="sc-logs__legend">{view.series.map((series) => <li key={series.key}><span className={`sc-logs__swatch sc-logs__seg--${series.source}-${series.name.toLowerCase()}`} /><span>{series.source === 'server' ? t('logs.server_log') : t('common.audit_log')} {seriesLabel(series)}</span></li>)}</ul><p className="sc-sr-only" id="sc-logs-plot-hint">{t('logs.arrow_keys_move_between_buckets')}</p><div className="sc-logs__plot" role="group" aria-labelledby="sc-logs-chart-title" aria-describedby="sc-logs-plot-hint">{view.bars.map((bar, index) => <button key={bar.startNs} ref={(node) => { barRefs.current[index] = node }} type="button" className={index === activeBucket ? 'sc-logs__bar sc-logs__bar--active' : 'sc-logs__bar'} tabIndex={index === activeBucket ? 0 : -1} aria-label={barName(bar)} onClick={() => logsForm.focusBucket(index)} onFocus={() => logsForm.focusBucket(index)} onKeyDown={(event) => onPlotKeyDown(event, index)}><span className="sc-logs__stack">{bar.total === 0 ? <span className="sc-logs__baseline" /> : bar.segments.map((segment) => <span key={segment.key} className={`sc-logs__segment sc-logs__seg--${segment.key}`} style={{ flexGrow: segment.count }} />)}</span></button>)}</div><div className="sc-logs__axis"><span>{formatDateNs(view.bars[0].startNs)}</span><span>{t('logs.bucket_width', { duration: formatDuration(Math.max(Number(BigInt(view.bucketNs) / 1_000_000_000n), 1)) })}</span><span>{formatDateNs(view.bars.at(-1)?.endNs ?? view.bars[0].endNs)}</span></div>{activeBar ? <p className="sc-logs__readout" role="status" aria-live="polite">{barName(activeBar)}</p> : null}<details className="sc-logs__table-wrap"><summary>{t('logs.show_the_numbers')}</summary><div className="sc-logs__table-scroll"><table className="sc-logs__table"><caption>{t('logs.timeline_table_caption')}</caption><thead><tr><th scope="col">{t('logs.time')}</th>{view.series.map((series) => <th scope="col" key={series.key}>{series.source === 'server' ? t('logs.server_log') : t('common.audit_log')} {seriesLabel(series)}</th>)}<th scope="col">{t('logs.total')}</th></tr></thead><tbody>{view.bars.map((bar) => <tr key={bar.startNs}><th scope="row">{formatDateNs(bar.startNs)}</th>{view.series.map((series) => <td key={series.key}>{formatNumber(countIn(bar, series.key))}</td>)}<td>{formatNumber(bar.total)}</td></tr>)}</tbody></table></div></details></>}</> : null}</section><p className="sc-sr-only" role="status" aria-live="polite">{loading ? t('logs.loading_logs') : failed ? t('logs.could_not_load_logs') : items.length === 0 ? t('logs.no_records_match') : tp('logs.showing_records', items.length)}</p>{loading ? <ProgressCircular label={t('logs.loading_logs')} /> : failed ? <p className="sc-logs__error" role="alert">{t('logs.could_not_load_logs')}</p> : items.length === 0 ? <div className="sc-logs__empty"><p>{t('logs.no_records_match')}</p><p className="sc-logs__empty-hint">{t('logs.widen_the_filters')}</p></div> : <><ul className="sc-logs__list">{items.map((item: UnifiedLogItem) => <li className="sc-logs__item" key={item.key}>{item.source === 'audit' ? <div className="sc-logs__row"><span className="sc-logs__level">{item.row.ok ? t('audit.success') : t('audit.failure')}</span><span className="sc-logs__body"><span className="sc-logs__msg">{item.row.event}</span><span className="sc-logs__meta"><span className="sc-logs__source">{t('common.audit_log')}</span>{auditMeta(item.row).map((part, index) => <span key={`${item.row.rowid}-${index}`}>{part}</span>)}</span></span></div> : <>{(() => { const attrs = Object.entries(item.record.attrs); const open = expanded.has(item.key); const body = <><span className={`sc-logs__level sc-logs__level--${item.record.level.toLowerCase()}`}>{LEVEL_KEY[item.record.level] ? t(LEVEL_KEY[item.record.level]) : item.record.level}</span><span className="sc-logs__body"><span className="sc-logs__msg">{item.record.msg}</span><span className="sc-logs__meta"><span className="sc-logs__source">{t('logs.server_log')}</span><span>{formatDateNs(item.record.ts_ns)}</span>{item.record.subsystem ? <span>{item.record.subsystem}</span> : null}{item.record.request_id ? <span>{t('logs.request', { id: item.record.request_id })}</span> : null}</span></span></>; return attrs.length ? <><button type="button" className="sc-logs__row sc-logs__row--button" aria-expanded={open} onClick={() => logsForm.toggleExpanded(item.key)}>{body}<span className="sc-logs__disclose">{tp('logs.attribute_count', attrs.length)}</span></button>{open ? <dl className="sc-logs__attrs">{attrs.map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl> : null}</> : <div className="sc-logs__row">{body}</div> })()}</>}</li>)}</ul>{truncated ? <p className="sc-logs__note" role="status">{tp('logs.reached_the_cap', items.length, { count: formatNumber(items.length) })}</p> : hasMore ? <div className="sc-logs__more"><Button variant="text" onClick={loadMore} loading={loadingMore}>{t('logs.load_more')}</Button></div> : null}</>}
  </section>
}
