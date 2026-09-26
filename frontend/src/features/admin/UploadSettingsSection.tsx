import { useEffect, useSyncExternalStore } from 'react'
import { useComponentState } from '../../hooks/use-component-state'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useI18n } from '../../hooks/use-i18n'
import { adminSettingsQuery, adminUploadSettingsMutation } from '../../lib/query/admin'
import { describeApiError } from '../../lib/api/error-text'
import { BYTES_PER_MB, bytesToMb, formatBytes } from '../../lib/format/bytes'
import { CHUNK_SIZE_MIN, DEFAULT_CONCURRENCY, loadStoredChunkSize, loadStoredConcurrency, MAX_CONCURRENCY, MIN_CONCURRENCY, subscribeUploadPreferences, validChunkSizeOverride } from '../../lib/upload/chunk-planner'
import { setUploadChunkSize, setUploadConcurrency } from '../../lib/upload/queue'
import { Button } from '../../lib/ui/Button'
import { Icon } from '../../lib/ui/Icon'
import { Switch } from '../../lib/ui/Switch'
import { TextField } from '../../lib/ui/TextField'
import { AdminCard } from './AdminCard'
import '../../styles/features/admin/admin-sections.css.ts'

export function UploadSettingsSection() {
  const settings = useQuery(adminSettingsQuery())
  const fields = settings.data?.fields
  const serverMin = Number(fields?.find((item) => item.key === 'upload.chunk_min_bytes')?.value ?? CHUNK_SIZE_MIN)
  const serverDefault = Number(fields?.find((item) => item.key === 'upload.chunk_default_bytes')?.value ?? CHUNK_SIZE_MIN * 2)
  const [cacheEnabled, setCacheEnabled] = useComponentState<boolean | null>(null)
  const [cacheAvailable, setCacheAvailable] = useComponentState<boolean | null>(null)
  return <UploadSettingsForm key={`${serverMin}:${serverDefault}`} serverMin={serverMin} serverDefault={serverDefault} cacheEnabled={cacheEnabled} cacheAvailable={cacheAvailable} onCacheResponse={(enabled, available) => { setCacheEnabled(enabled); setCacheAvailable(available) }} />
}

interface UploadSettingsFormProps {
  readonly serverMin: number
  readonly serverDefault: number
  readonly cacheEnabled: boolean | null
  readonly cacheAvailable: boolean | null
  readonly onCacheResponse: (enabled: boolean, available: boolean) => void
}

