import { useEffect, useMemo, useState } from 'react'
import { formatBytes } from '../../../lib/format/bytes'
import { formatDuration, formatNumber } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { describeApiError } from '../../../api/error-text'
import {
  useAdminSettings,
  useAdminStorage,
  useBuildIndex,
  useIndexEstimate,
  useIndexStatus,
  useRefreshIndexStatus,
  useSetNameIndex
} from '../api'
import { useJobStatus } from '../../jobs/api'
import { jobTray } from '../../jobs/tray-store'
import { Icon, StowBadge, StowButton, StowProgressCircular, StowSwitch, VirtualList } from '@/shared/ui'
import { AdminCard } from './AdminCard'
import * as styles from './StorageIndexSection.css'
import * as adminStyles from './admin.css'

const ACCURACY: Record<string, string> = {
  measured: 'storage.accuracy_counted_everything',
  modelled: 'storage.accuracy_counted_a_sample'
}
/* i18n */ ;('storage.accuracy_counted_everything')
/* i18n */ ;('storage.accuracy_counted_a_sample')

export function StorageIndexSection() {
  const { t } = useI18n()
  const storage = useAdminStorage()
  const storageItems = useMemo(() => (storage.data ? [storage.data, ...storage.data.shares] : []), [storage.data])
  const status = useIndexStatus()
  const settings = useAdminSettings()
  const [estimateRequested, setEstimateRequested] = useState(false)
  const estimate = useIndexEstimate(estimateRequested)
  const toggle = useSetNameIndex()
  const build = useBuildIndex()
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJobStatus(jobId)
  const refreshStatus = useRefreshIndexStatus()
  const nameEnabled = settings.data?.fields.find((field) => field.key === 'search.name_index_enabled')?.value === true
  useEffect(() => {
    if (job.data && job.data.state !== 'running') refreshStatus()
  }, [job.data, refreshStatus])
  function estimateCost(): void {
    if (!estimateRequested) setEstimateRequested(true)
    else void estimate.refetch()
  }
  function startBuild(): void {
    build.mutate(undefined, {
      onSuccess: (result) => {
        jobTray.track(result.id)
        setJobId(result.id)
      }
    })
  }
  let jobText: string | null = null
  if (job.data?.state === 'running') jobText = t('storage.build_running', { count: formatNumber(job.data.done) })
  else if (job.data?.state === 'done') jobText = t('storage.build_done', { count: formatNumber(job.data.done) })
  else if (job.data?.state === 'cancelled' || job.data?.state === 'interrupted')
    jobText = t('storage.build_stopped', { count: formatNumber(job.data.done) })
  else if (job.data?.state === 'error') jobText = t('storage.build_failed')
  const statusText = status.data
    ? !status.data.enabled
      ? t('storage.index_off')
      : status.data.entries === 0
        ? t('storage.index_empty')
        : t('storage.index_holding', { count: formatNumber(status.data.entries) })
    : null
  return (
    <>
      <AdminCard
        id="storage"
        title={t('common.storage')}
        subtitle={t('admin.server_storage_paths')}
        icon={<Icon name="storage" />}
      >
        {storage.isPending ? (
          <StowProgressCircular />
        ) : storage.error ? (
          <p className={adminStyles.error}>
            {describeApiError(storage.error, t('storage.could_not_load_storage_information'))}
          </p>
        ) : storage.data ? (
          <VirtualList
            className={styles.list}
            items={storageItems}
            itemKey={(item) => ('db_bytes' in item ? 'database' : `share:${item.label}`)}
            estimateSize={48}
            itemProps={() => ({ className: styles.item })}
            renderItem={(item) =>
              'db_bytes' in item ? (
                <>
                  <div className={styles.itemLabel}>
                    <Icon name="database" size={18} />
                    <span>{t('storage.file_database')}</span>
                  </div>
                  <strong className={styles.value}>{formatBytes(item.db_bytes)}</strong>
                </>
              ) : (
                <>
                  <div className={styles.itemLabel}>
                    <Icon name="folder" size={18} />
                    <span className={styles.itemName}>{item.label}</span>
                  </div>
                  <strong className={styles.value}>
                    {t('storage.free', { free: formatBytes(item.free_bytes), total: formatBytes(item.total_bytes) })}
                  </strong>
                </>
              )
            }
          />
        ) : null}
      </AdminCard>

      <AdminCard
        id="storage-index"
        title={t('storage.search_index')}
        subtitle={t('storage.what_the_index_is_for')}
        icon={<Icon name="search" />}
      >
        <div className={styles.statusRow}>
          <span>{t('storage.index_status')}</span>
          <StowBadge tone={status.data?.enabled ? 'accent' : 'neutral'}>
            {status.isPending ? <StowProgressCircular size={16} /> : statusText}
          </StowBadge>
        </div>

        {status.error ? (
          <p className={adminStyles.error}>
            {describeApiError(status.error, t('storage.could_not_load_index_status'))}
          </p>
        ) : null}
        {settings.error ? (
          <p className={adminStyles.error}>
            {describeApiError(settings.error, t('storage.could_not_load_index_settings'))}
          </p>
        ) : null}
        {status.data?.incomplete ? (
          <p className={adminStyles.warning} role="alert">
            {t('storage.index_incomplete')}
          </p>
        ) : null}

        {settings.isPending ? (
          <StowProgressCircular />
        ) : (
          <>
            <div className={adminStyles.storageToggleRow}>
              <StowSwitch
                checked={nameEnabled}
                label={t('storage.enable_name_index')}
                onChange={(checked) => toggle.mutate(checked)}
              />
              {toggle.isPending ? <span className={adminStyles.hint}>{t('common.saving')}</span> : null}
            </div>
            {toggle.error ? (
              <p className={adminStyles.error}>{describeApiError(toggle.error, t('common.could_not_save_settings'))}</p>
            ) : null}

            <div className={styles.indexCost}>
              {estimate.data ? (
                <>
                  <dl className={adminStyles.figures}>
                    <div>
                      <dt className={adminStyles.figureLabel}>{t('storage.files_to_index')}</dt>
                      <dd className={adminStyles.figureValue}>{formatNumber(estimate.data.files)}</dd>
                    </div>
                    <div>
                      <dt className={adminStyles.figureLabel}>{t('storage.disk_space_needed')}</dt>
                      <dd className={adminStyles.figureValue}>{formatBytes(estimate.data.index_bytes)}</dd>
                    </div>
                    <div>
                      <dt className={adminStyles.figureLabel}>{t('storage.time_to_build')}</dt>
                      <dd className={adminStyles.figureValue}>{formatDuration(estimate.data.build_secs)}</dd>
                    </div>
                  </dl>
                  <p className={adminStyles.hint}>
                    {ACCURACY[estimate.data.confidence] ? t(ACCURACY[estimate.data.confidence]) : null}{' '}
                    {t('storage.build_only_runs_while_idle')}
                  </p>
                </>
              ) : (
                <p className={adminStyles.hint}>{t('storage.measure_before_turning_on')}</p>
              )}
              <div className={adminStyles.row}>
                <StowButton variant="outlined" loading={estimate.isFetching} onClick={estimateCost}>
                  {estimate.data ? t('storage.measure_again') : t('storage.measure_the_cost')}
                </StowButton>
              </div>
              {estimate.error ? (
                <p className={adminStyles.error}>
                  {describeApiError(estimate.error, t('storage.could_not_compute_estimate'))}
                </p>
              ) : null}
            </div>

            <div className={adminStyles.row}>
              <StowButton
                variant="filled"
                loading={build.isPending}
                disabled={!nameEnabled || job.data?.state === 'running'}
                onClick={startBuild}
              >
                {t('storage.start_index_build')}
              </StowButton>
            </div>
            {jobText ? (
              <p className={adminStyles.hint} aria-live="polite">
                {jobText}
              </p>
            ) : null}
            <p className={adminStyles.hint}>
              {nameEnabled ? t('storage.first_build_is_manual') : t('storage.turn_it_on_before_building')}
            </p>
            {build.error ? (
              <p className={adminStyles.error}>
                {describeApiError(build.error, t('storage.could_not_start_index_build'))}
              </p>
            ) : null}
            <p className={adminStyles.hint}>
              {t('storage.turning_it_off_keeps_the_existing_index')} {t('storage.to_free_the_space_delete')}{' '}
              <code>.scindex</code> {t('storage.in_each_shared_folder')}
            </p>
          </>
        )}
      </AdminCard>
    </>
  )
}
