import { useMemo, useRef, useState } from 'react'
import type { KeyboardEvent } from 'react'
import { Controller, useForm, useWatch, type Control } from 'react-hook-form'
import { useDebouncedValue, useSet } from 'react-simplikit'
import { formatDateNs, formatDuration, formatNumber, t, tp } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import {
  DEBOUNCE_MS,
  EMPTY_FILTERS,
  MAX_RECORDS,
  pureActorLabel,
  pureIncludesAudit,
  pureIncludesServer,
  pureInterleave,
  pureKnownSubsystems,
  pureServerOnlyFiltersActive,
  pureTimelineView,
  type LogFilters,
  type LogSourceMode,
  type TimelineBar,
  type TimelineSeries,
  type UnifiedLogItem
} from '../logic/log-view'
import {
  ALL_LOG_LEVELS,
  useAdminAudit,
  useAdminLogs,
  useAdminTimeline,
  useAdminUsers,
  type AdminLogRecord,
  type AdminUser,
  type AuditRow
} from '../api'
import { Button } from '../../../ui/Button'
import { FormTextField } from '../../../ui/FormTextField'
import { ProgressCircular } from '../../../ui/ProgressCircular'
import { Checkbox } from '../../../ui/Checkbox'
import { VirtualList } from '../../../ui/VirtualList'
import * as styles from './LogsSection.css'
import * as utilitiesStyles from '../../../ui/utilities.css'
import { cx } from '../../../ui/cx'

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
  warn: styles.levelWarn,
  info: styles.levelInfo
}

/* i18n */ ;('logs.level_debug')
/* i18n */ ;('logs.level_error')
/* i18n */ ;('logs.level_info')
/* i18n */ ;('logs.level_warn')
/* i18n */ ;('logs.source_all')

type FilterValues = Omit<LogFilters, 'levels'> & { levels: string[] }

const DEFAULT_VALUES: FilterValues = { ...EMPTY_FILTERS, levels: [] }

// A fresh copy, so the watcher's deep comparison never sees the form's own mutable arrays.
const snapshot = (values: FilterValues): FilterValues => ({ ...values, levels: [...values.levels] })

const toFilters = (values: FilterValues): LogFilters => ({ ...values, levels: new Set(values.levels) })

const levelText = (level: string): string => (LEVEL_KEY[level] ? t(LEVEL_KEY[level]) : level)

const sourceText = (series: TimelineSeries): string =>
  series.source === 'server' ? t('logs.server_log') : t('common.audit_log')

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

const countIn = (bar: TimelineBar, key: string): number =>
  bar.segments.find((segment) => segment.key === key)?.count ?? 0

function auditMeta(row: AuditRow, users: readonly AdminUser[]): string[] {
  const actor = pureActorLabel(row, users)
  const actorText =
    actor.kind === 'system'
      ? t('common.system')
      : actor.kind === 'name'
        ? actor.name
        : t('common.user', { id: actor.id })
  return [formatDateNs(row.ts_ns), actorText, row.target, row.ip, row.detail].filter((part): part is string =>
    Boolean(part)
  )
}

