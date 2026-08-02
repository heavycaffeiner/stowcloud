<script lang="ts">
  // Upload/chunk settings — admin-only, same mounting pattern as
  // StorageIndexSection (`/admin` page, is_admin-gated).
  //
  // Two independent knobs, kept visually separate so their scope is never
  // ambiguous:
  //  1. Server-global chunk floor/default (`PATCH /api/admin/upload-settings`,
  // ) — persisted in upload.db, changes what every
  //     account's GET /api/auth/session reports and what a NEW upload session
  //     uses, on the whole server, immediately, without a restart.
  //  2. This browser's own 413 shrink-adaptation seed
  //     (`chunk-planner.ts`'s `CHUNK_SIZE_STORAGE_KEY`,
  //     `localStorage['sc.chunk_size']`) — the same value `worker.ts` already
  //     reads/writes. It only affects the chunk size a *new* upload session
  //     starts at on this one browser and can never go below the server floor.
  import { t } from '../../i18n'
  import { untrack } from 'svelte'
  import { authState, setAuthenticated } from '../../state/auth.svelte'
  import { api, ApiError } from '../../api/client'
  import { BYTES_PER_MB, bytesToMb, formatBytes } from '../../format/bytes'
  import { CHUNK_SIZE_MIN, CHUNK_SIZE_STORAGE_KEY } from '../../upload/chunk-planner'
  import Button from '../Button.svelte'
  import TextField from '../TextField.svelte'

  const serverMin = $derived(authState.session?.limits.chunk_min ?? CHUNK_SIZE_MIN)
  const serverDefault = $derived(authState.session?.limits.chunk_size ?? CHUNK_SIZE_MIN * 2)

  // ── server-global (admin write) ──

  // Seeded once from the current server value, then edited independently —
  // `untrack` marks that as deliberate (not a missed-reactivity bug).
  let minMb = $state(untrack(() => String(bytesToMb(serverMin))))
  let defaultMb = $state(untrack(() => String(bytesToMb(serverDefault))))
  let serverError = $state<string | null>(null)
  let serverSaving = $state(false)
  let serverSaved = $state(false)

  async function saveServerSettings(): Promise<void> {
    serverError = null
    serverSaved = false
    const minVal = Number(minMb)
    const defaultVal = Number(defaultMb)
    if (!Number.isFinite(minVal) || !Number.isFinite(defaultVal) || minVal <= 0 || defaultVal <= 0) {
      serverError = t('upload_settings.enter_valid_number')
      return
    }
    const minBytes = Math.round(minVal * BYTES_PER_MB)
    const defaultBytes = Math.round(defaultVal * BYTES_PER_MB)
    // Same rules the server enforces (`UploadEngine::set_chunk_settings`) —
    // checked here first so the common mistake gets a specific Korean
    // message instead of the server's generic one.
    if (minBytes < CHUNK_SIZE_MIN) {
      serverError = t('upload_settings.minimum_must_at_least', { min: formatBytes(CHUNK_SIZE_MIN) })
      return
    }
    if (defaultBytes < minBytes) {
      serverError = t('upload_settings.default_cannot_smaller_than_minimum')
      return
    }
    serverSaving = true
    try {
      const resp = await api.adminSetUploadSettings({ chunk_min: minBytes, chunk_default: defaultBytes })
      if (authState.session) {
        setAuthenticated({
          ...authState.session,
          limits: { ...authState.session.limits, chunk_min: resp.chunk_min, chunk_size: resp.chunk_default }
        })
      }
      serverSaved = true
    } catch (err) {
      if (err instanceof ApiError && err.code === 'fs.invalid_name') {
        serverError = t('upload_settings.value_entered_outside_server_s')
      } else {
        serverError = t('common.could_not_save')
      }
    } finally {
      serverSaving = false
    }
  }

  // ── this browser's override ──

  function readOverride(): number | null {
    try {
      const v = localStorage.getItem(CHUNK_SIZE_STORAGE_KEY)
      return v ? Number(v) : null
    } catch {
      return null
    }
  }

  const initialOverride = readOverride()
  let overrideBytes = $state<number | null>(initialOverride)
  let inputMb = $state(initialOverride ? String(bytesToMb(initialOverride)) : '')
  let error = $state<string | null>(null)
  let saved = $state(false)

  function save(): void {
    error = null
    saved = false
    const mb = Number(inputMb)
    if (!Number.isFinite(mb) || mb <= 0) {
      error = t('upload_settings.enter_valid_number')
      return
    }
    const bytes = Math.round(mb * BYTES_PER_MB)
    if (bytes < serverMin) {
      error = t('upload_settings.must_at_least_server_minimum', { min: formatBytes(serverMin) })
      return
    }
    try {
      localStorage.setItem(CHUNK_SIZE_STORAGE_KEY, String(bytes))
      overrideBytes = bytes
      saved = true
    } catch {
      error = t('common.could_not_save_settings')
    }
  }

  function reset(): void {
    try {
      localStorage.removeItem(CHUNK_SIZE_STORAGE_KEY)
    } catch {
      /* ignore */
    }
    overrideBytes = null
    inputMb = ''
    error = null
    saved = false
  }
