<script lang="ts">
  // Server settings: every operator-settable field this deployment has,
  // reachable from this one screen
  // (`go/internal/httpapi/handler/settings.go`). There is no config file, so
  // this is the only place a deployment is configured. Most groups apply live;
  // the bind address, db/symlink-policy/homes, single sign-on, the sandbox
  // policy and SMB's own on/off switch need a restart, handled by the restart
  // section at the bottom.
  //
  // `snapshot.fields` is one flat, dotted-key list keyed by section and
  // field — this component groups by an explicit key
  // set per form below, and anything left over (a field this screen doesn't
  // have a dedicated control for, always because it's `readonly_reason_key`'d —
  // the server never emits an editable field outside these groups)
  // renders generically at the bottom instead of being silently dropped.
  import { t } from '../../i18n'
  import { api, ApiError } from '../../api/client'
  import { describeApiError, serverKeyText } from '../../api/error-text'
  import { BYTES_PER_MB, bytesToMb, formatBytes } from '../../format/bytes'
  import type {
    SettingsSnapshot,
    SettingsField,
    SettingsSectionId,
    ApplyOutcome,
    SmbSettingsReq,
    SearchSettingsReq,
    ArchiveSettingsReq,
    NetworkSettingsReq,
    OidcSettingsReq,
    DbSettingsReq,
    SymlinkPolicyReq,
    HomesSettingsReq,
    WatchSettingsReq,
    PathsSettingsReq
  } from '../../api/types'
  import { SelectOutlined } from 'm3-svelte'
  import Button from '../Button.svelte'
  import TextField from '../TextField.svelte'
  import Switch from '../Switch.svelte'
  import ConfirmDialog from '../ConfirmDialog.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'

  const SMB_TOTP_OPTIONS = [
    { value: 'require_separate', text: t('server.require_separate_smb_password_default') },
    { value: 'block', text: t('server.smb_not_allowed') }
  ]
  const SYMLINK_OPTIONS = [
    { value: 'deny', text: t('server.deny_default') },
    { value: 'within_share', text: t('server.allow_only_targets_inside_share') },
    { value: 'follow', text: t('server.allow_all') }
  ]
  const WATCH_BACKEND_OPTIONS = [
    { value: 'auto', text: t('server.automatic_default') },
    { value: 'hotset', text: t('server.watch_recently_opened_folders') },
    { value: 'inotify_full', text: t('server.watch_every_folder') },
    { value: 'fanotify', text: t('server.watch_whole_filesystem') }
  ]

  let snapshot = $state<SettingsSnapshot | null>(null)
  let loading = $state(true)
  let loadError = $state<string | null>(null)

  function field(key: string): SettingsField | undefined {
    return snapshot?.fields.find((f) => f.key === key)
  }

  // The paths section: what the process opened before anything could be
  // configured, reported rather than offered. The bind address is not one of
  // them any more; it is a setting like the rest and lives with the network
  // form, which is the group it decides.
  const PATH_KEYS = ['data_dir', 'smb.config_dir']

  /**
   * The human name for a reported field.
   *
   * These two lists are read-only: they say what the server is running on
   * rather than offering a control, so they have no form label of their own.
   * Showing the settings key instead put `oidc.allow_private_endpoints` on
   * screen, which is the name the two programs use for it and not one anybody
   * reads. A key with no catalogue entry falls back to itself, which is worse
   * than a translation and better than a blank row.
   */
  //   /* i18n */ 'field.oidc_enabled'
  //   /* i18n */ 'field.oidc_issuer'
  //   /* i18n */ 'field.oidc_client_id'
  //   /* i18n */ 'field.oidc_display_name'
  //   /* i18n */ 'field.oidc_ca_cert_file'
  //   /* i18n */ 'field.oidc_allow_private_endpoints'
  //   /* i18n */ 'field.smb_config_dir'
  //   /* i18n */ 'field.data_dir'
  function fieldLabel(key: string): string {
    // Catalogue keys carry one dot, so a settings key's own dots become
    // underscores: `smb.config_dir` is looked up as `field.smb_config_dir`.
    const name = `field.${key.replaceAll('.', '_')}`
    const label = t(name)
    return label === name ? key : label
  }

  // What the server says an empty value means, for the two fields where empty
  // is a setting rather than a gap. Nothing renders when it sent no key.
  // The keys the server can send that no call site here shows literally:
  //   /* i18n */ 'settings.empty_trusts_no_proxy'
  //   /* i18n */ 'settings.empty_disables_netbios_name'
  //   /* i18n */ 'settings.readonly_data_dir'
  function emptyNote(key: string): string | null {
    const f = field(key)
    return f?.empty_means_key ? t(f.empty_means_key) : null
  }

  function sourceLabel(src: SettingsField['source']): string {
    if (src === 'admin_override') return t('server.admin_override')
    return t('server.default')
  }

  function arrToStr(v: unknown): string {
    return Array.isArray(v) ? v.join(', ') : ''
  }
  function strToArr(s: string): string[] {
    return s
      .split(',')
      .map((x) => x.trim())
      .filter(Boolean)
  }

  function outcomeText(o: ApplyOutcome): string {
    if (o.applied === 'serve_restarted') return t('server.listener_moved_to_the_new_address')
    if (o.applied === 'engine_restart') return t('server.server_is_restarting_to_apply')
    if (o.applied === 'reserved') return t('server.change_takes_full_effect_only')
    return t('server.change_took_effect_immediately')
  }

  function formatValue(v: unknown): string {
    if (v === null || v === undefined) return t('server.none')
    if (Array.isArray(v)) return v.length ? v.join(', ') : t('server.empty')
    if (typeof v === 'boolean') return v ? t('server.on') : t('server.off')
    // An unset string rendered as itself is a row with a label and nothing
    // beside it, which reads as a value that failed to load rather than one
    // nobody has set.
    if (v === '') return t('server.empty')
    return String(v)
  }

  // ── sections ──
  //
  // Saving is what tries the change. The server renders the SMB
  // configuration, writes a file into the homes root and checks the host list
  // still contains the host this browser is talking to, and refuses the write
  // when one of those fails, naming the field. There is no preview button
  // beside the save: it asked a question the next click answered.

  type Section = { id: SettingsSectionId; name: string }

  const SECTIONS: Section[] = [
    { id: 'smb', name: 'SMB' },
    { id: 'search', name: t('common.search') },
    { id: 'archive', name: t('server.zip_download') },
    { id: 'network', name: t('server.network') },
    { id: 'homes', name: t('server.home_folders') },
    { id: 'watch', name: t('server.file_watching') },
    { id: 'rate', name: t('server.request_rate') }
  ]

  function sectionName(id: SettingsSectionId): string {
    return SECTIONS.find((s) => s.id === id)?.name ?? id
  }

  // The catalogue renders the sentence; a refused save carries only the key
  // and its placeholders. The keys cannot be seen at the call site, so they
  // are named here for the extractor.
  /* i18n */ 'settings.out_of_range'
  /* i18n */ 'settings.host_list_empty'
  /* i18n */ 'settings.would_lock_you_out'
  /* i18n */ 'settings.proxy_range_is_everything'
  /* i18n */ 'settings.gid_zero_is_root'
  /* i18n */ 'settings.smb_render_failed'
  /* i18n */ 'settings.smb_config_dir_unavailable'
  /* i18n */ 'settings.dir_will_be_created'
  /* i18n */ 'settings.dir_is_writable'
  /* i18n */ 'settings.path_is_not_a_directory'
  /* i18n */ 'settings.above_kernel_watch_limit'
  /* i18n */ 'settings.within_kernel_watch_limit'
  /* i18n */ 'settings.invalid_host'
  /* i18n */ 'settings.invalid_cidr'
  /* i18n */ 'settings.path_must_be_absolute'
  /* i18n */ 'settings.unknown_totp_policy'
  /* i18n */ 'settings.must_be_at_least_one'
  /* i18n */ 'settings.invalid_bind_address'
  /* i18n */ 'settings.unknown_hardening_policy'
  /* i18n */ 'settings.guard_has_no_bound'
  /* i18n */ 'settings.required_when_enabled'
  /* i18n */ 'settings.issuer_must_be_https'
  /** The bodies each save sends. Numbers are sent as numbers: the server
   *  reads them as such, and a numeric string would fail its bound check for
   *  the wrong reason. */
  function smbBody(): Record<string, unknown> {
    return {
      enabled: smbEnabled,
      workgroup: smbWorkgroup,
      server_name: smbServerName,
      service_user: smbServiceUser,
      allow_public_bind: smbAllowPublicBind,
      totp_policy: smbTotpPolicy,
      service_gid: Number(smbServiceGid)
    }
  }

  function searchBody(): Record<string, unknown> {
    return {
      max_concurrent_fast: Number(searchMaxFast),
      max_concurrent_slow: Number(searchMaxSlow),
      walk_deadline_fast_ms: Number(searchDeadlineFast),
      walk_deadline_slow_ms: Number(searchDeadlineSlow)
    }
  }

  function archiveBody(): Record<string, unknown> {
    return { max_concurrent: Number(archiveMax) }
  }

  function rateBody(): Record<string, unknown> {
    return { per_sec: Number(ratePerSec), burst: Number(rateBurst) }
  }

  function networkBody(): Record<string, unknown> {
    return {
      app_hosts: strToArr(netAppHosts),
      trusted_proxies: strToArr(netTrustedProxies),
      bind: netBind.trim()
    }
  }

  function homesBody(): Record<string, unknown> {
    return { enabled: homesEnabled, root: homesRoot.trim() || null }
  }

  function watchBody(): Record<string, unknown> {
    return {
      backend: watchBackend,
      hot_set_max: Number(watchHotSetMax),
      full_threshold: Number(watchFullThreshold)
    }
  }



  // ── the restart a save takes ──
  //
  // Three sections are decided when the process builds what is under the
  // sandbox, so saving one restarts it. The restart is not a button any more:
  // the save takes it. What is left is the one question the server cannot
  // answer for itself, which is whether interrupting the work in flight is
  // acceptable. The counts come from the refusal itself rather than from a
  // separately polled snapshot, which could be stale by the time somebody
  // reacts to it.

  let busyOpen = $state(false)
  let busyCounts = $state<{ uploads: number; jobs: number } | null>(null)
  let busyRetry = $state<(() => Promise<void>) | null>(null)

  /** Runs a save, and on a busy refusal offers to take the restart anyway.
   *  retry is the same save with force set. */
  async function saving(
    run: () => Promise<ApplyOutcome>,
    retry: () => Promise<void>,
    fallback: string,
    set: (o: ApplyOutcome | null, err: string | null) => void
  ): Promise<void> {
    try {
      set(await run(), null)
      await load()
    } catch (err) {
      if (err instanceof ApiError && err.code === 'restart.busy') {
        busyCounts = {
          uploads: err.reasonNumber('active_uploads') ?? 0,
          jobs: err.reasonNumber('running_jobs') ?? 0
        }
        busyRetry = retry
        busyOpen = true
        return
      }
      set(null, describeApiError(err, fallback))
    }
  }

  async function confirmBusy(): Promise<void> {
    busyOpen = false
    const retry = busyRetry
    busyRetry = null
    if (retry) await retry()
  }

  // ── SMB ──

  let smbEnabled = $state(false)
  let smbWorkgroup = $state('')
  let smbServerName = $state('')
  let smbServiceUser = $state('')
  let smbAllowPublicBind = $state(false)
  let smbTotpPolicy = $state<'require_separate' | 'block'>('require_separate')
  let smbServiceGid = $state('')
  let smbSaving = $state(false)
  let smbError = $state<string | null>(null)
  let smbOutcome = $state<ApplyOutcome | null>(null)

  async function saveSmb(): Promise<void> {
    smbError = null
    smbOutcome = null
    const gid = Number(smbServiceGid)
    if (!Number.isInteger(gid) || gid < 0) {
      smbError = t('server.gid_must_integer_0')
      return
    }
    if (!smbWorkgroup.trim() || !smbServiceUser.trim() || !smbServerName.trim()) {
      smbError = t('server.enter_workgroup_server_name_service_account')
      return
    }
    const req: SmbSettingsReq = {
      enabled: smbEnabled,
      workgroup: smbWorkgroup,
      server_name: smbServerName,
      service_user: smbServiceUser,
      allow_public_bind: smbAllowPublicBind,
      totp_policy: smbTotpPolicy,
      service_gid: gid
    }
    smbSaving = true
    await saving(
      () => api.adminSetSmbSettings(req),
      () => saveSmbForced(req),
      t('server.could_not_save_smb_settings'),
      (o, e) => ((smbOutcome = o), (smbError = e))
    )
    smbSaving = false
  }

  async function saveSmbForced(req: SmbSettingsReq): Promise<void> {
    smbSaving = true
    await saving(
      () => api.adminSetSmbSettings({ ...req, force: true }),
      async () => {},
      t('server.could_not_save_smb_settings'),
      (o, e) => ((smbOutcome = o), (smbError = e))
    )
    smbSaving = false
  }

  // ── search ──

  let searchMaxFast = $state('')
  let searchMaxSlow = $state('')
  let searchDeadlineFast = $state('')
  let searchDeadlineSlow = $state('')
  let searchSaving = $state(false)
  let searchError = $state<string | null>(null)
  let searchOutcome = $state<ApplyOutcome | null>(null)

  async function saveSearch(): Promise<void> {
    searchError = null
    searchOutcome = null
    const nums = [searchMaxFast, searchMaxSlow, searchDeadlineFast, searchDeadlineSlow].map(Number)
    if (nums.some((n) => !Number.isInteger(n) || n < 0)) {
      searchError = t('server.every_value_must_integer_0')
      return
    }
    const req: SearchSettingsReq = {
      max_concurrent_fast: nums[0],
      max_concurrent_slow: nums[1],
      walk_deadline_fast_ms: nums[2],
      walk_deadline_slow_ms: nums[3]
    }
    searchSaving = true
    try {
      searchOutcome = await api.adminSetSearchSettings(req)
      await load()
    } catch (err) {
      searchError = describeApiError(err, t('server.could_not_save_search_settings'))
    } finally {
      searchSaving = false
    }
  }

  // ── archive ──

  let archiveMax = $state('')
  let archiveSaving = $state(false)
  let archiveError = $state<string | null>(null)
  let archiveOutcome = $state<ApplyOutcome | null>(null)

  async function saveArchive(): Promise<void> {
    archiveError = null
    archiveOutcome = null
    const n = Number(archiveMax)
    if (!Number.isInteger(n) || n < 1) {
      archiveError = t('server.must_integer_1_or_more')
      return
    }
    archiveSaving = true
    try {
      archiveOutcome = await api.adminSetArchiveSettings({ max_concurrent: n })
      await load()
    } catch (err) {
      archiveError = describeApiError(err, t('server.could_not_save_archive_settings'))
    } finally {
      archiveSaving = false
    }
  }

  // ── network ──
  //
  // Two editable fields, which is what the server actually stores and applies.
  // The bind address is in this form because it is in this section, and
  // saving it moves the socket: the server binds the new address before it
  // drops the old one, so a refused bind leaves the deployment reachable and
  // the save says so.

  let netAppHosts = $state('')
  let netTrustedProxies = $state('')
  let netBind = $state('')
  let netSaving = $state(false)
  let netError = $state<string | null>(null)
  let netOutcome = $state<ApplyOutcome | null>(null)

  async function saveNetwork(): Promise<void> {
    netError = null
    netOutcome = null
    const hosts = strToArr(netAppHosts)
    if (hosts.length === 0) {
      // The host list is the origin check. An empty one is a server that
      // admits nothing, and it is refused here rather than saved and
      // discovered on the next request.
      netError = t('server.enter_at_least_one_app_host')
      return
    }
    const req: NetworkSettingsReq = {
      app_hosts: hosts,
      trusted_proxies: strToArr(netTrustedProxies),
      bind: netBind.trim() || undefined
    }
    netSaving = true
    try {
      netOutcome = await api.adminSetNetworkSettings(req)
      await load()
    } catch (err) {
      netError = describeApiError(err, t('server.could_not_save_network_settings'))
    } finally {
      netSaving = false
    }
  }

  // ── homes (restart-required) ──

  let homesEnabled = $state(false)
  let homesRoot = $state('')
  let homesSaving = $state(false)
  let homesError = $state<string | null>(null)
  let homesOutcome = $state<ApplyOutcome | null>(null)

  async function saveHomes(): Promise<void> {
    homesError = null
    homesOutcome = null
    if (homesEnabled && !homesRoot.trim()) {
      homesError = t('server.enter_root_path_enable_home')
      return
    }
    const req: HomesSettingsReq = { enabled: homesEnabled, root: homesRoot.trim() || null }
    homesSaving = true
    await saving(
      () => api.adminSetHomesSettings(req),
      () => saveHomesForced(req),
      t('server.could_not_save_home_folder'),
      (o, e) => ((homesOutcome = o), (homesError = e))
    )
    homesSaving = false
  }

  async function saveHomesForced(req: HomesSettingsReq): Promise<void> {
    homesSaving = true
    await saving(
      () => api.adminSetHomesSettings({ ...req, force: true }),
      async () => {},
      t('server.could_not_save_home_folder'),
      (o, e) => ((homesOutcome = o), (homesError = e))
    )
    homesSaving = false
  }

  // ── file watching (restart-required) ──

  let watchBackend = $state<'auto' | 'hotset' | 'inotify_full' | 'fanotify'>('auto')
  let watchHotSetMax = $state('')
  let watchFullThreshold = $state('')
  let watchSaving = $state(false)
  let watchError = $state<string | null>(null)
  let watchOutcome = $state<ApplyOutcome | null>(null)

  async function saveWatch(): Promise<void> {
    watchError = null
    watchOutcome = null
    const hot = Number(watchHotSetMax)
    const full = Number(watchFullThreshold)
    if (!Number.isInteger(hot) || hot < 1 || !Number.isInteger(full) || full < 1) {
      watchError = t('server.watch_limit_must_integer_1')
      return
    }
    const req: WatchSettingsReq = { backend: watchBackend, hot_set_max: hot, full_threshold: full }
    watchSaving = true
    await saving(
      () => api.adminSetWatchSettings(req),
      () => saveWatchForced(req),
      t('server.could_not_save_file_watch'),
      (o, e) => ((watchOutcome = o), (watchError = e))
    )
    watchSaving = false
  }

  async function saveWatchForced(req: WatchSettingsReq): Promise<void> {
    watchSaving = true
    await saving(
      () => api.adminSetWatchSettings({ ...req, force: true }),
      async () => {},
      t('server.could_not_save_file_watch'),
      (o, e) => ((watchOutcome = o), (watchError = e))
    )
    watchSaving = false
  }

  // ── request rate ──

  let ratePerSec = $state('')
  let rateBurst = $state('')
  let rateSaving = $state(false)
  let rateError = $state<string | null>(null)
  let rateOutcome = $state<ApplyOutcome | null>(null)

  async function saveRate(): Promise<void> {
    rateError = null
    rateOutcome = null
    const perSec = Number(ratePerSec)
    const burst = Number(rateBurst)
    // Zero is not a low limit, it is an off switch: every request from every
    // visitor answers 429 the moment it applies. The server refuses it too.
    if (!Number.isInteger(perSec) || perSec < 1 || !Number.isInteger(burst) || burst < 1) {
      rateError = t('server.must_integer_1_or_more')
      return
    }
    rateSaving = true
    try {
      rateOutcome = await api.adminSetRateSettings({ per_sec: perSec, burst })
      await load()
    } catch (err) {
      rateError = describeApiError(err, t('server.could_not_save_rate_settings'))
    } finally {
      rateSaving = false
    }
  }

  // ── everything else this screen doesn't have a dedicated control for —
  // always read-only (the server never leaves an editable field
  // out of the groups above), shown with its Korean reason rather than
  // hidden. ──

  const EDITABLE_KEYS = new Set([
    'bind',
    'app_hosts',
    'allowed_origins',
    'trusted_proxies',
    'public_origins',
    'db.size_guard',
    'db.max_bytes',
    'db.min_free_bytes',
    'symlink_policy',
    'homes.enabled',
    'homes.root',
    'smb.enabled',
    'smb.workgroup',
    'smb.server_name',
    'smb.service_user',
    'smb.allow_public_bind',
    'smb.totp_policy',
    'smb.service_gid',
    'search.max_concurrent_fast',
    'search.max_concurrent_slow',
    'search.walk_deadline_fast_ms',
    'search.walk_deadline_slow_ms',
    'archive.max_concurrent',
    'rate.per_sec',
    'rate.burst',
    'watch.backend',
    'watch.hot_set_max',
    'watch.full_threshold',
    'oidc.enabled',
    'oidc.issuer',
    'oidc.client_id',
    'oidc.redirect_uris',
    'oidc.scopes',
    'oidc.display_name',
    'oidc.allow_private_endpoints',
    'oidc.smb_policy',
    'data_dir',
    'master_key_file',
    'smb.config_dir'
  ])

  const otherFields = $derived(snapshot?.fields.filter((f) => !EDITABLE_KEYS.has(f.key)) ?? [])

  /** Rows whose saved value is not what the running process is on, because
   *  the restart has not happened yet. */
  const pendingFields = $derived(snapshot?.fields.filter((f) => f.running_value !== undefined) ?? [])

  async function load(): Promise<void> {
    loading = true
    loadError = null
    try {
      snapshot = await api.adminGetServerSettings()
      smbEnabled = Boolean(field('smb.enabled')?.value)
      smbWorkgroup = String(field('smb.workgroup')?.value ?? '')
      smbServerName = String(field('smb.server_name')?.value ?? '')
      smbServiceUser = String(field('smb.service_user')?.value ?? '')
      smbAllowPublicBind = Boolean(field('smb.allow_public_bind')?.value)
      smbTotpPolicy = (field('smb.totp_policy')?.value as 'require_separate' | 'block') ?? 'require_separate'
      smbServiceGid = String(field('smb.service_gid')?.value ?? '1000')

      searchMaxFast = String(field('search.max_concurrent_fast')?.value ?? '')
      searchMaxSlow = String(field('search.max_concurrent_slow')?.value ?? '')
      searchDeadlineFast = String(field('search.walk_deadline_fast_ms')?.value ?? '')
      searchDeadlineSlow = String(field('search.walk_deadline_slow_ms')?.value ?? '')

      archiveMax = String(field('archive.max_concurrent')?.value ?? '')

      ratePerSec = String(field('rate.per_sec')?.value ?? '')
      rateBurst = String(field('rate.burst')?.value ?? '')

      netAppHosts = arrToStr(field('app_hosts')?.value)
      netTrustedProxies = arrToStr(field('trusted_proxies')?.value)
      netBind = String(field('bind')?.value ?? '')


      homesEnabled = Boolean(field('homes.enabled')?.value)
      homesRoot = String(field('homes.root')?.value ?? '')

      watchBackend = (field('watch.backend')?.value as typeof watchBackend) ?? 'auto'
      watchHotSetMax = String(field('watch.hot_set_max')?.value ?? '')
      watchFullThreshold = String(field('watch.full_threshold')?.value ?? '')

    } catch {
      loadError = t('server.could_not_load_server_settings')
    } finally {
      loading = false
    }
  }

  load()