export function LogsSection() {
  const { t, tp } = useI18n()
  const { control } = useForm<FilterValues>({ defaultValues: DEFAULT_VALUES })
  // A keystroke is not a request: the queries follow the filters once typing settles.
  const settledValues = useDebouncedValue(useWatch({ control, compute: snapshot }), DEBOUNCE_MS)
  const settled = useMemo(() => toFilters(settledValues), [settledValues])
  // Results for other filters remount the timeline and the list, which drops their cursor and expansions.
  const settledKey = JSON.stringify(settledValues)
  const includesServer = pureIncludesServer(settled.sourceMode)
  const includesAudit = pureIncludesAudit(settled.sourceMode)
  const logs = useAdminLogs(settled, includesServer)
  const audit = useAdminAudit(includesAudit)
  const users = useAdminUsers(includesAudit)
  const records = useMemo<readonly AdminLogRecord[]>(
    () => (includesServer ? (logs.data?.pages.flatMap((page) => page.records) ?? []) : []),
    [includesServer, logs.data]
  )
  const items = useMemo(
    () =>
      pureInterleave(records, includesAudit ? (audit.data?.pages.flatMap((page) => page.rows) ?? []) : [], MAX_RECORDS),
    [records, includesAudit, audit.data]
  )
  const loading = (includesServer && logs.isPending) || (includesAudit && audit.isPending)
  const loadingMore = (includesServer && logs.isFetchingNextPage) || (includesAudit && audit.isFetchingNextPage)
  const failed = (includesServer && logs.isError) || (includesAudit && audit.isError)
  const hasMore = (includesServer && logs.hasNextPage) || (includesAudit && audit.hasNextPage)
  const truncated = items.length >= MAX_RECORDS && hasMore
  const newestPage = logs.data?.pages.at(-1)
  const storedMegabytes = BigInt(newestPage?.stored_bytes ?? '0') / 1_048_576n
  const loadMore = (): void => {
    if (includesServer && logs.hasNextPage && !logs.isFetchingNextPage) void logs.fetchNextPage()
    if (includesAudit && audit.hasNextPage && !audit.isFetchingNextPage) void audit.fetchNextPage()
  }

  return (
    <section className={styles.root}>
      <h2 className={styles.title}>{t('common.logs')}</h2>
      <p className={styles.hint}>{t('logs.what_the_logs_are')}</p>
      <dl className={styles.figures}>
        <div className={styles.figure}>
          <dt className={styles.figureLabel}>{t('logs.stored_size')}</dt>
          <dd className={styles.figureValue}>
            {storedMegabytes === 0n
              ? t('logs.under_one_mb')
              : t('logs.megabytes', { size: formatNumber(Number(storedMegabytes)) })}
          </dd>
        </div>
        <div className={styles.figure}>
          <dt className={styles.figureLabel}>{t('logs.segments')}</dt>
          <dd className={styles.figureValue}>{formatNumber(newestPage?.segments ?? 0)}</dd>
        </div>
      </dl>
      <FilterForm control={control} knownSubsystems={pureKnownSubsystems(records)} />
      <LogTimeline key={settledKey} filters={settled} />
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
          <LogList key={settledKey} items={items} users={users.data ?? []} />
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

function FilterForm({
  control,
  knownSubsystems
}: {
  control: Control<FilterValues>
  knownSubsystems: readonly string[]
}) {
  const { t } = useI18n()
  const scopeNote = useWatch({
    control,
    compute: (values: FilterValues) => values.sourceMode !== 'server' && pureServerOnlyFiltersActive(toFilters(values))
  })
  return (
    <form className={styles.filters} onSubmit={(event) => event.preventDefault()}>
      <div className={styles.sources}>
        <span className={styles.groupLabel} id="sc-logs-source-label">
          {t('logs.source')}
        </span>
        <Controller
          control={control}
          name="sourceMode"
          render={({ field }) => (
            <div role="group" aria-labelledby="sc-logs-source-label">
              {SOURCE_MODES.map((option) => (
                <button
                  key={option.mode}
                  type="button"
                  className={cx(styles.sourceButton, field.value === option.mode && styles.sourceButtonActive)}
                  aria-pressed={field.value === option.mode}
                  onClick={() => field.onChange(option.mode)}
                >
                  {t(option.key)}
                </button>
              ))}
            </div>
          )}
        />
      </div>
      <fieldset className={styles.levels}>
        <legend className={styles.levelsLegend}>{t('logs.level')}</legend>
        <Controller
          control={control}
          name="levels"
          render={({ field }) => (
            <div className={styles.levelBoxes}>
              {ALL_LOG_LEVELS.map((level) => (
                <Checkbox
                  key={level}
                  checked={field.value.includes(level)}
                  label={levelText(level)}
                  onChange={(checked) =>
                    field.onChange(checked ? [...field.value, level] : field.value.filter((item) => item !== level))
                  }
                />
              ))}
            </div>
          )}
        />
      </fieldset>
      <div className={styles.fields}>
        <FormTextField
          control={control}
          name="text"
          className={styles.field}
          label={t('logs.search_text')}
          placeholder={t('logs.e_g_refused')}
          type="search"
          autoComplete="off"
        />
        <FormTextField
          control={control}
          name="subsystem"
          className={styles.field}
          label={t('logs.subsystem')}
          placeholder={t('logs.e_g_dav')}
          list="sc-logs-subsystems"
          autoComplete="off"
        />
        <datalist id="sc-logs-subsystems">
          {knownSubsystems.map((value) => (
            <option key={value} value={value} />
          ))}
        </datalist>
        <FormTextField
          control={control}
          name="requestId"
          className={styles.field}
          label={t('logs.request_id')}
          placeholder={t('logs.e_g_request_id')}
          autoComplete="off"
        />
        <FormTextField
          control={control}
          name="since"
          className={styles.field}
          label={t('logs.from')}
          type="datetime-local"
        />
        <FormTextField
          control={control}
          name="until"
          className={styles.field}
          label={t('logs.to')}
          type="datetime-local"
        />
      </div>
      {scopeNote ? (
        <p className={styles.scope} role="status">
          {t('logs.server_only_filters_note')}
        </p>
      ) : null}
      <p className={styles.autoNote} role="status">
        {t('logs.filters_update_automatically')}
      </p>
    </form>
  )
}

function LogTimeline({ filters }: { filters: LogFilters }) {
  const { t } = useI18n()
  const timeline = useAdminTimeline(filters, true)
  const view = useMemo(
    () => pureTimelineView(timeline.data ?? null, filters.sourceMode),
    [timeline.data, filters.sourceMode]
  )
  // Null until the reader moves it, so the first Tab lands on the newest bucket.
  const [focusedBucket, setFocusedBucket] = useState<number | null>(null)
  const barRefs = useRef<Array<HTMLButtonElement | null>>([])
  const barCount = view?.bars.length ?? 0
  const activeBucket = barCount === 0 ? -1 : Math.min(focusedBucket ?? barCount - 1, barCount - 1)
  const activeBar = activeBucket >= 0 ? (view?.bars[activeBucket] ?? null) : null
  const onPlotKeyDown = (event: KeyboardEvent, index: number): void => {
    const steps: Record<string, number> = { ArrowRight: 1, ArrowLeft: -1, Home: -barCount, End: barCount }
    const step = steps[event.key]
    if (!step || !barCount) return
    event.preventDefault()
    const next = Math.max(0, Math.min(index + step, barCount - 1))
    setFocusedBucket(next)
    barRefs.current[next]?.focus()
  }

  return (
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
                      {sourceText(series)} {seriesLabel(series)}
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
                    onClick={() => setFocusedBucket(index)}
                    onFocus={() => setFocusedBucket(index)}
                    onKeyDown={(event) => onPlotKeyDown(event, index)}
                  >
                    <span className={styles.stack}>
                      {bar.total === 0 ? (
                        <span className={styles.baseline} />
                      ) : (
                        bar.segments.map((segment) => (
                          <span
                            key={segment.key}
                            className={cx(styles.seg, SEGMENT_CLASS[`${segment.source}-${segment.name.toLowerCase()}`])}
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
                            {sourceText(series)} {seriesLabel(series)}
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
  )
}

function LogList({ items, users }: { items: readonly UnifiedLogItem[]; users: readonly AdminUser[] }) {
  // Lifted out of the rows because the list unmounts rows scrolled out of view.
  const [expanded, { toggle }] = useSet<string>()
  return (
    <VirtualList
      className={styles.list}
      items={items}
      itemKey={(item) => item.key}
      estimateSize={80}
      itemProps={() => ({ className: styles.item })}
      renderItem={(item) =>
        item.source === 'audit' ? (
          <AuditEntry row={item.row} users={users} />
        ) : (
          <ServerEntry record={item.record} open={expanded.has(item.key)} onToggle={() => toggle(item.key)} />
        )
      }
    />
  )
}

function AuditEntry({ row, users }: { row: AuditRow; users: readonly AdminUser[] }) {
  const { t } = useI18n()
  return (
    <div className={styles.row}>
      <span className={cx(styles.level, row.ok ? styles.levelOk : styles.levelFailed)}>
        {row.ok ? t('audit.success') : t('audit.failure')}
      </span>
      <span className={styles.body}>
        <span className={styles.msg}>{row.event}</span>
        <span className={styles.meta}>
          <span className={styles.source}>{t('common.audit_log')}</span>
          {auditMeta(row, users).map((part, index) => (
            <span key={`${row.rowid}-${index}`}>{part}</span>
          ))}
        </span>
      </span>
    </div>
  )
}

function ServerEntry({ record, open, onToggle }: { record: AdminLogRecord; open: boolean; onToggle: () => void }) {
  const { t, tp } = useI18n()
  const attrs = Object.entries(record.attrs)
  const body = (
    <>
      <span className={cx(styles.level, LEVEL_CLASS[record.level.toLowerCase()])}>{levelText(record.level)}</span>
      <span className={styles.body}>
        <span className={styles.msg}>{record.msg}</span>
        <span className={styles.meta}>
          <span className={styles.source}>{t('logs.server_log')}</span>
          <span>{formatDateNs(record.ts_ns)}</span>
          {record.subsystem ? <span>{record.subsystem}</span> : null}
          {record.request_id ? <span>{t('logs.request', { id: record.request_id })}</span> : null}
        </span>
      </span>
    </>
  )
  if (!attrs.length) return <div className={styles.row}>{body}</div>
  return (
    <>
      <button type="button" className={cx(styles.row, styles.rowButton)} aria-expanded={open} onClick={onToggle}>
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
  )
}
