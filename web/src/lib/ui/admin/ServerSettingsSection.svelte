<script lang="ts">
  // Server settings: every operator-settable field this deployment has,
  // reachable from this one screen (`go/engine/service/settings/catalogue`).
  // There is no config file, so this is the only place a deployment is
  // configured. Most groups apply live; homes, single sign-on's provider,
  // the sandbox policy and SMB's own on/off switch used to need a restart,
  // per this batch's field decisions only `security.hardening` still does
  // (Landlock/seccomp cannot be undone by the process that installed them),
  // handled by mounting `RestartDialog` whenever a save's own outcome says so.
  //
  // `snapshot.fields` is one flat, dotted-key list keyed by section and
  // field. This component groups by an explicit key set per form below
  // (`EDITABLE_KEYS`); anything left over renders read-only at the bottom
  // with whatever reason it carries, rather than being silently dropped,
  // a field this screen cannot edit is still visible, never invisible.
  import { t } from '../../i18n'
  import { createMutation, createQuery, useQueryClient } from '@tanstack/svelte-query'
  import { adminSettingsMutation, adminSettingsQuery, adminOidcEndpointsQuery } from '../../query/admin'
  import { keys } from '../../query/keys'
  import { describeApiError, serverKeyText } from '../../api/error-text'
  import { BYTES_PER_MB, bytesToMb } from '../../format/bytes'
  import type {
    SettingsField,
    SettingsFinding,
    ApplyOutcome,
    SmbSettingsReq,
    SearchSettingsReq,
    ArchiveSettingsReq,
    NetworkSettingsReq,
    HomesSettingsReq,
    WatchSettingsReq,
    DbSettingsReq,
    OidcSettingsReq,
    ThumbnailSettingsReq
  } from '../../api/types'
  import { Icon, SelectOutlined } from 'm3-svelte'
  import { icons } from '../../icons'
  import Button from '../Button.svelte'
  import TextField from '../TextField.svelte'
  import Switch from '../Switch.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import RestartDialog from './RestartDialog.svelte'
  import PathPickerDialog from '../PathPickerDialog.svelte'

  const SMB_TOTP_OPTIONS = [
    { value: 'require_separate', text: t('server.require_separate_smb_password_default') },
    { value: 'block', text: t('server.smb_not_allowed') }
  ]

  const queryClient = useQueryClient()
  const settingsResult = createQuery(() => adminSettingsQuery())
  const snapshot = $derived(settingsResult.data ?? null)
  const loading = $derived(settingsResult.isPending)
  const loadError = $derived(settingsResult.isError ? t('server.could_not_load_server_settings') : null)

  function field(key: string): SettingsField | undefined {
    return snapshot?.fields.find((f) => f.key === key)
  }

  /**
   * The human name for a reported field.
   *
   * A key with no catalogue entry falls back to itself, which is worse than
   * a translation and better than a blank row.
   */
  //   /* i18n */ 'field.smb_config_dir'
  //   /* i18n */ 'field.smb_agent_socket'
  //   /* i18n */ 'field.security_hardening'
  //   /* i18n */ 'field.data_dir'
  //   /* i18n */ 'field.thumbnail_enabled'
  //   /* i18n */ 'field.thumbnail_dir'
  function fieldLabel(key: string): string {
    // Catalogue keys carry one dot, so a settings key's own dots become
    // underscores: `smb.config_dir` is looked up as `field.smb_config_dir`.
    const name = `field.${key.replaceAll('.', '_')}`
    const label = t(name)
    return label === name ? key : label
  }

  // What the server says an empty value means, for the fields where empty is a
  // setting rather than a gap. Nothing renders when it sent no key.
  // The keys the server can send that no call site here shows literally:
  //   /* i18n */ 'settings.empty_app_hosts_first_boot'
  //   /* i18n */ 'settings.empty_trusted_proxies'
  //   /* i18n */ 'settings.empty_disables_netbios_name'
  //   /* i18n */ 'settings.empty_default_thumbs_dir'
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
  function stringArrayField(key: string): string[] {
    const value = field(key)?.value
    return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
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
  // own, showing it adds nothing a blank list didn't already say, so it is
  // the one reason filtered out here.
  function findingsOf(o: ApplyOutcome | null): SettingsFinding[] {
    return (o?.findings ?? []).filter((f) => f.reason !== 'settings.check_passed')
  }

  // ── range validation ──
  //
  // `SettingsField.range` carries the same bounds the server checks at save
  // time. Reading it here means a value outside the bound is refused before
  // the round trip, not after, the server-side check stays, this is only
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
  /** The same bound in megabytes, for the two fields the operator enters in
   *  megabytes and the wire carries in bytes. */
  function mbRangeAttrs(key: string): { min?: number; max?: number } {
    const r = intRangeAttrs(key)
    return {
      min: r.min === undefined ? undefined : Math.ceil(r.min / BYTES_PER_MB),
      max: r.max === undefined ? undefined : Math.floor(r.max / BYTES_PER_MB)
    }
  }

  // ── moving focus to a failed save's own error ──
  //
  // `role="alert"` announces the text to a screen reader; it does not move
  // anyone's focus. A keyboard or screen-reader user who just pressed Save
  // is left exactly where they were, with no indication anything happened
  // unless they go looking. `tabindex="-1"` makes the paragraph a valid
  // focus target without adding it to the tab order, and the action moves
  // focus to it whenever the message actually changes, not on every
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
  /* i18n */ 'settings.oidc_client_secret_required'
  /* i18n */ 'settings.canonical_url_not_an_app_host'
  /* i18n */ 'settings.duplicate_host'
  /* i18n */ 'settings.host_role_conflict'
  /* i18n */ 'settings.invalid_origin'

  // ── the restart a save needs ──
  //
  // Only `security.hardening` needs one now; every other section this
  // screen edits applies live, but the outcome is checked uniformly below
  // so a future section that starts needing one is covered for free. When a
  // save's own `ApplyOutcome` reports `restart_required`, `RestartDialog`
  // (owned by `FrontendRestart`) is opened with that outcome, it offers the
  // restart itself and polls health until the process answers again.

  let restartOutcome = $state<ApplyOutcome | null>(null)
  let restartOpen = $state(false)

  // One shared picker for every server-path field on this screen, same
  // reasoning as ShareManagementSection's own `pathPicker`.
  let pathPicker = $state<{ mode: 'folder' | 'file'; start: string; onpick: (path: string) => void } | null>(null)

  function openPathPicker(mode: 'folder' | 'file', start: string, onpick: (path: string) => void): void {
    pathPicker = { mode, start, onpick }
  }

  function onSettingsSaved(outcome: ApplyOutcome): void {
    if (outcome.restart_required) {
      restartOutcome = outcome
      restartOpen = true
    }
  }

  /** A save's displayed error is either a local validation refusal (checked
   *  before the request ever went out) or the mutation's own rejection, in
   *  that order, never both at once. */
  function groupError(validation: string | null, error: unknown, fallback: string): string | null {
    if (validation) return validation
    return error ? describeApiError(error, fallback) : null
  }

  /** A validation refusal hides the previous save's outcome rather than
   * showing a stale success beside the new complaint. A successful result
   * also belongs to the values that produced it: once the operator edits
   * the draft again, only the new draft status remains visible. */
  function groupOutcome(
    validation: string | null,
    outcome: ApplyOutcome | undefined,
    dirty = false
  ): ApplyOutcome | null {
    if (validation) return null
    if (!outcome) return null
    if (dirty && (outcome.stored || outcome.applied || outcome.restart_required)) return null
    return outcome
  }

  type SettingsGroup = 'smb' | 'search' | 'thumbnail' | 'archive' | 'network' | 'db' | 'homes' | 'watch' | 'rate' | 'oidc'

  // The query is shared by every card, but each card owns its own loaded
  // baseline and draft. A save records the submitted draft, then waits for
  // the first query revision after the successful mutation completion. This
  // keeps an older shared-query response from rolling a saved group back
  // while allowing the server to normalize the post-save snapshot.
  type SettingsSaveState = {
    submitted: string
    queryRevision: number | null
    accepted: boolean
  }

  let baselines = $state<Record<SettingsGroup, string | null>>({
    smb: null,
    search: null,
    thumbnail: null,
    archive: null,
    network: null,
    db: null,
    homes: null,
    watch: null,
    rate: null,
    oidc: null
  })
  let hydrated = $state<Record<SettingsGroup, boolean>>({
    smb: false,
    search: false,
    thumbnail: false,
    archive: false,
    network: false,
    db: false,
    homes: false,
    watch: false,
    rate: false,
    oidc: false
  })
  let settingsSaveState = $state<Record<SettingsGroup, SettingsSaveState | null>>({
    smb: null,
    search: null,
    thumbnail: null,
    archive: null,
    network: null,
    db: null,
    homes: null,
    watch: null,
    rate: null,
    oidc: null
  })
  // ── SMB ──

  let smbEnabled = $state(false)
  let smbWorkgroup = $state('')
  let smbServerName = $state('')
  let smbServiceUser = $state('')
  let smbAllowPublicBind = $state(false)
  let smbTotpPolicy = $state<'require_separate' | 'block'>('require_separate')
  let smbServiceGid = $state('')
  let smbInterfaces = $state('')
  let smbValidationError = $state<string | null>(null)
  const smbMutation = createMutation(() => adminSettingsMutation())
  const smbError = $derived(groupError(smbValidationError, smbMutation.error, t('server.could_not_save_smb_settings')))
  const smbOutcome = $derived(groupOutcome(smbValidationError, smbMutation.data, isSettingsDirty('smb')))

  function saveSmb(): void {
    smbValidationError = null
    smbMutation.reset()
    const gid = Number(smbServiceGid)
    if (!Number.isInteger(gid) || gid < 0) {
      smbValidationError = t('server.gid_must_integer_0')
      return
    }
    if (!smbWorkgroup.trim() || !smbServiceUser.trim() || !smbServerName.trim()) {
      smbValidationError = t('server.enter_workgroup_server_name_service_account')
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
    beginSettingsSave('smb', req)
    smbMutation.mutate(
      { section: 'smb', req },
      {
        onSuccess: (outcome) => finishSettingsSave('smb', req, outcome),
        onError: () => failSettingsSave('smb')
      }
    )
  }

  // ── search ── (both fields fully live)

  let searchMaxFast = $state('')
  let searchDeadlineFast = $state('')
  let searchValidationError = $state<string | null>(null)
  const searchMutation = createMutation(() => adminSettingsMutation())
  const searchError = $derived(groupError(searchValidationError, searchMutation.error, t('server.could_not_save_search_settings')))
  const searchOutcome = $derived(groupOutcome(searchValidationError, searchMutation.data, isSettingsDirty('search')))

  function saveSearch(): void {
    searchValidationError = null
    searchMutation.reset()
    const maxErr = rangeError('search.max_concurrent_fast', searchMaxFast)
    const deadlineErr = rangeError('search.walk_deadline_fast_ms', searchDeadlineFast)
    if (maxErr || deadlineErr) {
      searchValidationError = maxErr ?? deadlineErr
      return
    }
    const req: SearchSettingsReq = {
      max_concurrent_fast: Number(searchMaxFast),
      walk_deadline_fast_ms: Number(searchDeadlineFast)
    }
    beginSettingsSave('search', req)
    searchMutation.mutate(
      { section: 'search', req },
      {
        onSuccess: (outcome) => finishSettingsSave('search', req, outcome),
        onError: () => failSettingsSave('search')
      }
    )
  }

  // ── thumbnails ──

  let thumbnailEnabled = $state(true)
  let thumbnailDir = $state('')
  let thumbnailValidationError = $state<string | null>(null)
  const thumbnailMutation = createMutation(() => adminSettingsMutation())
  const thumbnailError = $derived(
    groupError(thumbnailValidationError, thumbnailMutation.error, t('server.could_not_save_thumbnail_settings'))
  )
  const thumbnailOutcome = $derived(groupOutcome(thumbnailValidationError, thumbnailMutation.data, isSettingsDirty('thumbnail')))

  function saveThumbnail(): void {
    thumbnailValidationError = null
    thumbnailMutation.reset()
    const req: ThumbnailSettingsReq = {
      enabled: thumbnailEnabled,
      dir: thumbnailDir.trim() || undefined
    }
    beginSettingsSave('thumbnail', req)
    thumbnailMutation.mutate(
      { section: 'thumbnail', req },
      {
        onSuccess: (outcome) => finishSettingsSave('thumbnail', req, outcome),
        onError: () => failSettingsSave('thumbnail')
      }
    )
  }

  // ── archive ──

  let archiveMax = $state('')
  let archiveValidationError = $state<string | null>(null)
  const archiveMutation = createMutation(() => adminSettingsMutation())
  const archiveError = $derived(groupError(archiveValidationError, archiveMutation.error, t('server.could_not_save_archive_settings')))
  const archiveOutcome = $derived(groupOutcome(archiveValidationError, archiveMutation.data, isSettingsDirty('archive')))

  function saveArchive(): void {
    archiveValidationError = null
    archiveMutation.reset()
    const rerr = rangeError('archive.max_concurrent', archiveMax)
    if (rerr) {
      archiveValidationError = rerr
      return
    }
    const req: ArchiveSettingsReq = { max_concurrent: Number(archiveMax) }
    beginSettingsSave('archive', req)
    archiveMutation.mutate(
      { section: 'archive', req },
      {
        onSuccess: (outcome) => finishSettingsSave('archive', req, outcome),
        onError: () => failSettingsSave('archive')
      }
    )
  }

  // ── network ──

  let netAppHosts = $state('')
  let netContentHosts = $state('')
  let netAllowedOrigins = $state('')
  let netCanonicalUrl = $state('')
  let netTrustedProxies = $state('')
  let netBind = $state('')
  let netValidationError = $state<string | null>(null)
  const netMutation = createMutation(() => adminSettingsMutation())
  const netError = $derived(groupError(netValidationError, netMutation.error, t('server.could_not_save_network_settings')))
  const netOutcome = $derived(groupOutcome(netValidationError, netMutation.data, isSettingsDirty('network')))

  function saveNetwork(): void {
    netValidationError = null
    netMutation.reset()
    const hosts = strToArr(netAppHosts)
    if (hosts.length === 0) {
      // The host list is the origin check. An empty one is a server that
      // admits nothing, and it is refused here rather than saved and
      // discovered on the next request.
      netValidationError = t('server.enter_at_least_one_app_host')
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
    beginSettingsSave('network', req)
    netMutation.mutate(
      { section: 'network', req },
      {
        onSuccess: (outcome) => finishSettingsSave('network', req, outcome),
        onError: () => failSettingsSave('network')
      }
    )
  }

  // ── the hop this request arrived over ──
  //
  // `snapshot.hop` is what the server observed about the very request that
  // fetched the settings, alongside the trusted-proxies list it is judged
  // against. Absent on a build too old to send it.
  const hop = $derived(snapshot?.hop ?? null)
  const hopAddress = $derived(hop?.peer ?? hop?.client ?? '')

  /** A bare address is not a valid `trusted_proxies` entry, the field is a
   *  CIDR list, and the mock's own validator (mirroring the server's)
   *  refuses anything without a `/`. Widening to a single-address range
   *  keeps the filled value something Save can actually accept. */
  function asCidr(address: string): string {
    return address.includes(':') ? `${address}/128` : `${address}/32`
  }

  /** Fills, never saves: the operator still reviews and presses Save, same
   *  as every other field on this screen. */
  function addObservedAddressToTrusted(): void {
    if (!hopAddress) return
    const entry = asCidr(hopAddress)
    const current = strToArr(netTrustedProxies)
    if (current.includes(entry)) return
    netTrustedProxies = [...current, entry].join(', ')
  }

  // ── database size guard ──

  let dbSizeGuard = $state(false)
  let dbMaxBytesMb = $state('')
  let dbMinFreeBytesMb = $state('')
  let dbValidationError = $state<string | null>(null)
  const dbMutation = createMutation(() => adminSettingsMutation())
  const dbError = $derived(groupError(dbValidationError, dbMutation.error, t('server.could_not_save_db_settings')))
  const dbOutcome = $derived(groupOutcome(dbValidationError, dbMutation.data, isSettingsDirty('db')))

  function saveDb(): void {
    dbValidationError = null
    dbMutation.reset()
    const maxMb = Number(dbMaxBytesMb)
    const minMb = Number(dbMinFreeBytesMb)
    if (!Number.isFinite(maxMb) || maxMb < 0 || !Number.isFinite(minMb) || minMb < 0) {
      dbValidationError = t('server.every_value_must_integer_0')
      return
    }
    const req: DbSettingsReq = {
      size_guard: dbSizeGuard,
      max_bytes: Math.round(maxMb * BYTES_PER_MB),
      min_free_bytes: Math.round(minMb * BYTES_PER_MB)
    }
    beginSettingsSave('db', req)
    dbMutation.mutate(
      { section: 'db', req },
      {
        onSuccess: (outcome) => finishSettingsSave('db', req, outcome),
        onError: () => failSettingsSave('db')
      }
    )
  }

  // ── home folders ──

  let homesEnabled = $state(false)
  let homesRoot = $state('')
  let homesValidationError = $state<string | null>(null)
  const homesMutation = createMutation(() => adminSettingsMutation())
  const homesError = $derived(groupError(homesValidationError, homesMutation.error, t('server.could_not_save_home_folder')))
  const homesOutcome = $derived(groupOutcome(homesValidationError, homesMutation.data, isSettingsDirty('homes')))

  function saveHomes(): void {
    homesValidationError = null
    homesMutation.reset()
    if (homesEnabled && !homesRoot.trim()) {
      homesValidationError = t('server.enter_root_path_enable_home')
      return
    }
    const req: HomesSettingsReq = { enabled: homesEnabled, root: homesRoot.trim() || null }
    beginSettingsSave('homes', req)
    homesMutation.mutate(
      { section: 'homes', req },
      {
        onSuccess: (outcome) => finishSettingsSave('homes', req, outcome),
        onError: () => failSettingsSave('homes')
      }
    )
  }

  // ── file watching ── (both bounds fully live)

  let watchHotSetMax = $state('')
  let watchFullThreshold = $state('')
  let watchValidationError = $state<string | null>(null)
  const watchMutation = createMutation(() => adminSettingsMutation())
  const watchError = $derived(groupError(watchValidationError, watchMutation.error, t('server.could_not_save_file_watch')))
  const watchOutcome = $derived(groupOutcome(watchValidationError, watchMutation.data, isSettingsDirty('watch')))

  function saveWatch(): void {
    watchValidationError = null
    watchMutation.reset()
    const hot = Number(watchHotSetMax)
    const full = Number(watchFullThreshold)
    if (!Number.isInteger(hot) || hot < 1 || !Number.isInteger(full) || full < 1) {
      watchValidationError = t('server.watch_limit_must_integer_1')
      return
    }
    const req: WatchSettingsReq = { hot_set_max: hot, full_threshold: full }
    beginSettingsSave('watch', req)
    watchMutation.mutate(
      { section: 'watch', req },
      {
        onSuccess: (outcome) => finishSettingsSave('watch', req, outcome),
        onError: () => failSettingsSave('watch')
      }
    )
  }

  // ── request rate ──

  let ratePerSec = $state('')
  let rateBurst = $state('')
  let rateValidationError = $state<string | null>(null)
  const rateMutation = createMutation(() => adminSettingsMutation())
  const rateError = $derived(groupError(rateValidationError, rateMutation.error, t('server.could_not_save_rate_settings')))
  const rateOutcome = $derived(groupOutcome(rateValidationError, rateMutation.data, isSettingsDirty('rate')))

  function saveRate(): void {
    rateValidationError = null
    rateMutation.reset()
    const perSec = Number(ratePerSec)
    const burst = Number(rateBurst)
    // Zero is not a low limit, it is an off switch: every request from every
    // visitor answers 429 the moment it applies. The server refuses it too.
    if (!Number.isInteger(perSec) || perSec < 1 || !Number.isInteger(burst) || burst < 1) {
      rateValidationError = t('server.must_integer_1_or_more')
      return
    }
    const req = { per_sec: perSec, burst }
    beginSettingsSave('rate', req)
    rateMutation.mutate(
      { section: 'rate', req },
      {
        onSuccess: (outcome) => finishSettingsSave('rate', req, outcome),
        onError: () => failSettingsSave('rate')
      }
    )
  }

  // ── single sign-on (OIDC) ──
  //
  // Applies live: the provider is rebuilt the next time settings load,
  // which the mutation's own invalidation triggers itself. `client_secret`
  // is write-only, the server never echoes it back, so an empty box here
  // means "leave the stored one alone", not "there is none".

  let oidcEnabled = $state(false)
  let oidcIssuer = $state('')
  let oidcClientId = $state('')
  let oidcClientSecret = $state('')
  let oidcScopes = $state('')
  let oidcDisplayName = $state('')
  let oidcAllowPrivateEndpoints = $state(false)
  let oidcCaCertFile = $state('')
  let oidcPublicClient = $state(false)
  let oidcValidationError = $state<string | null>(null)
  const oidcMutation = createMutation(() => adminSettingsMutation())
  const oidcError = $derived(groupError(oidcValidationError, oidcMutation.error, t('server.could_not_save_oidc_settings')))
  const oidcOutcome = $derived(groupOutcome(oidcValidationError, oidcMutation.data, isSettingsDirty('oidc')))

  function saveOidc(): void {
    oidcValidationError = null
    oidcMutation.reset()
    if (oidcEnabled && (!oidcIssuer.trim() || !oidcClientId.trim())) {
      oidcValidationError = t('settings.oidc_issuer_client_required')
      return
    }
    const req: OidcSettingsReq & { client_secret?: string } = {
      enabled: oidcEnabled,
      issuer: oidcIssuer.trim(),
      client_id: oidcClientId.trim(),
      scopes: strToArr(oidcScopes),
      display_name: oidcDisplayName,
      allow_private_endpoints: oidcAllowPrivateEndpoints,
      ca_cert_file: oidcCaCertFile.trim(),
      public_client: oidcPublicClient,
      smb_policy: 'block'
    }
    if (oidcClientSecret.trim()) req.client_secret = oidcClientSecret.trim()
    const visibleReq: OidcSettingsReq = {
      enabled: req.enabled,
      issuer: req.issuer,
      client_id: req.client_id,
      scopes: req.scopes,
      display_name: req.display_name,
      allow_private_endpoints: req.allow_private_endpoints,
      ca_cert_file: req.ca_cert_file,
      public_client: req.public_client,
      smb_policy: req.smb_policy
    }
    beginSettingsSave('oidc', visibleReq)
    oidcMutation.mutate(
      { section: 'oidc', req },
      {
        onSuccess: (outcome) => {
          const accepted = finishSettingsSave('oidc', visibleReq, outcome)
          if (accepted) oidcClientSecret = ''
        },
        onError: () => failSettingsSave('oidc')
      }
    )
  }

  // ── addition A/B: the two strings an operator registers at the provider ──
  //
  // Read-only, derived from `app_hosts`, never admin-entered: the same
  // reasoning as `Redirect URIs (comma-separated)` being removed above.
  // Shown on this card because both have to be registered where the client
  // is configured, not on the account-side card that merely links to it.
  const oidcEndpoints = createQuery(() => adminOidcEndpointsQuery())
  let oidcEndpointsAnnouncement = $state('')

  async function copyEndpoint(text: string, name: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(text)
      oidcEndpointsAnnouncement = t('settings.oidc_endpoint_copied', { name })
    } catch {
      // clipboard API unavailable: the value is still selectable as text
    }
  }

  function settingsFingerprint(value: unknown): string {
    return JSON.stringify(value)
  }

  function currentSettingsRequest(group: SettingsGroup): unknown {
    switch (group) {
      case 'smb':
        return {
          enabled: smbEnabled,
          workgroup: smbWorkgroup,
          server_name: smbServerName,
          service_user: smbServiceUser,
          allow_public_bind: smbAllowPublicBind,
          totp_policy: smbTotpPolicy,
          service_gid: Number(smbServiceGid),
          interfaces: strToArr(smbInterfaces)
        } satisfies SmbSettingsReq
      case 'search':
        return {
          max_concurrent_fast: Number(searchMaxFast),
          walk_deadline_fast_ms: Number(searchDeadlineFast)
        } satisfies SearchSettingsReq
      case 'thumbnail':
        return { enabled: thumbnailEnabled, dir: thumbnailDir.trim() || undefined } satisfies ThumbnailSettingsReq
      case 'archive':
        return { max_concurrent: Number(archiveMax) } satisfies ArchiveSettingsReq
      case 'network':
        return {
          app_hosts: strToArr(netAppHosts),
          content_hosts: strToArr(netContentHosts),
          allowed_origins: strToArr(netAllowedOrigins),
          compat_canonical_url: netCanonicalUrl.trim(),
          trusted_proxies: strToArr(netTrustedProxies),
          bind: netBind.trim() || undefined
        } satisfies NetworkSettingsReq
      case 'db':
        return {
          size_guard: dbSizeGuard,
          max_bytes: Math.round(Number(dbMaxBytesMb) * BYTES_PER_MB),
          min_free_bytes: Math.round(Number(dbMinFreeBytesMb) * BYTES_PER_MB)
        } satisfies DbSettingsReq
      case 'homes':
        return { enabled: homesEnabled, root: homesRoot.trim() || null } satisfies HomesSettingsReq
      case 'watch':
        return { hot_set_max: Number(watchHotSetMax), full_threshold: Number(watchFullThreshold) } satisfies WatchSettingsReq
      case 'rate':
        return { per_sec: Number(ratePerSec), burst: Number(rateBurst) }
      case 'oidc':
        return {
          enabled: oidcEnabled,
          issuer: oidcIssuer.trim(),
          client_id: oidcClientId.trim(),
          scopes: strToArr(oidcScopes),
          display_name: oidcDisplayName,
          allow_private_endpoints: oidcAllowPrivateEndpoints,
          ca_cert_file: oidcCaCertFile.trim(),
          public_client: oidcPublicClient,
          smb_policy: 'block'
        } satisfies OidcSettingsReq
    }
  }

  function snapshotSettingsRequest(group: SettingsGroup): unknown {
    switch (group) {
      case 'smb':
        return {
          enabled: Boolean(field('smb.enabled')?.value),
          workgroup: String(field('smb.workgroup')?.value ?? ''),
          server_name: String(field('smb.server_name')?.value ?? ''),
          service_user: String(field('smb.service_user')?.value ?? ''),
          allow_public_bind: Boolean(field('smb.allow_public_bind')?.value),
          totp_policy: (field('smb.totp_policy')?.value as 'require_separate' | 'block') ?? 'require_separate',
          service_gid: Number(field('smb.service_gid')?.value ?? 1000),
          interfaces: stringArrayField('smb.interfaces')
        } satisfies SmbSettingsReq
      case 'search':
        return {
          max_concurrent_fast: Number(field('search.max_concurrent_fast')?.value ?? 0),
          walk_deadline_fast_ms: Number(field('search.walk_deadline_fast_ms')?.value ?? 0)
        } satisfies SearchSettingsReq
      case 'thumbnail':
        return {
          enabled: field('thumbnail.enabled')?.value !== false,
          dir: String(field('thumbnail.dir')?.value ?? '').trim() || undefined
        } satisfies ThumbnailSettingsReq
      case 'archive':
        return { max_concurrent: Number(field('archive.max_concurrent')?.value ?? 0) } satisfies ArchiveSettingsReq
      case 'network':
        return {
          app_hosts: stringArrayField('app_hosts'),
          content_hosts: stringArrayField('content_hosts'),
          allowed_origins: stringArrayField('allowed_origins'),
          compat_canonical_url: String(field('compat_canonical_url')?.value ?? '').trim(),
          trusted_proxies: stringArrayField('trusted_proxies'),
          bind: String(field('bind')?.value ?? '').trim() || undefined
        } satisfies NetworkSettingsReq
      case 'db':
        return {
          size_guard: Boolean(field('db.size_guard')?.value),
          max_bytes: Number(field('db.max_bytes')?.value ?? 0),
          min_free_bytes: Number(field('db.min_free_bytes')?.value ?? 0)
        } satisfies DbSettingsReq
      case 'homes':
        return {
          enabled: Boolean(field('homes.enabled')?.value),
          root: String(field('homes.root')?.value ?? '').trim() || null
        } satisfies HomesSettingsReq
      case 'watch':
        return {
          hot_set_max: Number(field('watch.hot_set_max')?.value ?? 0),
          full_threshold: Number(field('watch.full_threshold')?.value ?? 0)
        } satisfies WatchSettingsReq
      case 'rate':
        return {
          per_sec: Number(field('rate.per_sec')?.value ?? 0),
          burst: Number(field('rate.burst')?.value ?? 0)
        }
      case 'oidc':
        return {
          enabled: Boolean(field('oidc.enabled')?.value),
          issuer: String(field('oidc.issuer')?.value ?? '').trim(),
          client_id: String(field('oidc.client_id')?.value ?? '').trim(),
          scopes: stringArrayField('oidc.scopes'),
          display_name: String(field('oidc.display_name')?.value ?? ''),
          allow_private_endpoints: Boolean(field('oidc.allow_private_endpoints')?.value),
          ca_cert_file: String(field('oidc.ca_cert_file')?.value ?? '').trim(),
          public_client: Boolean(field('oidc.public_client')?.value),
          smb_policy: 'block'
        } satisfies OidcSettingsReq
    }
  }

  function settingsQueryRevision(): number {
    return queryClient.getQueryState(keys.adminSettings())?.dataUpdateCount ?? 0
  }

  function isSettingsDirty(group: SettingsGroup): boolean {
    return hydrated[group] && baselines[group] !== settingsFingerprint(currentSettingsRequest(group))
  }

  function hydrateSettingsGroup(group: SettingsGroup, apply: () => void): void {
    const values = snapshotSettingsRequest(group)
    const fingerprint = settingsFingerprint(values)
    const save = settingsSaveState[group]
    if (save !== null) {
      // Do not hydrate while the mutation is still pending, or from the
      // snapshot that was current when the save completed. An active query is
      // invalidated after the success callback, so the first newer revision
      // is the post-save snapshot even when the server normalized values.
      if (!save.accepted || save.queryRevision === null || settingsQueryRevision() <= save.queryRevision) return
      settingsSaveState[group] = null
      baselines[group] = fingerprint

      // A post-submit edit owns this group's controls. Record the server
      // snapshot as the new baseline, but never replace that edit.
      if (settingsFingerprint(currentSettingsRequest(group)) !== save.submitted) {
        hydrated[group] = true
        return
      }
      apply()
      hydrated[group] = true
      return
    }
    if (hydrated[group] && isSettingsDirty(group)) return
    baselines[group] = fingerprint
    apply()
    hydrated[group] = true
  }

  function beginSettingsSave(group: SettingsGroup, request: unknown): void {
    settingsSaveState[group] = {
      submitted: settingsFingerprint(request),
      queryRevision: null,
      accepted: false
    }
  }

  function finishSettingsSave(group: SettingsGroup, request: unknown, outcome: ApplyOutcome): boolean {
    const accepted = outcome.stored || outcome.applied || outcome.restart_required
    const submitted = settingsFingerprint(request)
    if (accepted) {
      settingsSaveState[group] = {
        submitted,
        queryRevision: settingsQueryRevision(),
        accepted: true
      }
      baselines[group] = submitted
    } else {
      settingsSaveState[group] = null
    }
    onSettingsSaved(outcome)
    return accepted
  }

  function failSettingsSave(group: SettingsGroup): void {
    settingsSaveState[group] = null
  }

  function scrollToServerCard(id: string): void {
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }

  // ── everything else this screen doesn't have a dedicated control for,
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
    'oidc.scopes',
    'oidc.display_name',
    'oidc.allow_private_endpoints',
    'oidc.ca_cert_file',
    'oidc.public_client',
    'thumbnail.enabled',
    'thumbnail.dir'
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

  // Hydrate each card only while it is clean. A save invalidates this shared
  // query, so assigning every input from every response would replace text
  // the operator is currently editing in a different card.
  $effect(() => {
    if (!snapshot) return
    hydrateSettingsGroup('smb', () => {
      smbEnabled = Boolean(field('smb.enabled')?.value)
      smbWorkgroup = String(field('smb.workgroup')?.value ?? '')
      smbServerName = String(field('smb.server_name')?.value ?? '')
      smbServiceUser = String(field('smb.service_user')?.value ?? '')
      smbAllowPublicBind = Boolean(field('smb.allow_public_bind')?.value)
      smbTotpPolicy = (field('smb.totp_policy')?.value as 'require_separate' | 'block') ?? 'require_separate'
      smbServiceGid = String(field('smb.service_gid')?.value ?? '1000')
      smbInterfaces = arrToStr(field('smb.interfaces')?.value)
    })
    hydrateSettingsGroup('search', () => {
      searchMaxFast = String(field('search.max_concurrent_fast')?.value ?? '')
      searchDeadlineFast = String(field('search.walk_deadline_fast_ms')?.value ?? '')
    })
    hydrateSettingsGroup('archive', () => {
      archiveMax = String(field('archive.max_concurrent')?.value ?? '')
    })
    hydrateSettingsGroup('thumbnail', () => {
      thumbnailEnabled = field('thumbnail.enabled')?.value !== false
      thumbnailDir = String(field('thumbnail.dir')?.value ?? '')
    })
    hydrateSettingsGroup('rate', () => {
      ratePerSec = String(field('rate.per_sec')?.value ?? '')
      rateBurst = String(field('rate.burst')?.value ?? '')
    })
    hydrateSettingsGroup('network', () => {
      netAppHosts = arrToStr(field('app_hosts')?.value)
      netContentHosts = arrToStr(field('content_hosts')?.value)
      netAllowedOrigins = arrToStr(field('allowed_origins')?.value)
      netCanonicalUrl = String(field('compat_canonical_url')?.value ?? '')
      netTrustedProxies = arrToStr(field('trusted_proxies')?.value)
      netBind = String(field('bind')?.value ?? '')
    })
    hydrateSettingsGroup('db', () => {
      dbSizeGuard = Boolean(field('db.size_guard')?.value)
      dbMaxBytesMb = String(bytesToMb(Number(field('db.max_bytes')?.value ?? 0)))
      dbMinFreeBytesMb = String(bytesToMb(Number(field('db.min_free_bytes')?.value ?? 0)))
    })
    hydrateSettingsGroup('homes', () => {
      homesEnabled = Boolean(field('homes.enabled')?.value)
      homesRoot = String(field('homes.root')?.value ?? '')
    })
    hydrateSettingsGroup('watch', () => {
      watchHotSetMax = String(field('watch.hot_set_max')?.value ?? '')
      watchFullThreshold = String(field('watch.full_threshold')?.value ?? '')
    })
    hydrateSettingsGroup('oidc', () => {
      oidcEnabled = Boolean(field('oidc.enabled')?.value)
      oidcIssuer = String(field('oidc.issuer')?.value ?? '')
      oidcClientId = String(field('oidc.client_id')?.value ?? '')
      oidcScopes = arrToStr(field('oidc.scopes')?.value)
      oidcDisplayName = String(field('oidc.display_name')?.value ?? '')
      oidcAllowPrivateEndpoints = Boolean(field('oidc.allow_private_endpoints')?.value)
      oidcCaCertFile = String(field('oidc.ca_cert_file')?.value ?? '')
      oidcPublicClient = Boolean(field('oidc.public_client')?.value)
    })
  })
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
{#snippet saveStatus(group: SettingsGroup, pending: boolean, error: string | null, outcome: ApplyOutcome | null)}
  {#if pending}
    <p class="sc-admin-section__status" role="status">{t('common.saving')}</p>
  {:else if outcome && !outcome.stored && !outcome.applied && !outcome.restart_required}
    <p class="sc-admin-section__status sc-admin-section__status--error" role="status">{t('common.could_not_save')}</p>
  {:else if isSettingsDirty(group)}
    <p class="sc-admin-section__status" role="status">{t('settings.unsaved_changes')}</p>
  {:else if outcome}
    <p class="sc-admin-section__status" role="status">{outcomeText(outcome)}</p>
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
    <nav class="sc-server-settings__nav" aria-label={t('admin.server_settings_navigation')}>
      <span class="sc-sr-only">{t('admin.server_settings_navigation')}</span>
      <div class="sc-server-settings__nav-items">
        <button type="button" onclick={() => scrollToServerCard('server-smb')}>{t('admin.server_smb')}</button>
        <button type="button" onclick={() => scrollToServerCard('server-search')}>{t('admin.server_search')}</button>
        <button type="button" onclick={() => scrollToServerCard('server-network')}>{t('admin.server_network')}</button>
        <button type="button" onclick={() => scrollToServerCard('server-transfers')}>{t('admin.server_transfers')}</button>
        <button type="button" onclick={() => scrollToServerCard('server-security')}>{t('admin.server_security')}</button>
        <button type="button" onclick={() => scrollToServerCard('server-storage')}>{t('admin.server_storage_paths')}</button>
      </div>
    </nav>
    {#if snapshot.smb_public_bind_warning}
      <p class="sc-admin-section__warning" role="alert">
        {t('server.smb_reachable_from_outside_private')}
      </p>
    {/if}

    <!-- Permissions SMB cannot express, so it grants more than this screen
         says. Listed per grant rather than as one sentence: an admin has to
         know which share and which account to go and look at.
         The key comes from the server (`SmbOvergrant::kind_key`), so the
         extractor cannot see it at the call site below: these are the two
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
    <div class="sc-admin-card" id="server-smb">
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
        <Button variant="filled" onclick={saveSmb} loading={smbMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('smb', smbMutation.isPending, smbError, smbOutcome)}
        {#if smbError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={smbError}>{smbError}</p>{/if}
        {#if smbOutcome}
          {@render findingsList(smbOutcome)}
        {/if}
      </div>
      <!-- What the agent beside smbd did with the files this server rendered.
           The key comes from the server, so the extractor cannot see it at the
           call site:
                        /* i18n */ 'smb.agent_applied'
                        /* i18n */ 'smb.agent_applied_with_warnings'
                        /* i18n */ 'smb.agent_daemon_failed'
                        /* i18n */ 'smb.agent_problem'
                        /* i18n */ 'smb.agent_unreachable' -->
      {#if snapshot.smb_agent}
        {@const agent = snapshot.smb_agent}
        <!-- Each list is read through `?? []`: a server that sends `null` for
             an empty one would otherwise take this whole section down on
             `.length`, and a settings screen that throws while rendering is a
             tab that spins forever. -->
        {@const shares = agent.shares ?? []}
        {@const missingPaths = agent.missing_paths ?? []}
        {@const missingPassdb = agent.missing_passdb ?? []}
        <div
          class={agent.ok ? 'sc-admin-section__hint' : 'sc-admin-section__warning'}
          role={agent.ok ? 'status' : 'alert'}
        >
          <p>
            {t(agent.key, {
              shares: shares.length,
              interfaces: agent.interfaces,
              smbd: agent.smbd,
              error: agent.detail ?? ''
            })}
          </p>
          {#if missingPaths.length}
            <p>{t('smb.agent_missing_paths', { paths: missingPaths.join(', ') })}</p>
          {/if}
          {#if missingPassdb.length}
            <p>{t('smb.agent_missing_passdb', { users: missingPassdb.join(', ') })}</p>
          {/if}
          {#if agent.detail && !agent.ok}
            <p class="sc-server-settings__agent-detail">{agent.detail}</p>
          {/if}
        </div>
      {/if}
    </div>

    <!-- 2. Search -->
    <div class="sc-admin-card" id="server-search">
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
        <Button variant="filled" onclick={saveSearch} loading={searchMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('search', searchMutation.isPending, searchError, searchOutcome)}
        {#if searchError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={searchError}>{searchError}</p>{/if}
        {#if searchOutcome}
          {@render findingsList(searchOutcome)}
        {/if}
      </div>
    </div>

    <!-- 3. Zip download -->
    <div class="sc-admin-card" id="server-transfers">
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
        <Button variant="filled" onclick={saveArchive} loading={archiveMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('archive', archiveMutation.isPending, archiveError, archiveOutcome)}
        {#if archiveError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={archiveError}>{archiveError}</p>{/if}
        {#if archiveOutcome}
          {@render findingsList(archiveOutcome)}
        {/if}
      </div>
    </div>

    <!-- Thumbnails -->
    <div class="sc-admin-card">
      <div class="sc-admin-card-head">
        <div class="sc-admin-card-icon">
          <Icon icon={icons.image} size={20} />
        </div>
        <div class="sc-admin-card-meta">
          <h4 class="sc-admin-card-title">{t('server.thumbnail_settings')}</h4>
          <p class="sc-admin-card-subtitle">{t('server.thumbnail_enabled_description')}</p>
        </div>
      </div>
      <div class="sc-server-settings__form">
        <Switch checked={thumbnailEnabled} onchange={(v) => (thumbnailEnabled = v)} label={t('server.thumbnail_enabled')} />
        <div class="sc-server-settings__path-row">
          <TextField label={t('server.thumbnail_storage_dir')} bind:value={thumbnailDir} />
          <Button variant="outlined" onclick={() => openPathPicker('folder', thumbnailDir, (path) => (thumbnailDir = path))}>
            {#snippet icon()}<Icon icon={icons.folder} size={18} />{/snippet}
            {t('picker.browse_folder')}
          </Button>
        </div>
        <p class="sc-admin-section__hint">{t('server.thumbnail_storage_dir_description')}</p>
        <Button variant="filled" onclick={saveThumbnail} loading={thumbnailMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('thumbnail', thumbnailMutation.isPending, thumbnailError, thumbnailOutcome)}
        {#if thumbnailError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={thumbnailError}>{thumbnailError}</p>{/if}
        {#if thumbnailOutcome}
          {@render findingsList(thumbnailOutcome)}
        {/if}
      </div>
    </div>

    <!-- 4. Network -->
    <div class="sc-admin-card" id="server-network">
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
        {#if hop}
          <p class="sc-admin-section__hint">
            {t('server.requests_arriving_from', { address: hopAddress || t('server.unknown_address') })}
            {hop.peer_trusted ? t('server.peer_trusted') : t('server.peer_not_trusted')}
          </p>
          {#if !hop.peer_trusted && hop.forwarded_seen}
            <div class="sc-admin-section__warning" role="alert">
              <p>{t('server.forwarding_headers_ignored_hint', { address: hopAddress })}</p>
              <Button variant="text" onclick={addObservedAddressToTrusted} disabled={!hopAddress}>
                {t('server.add_observed_address_to_trusted', { address: hopAddress })}
              </Button>
            </div>
          {/if}
        {/if}
        <TextField label={t('server.bind_address')} bind:value={netBind} />
        <p class="sc-admin-section__hint">{t('server.bind_address_hint')}</p>
        <Button variant="filled" onclick={saveNetwork} loading={netMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('network', netMutation.isPending, netError, netOutcome)}
        {#if netError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={netError}>{netError}</p>{/if}
        {#if netOutcome}
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
        <!-- The field is megabytes and the catalogue's bound is bytes, so the
             bound is converted rather than applied as it stands. -->
        <TextField label={t('settings.db_max_bytes')} bind:value={dbMaxBytesMb} type="number" {...mbRangeAttrs('db.max_bytes')} />
        <TextField label={t('settings.db_min_free_bytes')} bind:value={dbMinFreeBytesMb} type="number" {...mbRangeAttrs('db.min_free_bytes')} />
        <Button variant="filled" onclick={saveDb} loading={dbMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('db', dbMutation.isPending, dbError, dbOutcome)}
        {#if dbOutcome}
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
        <div class="sc-server-settings__path-row">
          <TextField label={t('server.homes_root_path')} bind:value={homesRoot} />
          <Button variant="outlined" onclick={() => openPathPicker('folder', homesRoot, (path) => (homesRoot = path))}>
            {#snippet icon()}<Icon icon={icons.folder} size={18} />{/snippet}
            {t('picker.browse_folder')}
          </Button>
        </div>
        <Button variant="filled" onclick={saveHomes} loading={homesMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('homes', homesMutation.isPending, homesError, homesOutcome)}
        {#if homesError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={homesError}>{homesError}</p>{/if}
        {#if homesOutcome}
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
        <Button variant="filled" onclick={saveRate} loading={rateMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('rate', rateMutation.isPending, rateError, rateOutcome)}
        {#if rateError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={rateError}>{rateError}</p>{/if}
        {#if rateOutcome}
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
        <Button variant="filled" onclick={saveWatch} loading={watchMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('watch', watchMutation.isPending, watchError, watchOutcome)}
        {#if watchError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={watchError}>{watchError}</p>{/if}
        {#if watchOutcome}
          {@render findingsList(watchOutcome)}
        {/if}
      </div>
    </div>

    <!-- 9. Single sign-on -->
    <div class="sc-admin-card" id="server-security">
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
        <Switch checked={oidcPublicClient} onchange={(v) => (oidcPublicClient = v)} label={t('settings.oidc_public_client')} />
        <TextField label={t('settings.oidc_scopes')} bind:value={oidcScopes} />
        <TextField label={t('settings.oidc_display_name')} bind:value={oidcDisplayName} />
        <Switch checked={oidcAllowPrivateEndpoints} onchange={(v) => (oidcAllowPrivateEndpoints = v)} label={t('settings.oidc_allow_private_endpoints')} />
        <div class="sc-server-settings__path-row">
          <TextField label={t('field.oidc_ca_cert_file')} bind:value={oidcCaCertFile} />
          <Button variant="outlined" onclick={() => openPathPicker('file', oidcCaCertFile, (path) => (oidcCaCertFile = path))}>
            {#snippet icon()}<Icon icon={icons.file} size={18} />{/snippet}
            {t('picker.browse_file')}
          </Button>
        </div>
        <p class="sc-admin-section__hint">{t('server.connected_accounts_cannot_use_smb')}</p>
        <Button variant="filled" onclick={saveOidc} loading={oidcMutation.isPending}>{t('common.save')}</Button>
        {@render saveStatus('oidc', oidcMutation.isPending, oidcError, oidcOutcome)}
        {#if oidcError}<p class="sc-admin-section__error" role="alert" tabindex="-1" use:focusOnError={oidcError}>{oidcError}</p>{/if}
        {#if oidcOutcome}
          {@render findingsList(oidcOutcome)}
        {/if}
      </div>
      <!-- Addition A/B: the exact strings to register at the provider, one
           line per configured app host. Read-only and separate from the save
           form above: nothing here is sent back, it only shows what the
           server already computed from `app_hosts`. -->
      {#if oidcEndpoints.data && (oidcEndpoints.data.redirect_uris.length || oidcEndpoints.data.post_logout_redirect_uris.length)}
        <div class="sc-server-settings__endpoints">
          <p class="sc-admin-section__hint">{t('settings.oidc_endpoints_hint')}</p>
          <h5 class="sc-admin-section__subhead">{t('settings.oidc_effective_redirect_uris')}</h5>
          {#each oidcEndpoints.data.redirect_uris as uri (uri)}
            <div class="sc-server-settings__endpoint-row">
              <code class="sc-server-settings__endpoint-uri">{uri}</code>
              <Button
                variant="text"
                ariaLabel={t('common.copy_named', { name: uri })}
                onclick={() => copyEndpoint(uri, t('settings.oidc_effective_redirect_uris'))}
              >
                {t('common.copy')}
              </Button>
            </div>
          {/each}
          <h5 class="sc-admin-section__subhead">{t('settings.oidc_post_logout_redirect_uris')}</h5>
          {#each oidcEndpoints.data.post_logout_redirect_uris as uri (uri)}
            <div class="sc-server-settings__endpoint-row">
              <code class="sc-server-settings__endpoint-uri">{uri}</code>
              <Button
                variant="text"
                ariaLabel={t('common.copy_named', { name: uri })}
                onclick={() => copyEndpoint(uri, t('settings.oidc_post_logout_redirect_uris'))}
              >
                {t('common.copy')}
              </Button>
            </div>
          {/each}
          <p class="sc-server-settings__announce" aria-live="polite">{oidcEndpointsAnnouncement}</p>
        </div>
      {/if}
    </div>

    <!-- 10. Storage paths -->
    <div class="sc-admin-card" id="server-storage">
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
    queryClient.invalidateQueries({ queryKey: keys.adminSettings() })
  }}
/>

<PathPickerDialog
  open={pathPicker !== null}
  mode={pathPicker?.mode ?? 'folder'}
  start={pathPicker?.start ?? ''}
  onclose={() => (pathPicker = null)}
  onpick={(path) => {
    pathPicker?.onpick(path)
    pathPicker = null
  }}
/>

<style>
  .sc-admin-card {
    scroll-margin-top: 64px;
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
  .sc-admin-section__status {
    margin: 8px 0 0;
    color: var(--m3c-primary);
    @apply --m3-body-medium;
  }
  .sc-admin-section__status--error {
    color: var(--m3c-error);
  }
  .sc-server-settings__nav {
    position: sticky;
    top: 0;
    z-index: 1;
    margin: 0 0 16px;
    padding: 8px;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    background: color-mix(in srgb, var(--m3c-surface) 92%, transparent);
    backdrop-filter: blur(12px);
  }
  .sc-server-settings__nav-items {
    display: flex;
    gap: 4px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .sc-server-settings__nav-items::-webkit-scrollbar {
    display: none;
  }
  .sc-server-settings__nav button {
    flex: none;
    min-height: 36px;
    border: 0;
    border-radius: var(--m3-shape-full);
    padding: 0 12px;
    color: var(--m3c-on-surface-variant);
    background: transparent;
    cursor: pointer;
    white-space: nowrap;
    @apply --m3-label-large;
  }
  .sc-server-settings__nav button:hover {
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
  }
  .sc-server-settings__nav button:focus-visible {
    outline: 2px solid var(--m3c-primary);
    outline-offset: 2px;
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
  /* `.field`, not `.sc-field`: see UploadSettingsSection for the same
     migration leftover. */
  .sc-server-settings__form :global(.field) {
    width: 100%;
  }
  .sc-server-settings__form :global(.m3-container:has(> select)) {
    width: 100%;
  }
  .sc-server-settings__path-row {
    display: flex;
    gap: 8px;
    align-items: flex-end;
    width: 100%;
  }
  .sc-server-settings__path-row :global(.field) {
    width: auto;
    flex: 1 1 auto;
    min-width: 0;
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
  .sc-server-settings__endpoints {
    margin-top: 8px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    width: 100%;
  }
  .sc-server-settings__endpoint-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .sc-server-settings__endpoint-uri {
    font-family: var(--m3-font-mono, ui-monospace, monospace);
    @apply --m3-body-small;
    overflow-wrap: anywhere;
    user-select: all;
    flex: 1;
  }
  .sc-server-settings__announce {
    margin: 0;
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
</style>
