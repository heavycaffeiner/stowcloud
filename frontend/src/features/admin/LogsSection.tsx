import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { useMemo, useRef } from 'react'
import type { KeyboardEvent } from 'react'
import { ALL_LOG_LEVELS, type AdminLogRecord, type AuditRow } from '../../lib/api/types'
import { formatDateNs, formatDuration, formatNumber } from '../../lib/i18n'
import { useI18n } from '../../hooks/use-i18n'
import {
  DEBOUNCE_MS,
  MAX_RECORDS,
  pureActorLabel,
  pureIncludesAudit,
  pureIncludesServer,
  pureInterleave,
  pureKnownSubsystems,
  pureServerOnlyFiltersActive,
  pureTimelineView,
  type LogSourceMode,
  type TimelineBar,
  type TimelineSeries,
  type UnifiedLogItem
} from '../../lib/admin/log-view'
import { adminAuditQuery, adminLogsQuery, adminTimelineQuery } from '../../lib/query/logs'
import { adminUsersQuery } from '../../lib/query/admin'
import { useDebounced } from '../../hooks/use-debounced'
import { logsForm, useLogsFormStore } from '../../lib/store/logs.store'
import { Button } from '../../ui/Button'
import { TextField } from '../../ui/TextField'
import { ProgressCircular } from '../../ui/ProgressCircular'
import { Checkbox } from '../../ui/Checkbox'
import { VirtualList } from '../../ui/VirtualList'
import * as styles from './LogsSection.css'
import * as utilitiesStyles from '../../ui/utilities.css'
import { cx } from '../../ui/cx'

const SOURCE_MODES: readonly { mode: LogSourceMode; key: string }[] = [
  { mode: 'all', key: 'logs.source_all' },
  { mode: 'server', key: 'logs.server_log' },
  { mode: 'audit', key: 'common.audit_log' }
]
const LEVEL_KEY: Record<string, string> = {
  DEBUG: 'logs.level_debug',
  INFO: 'logs.level_info',
  WARN: 'logs.level_warn',
  ERROR: 'logs.level_error'
}
const OUTCOME_KEY: Record<string, string> = { ok: 'audit.success', failed: 'audit.failure' }
const SEGMENT_CLASS: Record<string, string> = {
  'server-debug': styles.segServerDebug,
  'server-info': styles.segServerInfo,
  'server-warn': styles.segServerWarn,
  'server-error': styles.segServerError,
  'audit-ok': styles.segAuditOk,
  'audit-failed': styles.segAuditFailed
}
const LEVEL_CLASS: Record<string, string> = {
  error: styles.levelError,
  failed: styles.levelFailed,
  warn: styles.levelWarn,
  info: styles.levelInfo,
  ok: styles.levelOk
}

