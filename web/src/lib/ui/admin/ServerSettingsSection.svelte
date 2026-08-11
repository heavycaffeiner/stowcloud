<script lang="ts">
  // Server settings — parity with `config.toml`: every operator-settable
  // field this deployment has, reachable from this one screen
  // (`crates/sc-http/src/settings_api.rs::SettingsSnapshot`). Most groups
  // apply live; network/db/symlink-policy/homes and SMB's own on/off switch
  // need a restart, handled by the restart section at the bottom.
  //
  // `snapshot.fields` is one flat, dotted-key list (mirrors `config.toml`'s
  // own `[section] key` shape) — this component groups by an explicit key
  // set per form below, and anything left over (a field this screen doesn't
  // have a dedicated control for, always because it's `readonly_reason_key`'d —
  // `settings_bridge.rs` never emits an editable field outside these groups)
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

  function sourceLabel(src: SettingsField['source']): string {
    if (src === 'admin_override') return t('server.admin_override')
    if (src === 'config_file') return 'config.toml'
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
    const parts: string[] = []
    if (o.applied_live) parts.push(t('server.change_took_effect_immediately'))
    if (o.restart_required) parts.push(t('server.change_takes_full_effect_only'))
    return parts.join(' ')
  }

  function formatValue(v: unknown): string {
    if (v === null || v === undefined) return t('server.none')
    if (Array.isArray(v)) return v.length ? v.join(', ') : t('server.empty')
    if (typeof v === 'boolean') return v ? t('server.on') : t('server.off')
    return String(v)
  }

  // ── revert one group to config.toml ──
  //
  // Every group is otherwise one-way: a stored override beats the file on
  // every boot, so without this the only way back is deleting `settings.db`,
  // which discards the other nine groups with it.
  //
  // Each button carries its own accessible name naming the group it reverts,
  // rather than ten copies of "Revert": a screen reader reaching the fourth
  // one has no other way to tell which.

  type Section = { id: SettingsSectionId; sourceKey: string; name: string }

  const SECTIONS: Section[] = [
    { id: 'smb', sourceKey: 'smb.enabled', name: 'SMB' },
    { id: 'search', sourceKey: 'search.rate_per_minute', name: t('common.search') },
    { id: 'archive', sourceKey: 'archive.max_concurrent', name: t('server.zip_download') },
    { id: 'network', sourceKey: 'bind', name: t('server.network') },
    { id: 'db', sourceKey: 'db.size_guard', name: t('server.database_size_guard') },
    { id: 'symlink-policy', sourceKey: 'symlink_policy', name: t('server.symlink_policy') },
    { id: 'homes', sourceKey: 'homes.enabled', name: t('server.home_folders') },
    { id: 'watch', sourceKey: 'watch.backend', name: t('server.file_watching') },
    { id: 'oidc', sourceKey: 'oidc.enabled', name: t('settings.single_sign_on') },
    { id: 'paths', sourceKey: 'data_dir', name: t('server.storage_paths') }
  ]

  function section(id: SettingsSectionId): Section {
    return SECTIONS.find((s) => s.id === id) as Section
  }

  /** Is this group currently overriding `config.toml`? Read off one
   *  representative row, since every row in a group shares one source. */
  function isOverridden(id: SettingsSectionId): boolean {
    return field(section(id).sourceKey)?.source === 'admin_override'
  }

  /** The values a revert discards, named rather than left to the admin's
   *  memory. It cannot name what replaces them: the file's own values are not
   *  on this screen, and inventing them would be worse than saying so. */
  function revertPreview(id: SettingsSectionId): string {
    const rows = snapshot?.fields.filter((f) => sectionIdOf(f.key) === id) ?? []
    // `ConfirmDialog` renders its message as a single paragraph, so the
    // separator has to read inline.
    return rows.map((f) => `${f.key}: ${formatValue(f.value)}`).join(', ')
  }

  const SECTION_OF_KEY: Record<string, SettingsSectionId> = {
    bind: 'network',
    app_hosts: 'network',
    content_hosts: 'network',
    allowed_origins: 'network',
    trusted_proxies: 'network',
    public_origins: 'network',
    symlink_policy: 'symlink-policy',
    data_dir: 'paths',
    master_key_file: 'paths',
    'smb.config_dir': 'paths'
  }

  function sectionIdOf(key: string): SettingsSectionId | undefined {
    if (SECTION_OF_KEY[key]) return SECTION_OF_KEY[key]
    const prefix = key.split('.')[0]
    return SECTIONS.some((s) => s.id === prefix) ? (prefix as SettingsSectionId) : undefined
  }

  let revertTarget = $state<SettingsSectionId | null>(null)
  let reverting = $state(false)
  let revertError = $state<string | null>(null)
  let revertOutcome = $state<ApplyOutcome | null>(null)

  async function confirmRevert(): Promise<void> {
    const id = revertTarget
    revertTarget = null
    if (!id) return
    revertError = null
    revertOutcome = null
    reverting = true
    try {
      revertOutcome = await api.adminClearServerSettings(id)
      await load()
    } catch (err) {
      revertError = describeApiError(err, t('server.could_not_revert_settings'))
    } finally {
      reverting = false
    }
  }

  // ── SMB ──

  let smbEnabled = $state(false)
  let smbWorkgroup = $state('')
  let smbServerName = $state('')
  let smbServiceUser = $state('')
  let smbAllowPublicBind = $state(false)
  let smbTotpPolicy = $state<'require_separate' | 'block'>('require_separate')
  let smbServiceUid = $state('')
  let smbServiceGid = $state('')
  let smbSaving = $state(false)
  let smbError = $state<string | null>(null)
  let smbOutcome = $state<ApplyOutcome | null>(null)

  async function saveSmb(): Promise<void> {
    smbError = null
    smbOutcome = null
    const uid = Number(smbServiceUid)
    const gid = Number(smbServiceGid)
    if (!Number.isInteger(uid) || uid < 0 || !Number.isInteger(gid) || gid < 0) {
      smbError = t('server.uid_gid_must_integer_0')
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
      service_uid: uid,
      service_gid: gid
    }
    smbSaving = true
    try {
      smbOutcome = await api.adminSetSmbSettings(req)
      await load()
    } catch (err) {
      smbError = describeApiError(err, t('server.could_not_save_smb_settings'))
    } finally {
      smbSaving = false
    }
  }

  // ── search ──

  let searchMaxFast = $state('')
  let searchMaxSlow = $state('')
  let searchDeadlineFast = $state('')
  let searchDeadlineSlow = $state('')
  let searchRate = $state('')
  let searchSaving = $state(false)
  let searchError = $state<string | null>(null)
  let searchOutcome = $state<ApplyOutcome | null>(null)

  async function saveSearch(): Promise<void> {
    searchError = null
    searchOutcome = null
    const nums = [searchMaxFast, searchMaxSlow, searchDeadlineFast, searchDeadlineSlow, searchRate].map(Number)
    if (nums.some((n) => !Number.isInteger(n) || n < 0)) {
      searchError = t('server.every_value_must_integer_0')
      return
    }
    // Zero here is not a low limit, it is an off switch: every search from
    // every user answers 429 from the moment it applies, live. The server
    // refuses it too; this just says so before the round trip.
    if (nums[4] < 1) {
      searchError = t('server.must_integer_1_or_more')
      return
    }
    const req: SearchSettingsReq = {
      max_concurrent_fast: nums[0],
      max_concurrent_slow: nums[1],
      walk_deadline_fast_ms: nums[2],
      walk_deadline_slow_ms: nums[3],
      rate_per_minute: nums[4]
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

  // ── network (restart-required) ──

  let netBind = $state('')
  let netAppHosts = $state('')
  let netContentHosts = $state('')
  let netAllowedOrigins = $state('')
  let netTrustedProxies = $state('')
  let netPublicOrigins = $state('')
  let netSaving = $state(false)
  let netError = $state<string | null>(null)
  let netOutcome = $state<ApplyOutcome | null>(null)

  /** The authority of a declared origin, for comparing against `app_hosts`. */
  function authorityOf(origin: string): string {
    const rest = origin.includes('://') ? origin.split('://')[1] : origin
    return rest.split(/[/?#]/)[0]
  }

  /** Origins whose host this server will not answer on. Not a refusal: the
   *  admin may be about to fill in `app_hosts` on the same screen. Declaring
   *  an origin does not admit it, and silently widening a security allowlist
   *  as a side effect of editing a display setting would be worse than the
   *  warning. */
  const netUnservedOrigins = $derived.by(() => {
    const hosts = strToArr(netAppHosts).map((h) => h.split(':')[0].toLowerCase())
    if (!hosts.length) return []
    return strToArr(netPublicOrigins).filter(
      (o) => !hosts.includes(authorityOf(o).split(':')[0].toLowerCase())
    )
  })

  /** What `public_origins` was when this screen loaded, so a save can say
   *  what is changing about it. */
  let netOriginsLoaded = $state<string[]>([])
  let netOriginsConfirmOpen = $state(false)

  /** Neither of these disconnects an enrolled client: admission is
   *  `app_hosts`, and these edits only change which name the server offers.
   *  They are still not something an operator infers from a text field, so
   *  they are said out loud before the save. */
  const netOriginChanges = $derived.by(() => {
    const next = strToArr(netPublicOrigins)
    const removed = netOriginsLoaded.filter((o) => !next.includes(o))
    const canonicalChanged = (netOriginsLoaded[0] ?? '') !== (next[0] ?? '')
    return { removed, canonicalChanged, canonical: next[0] ?? '' }
  })

  function openNetworkSave(): void {
    netError = null
    netOutcome = null
    if (!netBind.trim()) {
      netError = t('server.enter_bind_address')
      return
    }
    if (netOriginChanges.removed.length || netOriginChanges.canonicalChanged) {
      netOriginsConfirmOpen = true
      return
    }
    void saveNetwork()
  }

  async function saveNetwork(): Promise<void> {
    netOriginsConfirmOpen = false
    netError = null
    netOutcome = null
    const req: NetworkSettingsReq = {
      bind: netBind.trim(),
      app_hosts: strToArr(netAppHosts),
      content_hosts: strToArr(netContentHosts),
      allowed_origins: strToArr(netAllowedOrigins),
      trusted_proxies: strToArr(netTrustedProxies),
      public_origins: strToArr(netPublicOrigins)
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

  // ── db (restart-required) ──

  let dbSizeGuard = $state(false)
  // The wire is bytes; these two fields are MB, like every other size input.
  // A stored value that is not a whole number of MB is rounded to one on load,
  // so saving without editing writes the rounded value back — the guards are
  // coarse thresholds, and a screen that cannot show its own value is worse.
  let dbMaxMb = $state('')
  let dbMinFreeMb = $state('')
  let dbSaving = $state(false)
  let dbError = $state<string | null>(null)
  let dbOutcome = $state<ApplyOutcome | null>(null)

  async function saveDb(): Promise<void> {
    dbError = null
    dbOutcome = null
    const maxMb = Number(dbMaxMb)
    const minFreeMb = Number(dbMinFreeMb)
    if (!Number.isInteger(maxMb) || maxMb < 0 || !Number.isInteger(minFreeMb) || minFreeMb < 0) {
      dbError = t('server.every_value_must_integer_0_2')
      return
    }
    const maxBytes = maxMb * BYTES_PER_MB
    const minFree = minFreeMb * BYTES_PER_MB
    dbSaving = true
    try {
      dbOutcome = await api.adminSetDbSettings({ size_guard: dbSizeGuard, max_bytes: maxBytes, min_free_bytes: minFree })
      await load()
    } catch (err) {
      dbError = describeApiError(err, t('server.could_not_save_database_size'))
    } finally {
      dbSaving = false
    }
  }

  // ── symlink policy (restart-required) ──

  let symlinkPolicy = $state<'deny' | 'within_share' | 'follow'>('deny')
  let symlinkSaving = $state(false)
  let symlinkError = $state<string | null>(null)
  let symlinkOutcome = $state<ApplyOutcome | null>(null)

  async function saveSymlinkPolicy(): Promise<void> {
    symlinkError = null
    symlinkOutcome = null
    symlinkSaving = true
    try {
      symlinkOutcome = await api.adminSetSymlinkPolicySettings({ policy: symlinkPolicy })
      await load()
    } catch (err) {
      symlinkError = describeApiError(err, t('server.could_not_save_symlink_policy'))
    } finally {
      symlinkSaving = false
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
    homesSaving = true
    try {
      homesOutcome = await api.adminSetHomesSettings({ enabled: homesEnabled, root: homesRoot.trim() || null })
      await load()
    } catch (err) {
      homesError = describeApiError(err, t('server.could_not_save_home_folder'))
    } finally {
      homesSaving = false
    }
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
    try {
      watchOutcome = await api.adminSetWatchSettings(req)
      await load()
    } catch (err) {
      watchError = describeApiError(err, t('server.could_not_save_file_watch'))
    } finally {
      watchSaving = false
    }
  }

  // ── single sign-on (restart-required, all of it) ──
  //
  // The eight rows §6-4 of `docs/proposals/stowcloud-0-oidc-login.md` marks
  // UI-editable. The other two `oidc.*` settings fall through to the read-only
  // list below with the server's own reason attached, and that is deliberate
  // rather than an omission: `oidc.client_secret_file` names a file holding a
  // secret, and an admin override of `oidc.local_password_login` would beat
  // `config.toml` on every boot, so writing `deny` here and then losing the
  // provider would lock out everybody including whoever set it.
  //
  // `oidc.smb_policy` has exactly one accepted value, so there is no control
  // for it. A select with a single option is a control that cannot be used;
  // the hint under the form says what the value means instead.

  let oidcEnabled = $state(false)
  let oidcIssuer = $state('')
  let oidcClientId = $state('')
  let oidcRedirectUris = $state('')
  let oidcScopes = $state('')
  let oidcDisplayName = $state('')
  let oidcAllowPrivate = $state(false)
  let oidcSaving = $state(false)
  let oidcError = $state<string | null>(null)
  let oidcOutcome = $state<ApplyOutcome | null>(null)

  async function saveOidc(): Promise<void> {
    oidcError = null
    oidcOutcome = null
    // Pre-checked here as well as server-side, for the two rules the browser
    // can actually see. The server's answer to a bad redirect URI is not an
    // error at boot: it keeps single sign-on switched off while the rest of
    // the server runs normally, so an admin who typed one and pressed Save
    // would otherwise see "saved" and find out at the next restart.
    //
    // The client secret file is deliberately not among them: only the server
    // can tell whether a path is readable, so that one comes back as
    // `settings.oidc_secret_file_missing` and renders from the catalogue like
    // any other server refusal.
    const redirectUris = strToArr(oidcRedirectUris)
    if (oidcEnabled) {
      if (!redirectUris.length || redirectUris.some((u) => !u.startsWith('https://'))) {
        oidcError = t('server.redirect_uri_must_start_https')
        return
      }
      const hosts = strToArr(netAppHosts).map((h) => h.split(':')[0].toLowerCase())
      const unserved = redirectUris.filter(
        (u) => !hosts.includes(authorityOf(u).split(':')[0].toLowerCase())
      )
      if (unserved.length) {
        oidcError = t('server.redirect_host_must_be_in_app_hosts', { value: unserved.join(', ') })
        return
      }
      if (!oidcIssuer.trim() || !oidcClientId.trim()) {
        oidcError = t('server.enter_issuer_client_id')
        return
      }
    }
    const req: OidcSettingsReq = {
      enabled: oidcEnabled,
      issuer: oidcIssuer.trim(),
      client_id: oidcClientId.trim(),
      redirect_uris: redirectUris,
      scopes: strToArr(oidcScopes),
      display_name: oidcDisplayName.trim(),
      allow_private_endpoints: oidcAllowPrivate,
      smb_policy: 'block'
    }
    oidcSaving = true
    try {
      oidcOutcome = await api.adminSetOidcSettings(req)
      await load()
    } catch (err) {
      oidcError = describeApiError(err, t('server.could_not_save_single_sign'))
    } finally {
      oidcSaving = false
    }
  }

  // ── bootstrap paths (restart-required) ──
  //
  // The one group where a wrong value costs more than a wrong setting: an
  // unwritable `data_dir`, or one that doesn't already hold the databases,
  // means the next start comes up with no accounts at all. The server refuses
  // those outright (422 with a Korean reason, shown verbatim), so this form's
  // own job is just to make the admin say yes once before sending — the same
  // reason the restart button is two-step.

  let pathsDataDir = $state('')
  let pathsMasterKeyFile = $state('')
  let pathsSmbConfigDir = $state('')
  let pathsSaving = $state(false)
  let pathsError = $state<string | null>(null)
  let pathsOutcome = $state<ApplyOutcome | null>(null)
  let pathsConfirmOpen = $state(false)

  function openPathsConfirm(): void {
    pathsError = null
    pathsOutcome = null
    if (!pathsDataDir.trim() || !pathsSmbConfigDir.trim()) {
      pathsError = t('server.data_directory_smb_config_directory')
      return
    }
    pathsConfirmOpen = true
  }

  async function savePaths(): Promise<void> {
    pathsConfirmOpen = false
    const req: PathsSettingsReq = {
      data_dir: pathsDataDir.trim(),
      master_key_file: pathsMasterKeyFile.trim() || null,
      smb_config_dir: pathsSmbConfigDir.trim()
    }
    pathsSaving = true
    try {
      pathsOutcome = await api.adminSetPathsSettings(req)
      await load()
    } catch (err) {
      pathsError = describeApiError(err, t('server.could_not_save_path_settings'))
    } finally {
      pathsSaving = false
    }
  }

  // ── everything else this screen doesn't have a dedicated control for —
  // always read-only (`settings_bridge.rs` never leaves an editable field
  // out of the groups above), shown with its Korean reason rather than
  // hidden. ──

  const EDITABLE_KEYS = new Set([
    'bind',
    'app_hosts',
    'content_hosts',
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
    'smb.service_uid',
    'smb.service_gid',
    'search.max_concurrent_fast',
    'search.max_concurrent_slow',
    'search.walk_deadline_fast_ms',
    'search.walk_deadline_slow_ms',
    'search.rate_per_minute',
    'archive.max_concurrent',
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

  // ── restart (`POST /api/admin/server-settings/restart`) ──
  //
  // Two-step confirm, deliberately: step 1 is a plain "are you sure" with no
  // network call yet (nothing fires until the admin clicks its own confirm
  // button — a restart cannot happen from one accidental click). Only that
  // click issues the real `force:false` attempt. If the server refuses with
  // `409 restart.busy`, step 2 shows the exact counts from THAT response
  // (never a separately-polled snapshot, which could be stale by the time the
  // admin reacts to it) and asks for an explicit `force:true`.

  let restartConfirmOpen = $state(false)
  let restartBusyOpen = $state(false)
  let restartBusyCounts = $state<{ active_uploads: number; running_jobs: number } | null>(null)
  let restarting = $state(false)
  let restartError = $state<string | null>(null)
  let restartAccepted = $state(false)

  function openRestartConfirm(): void {
    restartError = null
    restartAccepted = false
    restartConfirmOpen = true
  }

  async function attemptRestart(force: boolean): Promise<void> {
    restarting = true
    restartError = null
    try {
      await api.adminRestartServer(force)
      restartAccepted = true
      restartConfirmOpen = false
      restartBusyOpen = false
    } catch (err) {
      if (err instanceof ApiError && err.code === 'restart.busy') {
        const d = err.detail
        restartBusyCounts = {
          active_uploads: typeof d?.active_uploads === 'number' ? d.active_uploads : 0,
          running_jobs: typeof d?.running_jobs === 'number' ? d.running_jobs : 0
        }
        restartConfirmOpen = false
        restartBusyOpen = true
      } else {
        restartError = describeApiError(err, t('server.could_not_send_restart_signal'))
        restartConfirmOpen = false
      }
    } finally {
      restarting = false
    }
  }

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
      smbServiceUid = String(field('smb.service_uid')?.value ?? '1000')
      smbServiceGid = String(field('smb.service_gid')?.value ?? '1000')

      searchMaxFast = String(field('search.max_concurrent_fast')?.value ?? '')
      searchMaxSlow = String(field('search.max_concurrent_slow')?.value ?? '')
      searchDeadlineFast = String(field('search.walk_deadline_fast_ms')?.value ?? '')
      searchDeadlineSlow = String(field('search.walk_deadline_slow_ms')?.value ?? '')
      searchRate = String(field('search.rate_per_minute')?.value ?? '')

      archiveMax = String(field('archive.max_concurrent')?.value ?? '')

      netBind = String(field('bind')?.value ?? '')
      netAppHosts = arrToStr(field('app_hosts')?.value)
      netContentHosts = arrToStr(field('content_hosts')?.value)
      netAllowedOrigins = arrToStr(field('allowed_origins')?.value)
      netTrustedProxies = arrToStr(field('trusted_proxies')?.value)
      netPublicOrigins = arrToStr(field('public_origins')?.value)
      netOriginsLoaded = strToArr(netPublicOrigins)

      dbSizeGuard = Boolean(field('db.size_guard')?.value)
      dbMaxMb = String(bytesToMb(Number(field('db.max_bytes')?.value ?? 0)))
      dbMinFreeMb = String(bytesToMb(Number(field('db.min_free_bytes')?.value ?? 0)))

      symlinkPolicy = (field('symlink_policy')?.value as 'deny' | 'within_share' | 'follow') ?? 'deny'

      homesEnabled = Boolean(field('homes.enabled')?.value)
      homesRoot = String(field('homes.root')?.value ?? '')

      watchBackend = (field('watch.backend')?.value as typeof watchBackend) ?? 'auto'
      watchHotSetMax = String(field('watch.hot_set_max')?.value ?? '')
      watchFullThreshold = String(field('watch.full_threshold')?.value ?? '')

      oidcEnabled = Boolean(field('oidc.enabled')?.value)
      oidcIssuer = String(field('oidc.issuer')?.value ?? '')
      oidcClientId = String(field('oidc.client_id')?.value ?? '')
      oidcRedirectUris = arrToStr(field('oidc.redirect_uris')?.value)
      oidcScopes = arrToStr(field('oidc.scopes')?.value)
      oidcDisplayName = String(field('oidc.display_name')?.value ?? '')
      oidcAllowPrivate = Boolean(field('oidc.allow_private_endpoints')?.value)

      pathsDataDir = String(field('data_dir')?.value ?? '')
      pathsMasterKeyFile = String(field('master_key_file')?.value ?? '')
      pathsSmbConfigDir = String(field('smb.config_dir')?.value ?? '')
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

    {#snippet revertButton(id: SettingsSectionId)}
      <!-- The group's name is in the visible label, not an aria-label beside
           a generic one: ten buttons reading "Revert" give a screen reader no
           way to tell the fourth from the seventh. -->
      <Button
        variant="outlined"
        disabled={!isOverridden(id) || reverting}
        onclick={() => (revertTarget = id)}
      >
        {t('server.revert_group_to_config', { group: section(id).name })}
      </Button>
    {/snippet}

    <!-- One badge per row that is waiting, grouped under the form it belongs
         to. A single global "something is pending" bit would not tell an
         administrator *which* change is waiting, which is the only useful
         part. -->
    {#snippet pendingRows(id: SettingsSectionId)}
      {@const rows = pendingFields.filter((f) => sectionIdOf(f.key) === id)}
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
    {#if revertError}<p class="sc-admin-section__error" role="alert">{revertError}</p>{/if}
    {#if revertOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(revertOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">SMB</h4>
    <div class="sc-server-settings__form">
      <Switch checked={smbEnabled} onchange={(v) => (smbEnabled = v)} label={t('server.enable_smb')} />
      <TextField label={t('server.workgroup')} bind:value={smbWorkgroup} />
      <TextField label={t('server.smb_server_name')} bind:value={smbServerName} />
      <TextField label={t('server.service_account_name')} bind:value={smbServiceUser} />
      <Switch checked={smbAllowPublicBind} onchange={(v) => (smbAllowPublicBind = v)} label={t('server.allow_access_from_outside_private')} />
      <SelectOutlined label={t('server.smb_access_2fa_users')} width="100%" options={SMB_TOTP_OPTIONS} bind:value={smbTotpPolicy} />
      <TextField label={t('server.service_account_uid')} bind:value={smbServiceUid} />
      <TextField label={t('server.service_account_gid')} bind:value={smbServiceGid} />
      <Button variant="filled" onclick={saveSmb} loading={smbSaving}>{t('common.save')}</Button>
      {@render pendingRows('smb')}
      {@render revertButton('smb')}
    </div>
    <p class="sc-admin-section__hint">
      {t('server.only_toggling_enable_smb_needs')}
    </p>
    {#if smbError}<p class="sc-admin-section__error" role="alert">{smbError}</p>{/if}
    {#if smbOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(smbOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('common.search')}</h4>
    <div class="sc-server-settings__form">
      <TextField label={t('server.concurrent_fast_searches')} bind:value={searchMaxFast} />
      <TextField label={t('server.concurrent_slow_searches')} bind:value={searchMaxSlow} />
      <TextField label={t('server.fast_search_timeout_ms')} bind:value={searchDeadlineFast} />
      <TextField label={t('server.slow_search_timeout_ms')} bind:value={searchDeadlineSlow} />
      <TextField label={t('server.requests_per_minute')} bind:value={searchRate} />
      <Button variant="filled" onclick={saveSearch} loading={searchSaving}>{t('common.save')}</Button>
      {@render revertButton('search')}
    </div>
    <p class="sc-admin-section__hint">{t('server.all_apply_immediately_no_restart')}</p>
    {#if searchError}<p class="sc-admin-section__error" role="alert">{searchError}</p>{/if}
    {#if searchOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(searchOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.zip_download')}</h4>
    <div class="sc-server-settings__form">
      <TextField label={t('server.concurrent_zip_streams')} bind:value={archiveMax} />
      <Button variant="filled" onclick={saveArchive} loading={archiveSaving}>{t('common.save')}</Button>
      {@render revertButton('archive')}
    </div>
    <p class="sc-admin-section__hint">{t('server.applies_immediately_no_restart_needed')}</p>
    {#if archiveError}<p class="sc-admin-section__error" role="alert">{archiveError}</p>{/if}
    {#if archiveOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(archiveOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.network')}</h4>
    <div class="sc-server-settings__form">
      <TextField label={t('server.bind_address_host_port')} bind:value={netBind} />
      <p class="sc-admin-section__hint">{t('server.bind_https_hint')}</p>
      <TextField label={t('server.app_hosts_comma_separated')} bind:value={netAppHosts} />
      <TextField label={t('server.content_hosts_comma_separated')} bind:value={netContentHosts} />
      <TextField label={t('server.allowed_origins_cors_comma_separated')} bind:value={netAllowedOrigins} />
      <TextField label={t('server.trusted_proxies_comma_separated')} bind:value={netTrustedProxies} />
      <TextField label={t('server.public_origins_comma_separated')} bind:value={netPublicOrigins} />
      <p class="sc-admin-section__hint">{t('server.public_origins_hint')}</p>
      {#if !strToArr(netPublicOrigins).length}
        <p class="sc-admin-section__hint">{t('server.no_public_origin_declared')}</p>
      {/if}
      {#if netUnservedOrigins.length}
        <p class="sc-admin-section__hint">
          {t('server.public_origin_not_in_app_hosts', { value: netUnservedOrigins.join(', ') })}
        </p>
      {/if}
      {@render pendingRows('network')}
      <Button variant="filled" onclick={openNetworkSave} loading={netSaving}>{t('common.save')}</Button>
      {@render revertButton('network')}
    </div>
    <p class="sc-admin-section__hint">{t('server.takes_effect_after_restart_listener')}</p>
    {#if netError}<p class="sc-admin-section__error" role="alert">{netError}</p>{/if}
    {#if netOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(netOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.database_size_guard')}</h4>
    <div class="sc-server-settings__form">
      <Switch checked={dbSizeGuard} onchange={(v) => (dbSizeGuard = v)} label={t('server.enable_size_guard')} />
      <TextField label={t('server.maximum_size_mb')} bind:value={dbMaxMb} />
      <TextField label={t('server.minimum_free_space_mb')} bind:value={dbMinFreeMb} />
      <Button variant="filled" onclick={saveDb} loading={dbSaving}>{t('common.save')}</Button>
      {@render pendingRows('db')}
      {@render revertButton('db')}
    </div>
    <p class="sc-admin-section__hint">
      {t('server.takes_effect_after_restart_currently', {
        max: formatBytes((Number(dbMaxMb) || 0) * BYTES_PER_MB),
        free: formatBytes((Number(dbMinFreeMb) || 0) * BYTES_PER_MB)
      })}
    </p>
    {#if dbError}<p class="sc-admin-section__error" role="alert">{dbError}</p>{/if}
    {#if dbOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(dbOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.symlink_policy')}</h4>
    <div class="sc-server-settings__form">
      <SelectOutlined label={t('server.policy')} width="100%" options={SYMLINK_OPTIONS} bind:value={symlinkPolicy} />
      <Button variant="filled" onclick={saveSymlinkPolicy} loading={symlinkSaving}>{t('common.save')}</Button>
      {@render pendingRows('symlink-policy')}
      {@render revertButton('symlink-policy')}
    </div>
    <p class="sc-admin-section__hint">{t('server.takes_effect_after_restart')}</p>
    {#if symlinkError}<p class="sc-admin-section__error" role="alert">{symlinkError}</p>{/if}
    {#if symlinkOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(symlinkOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.home_folders')}</h4>
    <div class="sc-server-settings__form">
      <Switch checked={homesEnabled} onchange={(v) => (homesEnabled = v)} label={t('server.enable_home_folders')} />
      <TextField label={t('server.root_path')} bind:value={homesRoot} placeholder="/srv/homes" />
      <Button variant="filled" onclick={saveHomes} loading={homesSaving}>{t('common.save')}</Button>
      {@render pendingRows('homes')}
      {@render revertButton('homes')}
    </div>
    <p class="sc-admin-section__hint">{t('server.takes_effect_after_restart')}</p>
    {#if homesError}<p class="sc-admin-section__error" role="alert">{homesError}</p>{/if}
    {#if homesOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(homesOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.file_watching')}</h4>
    <p class="sc-admin-section__hint">{t('server.what_file_watching_is_for')}</p>
    <div class="sc-server-settings__form">
      <SelectOutlined label={t('server.watch_mode')} width="100%" options={WATCH_BACKEND_OPTIONS} bind:value={watchBackend} />
      <TextField label={t('server.maximum_folders_watched_at_once')} bind:value={watchHotSetMax} />
      <TextField label={t('server.changes_before_a_full_rescan')} bind:value={watchFullThreshold} />
      <Button variant="filled" onclick={saveWatch} loading={watchSaving}>{t('common.save')}</Button>
      {@render pendingRows('watch')}
      {@render revertButton('watch')}
    </div>
    <p class="sc-admin-section__hint">
      {t('server.takes_effect_after_restart_when')}
    </p>
    {#if watchError}<p class="sc-admin-section__error" role="alert">{watchError}</p>{/if}
    {#if watchOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(watchOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('settings.single_sign_on')}</h4>
    <div class="sc-server-settings__form">
      <Switch checked={oidcEnabled} onchange={(v) => (oidcEnabled = v)} label={t('server.enable_single_sign')} />
      <TextField label={t('server.issuer_url')} bind:value={oidcIssuer} placeholder="https://idp.example.com/realms/main" />
      <TextField label={t('server.client_id')} bind:value={oidcClientId} />
      <TextField
        label={t('server.redirect_uris_comma_separated')}
        bind:value={oidcRedirectUris}
        placeholder="https://files.example.com/api/auth/oidc/callback"
      />
      <p class="sc-admin-section__hint">{t('server.redirect_uris_hint')}</p>
      <TextField label={t('server.scopes_comma_separated')} bind:value={oidcScopes} placeholder="openid, profile" />
      <TextField label={t('server.button_name_login_screen')} bind:value={oidcDisplayName} />
      <Switch
        checked={oidcAllowPrivate}
        onchange={(v) => (oidcAllowPrivate = v)}
        label={t('server.allow_provider_private_network_address')}
      />
      <Button variant="filled" onclick={saveOidc} loading={oidcSaving}>{t('common.save')}</Button>
      {@render pendingRows('oidc')}
      {@render revertButton('oidc')}
    </div>
    <p class="sc-admin-section__hint">
      {t('server.takes_effect_after_restart_redirect')}
    </p>
    <p class="sc-admin-section__hint">{t('server.connected_accounts_cannot_use_smb')}</p>
    {#if oidcError}<p class="sc-admin-section__error" role="alert">{oidcError}</p>{/if}
    {#if oidcOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(oidcOutcome)}</p>{/if}

    <h4 class="sc-admin-section__subhead">{t('server.storage_paths')}</h4>
    <div class="sc-server-settings__form">
      <TextField label={t('server.data_directory')} bind:value={pathsDataDir} placeholder="/var/lib/stowcloud" />
      <TextField
        label={t('server.master_key_file')}
        bind:value={pathsMasterKeyFile}
        placeholder="/var/lib/stowcloud/master.key"
      />
      <TextField label={t('server.smb_config_directory')} bind:value={pathsSmbConfigDir} placeholder="/etc/stowcloud/smb" />
      <Button variant="filled" onclick={openPathsConfirm} loading={pathsSaving}>{t('common.save')}</Button>
      {@render pendingRows('paths')}
      {@render revertButton('paths')}
    </div>
    <p class="sc-admin-section__hint">
      {t('server.takes_effect_after_restart_these')} <strong>{t('server.tell_server_where_you_have')}</strong> {t('server.they_do_not_move_server')}
    </p>
    {#if pathsError}<p class="sc-admin-section__error" role="alert">{pathsError}</p>{/if}
    {#if pathsOutcome}<p class="sc-admin-section__saved" role="status">{outcomeText(pathsOutcome)}</p>{/if}

    {#if otherFields.length}
      <h4 class="sc-admin-section__subhead">{t('server.other_edit_config_toml')}</h4>
      <p class="sc-admin-section__hint">
        {t('server.items_below_cannot_changed_here')}
      </p>
      <dl class="sc-server-settings__other">
        {#each otherFields as f (f.key)}
          <div>
            <dt>{f.key}</dt>
            <dd>
              {formatValue(f.value)}
              <span class="sc-server-settings__source">({sourceLabel(f.source)})</span>
              {#if f.running_value !== undefined}
                <br /><span class="sc-server-settings__badge">{t('server.pending_restart')}</span>
                {t('server.running_value_is', { value: formatValue(f.running_value) })}
              {/if}
              {#if f.readonly_reason_key}<br /><span class="sc-server-settings__reason">{serverKeyText(f.readonly_reason_key)}</span>{/if}
            </dd>
          </div>
        {/each}
      </dl>
    {/if}

    <h4 class="sc-admin-section__subhead">{t('server.restart_server')}</h4>
    <p class="sc-admin-section__hint">
      {t('server.if_you_have_changes_need')}
    </p>
    <Button variant="outlined" danger onclick={openRestartConfirm}>{t('server.restart_server')}</Button>
    {#if restartError}<p class="sc-admin-section__error" role="alert">{restartError}</p>{/if}
    {#if restartAccepted}
      <p class="sc-admin-section__saved" role="status">
        {t('server.restart_signal_sent_server_will')}
      </p>
    {/if}
  {/if}
</section>

<!-- Naming the values it restores, because two of them are not obvious from
     the button: removing an origin stops new enrolments on that name (the
     name keeps serving as long as it is in `app_hosts`), and changing the
     first entry changes what an unrecognised `Host` is answered with. -->
<ConfirmDialog
  open={revertTarget !== null}
  title={t('server.revert_group_to_config', { group: revertTarget ? section(revertTarget).name : '' })}
  message={`${t('server.revert_restores_these_values')}\n\n${revertTarget ? revertPreview(revertTarget) : ''}`}
  confirmLabel={t('server.revert')}
  danger
  onclose={() => (revertTarget = null)}
  onconfirm={confirmRevert}
/>

<ConfirmDialog
  open={netOriginsConfirmOpen}
  title={t('server.change_public_origins')}
  message={t('server.public_origin_change_effects', {
    removed: netOriginChanges.removed.join(', ') || t('server.none'),
    canonical: netOriginChanges.canonical || t('server.none')
  })}
  confirmLabel={t('common.save')}
  danger
  onclose={() => (netOriginsConfirmOpen = false)}
  onconfirm={saveNetwork}
/>

<ConfirmDialog
  open={pathsConfirmOpen}
  title={t('server.change_storage_paths')}
  message={t('server.if_data_directory_or_master')}
  confirmLabel={t('common.save')}
  danger
  onclose={() => (pathsConfirmOpen = false)}
  onconfirm={savePaths}
/>

<ConfirmDialog
  open={restartConfirmOpen}
  title={t('server.restart_server')}
  message={t('server.restarting_server_interrupts_service_briefly')}
  confirmLabel={t('server.restart')}
  danger
  onclose={() => (restartConfirmOpen = false)}
  onconfirm={() => attemptRestart(false)}
/>

<ConfirmDialog
  open={restartBusyOpen}
  title={t('server.work_still_progress')}
  message={t(
    'server.uploads_jobs_running_right_now',
    { uploads: restartBusyCounts?.active_uploads ?? 0, jobs: restartBusyCounts?.running_jobs ?? 0 }
  )}
  confirmLabel={t('server.restart_anyway')}
  danger
  onclose={() => (restartBusyOpen = false)}
  onconfirm={() => attemptRestart(true)}
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
