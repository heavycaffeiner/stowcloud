import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { formatBytes } from '../../../lib/format/bytes'
import { formatDuration, formatNumber } from '../../../lib/i18n'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { describeApiError } from '../../../lib/api/error-text'
import { adminBuildIndexMutation, adminIndexEstimateQuery, adminIndexSettingsMutation, adminIndexStatusQuery, adminSettingsQuery, adminStorageQuery } from '../../../lib/query/admin'
import { jobQuery } from '../../../lib/query/jobs'
import { jobTray } from '../../../lib/store/jobs.store'
import { Button } from '../Button'
import { Icon } from '../Icon'
import { Switch } from '../Switch'
import { ProgressCircular } from '../ProgressCircular'
import './admin-sections.css'

const ACCURACY: Record<string, string> = { measured: 'storage.accuracy_counted_everything', modelled: 'storage.accuracy_counted_a_sample' }
/* i18n */ 'storage.accuracy_counted_everything'
/* i18n */ 'storage.accuracy_counted_a_sample'

export function StorageIndexSection() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const storage = useQuery(adminStorageQuery())
  const status = useQuery(adminIndexStatusQuery())
  const settings = useQuery(adminSettingsQuery())
  const [estimateRequested, setEstimateRequested] = useState(false)
  const estimate = useQuery({ ...adminIndexEstimateQuery(), enabled: estimateRequested })
  const toggle = useMutation(adminIndexSettingsMutation())
  const build = useMutation(adminBuildIndexMutation())
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useQuery({ ...jobQuery(jobId ?? ''), enabled: jobId !== null })
  const nameEnabled = settings.data?.fields.find((field) => field.key === 'search.name_index_enabled')?.value === true
  useEffect(() => { if (job.data && job.data.state !== 'running') void qc.invalidateQueries({ queryKey: ['admin', 'index-status'] }) }, [job.data, qc])
  function estimateCost(): void { if (!estimateRequested) setEstimateRequested(true); else void estimate.refetch() }
  function startBuild(): void { build.mutate(undefined, { onSuccess: (result) => { jobTray.track(result.id); setJobId(result.id) } }) }
  let jobText: string | null = null
  if (job.data?.state === 'running') jobText = t('storage.build_running', { count: formatNumber(job.data.done) })
  else if (job.data?.state === 'done') jobText = t('storage.build_done', { count: formatNumber(job.data.done) })
  else if (job.data?.state === 'cancelled' || job.data?.state === 'interrupted') jobText = t('storage.build_stopped', { count: formatNumber(job.data.done) })
  else if (job.data?.state === 'error') jobText = t('storage.build_failed')
  const statusText = status.data ? !status.data.enabled ? t('storage.index_off') : status.data.entries === 0 ? t('storage.index_empty') : t('storage.index_holding', { count: formatNumber(status.data.entries) }) : null
  return (
    <>
      <article className="sc-admin-card">
        <div className="sc-admin-card-head">
          <div className="sc-admin-card-icon"><Icon name="storage" /></div>
          <div className="sc-admin-card-meta">
            <h3 className="sc-admin-card-title">{t('common.storage')}</h3>
            <p className="sc-admin-card-subtitle">{t('admin.server_storage_paths')}</p>
          </div>
        </div>
        {storage.isPending ? (
          <ProgressCircular />
        ) : storage.error ? (
          <p className="sc-admin-error">{describeApiError(storage.error, t('storage.could_not_load_storage_information'))}</p>
        ) : storage.data ? (
          <ul className="sc-storage-list">
            <li>
              <div className="sc-storage__item-label">
                <Icon name="database" size={18} />
                <span>{t('storage.file_database')}</span>
              </div>
              <strong>{formatBytes(storage.data.db_bytes)}</strong>
            </li>
            {storage.data.shares.map((share) => (
              <li key={share.label}>
                <div className="sc-storage__item-label">
                  <Icon name="folder" size={18} />
                  <span className="sc-filename">{share.label}</span>
                </div>
                <strong>{t('storage.free', { free: formatBytes(share.free_bytes), total: formatBytes(share.total_bytes) })}</strong>
              </li>
            ))}
          </ul>
        ) : null}
      </article>

      <article className="sc-admin-card">
        <div className="sc-admin-card-head">
          <div className="sc-admin-card-icon"><Icon name="search" /></div>
          <div className="sc-admin-card-meta">
            <h3 className="sc-admin-card-title">{t('storage.search_index')}</h3>
            <p className="sc-admin-card-subtitle">{t('storage.what_the_index_is_for')}</p>
          </div>
        </div>

        <div className="sc-storage__status-row">
          <span className="sc-storage__status-label">{t('storage.index_status')}</span>
          <span className={`sc-storage__status-badge ${status.data?.enabled ? 'sc-storage__status-badge--on' : ''}`}>
            {status.isPending ? <ProgressCircular size={16} /> : statusText}
          </span>
        </div>

        {status.error ? <p className="sc-admin-error">{describeApiError(status.error, t('storage.could_not_load_index_status'))}</p> : null}
        {settings.error ? <p className="sc-admin-error">{describeApiError(settings.error, t('storage.could_not_load_index_settings'))}</p> : null}
        {status.data?.incomplete ? <p className="sc-admin-warning" role="alert">{t('storage.index_incomplete')}</p> : null}

        {settings.isPending ? (
          <ProgressCircular />
        ) : (
          <>
            <div className="sc-storage__toggle-row">
              <Switch checked={nameEnabled} label={t('storage.enable_name_index')} onChange={(checked) => toggle.mutate(checked)} />
              {toggle.isPending ? <span className="sc-admin-note">{t('common.saving')}</span> : null}
            </div>
            {toggle.error ? <p className="sc-admin-error">{describeApiError(toggle.error, t('common.could_not_save_settings'))}</p> : null}

            <div className="sc-index-cost">
              {estimate.data ? (
                <>
                  <dl>
                    <div>
                      <dt>{t('storage.files_to_index')}</dt>
                      <dd>{formatNumber(estimate.data.files)}</dd>
                    </div>
                    <div>
                      <dt>{t('storage.disk_space_needed')}</dt>
                      <dd>{formatBytes(estimate.data.index_bytes)}</dd>
                    </div>
                    <div>
                      <dt>{t('storage.time_to_build')}</dt>
                      <dd>{formatDuration(estimate.data.build_secs)}</dd>
                    </div>
                  </dl>
                  <p className="sc-admin-note">{ACCURACY[estimate.data.confidence] ? t(ACCURACY[estimate.data.confidence]) : null} {t('storage.build_only_runs_while_idle')}</p>
                </>
              ) : (
                <p className="sc-admin-note">{t('storage.measure_before_turning_on')}</p>
              )}
              <div className="sc-admin-row">
                <Button variant="outlined" loading={estimate.isFetching} onClick={estimateCost}>
                  {estimate.data ? t('storage.measure_again') : t('storage.measure_the_cost')}
                </Button>
              </div>
              {estimate.error ? <p className="sc-admin-error">{describeApiError(estimate.error, t('storage.could_not_compute_estimate'))}</p> : null}
            </div>

            <div className="sc-admin-row">
              <Button variant="filled" loading={build.isPending} disabled={!nameEnabled || job.data?.state === 'running'} onClick={startBuild}>
                {t('storage.start_index_build')}
              </Button>
            </div>
            {jobText ? <p className="sc-admin-note" aria-live="polite">{jobText}</p> : null}
            <p className="sc-admin-note">{nameEnabled ? t('storage.first_build_is_manual') : t('storage.turn_it_on_before_building')}</p>
            {build.error ? <p className="sc-admin-error">{describeApiError(build.error, t('storage.could_not_start_index_build'))}</p> : null}
            <p className="sc-admin-hint">{t('storage.turning_it_off_keeps_the_existing_index')} {t('storage.to_free_the_space_delete')} <code>.scindex</code> {t('storage.in_each_shared_folder')}</p>
          </>
        )}
      </article>
    </>
  )
}
