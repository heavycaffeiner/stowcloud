import { useEffect, useState, useSyncExternalStore } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { adminSettingsQuery, adminUploadSettingsMutation } from '../../../lib/query/admin'
import { describeApiError } from '../../../lib/api/error-text'
import { BYTES_PER_MB, bytesToMb, formatBytes } from '../../../lib/format/bytes'
import { CHUNK_SIZE_MIN, DEFAULT_CONCURRENCY, loadStoredChunkSize, loadStoredConcurrency, MAX_CONCURRENCY, MIN_CONCURRENCY, subscribeUploadPreferences, validChunkSizeOverride } from '../../../lib/upload/chunk-planner'
import { setUploadChunkSize, setUploadConcurrency } from '../../../lib/upload/queue'
import { Button } from '../Button'
import { Icon } from '../Icon'
import { Switch } from '../Switch'
import { TextField } from '../TextField'
import './admin-sections.css'

export function UploadSettingsSection() {
  const { t } = useI18n()
  const settings = useQuery(adminSettingsQuery())
  const mutation = useMutation(adminUploadSettingsMutation())
  const field = (key: string) => settings.data?.fields.find((item) => item.key === key)?.value
  const serverMin = Number(field('upload.chunk_min_bytes') ?? CHUNK_SIZE_MIN)
  const serverDefault = Number(field('upload.chunk_default_bytes') ?? CHUNK_SIZE_MIN * 2)
  const [minMb, setMinMb] = useState(String(bytesToMb(CHUNK_SIZE_MIN)))
  const [defaultMb, setDefaultMb] = useState(String(bytesToMb(CHUNK_SIZE_MIN * 2)))
  const [baseline, setBaseline] = useState<string | null>(null)
  const [expected, setExpected] = useState<string | null>(null)
  const [hydrated, setHydrated] = useState(false)
  const [serverValidation, setServerValidation] = useState<string | null>(null)
  const [serverSaved, setServerSaved] = useState(false)
  const [cacheEnabled, setCacheEnabled] = useState<boolean | null>(null)
  const [cacheAvailable, setCacheAvailable] = useState<boolean | null>(null)
  const [cacheTouched, setCacheTouched] = useState(false)
  const override = useSyncExternalStore(subscribeUploadPreferences, () => loadStoredChunkSize(serverMin), () => null)
  const [inputMb, setInputMb] = useState(() => String((override ?? serverDefault) / BYTES_PER_MB))
  const [overrideError, setOverrideError] = useState<string | null>(null)
  const [overrideSaved, setOverrideSaved] = useState(false)
  const activeConcurrency = useSyncExternalStore(subscribeUploadPreferences, loadStoredConcurrency, () => DEFAULT_CONCURRENCY)
  const [concurrencyInput, setConcurrencyInput] = useState(() => String(activeConcurrency))
  const [concurrencyError, setConcurrencyError] = useState<string | null>(null)
  const [concurrencySaved, setConcurrencySaved] = useState(false)
  useEffect(() => { setInputMb(String((override ?? serverDefault) / BYTES_PER_MB)) }, [override, serverDefault])
  useEffect(() => { setConcurrencyInput(String(activeConcurrency)) }, [activeConcurrency])
  useEffect(() => { if (!settings.data) return; const fingerprint = JSON.stringify({ min: serverMin, def: serverDefault }); if (expected !== null && expected !== fingerprint) return; if (expected === fingerprint) setExpected(null); if (hydrated && baseline !== fingerprint) return; setMinMb(String(bytesToMb(serverMin))); setDefaultMb(String(bytesToMb(serverDefault))); setBaseline(fingerprint); setHydrated(true) }, [settings.data, serverMin, serverDefault, expected, hydrated, baseline])
  const serverDirty = hydrated && baseline !== JSON.stringify({ min: Math.round(Number(minMb) * BYTES_PER_MB), def: Math.round(Number(defaultMb) * BYTES_PER_MB) })
  async function saveServer(): Promise<void> { setServerValidation(null); setServerSaved(false); const min = Number(minMb); const def = Number(defaultMb); if (!Number.isFinite(min) || !Number.isFinite(def) || min <= 0 || def <= 0) { setServerValidation(t('upload_settings.enter_valid_number')); return } const minBytes = Math.round(min * BYTES_PER_MB); const defBytes = Math.round(def * BYTES_PER_MB); if (minBytes < CHUNK_SIZE_MIN) { setServerValidation(t('upload_settings.minimum_must_at_least', { min: formatBytes(CHUNK_SIZE_MIN) })); return } if (defBytes < minBytes) { setServerValidation(t('upload_settings.default_cannot_smaller_than_minimum')); return } try { const response = await mutation.mutateAsync({ chunk_min: minBytes, chunk_default: defBytes, ...(cacheTouched && cacheEnabled !== null ? { cache_enabled: cacheEnabled } : {}) }); const fingerprint = JSON.stringify({ min: response.chunk_min, def: response.chunk_default }); setMinMb(String(bytesToMb(response.chunk_min))); setDefaultMb(String(bytesToMb(response.chunk_default))); setBaseline(fingerprint); setExpected(fingerprint); setCacheEnabled(response.cache_enabled); setCacheAvailable(response.cache_available); setCacheTouched(false); setServerSaved(true) } catch { /* rendered below */ } }
  function saveOverride(): void {
    setOverrideError(null)
    setOverrideSaved(false)
    const mb = Number(inputMb)
    const bytes = Math.round(mb * BYTES_PER_MB)
    if (!Number.isFinite(mb) || mb <= 0 || !Number.isSafeInteger(bytes)) {
      setOverrideError(t('upload_settings.enter_valid_number'))
      return
    }
    if (!validChunkSizeOverride(bytes, serverMin)) {
      setOverrideError(t('upload_settings.must_at_least_server_minimum', { min: formatBytes(serverMin) }))
      return
    }
    if (!setUploadChunkSize(bytes, serverMin)) {
      setOverrideError(t('common.could_not_save_settings'))
      return
    }
    setInputMb(String(bytes / BYTES_PER_MB))
    setOverrideSaved(true)
  }
  function resetOverride(): void {
    setOverrideError(null)
    setOverrideSaved(false)
    if (!setUploadChunkSize(null)) {
      setOverrideError(t('common.could_not_save_settings'))
      return
    }
    setInputMb(String(serverDefault / BYTES_PER_MB))
  }
  function saveConcurrency(): void {
    setConcurrencyError(null)
    setConcurrencySaved(false)
    const value = Number(concurrencyInput)
    if (!Number.isInteger(value) || value < MIN_CONCURRENCY || value > MAX_CONCURRENCY) {
      setConcurrencyError(t('upload_settings.concurrency_must_between', { min: MIN_CONCURRENCY, max: MAX_CONCURRENCY }))
      return
    }
    if (!setUploadConcurrency(value)) {
      setConcurrencyError(t('common.could_not_save_settings'))
      return
    }
    setConcurrencyInput(String(value))
    setConcurrencySaved(true)
  }
  function resetConcurrency(): void {
    setConcurrencyError(null)
    setConcurrencySaved(false)
    if (!setUploadConcurrency(DEFAULT_CONCURRENCY)) {
      setConcurrencyError(t('common.could_not_save_settings'))
      return
    }
    setConcurrencyInput(String(DEFAULT_CONCURRENCY))
  }
  return (
    <>
      <article className="sc-admin-card">
        <div className="sc-admin-card-head">
          <div className="sc-admin-card-icon"><Icon name="upload" /></div>
          <div className="sc-admin-card-meta">
            <h3 className="sc-admin-card-title">{t('upload_settings.upload_chunk_size')}</h3>
            <p className="sc-admin-card-subtitle"><strong>{t('upload_settings.server_wide_setting')}</strong>: {t('upload_settings.two_values_below_apply_server')}</p>
          </div>
        </div>

        <div className="sc-upload-form">
          <TextField label={t('upload_settings.minimum_chunk_size_mb')} value={minMb} onValueChange={setMinMb} placeholder={String(bytesToMb(serverMin))} />
          <TextField label={t('upload_settings.default_chunk_size_mb')} value={defaultMb} onValueChange={setDefaultMb} placeholder={String(bytesToMb(serverDefault))} />
          <Button onClick={() => void saveServer()} loading={mutation.isPending}>{t('common.save')}</Button>
        </div>

        {serverValidation || mutation.error ? <p className="sc-admin-error" role="alert">{serverValidation ?? describeApiError(mutation.error, t('common.could_not_save'))}</p> : mutation.isPending ? <p className="sc-admin-note">{t('common.saving')}</p> : serverDirty ? <p className="sc-admin-note">{t('settings.unsaved_changes')}</p> : serverSaved ? <p className="sc-admin-saved" role="status">{t('upload_settings.server_wide_setting_saved')}</p> : null}

        <dl className="sc-upload-estimate">
          <div>
            <dt>{t('upload_settings.current_server_minimum')}</dt>
            <dd>{formatBytes(serverMin)}</dd>
          </div>
          <div>
            <dt>{t('upload_settings.current_server_default')}</dt>
            <dd>{formatBytes(serverDefault)}</dd>
          </div>
        </dl>
      </article>

      <article className="sc-admin-card">
        <div className="sc-admin-card-head">
          <div className="sc-admin-card-icon"><Icon name="history" /></div>
          <div className="sc-admin-card-meta">
            <h3 className="sc-admin-card-title">{t('upload_settings.cache_spool')}</h3>
            <p className="sc-admin-card-subtitle">{t('upload_settings.cache_spool_hint')}</p>
          </div>
        </div>

        {cacheAvailable === false ? (
          <p className="sc-admin-error" role="alert">{t('upload_settings.cache_spool_unavailable')}</p>
        ) : (
          <div className="sc-storage__toggle-row">
            <Switch
              checked={cacheEnabled === true}
              label={t('upload_settings.cache_spool_enable')}
              onChange={(checked) => { setCacheEnabled(checked); setCacheTouched(true) }}
            />
            {cacheEnabled === null ? <p className="sc-admin-note">{t('upload_settings.cache_spool_state_unknown')}</p> : null}
          </div>
        )}
      </article>

      <article className="sc-admin-card">
        <div className="sc-admin-card-head">
          <div className="sc-admin-card-icon"><Icon name="settings" /></div>
          <div className="sc-admin-card-meta">
            <h3 className="sc-admin-card-title">{t('upload_settings.override_browser_only')}</h3>
            <p className="sc-admin-card-subtitle">{t('upload_settings.unlike_server_wide_setting_above')}</p>
          </div>
        </div>

        <div className="sc-upload-form">
          <TextField label={t('upload_settings.browser_default_chunk_size_mb')} value={inputMb} error={overrideError} placeholder={String(bytesToMb(serverDefault))} onValueChange={setInputMb} />
          <div className="sc-upload-actions">
            <Button onClick={saveOverride}>{t('common.save')}</Button>
            <Button variant="text" onClick={resetOverride} disabled={override === null}>{t('upload_settings.reset_server_default')}</Button>
          </div>
        </div>

        {overrideSaved ? <p className="sc-admin-saved" role="status">{t('upload_settings.saved_uploads_started_from_now', { size: formatBytes(override ?? 0) })}</p> : override !== null ? <p className="sc-admin-note">{t('upload_settings.current_override', { size: formatBytes(override) })}</p> : <p className="sc-admin-note">{t('upload_settings.currently_using_server_default')}</p>}
      </article>

      <article className="sc-admin-card">
        <div className="sc-admin-card-head">
          <div className="sc-admin-card-icon"><Icon name="speed" /></div>
          <div className="sc-admin-card-meta">
            <h3 className="sc-admin-card-title">{t('upload_settings.concurrency_limit')}</h3>
            <p className="sc-admin-card-subtitle">{t('upload_settings.concurrency_limit_hint')}</p>
          </div>
        </div>

        <div className="sc-upload-form">
          <TextField label={t('upload_settings.concurrency_limit')} value={concurrencyInput} error={concurrencyError} placeholder={String(DEFAULT_CONCURRENCY)} onValueChange={setConcurrencyInput} />
          <div className="sc-upload-actions">
            <Button onClick={saveConcurrency}>{t('common.save')}</Button>
            <Button variant="text" onClick={resetConcurrency} disabled={activeConcurrency === DEFAULT_CONCURRENCY}>{t('upload_settings.reset_concurrency_default')}</Button>
          </div>
        </div>

        {concurrencySaved ? <p className="sc-admin-saved" role="status">{t('upload_settings.concurrency_saved')}</p> : <p className="sc-admin-note">{t('upload_settings.current_concurrency', { count: activeConcurrency })}</p>}
      </article>
    </>
  )
}
