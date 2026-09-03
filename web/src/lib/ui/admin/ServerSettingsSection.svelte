<script lang="ts">
  // Server settings: every operator-settable field this deployment has,
  // reachable from this one screen (`go/engine/service/settings/catalogue`).
  // There is no config file, so this is the only place a deployment is
  // configured. Most groups apply live; homes, single sign-on's provider,
  // the sandbox policy and SMB's own on/off switch used to need a restart —
  // per this batch's field decisions only `security.hardening` still does
  // (Landlock/seccomp cannot be undone by the process that installed them),
  // handled by mounting `RestartDialog` whenever a save's own outcome says so.
  //
  // `snapshot.fields` is one flat, dotted-key list keyed by section and
  // field. This component groups by an explicit key set per form below
  // (`EDITABLE_KEYS`); anything left over renders read-only at the bottom
  // with whatever reason it carries, rather than being silently dropped —
  // a field this screen cannot edit is still visible, never invisible.
  import { t } from '../../i18n'
  import { api } from '../../api/client'
  import { describeApiError, serverKeyText } from '../../api/error-text'
  import { BYTES_PER_MB, bytesToMb } from '../../format/bytes'
  import type {
    SettingsSnapshot,
    SettingsField,
    SettingsFinding,
    SettingsSectionId,
    ApplyOutcome,
    SmbSettingsReq,
    SearchSettingsReq,
    ArchiveSettingsReq,
    NetworkSettingsReq,
    HomesSettingsReq,
    WatchSettingsReq,
    DbSettingsReq,
    OidcSettingsReq
  } from '../../api/types'
  import { Icon, SelectOutlined } from 'm3-svelte'
  import { icons } from '../../icons'
  import Button from '../Button.svelte'
  import TextField from '../TextField.svelte'
  import Switch from '../Switch.svelte'
  import ConfirmDialog from '../ConfirmDialog.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import RestartDialog from './RestartDialog.svelte'

  const SMB_TOTP_OPTIONS = [
    { value: 'require_separate', text: t('server.require_separate_smb_password_default') },
    { value: 'block', text: t('server.smb_not_allowed') }
  ]

  let snapshot = $state<SettingsSnapshot | null>(null)
  let loading = $state(true)
  let loadError = $state<string | null>(null)

  function field(key: string): SettingsField | undefined {
    return snapshot?.fields.find((f) => f.key === key)
  }

  /**
   * The human name for a reported field.
   *
   * A key with no catalogue entry falls back to itself, which is worse than
   * a translation and better than a blank row.
   */
  //   /* i18n */ 'field.oidc_ca_cert_file'
  //   /* i18n */ 'field.smb_config_dir'
  //   /* i18n */ 'field.smb_agent_socket'
  //   /* i18n */ 'field.security_hardening'
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
  function emptyNote(key: string): string | null {
    const f = field(key)
    return f?.empty_means_key ? t(f.empty_means_key) : null
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

  // The server answers three separate facts: stored, applied, and whether a
  // restart is needed.
  function outcomeText(o: ApplyOutcome): string {
    if (o.restart_required) return t('server.change_takes_full_effect_only')
    if (o.applied) return t('server.change_took_effect_immediately')
    if (o.stored) return t('server.change_takes_full_effect_only')
    return t('common.could_not_save_settings')
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

  // ── the checker's own findings ──
  //
  // A save answers with what it learned by trying the change, not just
  // whether it worked. A blocking finding refused the save outright; an
  // advisory one saved anyway and is worth reading. `settings.check_passed`
  // is the checker's "nothing to say" filler and carries no field of its
  // own — showing it adds nothing a blank list didn't already say, so it is
  // the one reason filtered out here.
  function findingsOf(o: ApplyOutcome | null): SettingsFinding[] {
    return (o?.findings ?? []).filter((f) => f.reason !== 'settings.check_passed')
  }

  // ── range validation ──
  //
  // `SettingsField.range` carries the same bounds the server checks at save
  // time. Reading it here means a value outside the bound is refused before
  // the round trip, not after — the server-side check stays, this is only
  // for the operator's benefit.
  function rangeError(key: string, raw: string): string | null {
    const r = field(key)?.range
    if (!r || r.kind !== 'int') return null
    const n = Number(raw)
    if (!Number.isInteger(n) || n < r.min || n > r.max) {
      return t('server.enter_a_value_between', { min: r.min, max: r.max })
    }
    return null
  }
  function intRangeAttrs(key: string): { min?: number; max?: number } {
    const r = field(key)?.range
    return r && r.kind === 'int' ? { min: r.min, max: r.max } : {}
  }

  // ── moving focus to a failed save's own error ──
  //
  // `role="alert"` announces the text to a screen reader; it does not move
  // anyone's focus. A keyboard or screen-reader user who just pressed Save
  // is left exactly where they were, with no indication anything happened
  // unless they go looking. `tabindex="-1"` makes the paragraph a valid
  // focus target without adding it to the tab order, and the action moves
  // focus to it whenever the message actually changes — not on every
  // re-render, so retrying with the identical refusal does not steal focus
  // a second time from whatever the operator has since done.
  function focusOnError(node: HTMLElement, error: string | null) {
    let last: string | null = null
    function apply(v: string | null) {
      if (v && v !== last) node.focus()
      last = v
    }
    apply(error)
    return { update: apply }
  }

  // ── sections ──
  //
  // Saving is what tries the change. The server renders the SMB
  // configuration, writes a file into the homes root and checks the host list
  // still contains the host this browser is talking to, and refuses the write
  // when one of those fails, naming the field. There is no preview button
  // beside the save: it asked a question the next click answered.

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
  /* i18n */ 'settings.canonical_url_not_an_app_host'
  /* i18n */ 'settings.duplicate_host'
  /* i18n */ 'settings.host_role_conflict'
  /* i18n */ 'settings.invalid_origin'

  // ── the restart a save needs ──
  //
  // Only `security.hardening` needs one now; every other section this
  // screen edits applies live. When a save's own `ApplyOutcome` reports
  // `restart_required`, `RestartDialog` (owned by `FrontendRestart`) is
  // opened with that outcome — it offers the restart itself, polls health
  // until the process answers again, and calls back here to reload.

  let restartOutcome = $state<ApplyOutcome | null>(null)
  let restartOpen = $state(false)

  /** Runs a save and offers a restart when the outcome needs one. */
  async function saving(
    run: () => Promise<ApplyOutcome>,
    fallback: string,
    set: (o: ApplyOutcome | null, err: string | null) => void
  ): Promise<void> {
    try {
      const outcome = await run()
      set(outcome, null)
      if (outcome.restart_required) {
        restartOutcome = outcome
        restartOpen = true
      }
      await load()
    } catch (err) {
      set(null, describeApiError(err, fallback))
    }
  }

  // ── SMB ──

  let smbEnabled = $state(false)
  let smbWorkgroup = $state('')
  let smbServerName = $state('')
  let smbServiceUser = $state('')
  let smbAllowPublicBind = $state(false)
  let smbTotpPolicy = $state<'require_separate' | 'block'>('require_separate')
  let smbServiceGid = $state('')
  let smbInterfaces = $state('')
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
      service_gid: gid,
      interfaces: strToArr(smbInterfaces)
    }
    smbSaving = true
    await saving(
      () => api.adminSetSmbSettings(req),
      t('server.could_not_save_smb_settings'),
      (o, e) => ((smbOutcome = o), (smbError = e))
    )
    smbSaving = false
  }

  // ── search ── (both fields fully live)

  let searchMaxFast = $state('')
  let searchDeadlineFast = $state('')
  let searchSaving = $state(false)
  let searchError = $state<string | null>(null)
  let searchOutcome = $state<ApplyOutcome | null>(null)

  async function saveSearch(): Promise<void> {
    searchError = null
    searchOutcome = null
    const maxErr = rangeError('search.max_concurrent_fast', searchMaxFast)
    const deadlineErr = rangeError('search.walk_deadline_fast_ms', searchDeadlineFast)
    if (maxErr || deadlineErr) {
      searchError = maxErr ?? deadlineErr
      return
    }
    const req: SearchSettingsReq = {
      max_concurrent_fast: Number(searchMaxFast),
      walk_deadline_fast_ms: Number(searchDeadlineFast)
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
    const rerr = rangeError('archive.max_concurrent', archiveMax)
    if (rerr) {
      archiveError = rerr
      return
    }
    archiveSaving = true
    try {
      archiveOutcome = await api.adminSetArchiveSettings({ max_concurrent: Number(archiveMax) })
      await load()
    } catch (err) {
      archiveError = describeApiError(err, t('server.could_not_save_archive_settings'))
    } finally {
      archiveSaving = false
    }
  }

  // ── network ──

  let netAppHosts = $state('')
  let netContentHosts = $state('')
  let netAllowedOrigins = $state('')
  let netCanonicalUrl = $state('')
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
      content_hosts: strToArr(netContentHosts),
      allowed_origins: strToArr(netAllowedOrigins),
      compat_canonical_url: netCanonicalUrl.trim(),
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

  // ── database size guard ──

  let dbSizeGuard = $state(false)
  let dbMaxBytesMb = $state('')
  let dbMinFreeBytesMb = $state('')
  let dbSaving = $state(false)
  let dbError = $state<string | null>(null)
  let dbOutcome = $state<ApplyOutcome | null>(null)

  async function saveDb(): Promise<void> {
    dbError = null
    dbOutcome = null
    const maxMb = Number(dbMaxBytesMb)
    const minMb = Number(dbMinFreeBytesMb)
    if (!Number.isFinite(maxMb) || maxMb < 0 || !Number.isFinite(minMb) || minMb < 0) {
      dbError = t('server.every_value_must_integer_0')
      return
    }
    const req: DbSettingsReq = {
      size_guard: dbSizeGuard,
      max_bytes: Math.round(maxMb * BYTES_PER_MB),
      min_free_bytes: Math.round(minMb * BYTES_PER_MB)
    }
    dbSaving = true
    await saving(
      () => api.adminSetDbSettings(req),
      t('server.could_not_save_db_settings'),
      (o, e) => ((dbOutcome = o), (dbError = e))
    )
    dbSaving = false
  }

  // ── home folders ──

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
      t('server.could_not_save_home_folder'),
      (o, e) => ((homesOutcome = o), (homesError = e))
    )
    homesSaving = false
  }

  // ── file watching ── (both bounds fully live)

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
    const req: WatchSettingsReq = { hot_set_max: hot, full_threshold: full }
    watchSaving = true
    await saving(
      () => api.adminSetWatchSettings(req),
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

  // ── single sign-on (OIDC) ──
  //
  // Applies live: the provider is rebuilt the next time settings load,
  // which this save triggers itself. `client_secret` is write-only — the
  // server never echoes it back, so an empty box here means "leave the
  // stored one alone", not "there is none".

  let oidcEnabled = $state(false)
  let oidcIssuer = $state('')
  let oidcClientId = $state('')
  let oidcClientSecret = $state('')
  let oidcRedirectUris = $state('')
  let oidcScopes = $state('')
  let oidcDisplayName = $state('')
  let oidcAllowPrivateEndpoints = $state(false)
  let oidcSaving = $state(false)
  let oidcError = $state<string | null>(null)
  let oidcOutcome = $state<ApplyOutcome | null>(null)

  async function saveOidc(): Promise<void> {
    oidcError = null
    oidcOutcome = null
    if (oidcEnabled && (!oidcIssuer.trim() || !oidcClientId.trim())) {
      oidcError = t('server.enter_workgroup_server_name_service_account')
      return
    }
    const req: OidcSettingsReq & { client_secret?: string } = {
      enabled: oidcEnabled,
      issuer: oidcIssuer.trim(),
      client_id: oidcClientId.trim(),
      redirect_uris: strToArr(oidcRedirectUris),
      scopes: strToArr(oidcScopes),
      display_name: oidcDisplayName,
      allow_private_endpoints: oidcAllowPrivateEndpoints,
      smb_policy: 'block'
    }
    if (oidcClientSecret.trim()) req.client_secret = oidcClientSecret.trim()
    oidcSaving = true
    await saving(
      () => api.adminSetOidcSettings(req),
      t('server.could_not_save_oidc_settings'),
      (o, e) => ((oidcOutcome = o), (oidcError = e))
    )
    if (!oidcError) oidcClientSecret = ''
    oidcSaving = false
  }

  // ── everything else this screen doesn't have a dedicated control for —
  // always read-only (the server never leaves an editable field
  // out of the groups above), shown with its reason rather than hidden. ──

  const EDITABLE_KEYS = new Set([
    'bind',
    'app_hosts',
    'content_hosts',
    'allowed_origins',
    'compat_canonical_url',
    'trusted_proxies',
    'db.size_guard',
    'db.max_bytes',
    'db.min_free_bytes',
    'homes.enabled',
    'homes.root',
    'smb.enabled',
    'smb.workgroup',
    'smb.server_name',
    'smb.service_user',
    'smb.allow_public_bind',
    'smb.totp_policy',
    'smb.service_gid',
    'smb.interfaces',
    'search.max_concurrent_fast',
    'search.walk_deadline_fast_ms',
    'archive.max_concurrent',
    'rate.per_sec',
    'rate.burst',
    'watch.hot_set_max',
    'watch.full_threshold',
    'oidc.enabled',
    'oidc.issuer',
    'oidc.client_id',
    'oidc.redirect_uris',
    'oidc.scopes',
    'oidc.display_name',
    'oidc.allow_private_endpoints'
  ])

  // Reported, never editable: what the process opened before anything could
  // be configured. `data_dir` is a process argument; `smb.config_dir` is
  // read under the `smb` section but is the sidecar's own mounted
  // directory, the other side of a container boundary this browser cannot
  // move by writing a new path. Rendered unconditionally, not gated on
  // `snapshot.fields` carrying the key: `data_dir` is not a catalogue field
  // at all (it never was one this build could safely offer), so the reason
  // has to stand on its own rather than annotate a row that may not exist.
  const PATH_KEYS = ['data_dir', 'smb.config_dir']

  // Per-share, not per-deployment: `symlink_policy` lives on each share's
  // own row (`AdminShare`, set from the folder-share screen's own edit
  // dialog), not in the settings document this screen edits. A control
  // here would store a value nothing reads. Same treatment as the paths
  // above: explained regardless of whether the snapshot happens to carry a
  // synthetic entry for it.

  const otherFields = $derived(
    (snapshot?.fields ?? []).filter(
      (f) => !EDITABLE_KEYS.has(f.key) && !PATH_KEYS.includes(f.key) && f.key !== 'symlink_policy'
    )
  )

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
      smbInterfaces = arrToStr(field('smb.interfaces')?.value)

      searchMaxFast = String(field('search.max_concurrent_fast')?.value ?? '')
      searchDeadlineFast = String(field('search.walk_deadline_fast_ms')?.value ?? '')

      archiveMax = String(field('archive.max_concurrent')?.value ?? '')

      ratePerSec = String(field('rate.per_sec')?.value ?? '')
      rateBurst = String(field('rate.burst')?.value ?? '')

      netAppHosts = arrToStr(field('app_hosts')?.value)
      netContentHosts = arrToStr(field('content_hosts')?.value)
      netAllowedOrigins = arrToStr(field('allowed_origins')?.value)
      netCanonicalUrl = String(field('compat_canonical_url')?.value ?? '')
      netTrustedProxies = arrToStr(field('trusted_proxies')?.value)
      netBind = String(field('bind')?.value ?? '')

      dbSizeGuard = Boolean(field('db.size_guard')?.value)
      dbMaxBytesMb = String(bytesToMb(Number(field('db.max_bytes')?.value ?? 0)))
      dbMinFreeBytesMb = String(bytesToMb(Number(field('db.min_free_bytes')?.value ?? 0)))

      homesEnabled = Boolean(field('homes.enabled')?.value)
      homesRoot = String(field('homes.root')?.value ?? '')

      watchHotSetMax = String(field('watch.hot_set_max')?.value ?? '')
      watchFullThreshold = String(field('watch.full_threshold')?.value ?? '')

      oidcEnabled = Boolean(field('oidc.enabled')?.value)
      oidcIssuer = String(field('oidc.issuer')?.value ?? '')
      oidcClientId = String(field('oidc.client_id')?.value ?? '')
      oidcRedirectUris = arrToStr(field('oidc.redirect_uris')?.value)
      oidcScopes = arrToStr(field('oidc.scopes')?.value)
      oidcDisplayName = String(field('oidc.display_name')?.value ?? '')
      oidcAllowPrivateEndpoints = Boolean(field('oidc.allow_private_endpoints')?.value)
    } catch {
      loadError = t('server.could_not_load_server_settings')
    } finally {
      loading = false
    }
  }

  load()