</script>

<section class="sc-admin-section">
  <h3>{t('upload_settings.upload_chunk_size')}</h3>

  <h4 class="sc-admin-section__subhead">{t('upload_settings.server_wide_setting')}</h4>
  <p class="sc-admin-section__hint">
    {t('upload_settings.two_values_below_apply_server')}
  </p>
  <div class="sc-admin-section__upload-form">
    <TextField label={t('upload_settings.minimum_chunk_size_mb')} bind:value={minMb} placeholder={String(bytesToMb(serverMin))} />
    <TextField label={t('upload_settings.default_chunk_size_mb')} bind:value={defaultMb} placeholder={String(bytesToMb(serverDefault))} />
    <Button variant="filled" onclick={saveServerSettings} loading={serverSaving}>{t('common.save')}</Button>
  </div>
  {#if serverError}
    <p class="sc-admin-section__upload-error" role="alert">{serverError}</p>
  {:else if serverSaved}
    <p class="sc-admin-section__upload-saved" role="status">{t('upload_settings.server_wide_setting_saved')}</p>
  {/if}
  <dl class="sc-admin-section__estimate">
    <div><dt>{t('upload_settings.current_server_minimum')}</dt><dd>{formatBytes(serverMin)}</dd></div>
    <div><dt>{t('upload_settings.current_server_default')}</dt><dd>{formatBytes(serverDefault)}</dd></div>
  </dl>

  <h4 class="sc-admin-section__subhead">{t('upload_settings.override_browser_only')}</h4>
  <p class="sc-admin-section__hint">
    {t('upload_settings.unlike_server_wide_setting_above')}
  </p>
  <div class="sc-admin-section__upload-form">
    <TextField label={t('upload_settings.browser_default_chunk_size_mb')} bind:value={inputMb} error={error} placeholder={String(bytesToMb(serverDefault))} />
    <div class="sc-admin-section__upload-actions">
      <Button variant="filled" onclick={save}>{t('common.save')}</Button>
      <Button variant="text" onclick={reset} disabled={overrideBytes === null}>{t('upload_settings.reset_server_default')}</Button>
    </div>
  </div>
  {#if saved}
    <p class="sc-admin-section__upload-saved" role="status">
      {t('upload_settings.saved_uploads_started_from_now', { size: formatBytes(overrideBytes ?? 0) })}
    </p>
  {:else if overrideBytes !== null}
    <p class="sc-admin-section__upload-current">{t('upload_settings.current_override', { size: formatBytes(overrideBytes) })}</p>
  {:else}
    <p class="sc-admin-section__upload-current">{t('upload_settings.currently_using_server_default')}</p>
  {/if}
</section>

<style>
  .sc-admin-section {
    margin-block: 24px;
  }
  .sc-admin-section h3 {
    margin: 0 0 8px;
    @apply --m3-title-medium;
  }
  .sc-admin-section__subhead {
    margin: 16px 0 8px;
    @apply --m3-title-small;
  }
  .sc-admin-section__hint {
    max-width: 640px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    margin-bottom: 16px;
  }
  .sc-admin-section__estimate {
    display: grid;
    grid-template-columns: repeat(2, minmax(160px, 1fr));
    gap: 8px 24px;
    margin: 8px 0 16px;
  }
  .sc-admin-section__estimate div {
    display: flex;
    flex-direction: column;
  }
  .sc-admin-section__estimate dt {
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-admin-section__estimate dd {
    margin: 0;
    @apply --m3-body-large;
  }
  .sc-admin-section__upload-form {
    display: flex;
    align-items: flex-end;
    flex-wrap: wrap;
    gap: 16px;
    max-width: 480px;
  }
  /* The wrapper TextField.svelte renders is `.field`, not `.sc-field` — the
     selector was left behind when that component became an m3-svelte adapter,
     so the fields kept their intrinsic width instead of sharing the row. */
  .sc-admin-section__upload-form :global(.field) {
    flex: 1 1 200px;
  }
  .sc-admin-section__upload-actions {
    display: flex;
    gap: 8px;
  }
  .sc-admin-section__upload-saved {
    margin: 8px 0 0;
    color: var(--m3c-primary);
    @apply --m3-body-medium;
  }
  .sc-admin-section__upload-error {
    margin: 8px 0 0;
    color: var(--m3c-error);
    @apply --m3-body-medium;
  }
  .sc-admin-section__upload-current {
    margin: 8px 0 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-medium;
  }
</style>
