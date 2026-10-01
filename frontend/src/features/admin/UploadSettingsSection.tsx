import { useState, useSyncExternalStore } from 'react'
import { useForm } from 'react-hook-form'
import { t } from '../../lib/i18n'
import { useI18n } from '../../hooks/use-i18n'
import { useAdminSettings, useSaveUploadSettings } from './api'
import { describeApiError } from '../../api/error-text'
import { BYTES_PER_MB, bytesToMb, formatBytes } from '../../lib/format/bytes'
import {
  CHUNK_SIZE_MIN,
  DEFAULT_CONCURRENCY,
  MAX_CONCURRENCY,
  MIN_CONCURRENCY,
  validChunkSizeOverride
} from '../../lib/upload/chunk-planner'
import { loadStoredChunkSize, loadStoredConcurrency, subscribeUploadPreferences } from '../../lib/upload/preferences'
import { setUploadChunkSize, setUploadConcurrency } from '../uploads/queue'
import { Button } from '../../ui/Button'
import { FormTextField } from '../../ui/FormTextField'
import { Icon } from '../../ui/Icon'
import { Switch } from '../../ui/Switch'
import { AdminCard } from './AdminCard'
import * as styles from './UploadSettingsSection.css'
import * as adminStyles from './admin.css'

const chunkBytes = (mb: string): number => Math.round(Number(mb) * BYTES_PER_MB)

interface ServerValues {
  minMb: string
  defaultMb: string
}

function serverProblem({ minMb, defaultMb }: ServerValues): string | null {
  const min = Number(minMb)
  const def = Number(defaultMb)
  if (!Number.isFinite(min) || !Number.isFinite(def) || min <= 0 || def <= 0)
    return t('upload_settings.enter_valid_number')
  if (chunkBytes(minMb) < CHUNK_SIZE_MIN)
    return t('upload_settings.minimum_must_at_least', { min: formatBytes(CHUNK_SIZE_MIN) })
  if (chunkBytes(defaultMb) < chunkBytes(minMb)) return t('upload_settings.default_cannot_smaller_than_minimum')
  return null
}

function overrideProblem(mb: string, serverMin: number): string | true {
  const bytes = chunkBytes(mb)
  if (!(Number(mb) > 0) || !Number.isSafeInteger(bytes)) return t('upload_settings.enter_valid_number')
  if (!validChunkSizeOverride(bytes, serverMin))
    return t('upload_settings.must_at_least_server_minimum', { min: formatBytes(serverMin) })
  return true
}

function concurrencyProblem(input: string): string | true {
  const value = Number(input)
  return (
    (Number.isInteger(value) && value >= MIN_CONCURRENCY && value <= MAX_CONCURRENCY) ||
    t('upload_settings.concurrency_must_between', { min: MIN_CONCURRENCY, max: MAX_CONCURRENCY })
  )
}

export function UploadSettingsSection() {
  const fields = useAdminSettings().data?.fields
  const serverMin = Number(fields?.find((item) => item.key === 'upload.chunk_min_bytes')?.value ?? CHUNK_SIZE_MIN)
  const serverDefault = Number(
    fields?.find((item) => item.key === 'upload.chunk_default_bytes')?.value ?? CHUNK_SIZE_MIN * 2
  )
  return (
    <>
      <ServerUploadCards serverMin={serverMin} serverDefault={serverDefault} />
      <ChunkOverrideCard serverMin={serverMin} serverDefault={serverDefault} />
      <ConcurrencyCard />
    </>
  )
}

interface ServerSizes {
  serverMin: number
  serverDefault: number
}

