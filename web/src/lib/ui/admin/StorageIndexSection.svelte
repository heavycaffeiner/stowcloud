<script lang="ts">
  // Storage usage and the optional file-name search index, admin-only,
  // rendered by `/admin` after it has checked `is_admin`.
  // GET /api/admin/storage, GET/POST /api/admin/index/estimate,
  // GET/PATCH /api/admin/index/settings.
  //
  // The index switch is a persisted runtime override, not a config-file edit:
  // it survives a restart on its own, the same way the upload chunk size does.
  // Its current value comes off the same settings snapshot
  // `ServerSettingsSection` reads (`adminSettingsQuery`), under the key the
  // server stores it by, since there is no endpoint for this one field alone
  // (`api.adminIndexSettings` used to parse the identical response); the two
  // screens share the one cache entry, so a build here or a save there
  // invalidate each other correctly.
  //
  // The cost figures are deliberately three numbers and one sentence. The
  // estimator can also derive them term by term, but that derivation is
  // written to the server log: on screen it read as noise to everyone who
  // had not written the estimator.
  import { createMutation, createQuery } from '@tanstack/svelte-query'
  import { formatDuration, formatNumber, t } from '../../i18n'
  import { describeApiError } from '../../api/error-text'
  import { formatBytes } from '../../format/bytes'
  import { adminBuildIndexMutation, adminIndexEstimateQuery, adminIndexSettingsMutation, adminSettingsQuery, adminStorageQuery } from '../../query/admin'
  import { jobTray } from '../../store/jobs.store'
  import Button from '../Button.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import Switch from '../Switch.svelte'

  /** How much of the folder tree the estimate actually counted, in a sentence.
   *  The server sends a code so that this screen owns the wording; an unknown
   *  code means the server is newer than this build, and saying nothing is
   *  better than inventing a claim about accuracy.
   *
   *  `measured` is the posting term derived from sampled blocks, `modelled`
   *  the analytic fallback. These are the two the server has ever sent; the
   *  high/medium/low this once listed matched nothing, so the sentence never
   *  appeared. */
  const ACCURACY: Record<string, string> = {
    measured: /* i18n */ 'storage.accuracy_counted_everything',
    modelled: /* i18n */ 'storage.accuracy_counted_a_sample'
  }

  const storageQuery = createQuery(() => adminStorageQuery())
  const storage = $derived(storageQuery.data ?? null)
  const storageLoading = $derived(storageQuery.isPending)
  const storageError = $derived(storageQuery.error ? describeApiError(storageQuery.error, t('storage.could_not_load_storage_information')) : null)

  // The estimate is deliberately not fetched until asked: it walks every
  // shared folder, so it stays a button rather than something this screen
  // does on load. `enabled` flips true on the first click; every click after
  // that is a plain refetch of the same query.
  let estimateRequested = $state(false)
  const estimateQuery = createQuery(() => ({ ...adminIndexEstimateQuery(), enabled: estimateRequested }))
  const estimate = $derived(estimateQuery.data ?? null)
  const estimateLoading = $derived(estimateQuery.isFetching)
  const estimateError = $derived(estimateQuery.error ? describeApiError(estimateQuery.error, t('storage.could_not_compute_estimate')) : null)

  function runEstimate(): void {
    if (!estimateRequested) estimateRequested = true
    else void estimateQuery.refetch()
  }

  const settingsQuery = createQuery(() => adminSettingsQuery())
  const nameEnabled = $derived(settingsQuery.data?.fields.find((f) => f.key === 'search.name_index_enabled')?.value === true)
  const settingsLoading = $derived(settingsQuery.isPending)

  const toggleMut = createMutation(() => adminIndexSettingsMutation())
  const settingsSaving = $derived(toggleMut.isPending)
  const settingsError = $derived.by(() => {
    if (toggleMut.error) return describeApiError(toggleMut.error, t('common.could_not_save_settings'))
    if (settingsQuery.error) return describeApiError(settingsQuery.error, t('storage.could_not_load_index_settings'))
    return null
  })

  function onToggleEnabled(checked: boolean): void {
    toggleMut.mutate(checked)
  }

  // Fire-and-forget through the shared job queue (`JobTray`, mounted once at
  // the app layout): same pattern as `downloadAsArchive`'s `jobTray.track`
  // call, not a second progress mechanism just for this button.
  const buildMut = createMutation(() => adminBuildIndexMutation())
  const buildRunning = $derived(buildMut.isPending)
  const buildError = $derived(buildMut.error ? describeApiError(buildMut.error, t('storage.could_not_start_index_build')) : null)

  function startBuild(): void {
    buildMut.mutate(undefined, { onSuccess: (job) => jobTray.track(job.id) })
  }
</script>