</script>

<section class="sc-admin-section">
  <h3>{t('server.server_settings')}</h3>
  <p class="sc-admin-section__hint">
    {t('server.anything_settable_config_toml_can')}
  </p>

  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-admin-section__error" role="alert">{loadError}</p>
  {:else if snapshot}
    {#if snapshot.smb_public_bind_warning}
      <p class="sc-admin-section__warning" role="alert">
        {t('server.smb_reachable_from_outside_private')}
      </p>
    {/if}

    <!-- Permissions SMB cannot express, so it grants more than this screen
         says. Listed per grant rather than as one sentence: an admin has to
         know which share and which account to go and look at.
         The key comes from the server (`SmbOvergrant::kind_key`), so the
         extractor cannot see it at the call site below — these are the two
         it can send: /* i18n */ 'smb.write_list_grants_more'
                      /* i18n */ 'smb.deny_below_root_ignored' -->
    {#if snapshot.smb_overgrants?.length}
      <div class="sc-admin-section__warning" role="alert">
        <p>{t('server.smb_grants_more_than_configured')}</p>
        <ul>
          {#each snapshot.smb_overgrants as o (o.share + ' ' + o.user + ' ' + o.key)}
            <li>{t(o.key, { share: o.share, user: o.user, detail: o.detail.join(', ') })}</li>
          {/each}
        </ul>
      </div>
    {/if}

    <!-- One badge per row that is waiting, grouped under the form it belongs
         to. A single global "something is pending" bit would not tell an
         administrator *which* change is waiting, which is the only useful
         part. -->
    {#snippet pendingRows(id: SettingsSectionId)}
      {@const rows = pendingFields.filter((f) => f.key.split('.')[0] === id)}
      {#each rows as f (f.key)}
        <p class="sc-server-settings__pending">
          <span class="sc-server-settings__badge">{t('server.pending_restart')}</span>
          {f.key}: {t('server.running_value_is', { value: formatValue(f.running_value) })}
        </p>
      {/each}
    {/snippet}

    {#if pendingFields.length}
      <p class="sc-admin-section__warning" role="status">
        {t('server.saved_changes_awaiting_restart', { count: pendingFields.length })}
      </p>
    {/if}

    <h4 class="sc-admin-section__subhead">SMB</h4>
    <div class="sc-server-settings__form">
      <Switch checked={smbEnabled} onchange={(v) => (smbEnabled = v)} label={t('server.enable_smb')} />
      <TextField label={t('server.workgroup')} bind:value={smbWorkgroup} />
      <TextField label={t('server.smb_server_name')} bind:value={smbServerName} />
      {#if !smbServerName.trim() && emptyNote('smb.server_name')}
        <p class="sc-server-settings__empty-note">{emptyNote('smb.server_name')}</p>
      {/if}
      <TextField label={t('server.service_account_name')} bind:value={smbServiceUser} />
      <Switch checked={smbAllowPublicBind} onchange={(v) => (smbAllowPublicBind = v)} label={t('server.allow_access_from_outside_private')} />
      <SelectOutlined label={t('server.smb_access_2fa_users')} width="100%" options={SMB_TOTP_OPTIONS} bind:value={smbTotpPolicy} />
      <TextField label={t('server.service_account_gid')} bind:value={smbServiceGid} />
      <Button variant="filled" onclick={saveSmb} loading={smbSaving}>{t('common.save')}</Button>
      {@render pendingRows('smb')}
    </div>
    <p class="sc-admin-section__hint">
      {t('server.only_toggling_enable_smb_needs')}
    </p>
    {#if smbError}<p class="sc-admin-section__error" role="alert">{smbError}</p>{/if}
    {#if smbOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(smbOutcome)}</p>{/if}

    <!-- What the agent beside smbd did with the files this server rendered.
         Everything here is true of that side and unknowable from this one:
         which addresses smbd ended up bound to, which share paths do not
         exist over there, which accounts have no passdb entry. Writing the
         files used to be the end of it, and a share that never reached a
         client looked exactly like one that did.
         The key comes from the server, so the extractor cannot see it at the
         call site — these are the three it can send:
                      /* i18n */ 'smb.agent_applied'
                      /* i18n */ 'smb.agent_problem'
                      /* i18n */ 'smb.agent_unreachable' -->
    {#if snapshot.smb_agent}
      {@const agent = snapshot.smb_agent}
      <div
        class={agent.ok ? 'sc-admin-section__hint' : 'sc-admin-section__warning'}
        role={agent.ok ? 'status' : 'alert'}
      >
        <p>
          {t(agent.key, {
            shares: agent.shares.length,
            interfaces: agent.interfaces,
            smbd: agent.smbd
          })}
        </p>
        {#if agent.missing_paths.length}
          <p>{t('smb.agent_missing_paths', { paths: agent.missing_paths.join(', ') })}</p>
        {/if}
        {#if agent.missing_passdb.length}
          <p>{t('smb.agent_missing_passdb', { users: agent.missing_passdb.join(', ') })}</p>
        {/if}
        <!-- Verbatim, not translated: it comes from testparm, pdbedit or the
             agent itself, and rewording a diagnostic is how it stops matching
             what a search finds. -->
        {#if agent.detail && !agent.ok}
          <p class="sc-server-settings__agent-detail">{agent.detail}</p>
        {/if}
      </div>
    {/if}

    <h4 class="sc-admin-section__subhead">{t('common.search')}</h4>
    <div class="sc-server-settings__form">
      <TextField label={t('server.concurrent_fast_searches')} bind:value={searchMaxFast} />
      <TextField label={t('server.concurrent_slow_searches')} bind:value={searchMaxSlow} />
      <TextField label={t('server.fast_search_timeout_ms')} bind:value={searchDeadlineFast} />
      <TextField label={t('server.slow_search_timeout_ms')} bind:value={searchDeadlineSlow} />
      <Button variant="filled" onclick={saveSearch} loading={searchSaving}>{t('common.save')}</Button>
    </div>
    <p class="sc-admin-section__hint">{t('server.all_apply_immediately_no_restart')}</p>
    {#if searchError}<p class="sc-admin-section__error" role="alert">{searchError}</p>{/if}
    {#if searchOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(searchOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.zip_download')}</h4>
    <div class="sc-server-settings__form">
      <TextField label={t('server.concurrent_zip_streams')} bind:value={archiveMax} />
      <Button variant="filled" onclick={saveArchive} loading={archiveSaving}>{t('common.save')}</Button>
    </div>
    <p class="sc-admin-section__hint">{t('server.applies_immediately_no_restart_needed')}</p>
    {#if archiveError}<p class="sc-admin-section__error" role="alert">{archiveError}</p>{/if}
    {#if archiveOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(archiveOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.network')}</h4>
    <div class="sc-server-settings__form">
      <TextField label={t('server.app_hosts_comma_separated')} bind:value={netAppHosts} />
      <p class="sc-admin-section__hint">{t('server.app_hosts_hint')}</p>
      <TextField label={t('server.trusted_proxies_comma_separated')} bind:value={netTrustedProxies} />
      {#if !netTrustedProxies.trim() && emptyNote('trusted_proxies')}
        <p class="sc-server-settings__empty-note">{emptyNote('trusted_proxies')}</p>
      {/if}
      <TextField label={t('server.bind_address')} bind:value={netBind} />
      <p class="sc-admin-section__hint">{t('server.bind_address_hint')}</p>
      <Button variant="filled" onclick={saveNetwork} loading={netSaving}>{t('common.save')}</Button>
    </div>
    <p class="sc-admin-section__hint">{t('server.applies_immediately_no_restart_needed')}</p>
    {#if netError}<p class="sc-admin-section__error" role="alert">{netError}</p>{/if}
    {#if netOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(netOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.home_folders')}</h4>
    <p class="sc-admin-section__hint">{t('server.home_folders_hint')}</p>
    <div class="sc-server-settings__form">
      <Switch checked={homesEnabled} onchange={(v) => (homesEnabled = v)} label={t('server.enable_home_folders')} />
      <TextField label={t('server.homes_root_path')} bind:value={homesRoot} />
      <Button variant="filled" onclick={saveHomes} loading={homesSaving}>{t('common.save')}</Button>
      {@render pendingRows('homes')}
    </div>
    <p class="sc-admin-section__hint">{t('server.takes_effect_after_restart_when')}</p>
    {#if homesError}<p class="sc-admin-section__error" role="alert">{homesError}</p>{/if}
    {#if homesOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(homesOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.request_rate')}</h4>
    <p class="sc-admin-section__hint">{t('server.what_the_request_rate_is_for')}</p>
    <div class="sc-server-settings__form">
      <TextField label={t('server.requests_per_second')} bind:value={ratePerSec} />
      <TextField label={t('server.burst_allowance')} bind:value={rateBurst} />
      <Button variant="filled" onclick={saveRate} loading={rateSaving}>{t('common.save')}</Button>
    </div>
    <p class="sc-admin-section__hint">{t('server.all_apply_immediately_no_restart')}</p>
    {#if rateError}<p class="sc-admin-section__error" role="alert">{rateError}</p>{/if}
    {#if rateOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(rateOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.file_watching')}</h4>
    <p class="sc-admin-section__hint">{t('server.what_file_watching_is_for')}</p>
    <div class="sc-server-settings__form">
      <SelectOutlined label={t('server.watch_mode')} width="100%" options={WATCH_BACKEND_OPTIONS} bind:value={watchBackend} />
      <TextField label={t('server.maximum_folders_watched_at_once')} bind:value={watchHotSetMax} />
      <TextField label={t('server.changes_before_a_full_rescan')} bind:value={watchFullThreshold} />
      <Button variant="filled" onclick={saveWatch} loading={watchSaving}>{t('common.save')}</Button>
      {@render pendingRows('watch')}
    </div>
    <p class="sc-admin-section__hint">
      {t('server.takes_effect_after_restart_when')}
    </p>
    {#if watchError}<p class="sc-admin-section__error" role="alert">{watchError}</p>{/if}
    {#if watchOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(watchOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('settings.single_sign_on')}</h4>
    <dl class="sc-server-settings__other">
      {#each snapshot.fields.filter((f) => f.key.startsWith('oidc.')) as f (f.key)}
        <div>
          <dt>{fieldLabel(f.key)}</dt>
          <dd>
            {formatValue(f.value)}
            {#if f.readonly_reason_key}
              <br /><span class="sc-server-settings__reason">{serverKeyText(f.readonly_reason_key)}</span>
            {/if}
          </dd>
        </div>
      {/each}
    </dl>
    <p class="sc-admin-section__hint">{t('server.connected_accounts_cannot_use_smb')}</p>

    <h4 class="sc-admin-section__subhead">{t('server.storage_paths')}</h4>
    <dl class="sc-server-settings__other">
      {#each snapshot.fields.filter((f) => PATH_KEYS.includes(f.key)) as f (f.key)}
        <div>
          <dt>{fieldLabel(f.key)}</dt>
          <dd>
            {formatValue(f.value)}
            {#if f.readonly_reason_key}
              <br /><span class="sc-server-settings__reason">{serverKeyText(f.readonly_reason_key)}</span>
            {/if}
          </dd>
        </div>
      {/each}
    </dl>

  {/if}
</section>

<ConfirmDialog
  open={busyOpen}
  title={t('server.work_still_progress')}
  message={t('server.uploads_jobs_running_right_now', {
    uploads: busyCounts?.uploads ?? 0,
    jobs: busyCounts?.jobs ?? 0
  })}
  confirmLabel={t('server.restart_anyway')}
  danger
  onclose={() => ((busyOpen = false), (busyRetry = null))}
  onconfirm={confirmBusy}
/>

<style>
  .sc-admin-section {
    margin-block: 24px;
  }
  .sc-admin-section h3 {
    margin: 0 0 8px;
    @apply --m3-title-medium;
  }
  .sc-admin-section__subhead {
    /* 32 above, 8 below. The subhead starts a new settings group, and at 24
       the space before it was only twice the 12px that separates a field from
       its own hint, so the groups ran together. */
    margin: 32px 0 8px;
    @apply --m3-title-small;
  }
  .sc-admin-section__hint {
    max-width: 640px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    margin-bottom: 16px;
  }
  .sc-admin-section__warning {
    padding: 12px 16px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-medium;
    margin-bottom: 16px;
  }
  .sc-admin-section__error {
    margin: 8px 0 0;
    color: var(--m3c-error);
    @apply --m3-body-medium;
  }
  .sc-admin-section__saved {
    margin: 8px 0 0;
    color: var(--m3c-primary);
    @apply --m3-body-medium;
  }
  .sc-server-settings__form {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 16px;
    max-width: 480px;
  }
  /* `.field`, not `.sc-field` — see UploadSettingsSection for the same
     migration leftover. */
  .sc-server-settings__form :global(.field) {
    width: 100%;
  }
  /* SelectOutlined's own `width` prop sets `width` on the `<select>`, whose
     containing block is the shrink-to-fit `.m3-container`; under this form's
     `align-items: flex-start` that resolves back to the widest option's text.
     The container is what has to be told to fill the column. */
  .sc-server-settings__form :global(.m3-container:has(> select)) {
    width: 100%;
  }
  .sc-server-settings__other {
    margin: 0 0 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .sc-server-settings__other dt {
    font-family: monospace;
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-server-settings__other dd {
    margin: 0;
    @apply --m3-body-medium;
  }
  .sc-server-settings__source {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-server-settings__reason {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-server-settings__pending {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  /* What the dry run found. The level is spelled out in the text beside each
     item, so the colour is reinforcement rather than the only carrier. */
  /* What an empty field means, shown only while it is empty. */
  .sc-server-settings__empty-note {
    margin: 0;
    grid-column: 1 / -1;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-server-settings__findings {
    margin: 8px 0 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 4px;
  }
  .sc-server-settings__finding {
    margin: 0;
    padding: 8px 12px;
    border-radius: 4px;
    border-left: 4px solid var(--m3c-outline);
    background: var(--m3c-surface-container);
    color: var(--m3c-on-surface);
    overflow-wrap: anywhere;
    @apply --m3-body-small;
  }
  .sc-server-settings__finding--block {
    border-left-color: var(--m3c-error);
  }
  .sc-server-settings__finding--warn {
    border-left-color: var(--m3c-tertiary, var(--m3c-outline));
  }
  .sc-server-settings__finding--ok {
    border-left-color: var(--m3c-primary);
  }
  .sc-server-settings__finding-level {
    font-weight: 600;
    margin-right: 4px;
  }
  .sc-server-settings__finding code {
    font-family: var(--m3-font-mono, ui-monospace, monospace);
    margin-right: 4px;
  }
  /* A diagnostic from another program: monospace so a path or a directive in
     it reads as the literal string it is, and wrapping so a long testparm
     line stays inside the card. */
  .sc-server-settings__agent-detail {
    margin: 8px 0 0;
    font-family: var(--m3-font-mono, ui-monospace, monospace);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    @apply --m3-body-small;
  }
  /* The badge carries its own text. Colour alone would leave a row's pending
     state invisible to anyone who cannot see the difference. */
  .sc-server-settings__badge {
    display: inline-block;
    padding: 4px 8px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-tertiary-container);
    color: var(--m3c-on-tertiary-container);
    @apply --m3-label-small;
  }
</style>