</script>

{#snippet findingsList(o: ApplyOutcome | null)}
  {@const findings = findingsOf(o)}
  {#if findings.length}
    <ul class="sc-server-settings__findings">
      {#each findings as f, i (f.section + (f.field ?? '') + f.reason + i)}
        <li
          class="sc-server-settings__finding"
          class:sc-server-settings__finding--block={f.blocking}
          class:sc-server-settings__finding--ok={!f.blocking}
        >
          <span class="sc-server-settings__finding-level">
            {f.blocking ? t('settings.finding_blocking') : t('settings.finding_advisory')}
          </span>
          {#if f.field}<code>{f.field}</code>{/if}
          {t(f.reason, f.args ?? {})}
        </li>
      {/each}
    </ul>
  {/if}
{/snippet}

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
          {#each snapshot.smb_overgrants as o (o.share + '' + o.user + '' + o.key)}
            <li>{t(o.key, { share: o.share, user: o.user, detail: o.detail.join(', ') })}</li>
          {/each}
        </ul>
      </div>
    {/if}

    <!-- 1. SMB -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.folder} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">SMB</h4>
          <p class="sc-admin-card-subtitle">{t('server.all_apply_immediately_no_restart')}</p>
        </div>
      </div>
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
        <TextField label={t('server.service_account_gid')} bind:value={smbServiceGid} type="number" {...intRangeAttrs('smb.service_gid')} />
        <TextField label={t('settings.smb_interfaces')} bind:value={smbInterfaces} />
        <p class="sc-admin-section__hint">{t('settings.smb_interfaces_hint')}</p>
        <Button variant="filled" onclick={saveSmb} loading={smbSaving}>{t('common.save')}</Button>
        {#if smbError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={smbError}>{smbError}</p>{/if}
        {#if smbOutcome}
          <p class="sc-admin-section__saved" role="status">{outcomeText(smbOutcome)}</p>
          {@render findingsList(smbOutcome)}
        {/if}
      </div>
      <!-- What the agent beside smbd did with the files this server rendered.
           The key comes from the server, so the extractor cannot see it at the
           call site:
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
          {#if agent.detail && !agent.ok}
            <p class="sc-server-settings__agent-detail">{agent.detail}</p>
          {/if}
        </div>
      {/if}
    </div>

    <!-- 2. Search -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.search} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('common.search')}</h4>
          <p class="sc-admin-card-subtitle">{t('server.all_apply_immediately_no_restart')}</p>
        </div>
      </div>
      <div class="sc-server-settings__form">
        <TextField label={t('server.concurrent_fast_searches')} bind:value={searchMaxFast} type="number" {...intRangeAttrs('search.max_concurrent_fast')} />
        <TextField label={t('server.fast_search_timeout_ms')} bind:value={searchDeadlineFast} type="number" {...intRangeAttrs('search.walk_deadline_fast_ms')} />
        <Button variant="filled" onclick={saveSearch} loading={searchSaving}>{t('common.save')}</Button>
        {#if searchError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={searchError}>{searchError}</p>{/if}
        {#if searchOutcome}
          <p class="sc-admin-section__saved" role="status">{outcomeText(searchOutcome)}</p>
          {@render findingsList(searchOutcome)}
        {/if}
      </div>
    </div>

    <!-- 3. Zip download -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.download} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('server.zip_download')}</h4>
          <p class="sc-admin-card-subtitle">{t('server.applies_immediately_no_restart_needed')}</p>
        </div>
      </div>
      <div class="sc-server-settings__form">
        <TextField label={t('server.concurrent_zip_streams')} bind:value={archiveMax} type="number" {...intRangeAttrs('archive.max_concurrent')} />
        <Button variant="filled" onclick={saveArchive} loading={archiveSaving}>{t('common.save')}</Button>
        {#if archiveError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={archiveError}>{archiveError}</p>{/if}
        {#if archiveOutcome}
          <p class="sc-admin-section__saved" role="status">{outcomeText(archiveOutcome)}</p>
          {@render findingsList(archiveOutcome)}
        {/if}
      </div>
    </div>

    <!-- 4. Network -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.link} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('server.network')}</h4>
          <p class="sc-admin-card-subtitle">{t('server.applies_immediately_no_restart_needed')}</p>
        </div>
      </div>
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
        {#if netError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={netError}>{netError}</p>{/if}
        {#if netOutcome}
          <p class="sc-admin-section__saved" role="status">{outcomeText(netOutcome)}</p>
          {@render findingsList(netOutcome)}
        {/if}
      </div>
    </div>

    <!-- 5. Database -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons['folder-tree']} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('server.database')}</h4>
          <p class="sc-admin-card-subtitle">{t('settings.db_size_guard_hint')}</p>
        </div>
      </div>
      <div class="sc-server-settings__form">
        <Switch checked={dbSizeGuard} onchange={(v) => (dbSizeGuard = v)} label={t('settings.db_size_guard')} />
        <TextField label={t('settings.db_max_bytes')} bind:value={dbMaxBytesMb} type="number" min={0} />
        <TextField label={t('settings.db_min_free_bytes')} bind:value={dbMinFreeBytesMb} type="number" min={0} />
        <Button variant="filled" onclick={saveDb} loading={dbSaving}>{t('common.save')}</Button>
        {#if dbError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={dbError}>{dbError}</p>{/if}
        {#if dbOutcome}
          <p class="sc-admin-section__saved" role="status">{outcomeText(dbOutcome)}</p>
          {@render findingsList(dbOutcome)}
        {/if}
      </div>
    </div>

    <!-- 6. Home folders -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.home} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('server.home_folders')}</h4>
          <p class="sc-admin-card-subtitle">{t('server.home_folders_hint')}</p>
        </div>
      </div>
      <div class="sc-server-settings__form">
        <Switch checked={homesEnabled} onchange={(v) => (homesEnabled = v)} label={t('server.enable_home_folders')} />
        <TextField label={t('server.homes_root_path')} bind:value={homesRoot} />
        <Button variant="filled" onclick={saveHomes} loading={homesSaving}>{t('common.save')}</Button>
        {#if homesError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={homesError}>{homesError}</p>{/if}
        {#if homesOutcome}
          <p class="sc-admin-section__saved" role="status">{outcomeText(homesOutcome)}</p>
          {@render findingsList(homesOutcome)}
        {/if}
      </div>
    </div>

    <!-- 7. Request rate -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.refresh} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('server.request_rate')}</h4>
          <p class="sc-admin-card-subtitle">{t('server.what_the_request_rate_is_for')}</p>
        </div>
      </div>
      <div class="sc-server-settings__form">
        <TextField label={t('server.requests_per_second')} bind:value={ratePerSec} type="number" {...intRangeAttrs('rate.per_sec')} />
        <TextField label={t('server.burst_allowance')} bind:value={rateBurst} type="number" {...intRangeAttrs('rate.burst')} />
        <Button variant="filled" onclick={saveRate} loading={rateSaving}>{t('common.save')}</Button>
        {#if rateError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={rateError}>{rateError}</p>{/if}
        {#if rateOutcome}
          <p class="sc-admin-section__saved" role="status">{outcomeText(rateOutcome)}</p>
          {@render findingsList(rateOutcome)}
        {/if}
      </div>
    </div>

    <!-- 8. File watching -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.info} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('server.file_watching')}</h4>
          <p class="sc-admin-card-subtitle">{t('server.what_file_watching_is_for')}</p>
        </div>
      </div>
      <div class="sc-server-settings__form">
        <TextField label={t('server.maximum_folders_watched_at_once')} bind:value={watchHotSetMax} type="number" {...intRangeAttrs('watch.hot_set_max')} />
        <TextField label={t('server.changes_before_a_full_rescan')} bind:value={watchFullThreshold} type="number" {...intRangeAttrs('watch.full_threshold')} />
        <Button variant="filled" onclick={saveWatch} loading={watchSaving}>{t('common.save')}</Button>
        {#if watchError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={watchError}>{watchError}</p>{/if}
        {#if watchOutcome}
          <p class="sc-admin-section__saved" role="status">{outcomeText(watchOutcome)}</p>
          {@render findingsList(watchOutcome)}
        {/if}
      </div>
    </div>

    <!-- 9. Single sign-on -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.lock} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('settings.single_sign_on')}</h4>
          <p class="sc-admin-card-subtitle">{t('server.single_sign_on_hint')}</p>
        </div>
      </div>
      <div class="sc-server-settings__form">
        <Switch checked={oidcEnabled} onchange={(v) => (oidcEnabled = v)} label={t('settings.oidc_enable')} />
        <TextField label={t('settings.oidc_issuer')} bind:value={oidcIssuer} />
        <TextField label={t('settings.oidc_client_id')} bind:value={oidcClientId} />
        <TextField label={t('common.password')} type="password" bind:value={oidcClientSecret} placeholder={t('settings.secret_is_write_only')} />
        <TextField label={t('settings.oidc_redirect_uris')} bind:value={oidcRedirectUris} />
        <TextField label={t('settings.oidc_scopes')} bind:value={oidcScopes} />
        <TextField label={t('settings.oidc_display_name')} bind:value={oidcDisplayName} />
        <Switch checked={oidcAllowPrivateEndpoints} onchange={(v) => (oidcAllowPrivateEndpoints = v)} label={t('settings.oidc_allow_private_endpoints')} />
        <p class="sc-admin-section__hint">{t('server.connected_accounts_cannot_use_smb')}</p>
        <Button variant="filled" onclick={saveOidc} loading={oidcSaving}>{t('common.save')}</Button>
        {#if oidcError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={oidcError}>{oidcError}</p>{/if}
        {#if oidcOutcome}
          <p class="sc-admin-section__saved" role="status">{outcomeText(oidcOutcome)}</p>
          {@render findingsList(oidcOutcome)}
        {/if}
      </div>
    </div>

    <!-- 10. Storage paths -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.info} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('server.storage_paths')}</h4>
          <p class="sc-admin-card-subtitle">{t('settings.paths_readonly_reason')}</p>
        </div>
      </div>
      <dl class="sc-server-settings__other">
        {#each PATH_KEYS as key (key)}
          <div>
            <dt>{fieldLabel(key)}</dt>
            <dd>
              {formatValue(field(key)?.value)}
            </dd>
          </div>
        {/each}
        <div>
          <dt>{fieldLabel('symlink_policy')}</dt>
          <dd>
            <span class="sc-server-settings__reason">{t('settings.readonly_per_share_symlink_policy')}</span>
          </dd>
        </div>
      </dl>

      {#if otherFields.length}
        <h5 class="sc-admin-section__subhead">{t('settings.settings_sections')}</h5>
        <dl class="sc-server-settings__other">
          {#each otherFields as f (f.key)}
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
    </div>
  {/if}
</section>
<RestartDialog
  open={restartOpen}
  outcome={restartOutcome}
  onclose={() => (restartOpen = false)}
  onrestarted={() => {
    restartOpen = false
    load()
  }}
/>

<style>
  .sc-admin-card {
    display: flex;
    flex-direction: column;
    margin-bottom: 24px;
    padding: 24px;
    border-radius: var(--m3-shape-large);
    border: 1px solid var(--m3c-outline-variant);
    background: var(--m3c-surface-container-low);
    gap: 16px;
  }
  .sc-admin-card-head {
    display: flex;
    align-items: center;
    gap: 16px;
  }
  .sc-admin-card-icon {
    flex: none;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 40px;
    height: 40px;
    border-radius: var(--m3-shape-full);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-primary);
  }
  .sc-admin-card-meta {
    flex: 1;
    min-width: 0;
  }
  .sc-admin-card-title {
    margin: 0;
    @apply --m3-title-medium;
    font-weight: 600;
  }
  .sc-admin-card-subtitle {
    margin: 4px 0 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  @media (max-width: 599.98px) {
    .sc-admin-card {
      padding: 16px;
      gap: 12px;
    }
    .sc-admin-card-head {
      gap: 12px;
    }
  }
  .sc-admin-section {
    margin-block: 24px;
  }
  .sc-admin-section h3 {
    margin: 0 0 8px;
    @apply --m3-title-medium;
  }
  .sc-admin-section__subhead {
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
  .sc-admin-section__error:focus {
    outline: 2px solid var(--m3c-error);
    outline-offset: 2px;
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
  /* A hint inside the form is spaced by the form's own gap. Its own margins
     add to that: the paragraph's default top margin made a 16px gap 28.  */
  .sc-server-settings__form > .sc-admin-section__hint {
    margin: 0;
  }
  /* `.field`, not `.sc-field` — see UploadSettingsSection for the same
     migration leftover. */
  .sc-server-settings__form :global(.field) {
    width: 100%;
  }
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
  .sc-server-settings__reason {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
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
  /* Blocking and advisory are told apart by the label text
     (`settings.finding_blocking`/`settings.finding_advisory`), not by the
     border colour alone: colour is reinforcement for a sighted operator who
     can tell red from grey, the label is what a colourblind operator or a
     screen reader gets instead. */
  .sc-server-settings__finding--block {
    border-left-color: var(--m3c-error);
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
  .sc-server-settings__agent-detail {
    margin: 8px 0 0;
    font-family: var(--m3-font-mono, ui-monospace, monospace);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    @apply --m3-body-small;
  }
</style>