function ServerUploadCards({ serverMin, serverDefault }: ServerSizes) {
  const { t } = useI18n()
  const save = useSaveUploadSettings()
  // An edit survives a refetch of the server values; untouched fields follow it.
  const { control, handleSubmit, setError, reset, formState } = useForm<ServerValues>({
    values: { minMb: String(bytesToMb(serverMin)), defaultMb: String(bytesToMb(serverDefault)) },
    resetOptions: { keepDirtyValues: true }
  })
  // The server reports the cache state only in a save answer; null until then.
  const [cache, setCache] = useState<{ enabled: boolean; available: boolean } | null>(null)
  const [cacheDraft, setCacheDraft] = useState<boolean | null>(null)
  const cacheOn = cacheDraft ?? cache?.enabled ?? null
  const submit = handleSubmit((values) => {
    if (save.isPending) return
    const problem = serverProblem(values)
    if (problem) {
      setError('root', { message: problem })
      return
    }
    save.mutate(
      {
        chunk_min: chunkBytes(values.minMb),
        chunk_default: chunkBytes(values.defaultMb),
        ...(cacheDraft !== null ? { cache_enabled: cacheDraft } : {})
      },
      {
        onSuccess: (response) => {
          reset({ minMb: String(bytesToMb(response.chunk_min)), defaultMb: String(bytesToMb(response.chunk_default)) })
          setCache({ enabled: response.cache_enabled, available: response.cache_available })
          setCacheDraft(null)
        }
      }
    )
  })
  const error =
    formState.errors.root?.message ?? (save.error ? describeApiError(save.error, t('common.could_not_save')) : null)

  return (
    <>
      <AdminCard
        id="upload-chunk-size"
        title={t('upload_settings.upload_chunk_size')}
        subtitle={
          <>
            <strong>{t('upload_settings.server_wide_setting')}</strong>:{' '}
            {t('upload_settings.two_values_below_apply_server')}
          </>
        }
        icon={<Icon name="upload" />}
      >
        <form className={styles.form} onSubmit={(event) => void submit(event)}>
          <FormTextField
            control={control}
            name="minMb"
            className={styles.field}
            label={t('upload_settings.minimum_chunk_size_mb')}
            placeholder={String(bytesToMb(serverMin))}
          />
          <FormTextField
            control={control}
            name="defaultMb"
            className={styles.field}
            label={t('upload_settings.default_chunk_size_mb')}
            placeholder={String(bytesToMb(serverDefault))}
          />
          <Button type="submit" loading={save.isPending}>
            {t('common.save')}
          </Button>
        </form>
        {error ? (
          <p className={adminStyles.error} role="alert">
            {error}
          </p>
        ) : save.isPending ? (
          <p className={adminStyles.note}>{t('common.saving')}</p>
        ) : formState.isDirty ? (
          <p className={adminStyles.note}>{t('settings.unsaved_changes')}</p>
        ) : save.isSuccess ? (
          <p className={styles.adminSaved} role="status">
            {t('upload_settings.server_wide_setting_saved')}
          </p>
        ) : null}
        <dl className={styles.estimate}>
          <div>
            <dt className={styles.estimateLabel}>{t('upload_settings.current_server_minimum')}</dt>
            <dd className={styles.estimateValue}>{formatBytes(serverMin)}</dd>
          </div>
          <div>
            <dt className={styles.estimateLabel}>{t('upload_settings.current_server_default')}</dt>
            <dd className={styles.estimateValue}>{formatBytes(serverDefault)}</dd>
          </div>
        </dl>
      </AdminCard>

      <AdminCard
        id="upload-cache-spool"
        title={t('upload_settings.cache_spool')}
        subtitle={t('upload_settings.cache_spool_hint')}
        icon={<Icon name="history" />}
      >
        {cache?.available === false ? (
          <p className={adminStyles.error} role="alert">
            {t('upload_settings.cache_spool_unavailable')}
          </p>
        ) : (
          <div className={adminStyles.storageToggleRow}>
            <Switch
              checked={cacheOn === true}
              label={t('upload_settings.cache_spool_enable')}
              onChange={setCacheDraft}
            />
            {cacheOn === null ? (
              <p className={adminStyles.note}>{t('upload_settings.cache_spool_state_unknown')}</p>
            ) : null}
          </div>
        )}
      </AdminCard>
    </>
  )
}