<section class="sc-admin-section">
  <h3>{t('common.storage')}</h3>
  {#if storageLoading}
    <ProgressCircular />
  {:else if storageError}
    <p class="sc-admin-section__error">{storageError}</p>
  {:else if storage}
    <ul class="sc-admin-section__shares">
      <li>
        <span class="sc-admin-section__share-label">{t('storage.file_database')}</span>
        <span class="sc-admin-section__share-usage">{formatBytes(storage.db_bytes)}</span>
      </li>
      {#each storage.shares as s (s.label)}
        <li>
          <span class="sc-filename sc-admin-section__share-label">{s.label}</span>
          <span class="sc-admin-section__share-usage">
            {t('storage.free', { free: formatBytes(s.free_bytes), total: formatBytes(s.total_bytes) })}
          </span>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<section class="sc-admin-section">
  <h3>{t('storage.search_index')}</h3>
  <p class="sc-admin-section__hint">{t('storage.what_the_index_is_for')}</p>

  {#if settingsLoading}
    <ProgressCircular />
  {:else}
    <div class="sc-admin-section__row">
      <Switch
        checked={nameEnabled}
        label={t('storage.enable_name_index')}
        onchange={onToggleEnabled}
      />
      {#if settingsSaving}<span class="sc-admin-section__saving">{t('common.saving')}</span>{/if}
    </div>
    {#if settingsError}<p class="sc-admin-section__error">{settingsError}</p>{/if}

    <!-- Cost before commitment: the estimate walks every shared folder, so it
         stays a button rather than something the admin screen does on load. -->
    <div class="sc-admin-section__cost">
      {#if estimate}
        <dl class="sc-admin-section__figures">
          <div>
            <dt>{t('storage.files_to_index')}</dt>
            <dd>{formatNumber(estimate.files)}</dd>
          </div>
          <div>
            <dt>{t('storage.disk_space_needed')}</dt>
            <dd>{formatBytes(estimate.index_bytes)}</dd>
          </div>
          <div>
            <dt>{t('storage.time_to_build')}</dt>
            <dd>{formatDuration(estimate.build_secs)}</dd>
          </div>
        </dl>
        <p class="sc-admin-section__note">
          {#if ACCURACY[estimate.confidence]}{t(ACCURACY[estimate.confidence])}{/if}
          {t('storage.build_only_runs_while_idle')}
        </p>
      {:else}
        <p class="sc-admin-section__note">{t('storage.measure_before_turning_on')}</p>
      {/if}
      <Button variant="outlined" loading={estimateLoading} onclick={runEstimate}>
        {estimate ? t('storage.measure_again') : t('storage.measure_the_cost')}
      </Button>
      {#if estimateError}<p class="sc-admin-section__error">{estimateError}</p>{/if}
    </div>

    <div class="sc-admin-section__row">
      <Button
        variant="outlined"
        loading={buildRunning}
        disabled={!nameEnabled}
        onclick={startBuild}
      >
        {t('storage.start_index_build')}
      </Button>
    </div>
    <p class="sc-admin-section__note">
      {nameEnabled ? t('storage.first_build_is_manual') : t('storage.turn_it_on_before_building')}
    </p>
    {#if buildError}<p class="sc-admin-section__error">{buildError}</p>{/if}

    <p class="sc-admin-section__hint">
      {t('storage.turning_it_off_keeps_the_existing_index')}
      {t('storage.to_free_the_space_delete')} <code>.scindex</code>
      {t('storage.in_each_shared_folder')}
    </p>
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
  .sc-admin-section__error {
    margin: 8px 0 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-admin-section__row {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 12px;
  }
  .sc-admin-section__saving {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-admin-section__hint {
    max-width: 640px;
    margin: 0 0 16px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-admin-section__note {
    max-width: 640px;
    margin: 0 0 12px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-admin-section__shares {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .sc-admin-section__shares li {
    display: flex;
    justify-content: space-between;
    gap: 16px;
    max-width: 480px;
    padding-block: 4px;
  }
  .sc-admin-section__share-label {
    /* Without `min-width: 0` this flex item cannot shrink below its text's
       min-content size, so the fixed-width usage column squeezes it and a long
       label wraps one syllable per line instead of ellipsizing. */
    min-width: 0;
    flex: 1 1 auto;
  }
  .sc-admin-section__share-usage {
    flex-shrink: 0;
    color: var(--m3c-on-surface-variant);
  }
  .sc-admin-section__cost {
    max-width: 640px;
    margin-bottom: 16px;
    padding: 16px;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
  }
  .sc-admin-section__figures {
    display: grid;
    /* Three figures side by side where there is room, stacked where there is
       not: a fixed three-column grid put "about 42 seconds" on two lines on a
       phone. */
    grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
    gap: 16px;
    margin: 0 0 12px;
  }
  .sc-admin-section__figures div {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .sc-admin-section__figures dt {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-admin-section__figures dd {
    margin: 0;
    color: var(--m3c-on-surface);
    @apply --m3-title-medium;
  }
</style>