/* i18n */ ;('logs.arrow_keys_move_between_buckets')
/* i18n */ ;('logs.level_debug')
/* i18n */ ;('logs.level_error')
/* i18n */ ;('logs.level_info')
/* i18n */ ;('logs.level_warn')
/* i18n */ ;('logs.loading_timeline')
/* i18n */ ;('logs.show_the_numbers')
/* i18n */ ;('logs.source_all')
/* i18n */ ;('logs.time')
/* i18n */ ;('logs.timeline_table_caption')
/* i18n */ ;('logs.total')
export function LogsSection() {
  const { t, tp } = useI18n()
  const filters = useLogsFormStore((state) => state.filters)
  const expanded = useLogsFormStore((state) => state.expanded)
  const focusedBucket = useLogsFormStore((state) => state.focusedBucket)
  const settled = useDebounced(filters, DEBOUNCE_MS)
  const includesServer = pureIncludesServer(settled.sourceMode)
  const includesAudit = pureIncludesAudit(settled.sourceMode)
  const logs = useInfiniteQuery(adminLogsQuery(settled, includesServer))
  const audit = useInfiniteQuery(adminAuditQuery(settled, includesAudit))
  const timeline = useQuery(adminTimelineQuery(settled, true))
  const users = useQuery({ ...adminUsersQuery(), enabled: includesAudit })
  const records: AdminLogRecord[] = includesServer ? (logs.data?.pages.flatMap((page) => page.records) ?? []) : []
  const auditRows: AuditRow[] = includesAudit ? (audit.data?.pages.flatMap((page) => page.rows) ?? []) : []
  const items = useMemo(() => pureInterleave(records, auditRows, MAX_RECORDS), [records, auditRows])
  const view = useMemo(
    () => pureTimelineView(timeline.data ?? null, settled.sourceMode),
    [timeline.data, settled.sourceMode]
  )
  const serverOnlyActive = pureServerOnlyFiltersActive(filters)
  const knownSubsystems = pureKnownSubsystems(records)
  const loading = (includesServer && logs.isPending) || (includesAudit && audit.isPending)
  const loadingMore = (includesServer && logs.isFetchingNextPage) || (includesAudit && audit.isFetchingNextPage)
  const failed = (includesServer && logs.isError) || (includesAudit && audit.isError)
  const hasMore = (includesServer && logs.hasNextPage) || (includesAudit && audit.hasNextPage)
  const truncated = items.length >= MAX_RECORDS && hasMore
  const newestPage = logs.data?.pages.at(-1)
  const storedBytes = BigInt(newestPage?.stored_bytes ?? '0')
  const storedLabel =
    storedBytes / 1_048_576n === 0n
      ? t('logs.under_one_mb')
      : t('logs.megabytes', { size: formatNumber(Number(storedBytes / 1_048_576n)) })
  const barCount = view?.bars.length ?? 0
  const activeBucket = barCount === 0 ? -1 : Math.min(focusedBucket ?? barCount - 1, barCount - 1)
  const activeBar = activeBucket >= 0 ? (view?.bars[activeBucket] ?? null) : null
  const barRefs = useRef<Array<HTMLButtonElement | null>>([])
  tp('logs.attribute_count', 0)
  tp('logs.showing_records', 0)
  tp('logs.reached_the_cap', 0)
  function loadMore(): void {
    if (includesServer && logs.hasNextPage && !logs.isFetchingNextPage) void logs.fetchNextPage()
    if (includesAudit && audit.hasNextPage && !audit.isFetchingNextPage) void audit.fetchNextPage()
  }
  function itemKey(item: UnifiedLogItem): string {
    return item.key
  }
  function moveFocus(index: number): void {
    if (!barCount) return
    const next = Math.max(0, Math.min(index, barCount - 1))
    logsForm.focusBucket(next)
    barRefs.current[next]?.focus()
  }
  function onPlotKeyDown(event: KeyboardEvent, index: number): void {
    const step =
      event.key === 'ArrowRight'
        ? 1
        : event.key === 'ArrowLeft'
          ? -1
          : event.key === 'Home'
            ? -barCount
            : event.key === 'End'
              ? barCount
              : 0
    if (!step) return
    event.preventDefault()
    moveFocus(index + step)
  }
  function seriesLabel(series: TimelineSeries): string {
    const key = series.source === 'server' ? LEVEL_KEY[series.name] : OUTCOME_KEY[series.name]
    return key ? t(key) : series.name
  }
  function barName(bar: TimelineBar): string {
    const range = t('logs.bucket_range', { start: formatDateNs(bar.startNs), end: formatDateNs(bar.endNs) })
    const total = tp('logs.event_count', bar.total, { count: formatNumber(bar.total) })
    const parts = bar.segments.map((segment) =>
      t('logs.series_count', { name: seriesLabel(segment), count: formatNumber(segment.count) })
    )
    return `${range}. ${total}${parts.length ? `. ${parts.join(', ')}` : ''}`
  }
  function countIn(bar: TimelineBar, key: string): number {
    return bar.segments.find((segment) => segment.key === key)?.count ?? 0
  }
  function actorText(row: AuditRow): string {
    const actor = pureActorLabel(row, users.data ?? [])
    return actor.kind === 'system'
      ? t('common.system')
      : actor.kind === 'name'
        ? actor.name
        : t('common.user', { id: actor.id })
  }
  function auditMeta(row: AuditRow): string[] {
    const result = [formatDateNs(row.ts_ns), actorText(row)]
    if (row.target) result.push(row.target)
    if (row.ip) result.push(row.ip)
    if (row.detail) result.push(row.detail)
    return result
  }
  return (
    <section className={styles.root}>
      <h2 className={styles.title}>{t('common.logs')}</h2>
      <p className={styles.hint}>{t('logs.what_the_logs_are')}</p>
      <dl className={styles.figures}>
        <div className={styles.figure}>
          <dt className={styles.figureLabel}>{t('logs.stored_size')}</dt>
          <dd className={styles.figureValue}>{storedLabel}</dd>
        </div>
        <div className={styles.figure}>
          <dt className={styles.figureLabel}>{t('logs.segments')}</dt>
          <dd className={styles.figureValue}>{formatNumber(newestPage?.segments ?? 0)}</dd>
        </div>
      </dl>
      <form className={styles.filters} onSubmit={(event) => event.preventDefault()}>
        <div className={styles.sources}>
          <span className={styles.groupLabel} id="sc-logs-source-label">
            {t('logs.source')}
          </span>
          <div role="group" aria-labelledby="sc-logs-source-label">
            {SOURCE_MODES.map((option) => (
              <button
                key={option.mode}
                type="button"
                className={cx(styles.sourceButton, filters.sourceMode === option.mode && styles.sourceButtonActive)}
                aria-pressed={filters.sourceMode === option.mode}
                onClick={() => logsForm.patch({ sourceMode: option.mode })}
              >
                {t(option.key)}
              </button>
            ))}
          </div>
        </div>
        <fieldset className={styles.levels}>
          <legend className={styles.levelsLegend}>{t('logs.level')}</legend>
          <div className={styles.levelBoxes}>
            {ALL_LOG_LEVELS.map((level) => (
              <Checkbox
                key={level}
                checked={filters.levels.has(level)}
                label={LEVEL_KEY[level] ? t(LEVEL_KEY[level]) : level}
                onChange={() => logsForm.toggleLevel(level)}
              />
            ))}
          </div>
        </fieldset>
        <div className={styles.fields}>
          <TextField
            className={styles.field}
            label={t('logs.search_text')}
            placeholder={t('logs.e_g_refused')}
            type="search"
            value={filters.text}
            onValueChange={(value) => logsForm.patch({ text: value })}
            autoComplete="off"
          />
          <TextField
            className={styles.field}
            label={t('logs.subsystem')}
            placeholder={t('logs.e_g_dav')}
            list="sc-logs-subsystems"
            value={filters.subsystem}
            onValueChange={(value) => logsForm.patch({ subsystem: value })}
            autoComplete="off"
          />
          <datalist id="sc-logs-subsystems">
            {knownSubsystems.map((value) => (
              <option key={value} value={value} />
            ))}
          </datalist>
          <TextField
            className={styles.field}
            label={t('logs.request_id')}
            placeholder={t('logs.e_g_request_id')}
            value={filters.requestId}
            onValueChange={(value) => logsForm.patch({ requestId: value })}
            autoComplete="off"
          />
          <TextField
            className={styles.field}
            label={t('logs.from')}
            type="datetime-local"
            value={filters.since}
            onValueChange={(value) => logsForm.patch({ since: value })}
          />
          <TextField
            className={styles.field}
            label={t('logs.to')}
            type="datetime-local"
            value={filters.until}
            onValueChange={(value) => logsForm.patch({ until: value })}
          />
        </div>
        {serverOnlyActive && filters.sourceMode !== 'server' ? (
          <p className={styles.scope} role="status">
            {t('logs.server_only_filters_note')}
          </p>
        ) : null}
        <p className={styles.autoNote} role="status">
          {t('logs.filters_update_automatically')}
        </p>
      </form>
      <section className={styles.chart} aria-labelledby="sc-logs-chart-title">
        <div className={styles.chartHead}>
          <h3 id="sc-logs-chart-title" className={styles.timelineTitle}>
            {t('logs.timeline')}
          </h3>
          <p className={styles.hint}>{t('logs.timeline_description')}</p>
        </div>
        {timeline.isPending && !view ? (
          <ProgressCircular label={t('logs.loading_timeline')} />
        ) : timeline.isError && !view ? (
          <p className={styles.note} role="status">
            {t('logs.could_not_load_timeline')}
          </p>
        ) : view ? (
          <>
            {view.truncated ? (
              <p className={styles.warn} role="status">
                {t('logs.timeline_truncated')}
              </p>
            ) : null}
            {view.total === 0 ? (
              <div className={styles.empty}>
                <p className={styles.emptyText}>{t('logs.no_events_in_window')}</p>
                <p className={cx(styles.emptyText, styles.emptyHint)}>{t('logs.widen_the_filters')}</p>
              </div>
            ) : (
              <>
                <ul className={styles.legend}>
                  {view.series.map((series) => (
                    <li key={series.key} className={styles.legendItem}>
                      <span
                        className={cx(styles.swatch, SEGMENT_CLASS[`${series.source}-${series.name.toLowerCase()}`])}
                      />
                      <span>
                        {series.source === 'server' ? t('logs.server_log') : t('common.audit_log')}{' '}
                        {seriesLabel(series)}
                      </span>
                    </li>
                  ))}
                </ul>
                <p className={utilitiesStyles.srOnly} id="sc-logs-plot-hint">
                  {t('logs.arrow_keys_move_between_buckets')}
                </p>
                <div
                  className={styles.plot}
                  role="group"
                  aria-labelledby="sc-logs-chart-title"
                  aria-describedby="sc-logs-plot-hint"
                >
                  {view.bars.map((bar, index) => (
                    <button
                      key={bar.startNs}
                      ref={(node) => {
                        barRefs.current[index] = node
                      }}
                      type="button"
                      className={cx(styles.bar, index === activeBucket && styles.barActive)}
                      tabIndex={index === activeBucket ? 0 : -1}
                      aria-label={barName(bar)}
                      onClick={() => logsForm.focusBucket(index)}
                      onFocus={() => logsForm.focusBucket(index)}
                      onKeyDown={(event) => onPlotKeyDown(event, index)}
                    >
                      <span className={styles.stack}>
                        {bar.total === 0 ? (
                          <span className={styles.baseline} />
                        ) : (
                          bar.segments.map((segment) => (
                            <span
                              key={segment.key}
                              className={cx(
                                styles.seg,
                                SEGMENT_CLASS[`${segment.source}-${segment.name.toLowerCase()}`]
                              )}
                              style={{ height: `${segment.percent}%` }}
                            />
                          ))
                        )}
                      </span>
                    </button>
                  ))}
                </div>
                <div className={styles.axis}>
                  <span>{formatDateNs(view.bars[0].startNs)}</span>
                  <span>
                    {t('logs.bucket_width', {
                      duration: formatDuration(Math.max(Number(BigInt(view.bucketNs) / 1_000_000_000n), 1))
                    })}
                  </span>
                  <span>{formatDateNs(view.bars.at(-1)?.endNs ?? view.bars[0].endNs)}</span>
                </div>
                {activeBar ? (
                  <p className={styles.readout} role="status" aria-live="polite">
                    {barName(activeBar)}
                  </p>
                ) : null}
                <details className={styles.tableWrap}>
                  <summary className={styles.tableSummary}>{t('logs.show_the_numbers')}</summary>
                  <div className={styles.tableScroll}>
                    <table className={styles.table}>
                      <caption className={styles.tableCaption}>{t('logs.timeline_table_caption')}</caption>
                      <thead>
                        <tr>
                          <th scope="col" className={cx(styles.tableCell, styles.tableCol)}>
                            {t('logs.time')}
                          </th>
                          {view.series.map((series) => (
                            <th scope="col" key={series.key} className={cx(styles.tableCell, styles.tableCol)}>
                              {series.source === 'server' ? t('logs.server_log') : t('common.audit_log')}{' '}
                              {seriesLabel(series)}
                            </th>
                          ))}
                          <th scope="col" className={cx(styles.tableCell, styles.tableCol)}>
                            {t('logs.total')}
                          </th>
                        </tr>
                      </thead>
                      <tbody>
                        {view.bars.map((bar) => (
                          <tr key={bar.startNs}>
                            <th scope="row" className={styles.tableCell}>
                              {formatDateNs(bar.startNs)}
                            </th>
                            {view.series.map((series) => (
                              <td key={series.key} className={cx(styles.tableCell, styles.tableNum)}>
                                {formatNumber(countIn(bar, series.key))}
                              </td>
                            ))}
                            <td className={cx(styles.tableCell, styles.tableNum)}>{formatNumber(bar.total)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </details>
              </>
            )}
          </>
        ) : null}
      </section>
      <p className={utilitiesStyles.srOnly} role="status" aria-live="polite">
        {loading
          ? t('logs.loading_logs')
          : failed
            ? t('logs.could_not_load_logs')
            : items.length === 0
              ? t('logs.no_records_match')
              : tp('logs.showing_records', items.length)}
      </p>
      {loading ? (
        <ProgressCircular label={t('logs.loading_logs')} />
      ) : failed ? (
        <p className={styles.error} role="alert">
          {t('logs.could_not_load_logs')}
        </p>
      ) : items.length === 0 ? (
        <div className={styles.empty}>
          <p className={styles.emptyText}>{t('logs.no_records_match')}</p>
          <p className={cx(styles.emptyText, styles.emptyHint)}>{t('logs.widen_the_filters')}</p>
        </div>
      ) : (
        <>
          <VirtualList
            className={styles.list}
            items={items}
            itemKey={itemKey}
            estimateSize={80}
            itemProps={() => ({ className: styles.item })}
            renderItem={(item: UnifiedLogItem) => (
              <>
                {item.source === 'audit' ? (
                  <div className={styles.row}>
                    <span className={styles.level}>{item.row.ok ? t('audit.success') : t('audit.failure')}</span>
                    <span className={styles.body}>
                      <span className={styles.msg}>{item.row.event}</span>
                      <span className={styles.meta}>
                        <span className={styles.source}>{t('common.audit_log')}</span>
                        {auditMeta(item.row).map((part, index) => (
                          <span key={`${item.row.rowid}-${index}`}>{part}</span>
                        ))}
                      </span>
                    </span>
                  </div>
                ) : (
                  <>
                    {(() => {
                      const attrs = Object.entries(item.record.attrs)
                      const open = expanded.has(item.key)
                      const body = (
                        <>
                          <span className={cx(styles.level, LEVEL_CLASS[item.record.level.toLowerCase()])}>
                            {LEVEL_KEY[item.record.level] ? t(LEVEL_KEY[item.record.level]) : item.record.level}
                          </span>
                          <span className={styles.body}>
                            <span className={styles.msg}>{item.record.msg}</span>
                            <span className={styles.meta}>
                              <span className={styles.source}>{t('logs.server_log')}</span>
                              <span>{formatDateNs(item.record.ts_ns)}</span>
                              {item.record.subsystem ? <span>{item.record.subsystem}</span> : null}
                              {item.record.request_id ? (
                                <span>{t('logs.request', { id: item.record.request_id })}</span>
                              ) : null}
                            </span>
                          </span>
                        </>
                      )
                      return attrs.length ? (
                        <>
                          <button
                            type="button"
                            className={cx(styles.row, styles.rowButton)}
                            aria-expanded={open}
                            onClick={() => logsForm.toggleExpanded(item.key)}
                          >
                            {body}
                            <span className={styles.disclose}>{tp('logs.attribute_count', attrs.length)}</span>
                          </button>
                          {open ? (
                            <dl className={styles.attrs}>
                              {attrs.map(([key, value]) => (
                                <div key={key} className={styles.attr}>
                                  <dt className={styles.attrLabel}>{key}</dt>
                                  <dd className={styles.attrValue}>{value}</dd>
                                </div>
                              ))}
                            </dl>
                          ) : null}
                        </>
                      ) : (
                        <div className={styles.row}>{body}</div>
                      )
                    })()}
                  </>
                )}
              </>
            )}
          />
          {truncated ? (
            <p className={styles.note} role="status">
              {tp('logs.reached_the_cap', items.length, { count: formatNumber(items.length) })}
            </p>
          ) : hasMore ? (
            <div className={styles.more}>
              <Button variant="text" onClick={loadMore} loading={loadingMore}>
                {t('logs.load_more')}
              </Button>
            </div>
          ) : null}
        </>
      )}
    </section>
  )
}