function ChunkOverrideCard({ serverMin, serverDefault }: ServerSizes) {
  const { t } = useI18n()
  const override = useSyncExternalStore(
    subscribeUploadPreferences,
    () => loadStoredChunkSize(serverMin),
    () => null
  )
  const { control, handleSubmit, setError, clearErrors } = useForm({
    values: { mb: String((override ?? serverDefault) / BYTES_PER_MB) }
  })
  const [saved, setSaved] = useState(false)
  const submit = handleSubmit(
    ({ mb }) => {
      const ok = setUploadChunkSize(chunkBytes(mb), serverMin)
      if (!ok) setError('mb', { message: t('common.could_not_save_settings') })
      setSaved(ok)
    },
    () => setSaved(false)
  )
  function resetOverride(): void {
    clearErrors()
    setSaved(false)
    if (!setUploadChunkSize(null)) setError('mb', { message: t('common.could_not_save_settings') })
  }

  return (
    <AdminCard
      id="upload-browser-override"
      title={t('upload_settings.override_browser_only')}
      subtitle={t('upload_settings.unlike_server_wide_setting_above')}
      icon={<Icon name="settings" />}
    >
      <form className={styles.form} onSubmit={(event) => void submit(event)}>
        <FormTextField
          control={control}
          name="mb"
          rules={{ validate: (value) => overrideProblem(value, serverMin) }}
          className={styles.field}
          label={t('upload_settings.browser_default_chunk_size_mb')}
          placeholder={String(bytesToMb(serverDefault))}
        />
        <div className={styles.actions}>
          <Button type="submit">{t('common.save')}</Button>
          <Button variant="text" onClick={resetOverride} disabled={override === null}>
            {t('upload_settings.reset_server_default')}
          </Button>
        </div>
      </form>
      {saved ? (
        <p className={styles.adminSaved} role="status">
          {t('upload_settings.saved_uploads_started_from_now', { size: formatBytes(override ?? 0) })}
        </p>
      ) : override !== null ? (
        <p className={adminStyles.note}>{t('upload_settings.current_override', { size: formatBytes(override) })}</p>
      ) : (
        <p className={adminStyles.note}>{t('upload_settings.currently_using_server_default')}</p>
      )}
    </AdminCard>
  )
}

function ConcurrencyCard() {
  const { t } = useI18n()
  const active = useSyncExternalStore(subscribeUploadPreferences, loadStoredConcurrency, () => DEFAULT_CONCURRENCY)
  const { control, handleSubmit, setError, clearErrors } = useForm({ values: { count: String(active) } })
  const [saved, setSaved] = useState(false)
  const submit = handleSubmit(
    ({ count }) => {
      const ok = setUploadConcurrency(Number(count))
      if (!ok) setError('count', { message: t('common.could_not_save_settings') })
      setSaved(ok)
    },
    () => setSaved(false)
  )
  function resetConcurrency(): void {
    clearErrors()
    setSaved(false)
    if (!setUploadConcurrency(DEFAULT_CONCURRENCY)) setError('count', { message: t('common.could_not_save_settings') })
  }

  return (
    <AdminCard
      id="upload-concurrency"
      title={t('upload_settings.concurrency_limit')}
      subtitle={t('upload_settings.concurrency_limit_hint')}
      icon={<Icon name="speed" />}
    >
      <form className={styles.form} onSubmit={(event) => void submit(event)}>
        <FormTextField
          control={control}
          name="count"
          rules={{ validate: concurrencyProblem }}
          className={styles.field}
          label={t('upload_settings.concurrency_limit')}
          placeholder={String(DEFAULT_CONCURRENCY)}
        />
        <div className={styles.actions}>
          <Button type="submit">{t('common.save')}</Button>
          <Button variant="text" onClick={resetConcurrency} disabled={active === DEFAULT_CONCURRENCY}>
            {t('upload_settings.reset_concurrency_default')}
          </Button>
        </div>
      </form>
      {saved ? (
        <p className={styles.adminSaved} role="status">
          {t('upload_settings.concurrency_saved')}
        </p>
      ) : (
        <p className={adminStyles.note}>{t('upload_settings.current_concurrency', { count: active })}</p>
      )}
    </AdminCard>
  )
}