function UploadSettingsForm({ serverMin, serverDefault, cacheEnabled, cacheAvailable, onCacheResponse }: UploadSettingsFormProps) {
  const { t } = useI18n()
  const mutation = useMutation(adminUploadSettingsMutation())
  const override = useSyncExternalStore(subscribeUploadPreferences, () => loadStoredChunkSize(serverMin), () => null)
  const activeConcurrency = useSyncExternalStore(subscribeUploadPreferences, loadStoredConcurrency, () => DEFAULT_CONCURRENCY)
  type UploadFormState = { minMb: string; defaultMb: string; baseline: string; serverValidation: string | null; serverSaved: boolean; cacheTouched: boolean; cacheDraft: boolean | null; inputMb: string; overrideError: string | null; overrideSaved: boolean; concurrencyInput: string; concurrencyError: string | null; concurrencySaved: boolean }
  const [state, setState] = useComponentState<UploadFormState>({ minMb: String(bytesToMb(serverMin)), defaultMb: String(bytesToMb(serverDefault)), baseline: JSON.stringify({ min: serverMin, def: serverDefault }), serverValidation: null, serverSaved: false, cacheTouched: false, cacheDraft: cacheEnabled, inputMb: String((override ?? serverDefault) / BYTES_PER_MB), overrideError: null, overrideSaved: false, concurrencyInput: String(activeConcurrency), concurrencyError: null, concurrencySaved: false })
  const { minMb, defaultMb, baseline, serverValidation, serverSaved, cacheTouched, cacheDraft, inputMb, overrideError, overrideSaved, concurrencyInput, concurrencyError, concurrencySaved } = state
  const patchState = (patch: Partial<UploadFormState>): void => setState((current) => ({ ...current, ...patch }))
  const setMinMb = (value: string): void => patchState({ minMb: value })
  const setDefaultMb = (value: string): void => patchState({ defaultMb: value })
  const setBaseline = (value: string): void => patchState({ baseline: value })
  const setServerValidation = (value: string | null): void => patchState({ serverValidation: value })
  const setServerSaved = (value: boolean): void => patchState({ serverSaved: value })
  const setCacheTouched = (value: boolean): void => patchState({ cacheTouched: value })
  const setCacheDraft = (value: boolean | null): void => patchState({ cacheDraft: value })
  const setInputMb = (value: string): void => patchState({ inputMb: value })
  const setOverrideError = (value: string | null): void => patchState({ overrideError: value })
  const setOverrideSaved = (value: boolean): void => patchState({ overrideSaved: value })
  const setConcurrencyInput = (value: string): void => patchState({ concurrencyInput: value })
  const setConcurrencyError = (value: string | null): void => patchState({ concurrencyError: value })
  const setConcurrencySaved = (value: boolean): void => patchState({ concurrencySaved: value })
  useEffect(() => { setInputMb(String((override ?? serverDefault) / BYTES_PER_MB)) }, [override, serverDefault])
  useEffect(() => { setConcurrencyInput(String(activeConcurrency)) }, [activeConcurrency])
  const serverDirty = baseline !== JSON.stringify({ min: Math.round(Number(minMb) * BYTES_PER_MB), def: Math.round(Number(defaultMb) * BYTES_PER_MB) })
  async function saveServer(): Promise<void> { setServerValidation(null); setServerSaved(false); const min = Number(minMb); const def = Number(defaultMb); if (!Number.isFinite(min) || !Number.isFinite(def) || min <= 0 || def <= 0) { setServerValidation(t('upload_settings.enter_valid_number')); return } const minBytes = Math.round(min * BYTES_PER_MB); const defBytes = Math.round(def * BYTES_PER_MB); if (minBytes < CHUNK_SIZE_MIN) { setServerValidation(t('upload_settings.minimum_must_at_least', { min: formatBytes(CHUNK_SIZE_MIN) })); return } if (defBytes < minBytes) { setServerValidation(t('upload_settings.default_cannot_smaller_than_minimum')); return } try { const response = await mutation.mutateAsync({ chunk_min: minBytes, chunk_default: defBytes, ...(cacheTouched && cacheDraft !== null ? { cache_enabled: cacheDraft } : {}) }); const fingerprint = JSON.stringify({ min: response.chunk_min, def: response.chunk_default }); setMinMb(String(bytesToMb(response.chunk_min))); setDefaultMb(String(bytesToMb(response.chunk_default))); setBaseline(fingerprint); onCacheResponse(response.cache_enabled, response.cache_available); setCacheDraft(response.cache_enabled); setCacheTouched(false); setServerSaved(true) } catch { /* rendered below */ } }
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
      <AdminCard id="upload-chunk-size" title={t('upload_settings.upload_chunk_size')} subtitle={<><strong>{t('upload_settings.server_wide_setting')}</strong>: {t('upload_settings.two_values_below_apply_server')}</>} icon={<Icon name="upload" />}>
        <div className="sc-upload-form">
          <TextField label={t('upload_settings.minimum_chunk_size_mb')} value={minMb} onValueChange={setMinMb} placeholder={String(bytesToMb(serverMin))} />
          <TextField label={t('upload_settings.default_chunk_size_mb')} value={defaultMb} onValueChange={setDefaultMb} placeholder={String(bytesToMb(serverDefault))} />
          <Button onClick={() => void saveServer()} loading={mutation.isPending}>{t('common.save')}</Button>
        </div>
        {serverValidation || mutation.error ? <p className="sc-admin-error" role="alert">{serverValidation ?? describeApiError(mutation.error, t('common.could_not_save'))}</p> : mutation.isPending ? <p className="sc-admin-note">{t('common.saving')}</p> : serverDirty ? <p className="sc-admin-note">{t('settings.unsaved_changes')}</p> : serverSaved ? <p className="sc-admin-saved" role="status">{t('upload_settings.server_wide_setting_saved')}</p> : null}
        <dl className="sc-upload-estimate"><div><dt>{t('upload_settings.current_server_minimum')}</dt><dd>{formatBytes(serverMin)}</dd></div><div><dt>{t('upload_settings.current_server_default')}</dt><dd>{formatBytes(serverDefault)}</dd></div></dl>
      </AdminCard>

      <AdminCard id="upload-cache-spool" title={t('upload_settings.cache_spool')} subtitle={t('upload_settings.cache_spool_hint')} icon={<Icon name="history" />}>
        {cacheAvailable === false ? <p className="sc-admin-error" role="alert">{t('upload_settings.cache_spool_unavailable')}</p> : <div className="sc-storage-toggle-row"><Switch checked={cacheDraft === true} label={t('upload_settings.cache_spool_enable')} onChange={(checked) => { setCacheDraft(checked); setCacheTouched(true) }} />{cacheDraft === null ? <p className="sc-admin-note">{t('upload_settings.cache_spool_state_unknown')}</p> : null}</div>}
      </AdminCard>

      <AdminCard id="upload-browser-override" title={t('upload_settings.override_browser_only')} subtitle={t('upload_settings.unlike_server_wide_setting_above')} icon={<Icon name="settings" />}>
        <div className="sc-upload-form"><TextField label={t('upload_settings.browser_default_chunk_size_mb')} value={inputMb} error={overrideError} placeholder={String(bytesToMb(serverDefault))} onValueChange={setInputMb} /><div className="sc-upload-actions"><Button onClick={saveOverride}>{t('common.save')}</Button><Button variant="text" onClick={resetOverride} disabled={override === null}>{t('upload_settings.reset_server_default')}</Button></div></div>
        {overrideSaved ? <p className="sc-admin-saved" role="status">{t('upload_settings.saved_uploads_started_from_now', { size: formatBytes(override ?? 0) })}</p> : override !== null ? <p className="sc-admin-note">{t('upload_settings.current_override', { size: formatBytes(override) })}</p> : <p className="sc-admin-note">{t('upload_settings.currently_using_server_default')}</p>}
      </AdminCard>

      <AdminCard id="upload-concurrency" title={t('upload_settings.concurrency_limit')} subtitle={t('upload_settings.concurrency_limit_hint')} icon={<Icon name="speed" />}>
        <div className="sc-upload-form"><TextField label={t('upload_settings.concurrency_limit')} value={concurrencyInput} error={concurrencyError} placeholder={String(DEFAULT_CONCURRENCY)} onValueChange={setConcurrencyInput} /><div className="sc-upload-actions"><Button onClick={saveConcurrency}>{t('common.save')}</Button><Button variant="text" onClick={resetConcurrency} disabled={activeConcurrency === DEFAULT_CONCURRENCY}>{t('upload_settings.reset_concurrency_default')}</Button></div></div>
        {concurrencySaved ? <p className="sc-admin-saved" role="status">{t('upload_settings.concurrency_saved')}</p> : <p className="sc-admin-note">{t('upload_settings.current_concurrency', { count: activeConcurrency })}</p>}
      </AdminCard>
    </>
  )
}
