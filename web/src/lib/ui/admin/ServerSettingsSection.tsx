import { useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { describeApiError, serverKeyText } from '../../api/error-text'
import type { ApplyOutcome, SettingsSnapshot } from '../../api/types'
import { BYTES_PER_MB, bytesToMb } from '../../format/bytes'
import { useI18n } from '../../i18n/use-i18n'
import { adminOidcEndpointsQuery, adminSettingsMutation, adminSettingsQuery } from '../../query/admin'
import { keys } from '../../query/keys'
import { Button } from '../Button'
import { PathPickerDialog } from '../PathPickerDialog'
import { TextField } from '../TextField'
import { Icon } from '../Icon'
import { Switch } from '../Switch'
import { RestartDialog } from './RestartDialog'
import './ServerSettingsSection.css'

type Group = 'smb' | 'search' | 'thumbnail' | 'archive' | 'network' | 'db' | 'homes' | 'watch' | 'rate' | 'oidc'
type PathMode = 'folder' | 'file'
type Values = Record<string, unknown>
type Translator = (key: string, params?: Record<string, string | number>) => string

/* i18n */ 'server.allow_access_from_outside_private'
/* i18n */ 'server.enable_smb'
/* i18n */ 'server.require_separate_smb_password_default'
/* i18n */ 'server.service_account_gid'
/* i18n */ 'server.service_account_name'
/* i18n */ 'server.smb_access_2fa_users'
/* i18n */ 'server.smb_not_allowed'
/* i18n */ 'server.smb_server_name'
/* i18n */ 'server.workgroup'
/* i18n */ 'settings.smb_interfaces'
/* i18n */ 'settings.smb_interfaces_hint'
/* i18n */ 'admin.server_network'
/* i18n */ 'admin.server_search'
/* i18n */ 'admin.server_security'
/* i18n */ 'admin.server_storage_paths'
/* i18n */ 'admin.server_transfers'
/* i18n */ 'field.data_dir'
/* i18n */ 'field.security_hardening'
/* i18n */ 'field.smb_agent_socket'
/* i18n */ 'field.smb_config_dir'
/* i18n */ 'field.thumbnail_dir'
/* i18n */ 'field.thumbnail_enabled'
/* i18n */ 'server.could_not_save_archive_settings'
/* i18n */ 'server.could_not_save_db_settings'
/* i18n */ 'server.could_not_save_file_watch'
/* i18n */ 'server.could_not_save_home_folder'
/* i18n */ 'server.could_not_save_network_settings'
/* i18n */ 'server.could_not_save_oidc_settings'
/* i18n */ 'server.could_not_save_rate_settings'
/* i18n */ 'server.could_not_save_search_settings'
/* i18n */ 'server.could_not_save_smb_settings'
/* i18n */ 'server.could_not_save_thumbnail_settings'
/* i18n */ 'server.database'
/* i18n */ 'settings.db_max_bytes'
/* i18n */ 'settings.db_min_free_bytes'
/* i18n */ 'settings.db_size_guard'
/* i18n */ 'settings.db_size_guard_hint'
/* i18n */ 'settings.dir_is_writable'
/* i18n */ 'settings.dir_will_be_created'
/* i18n */ 'settings.empty_app_hosts_first_boot'
/* i18n */ 'settings.empty_default_thumbs_dir'
/* i18n */ 'settings.empty_disables_netbios_name'
/* i18n */ 'settings.empty_trusted_proxies'
/* i18n */ 'settings.within_kernel_watch_limit'
/* i18n */ 'smb.agent_applied'
/* i18n */ 'smb.agent_applied_with_warnings'
/* i18n */ 'smb.agent_daemon_failed'
/* i18n */ 'smb.agent_problem'
/* i18n */ 'smb.agent_unreachable'
/* i18n */ 'smb.deny_below_root_ignored'
/* i18n */ 'smb.write_list_grants_more'
/* i18n */ 'settings.out_of_range'
/* i18n */ 'settings.host_list_empty'
/* i18n */ 'settings.would_lock_you_out'
/* i18n */ 'settings.proxy_range_is_everything'
/* i18n */ 'settings.gid_zero_is_root'
/* i18n */ 'settings.smb_render_failed'
/* i18n */ 'settings.smb_config_dir_unavailable'
/* i18n */ 'settings.path_is_not_a_directory'
/* i18n */ 'settings.above_kernel_watch_limit'
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
/* i18n */ 'smb.agent_missing_paths'
/* i18n */ 'smb.agent_missing_passdb'
function emptyNote(snapshot: SettingsSnapshot | undefined, key: string, t: Translator): string | null {
  const field = snapshot?.fields.find((item) => item.key === key)
  return field?.empty_means_key ? t(field.empty_means_key) : null
}
const GROUPS: Group[] = ['smb', 'search', 'thumbnail', 'archive', 'network', 'db', 'homes', 'watch', 'rate', 'oidc']
const EDITABLE_KEYS = new Set([
  'bind', 'app_hosts', 'content_hosts', 'allowed_origins', 'compat_canonical_url', 'trusted_proxies',
  'search.walk_deadline_fast_ms', 'archive.max_concurrent', 'rate.per_sec', 'rate.burst',
  'watch.hot_set_max', 'watch.full_threshold', 'oidc.enabled', 'oidc.issuer', 'oidc.client_id',
  'oidc.scopes', 'oidc.display_name', 'oidc.allow_private_endpoints', 'oidc.ca_cert_file',
  'oidc.public_client', 'thumbnail.enabled', 'thumbnail.dir'
])
const PATH_KEYS = ['data_dir', 'smb.config_dir']

function json(value: unknown): string { return JSON.stringify(value) }
function asArray(value: unknown): string[] { return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [] }
function csv(value: unknown): string { return asArray(value).join(', ') }
function split(value: string): string[] { return value.split(',').map((part) => part.trim()).filter(Boolean) }
function fieldLabel(t: Translator, key: string): string { const name = `field.${key.replaceAll('.', '_')}`; const value = t(name); return value === name ? key : value }
function displayValue(t: Translator, value: unknown): string {
  if (value === null || value === undefined) return t('server.none')
  if (Array.isArray(value)) return value.length ? value.join(', ') : t('server.empty')
  if (typeof value === 'boolean') return value ? t('server.on') : t('server.off')
  return value === '' ? t('server.empty') : String(value)
}
function valueMap(snapshot: SettingsSnapshot): Values { return Object.fromEntries(snapshot.fields.map((field) => [field.key, field.value])) }
function groupKeys(group: Group): string[] {
  switch (group) {
    case 'smb': return ['smb.enabled', 'smb.workgroup', 'smb.server_name', 'smb.service_user', 'smb.allow_public_bind', 'smb.totp_policy', 'smb.service_gid', 'smb.interfaces']
    case 'search': return ['search.max_concurrent_fast', 'search.walk_deadline_fast_ms']
    case 'thumbnail': return ['thumbnail.enabled', 'thumbnail.dir']
    case 'archive': return ['archive.max_concurrent']
    case 'network': return ['app_hosts', 'content_hosts', 'allowed_origins', 'compat_canonical_url', 'trusted_proxies', 'bind']
    case 'db': return ['db.size_guard', 'db.max_bytes', 'db.min_free_bytes']
    case 'homes': return ['homes.enabled', 'homes.root']
    case 'watch': return ['watch.hot_set_max', 'watch.full_threshold']
    case 'rate': return ['rate.per_sec', 'rate.burst']
    case 'oidc': return ['oidc.enabled', 'oidc.issuer', 'oidc.client_id', 'oidc.scopes', 'oidc.display_name', 'oidc.allow_private_endpoints', 'oidc.ca_cert_file', 'oidc.public_client']
  }
}
function initialValues(snapshot: SettingsSnapshot): Values {
  const fields = valueMap(snapshot)
  const value = (key: string) => fields[key]
  return {
    'smb.enabled': Boolean(value('smb.enabled')), 'smb.workgroup': String(value('smb.workgroup') ?? ''),
    'smb.server_name': String(value('smb.server_name') ?? ''), 'smb.service_user': String(value('smb.service_user') ?? ''),
    'smb.allow_public_bind': Boolean(value('smb.allow_public_bind')), 'smb.totp_policy': String(value('smb.totp_policy') ?? 'require_separate'),
    'smb.service_gid': String(value('smb.service_gid') ?? '1000'), 'smb.interfaces': csv(value('smb.interfaces')),
    'search.max_concurrent_fast': String(value('search.max_concurrent_fast') ?? ''), 'search.walk_deadline_fast_ms': String(value('search.walk_deadline_fast_ms') ?? ''),
    'thumbnail.enabled': value('thumbnail.enabled') !== false, 'thumbnail.dir': String(value('thumbnail.dir') ?? ''),
    'archive.max_concurrent': String(value('archive.max_concurrent') ?? ''), 'app_hosts': csv(value('app_hosts')),
    'content_hosts': csv(value('content_hosts')), 'allowed_origins': csv(value('allowed_origins')), 'compat_canonical_url': String(value('compat_canonical_url') ?? ''),
    'trusted_proxies': csv(value('trusted_proxies')), bind: String(value('bind') ?? ''), 'db.size_guard': Boolean(value('db.size_guard')),
    'db.max_bytes': String(bytesToMb(Number(value('db.max_bytes') ?? 0))), 'db.min_free_bytes': String(bytesToMb(Number(value('db.min_free_bytes') ?? 0))),
    'homes.enabled': Boolean(value('homes.enabled')), 'homes.root': String(value('homes.root') ?? ''),
    'watch.hot_set_max': String(value('watch.hot_set_max') ?? ''), 'watch.full_threshold': String(value('watch.full_threshold') ?? ''),
    'rate.per_sec': String(value('rate.per_sec') ?? ''), 'rate.burst': String(value('rate.burst') ?? ''),
    'oidc.enabled': Boolean(value('oidc.enabled')), 'oidc.issuer': String(value('oidc.issuer') ?? ''), 'oidc.client_id': String(value('oidc.client_id') ?? ''),
    'oidc.scopes': csv(value('oidc.scopes')), 'oidc.display_name': String(value('oidc.display_name') ?? ''),
    'oidc.allow_private_endpoints': Boolean(value('oidc.allow_private_endpoints')), 'oidc.ca_cert_file': String(value('oidc.ca_cert_file') ?? ''),
    'oidc.public_client': Boolean(value('oidc.public_client'))
  }
}
function requestFor(group: Group, values: Values, secret = ''): Values {
  switch (group) {
    case 'smb': return { enabled: Boolean(values['smb.enabled']), workgroup: String(values['smb.workgroup'] ?? ''), server_name: String(values['smb.server_name'] ?? ''), service_user: String(values['smb.service_user'] ?? ''), allow_public_bind: Boolean(values['smb.allow_public_bind']), totp_policy: values['smb.totp_policy'] === 'block' ? 'block' : 'require_separate', service_gid: Number(values['smb.service_gid']), interfaces: split(String(values['smb.interfaces'] ?? '')) }
    case 'search': return { max_concurrent_fast: Number(values['search.max_concurrent_fast']), walk_deadline_fast_ms: Number(values['search.walk_deadline_fast_ms']) }
    case 'thumbnail': return { enabled: Boolean(values['thumbnail.enabled']), dir: String(values['thumbnail.dir'] ?? '').trim() || undefined }
    case 'archive': return { max_concurrent: Number(values['archive.max_concurrent']) }
    case 'network': return { app_hosts: split(String(values.app_hosts ?? '')), content_hosts: split(String(values.content_hosts ?? '')), allowed_origins: split(String(values.allowed_origins ?? '')), compat_canonical_url: String(values.compat_canonical_url ?? '').trim(), trusted_proxies: split(String(values.trusted_proxies ?? '')), bind: String(values.bind ?? '').trim() || undefined }
    case 'db': return { size_guard: Boolean(values['db.size_guard']), max_bytes: Math.round(Number(values['db.max_bytes']) * BYTES_PER_MB), min_free_bytes: Math.round(Number(values['db.min_free_bytes']) * BYTES_PER_MB) }
    case 'homes': return { enabled: Boolean(values['homes.enabled']), root: String(values['homes.root'] ?? '').trim() || null }
    case 'watch': return { hot_set_max: Number(values['watch.hot_set_max']), full_threshold: Number(values['watch.full_threshold']) }
    case 'rate': return { per_sec: Number(values['rate.per_sec']), burst: Number(values['rate.burst']) }
    case 'oidc': { const req: Values = { enabled: Boolean(values['oidc.enabled']), issuer: String(values['oidc.issuer'] ?? '').trim(), client_id: String(values['oidc.client_id'] ?? '').trim(), scopes: split(String(values['oidc.scopes'] ?? '')), display_name: String(values['oidc.display_name'] ?? ''), allow_private_endpoints: Boolean(values['oidc.allow_private_endpoints']), ca_cert_file: String(values['oidc.ca_cert_file'] ?? '').trim(), public_client: Boolean(values['oidc.public_client']), smb_policy: 'block' }; if (secret.trim()) req.client_secret = secret.trim(); return req }
  }
}
function findingList(outcome: ApplyOutcome | null, t: Translator): ReactNode {
  const findings = (outcome?.findings ?? []).filter((finding) => finding.reason !== 'settings.check_passed')
  if (!findings.length) return null
  return <ul className="sc-server-settings__findings">{findings.map((finding, index) => <li key={`${finding.section}-${finding.field ?? ''}-${finding.reason}-${index}`} className={`sc-server-settings__finding ${finding.blocking ? 'sc-server-settings__finding--block' : 'sc-server-settings__finding--ok'}`}><strong>{finding.blocking ? t('settings.finding_blocking') : t('settings.finding_advisory')}</strong>{finding.field ? <code>{finding.field}</code> : null}{t(finding.reason, finding.args ?? {})}</li>)}</ul>
}

export function ServerSettingsSection() {
  const { t } = useI18n()
  const queryClient = useQueryClient()
  const settings = useQuery(adminSettingsQuery())
  const endpoints = useQuery(adminOidcEndpointsQuery())
  const snapshot = settings.data
  const [values, setValues] = useState<Values>({})
  const [baselines, setBaselines] = useState<Record<Group, string | null>>(() => Object.fromEntries(GROUPS.map((group) => [group, null])) as Record<Group, string | null>)
  const [hydrated, setHydrated] = useState<Record<Group, boolean>>(() => Object.fromEntries(GROUPS.map((group) => [group, false])) as Record<Group, boolean>)
  const [activeGroup, setActiveGroup] = useState<Group | null>(null)
  const [validationError, setValidationError] = useState<string | null>(null)
  const [outcome, setOutcome] = useState<ApplyOutcome | null>(null)
  const [restartOutcome, setRestartOutcome] = useState<ApplyOutcome | null>(null)
  const [pathPicker, setPathPicker] = useState<{ mode: PathMode; key: string } | null>(null)
  const [secret, setSecret] = useState('')
  const [announcement, setAnnouncement] = useState('')
  const errorRef = useRef<HTMLParagraphElement>(null)
  const mutation = useMutation(adminSettingsMutation())
  const fields = useMemo(() => snapshot ? valueMap(snapshot) : {}, [snapshot])

  useEffect(() => {
    if (!snapshot) return
    const nextValues = initialValues(snapshot)
    setValues((current) => {
      const next = { ...current }
      for (const group of GROUPS) {
        const currentRequest = hydrated[group] ? json(requestFor(group, current)) : null
        if (hydrated[group] && baselines[group] !== null && currentRequest !== baselines[group]) continue
        for (const key of groupKeys(group)) next[key] = nextValues[key]
        const baselineRequest = requestFor(group, next)
        setBaselines((before) => ({ ...before, [group]: json(baselineRequest) }))
        setHydrated((before) => ({ ...before, [group]: true }))
      }
      return next
    })
  }, [snapshot])

  useEffect(() => { if (validationError) errorRef.current?.focus() }, [validationError])
  function isDirty(group: Group): boolean { return hydrated[group] === true && baselines[group] !== json(requestFor(group, values)) }
  function setValue(key: string, value: unknown) { setValues((before) => ({ ...before, [key]: value })); setValidationError(null); setOutcome(null) }
  function rangeError(key: string, raw: string): string | null {
    const range = snapshot?.fields.find((field) => field.key === key)?.range
    if (range?.kind === 'int') { const number = Number(raw); if (!Number.isInteger(number) || number < range.min || number > range.max) return t('server.enter_a_value_between', { min: range.min, max: range.max }) }
    return null
  }
  function submit(group: Group) {
    if (!snapshot) return
    setActiveGroup(group); setValidationError(null); setOutcome(null); mutation.reset()
    let error: string | null = null
    if (group === 'smb') {
      if (!String(values['smb.workgroup'] ?? '').trim() || !String(values['smb.server_name'] ?? '').trim() || !String(values['smb.service_user'] ?? '').trim()) error = t('server.enter_workgroup_server_name_service_account')
      else if (!Number.isInteger(Number(values['smb.service_gid'])) || Number(values['smb.service_gid']) < 0) error = t('server.gid_must_integer_0')
      else error = rangeError('smb.service_gid', String(values['smb.service_gid'] ?? ''))
    }
    if (group === 'network' && split(String(values.app_hosts ?? '')).length === 0) error = t('server.enter_at_least_one_app_host')
    if (group === 'homes' && values['homes.enabled'] && !String(values['homes.root'] ?? '').trim()) error = t('server.enter_root_path_enable_home')
    if (group === 'oidc' && values['oidc.enabled'] && (!String(values['oidc.issuer'] ?? '').trim() || !String(values['oidc.client_id'] ?? '').trim())) error = t('settings.oidc_issuer_client_required')
    if (group === 'db' && (!Number.isInteger(Number(values['db.max_bytes'])) || Number(values['db.max_bytes']) < 0 || !Number.isInteger(Number(values['db.min_free_bytes'])) || Number(values['db.min_free_bytes']) < 0)) error = t('server.every_value_must_integer_0')
    if (group === 'rate' && (!Number.isInteger(Number(values['rate.per_sec'])) || Number(values['rate.per_sec']) < 1 || !Number.isInteger(Number(values['rate.burst'])) || Number(values['rate.burst']) < 1)) error = t('server.must_integer_1_or_more')
    if (group === 'watch' && (!Number.isInteger(Number(values['watch.hot_set_max'])) || Number(values['watch.hot_set_max']) < 1 || !Number.isInteger(Number(values['watch.full_threshold'])) || Number(values['watch.full_threshold']) < 1)) error = t('server.watch_limit_must_integer_1')
    for (const key of groupKeys(group)) if (!error && typeof values[key] === 'string' && ['smb.service_gid', 'search.max_concurrent_fast', 'search.walk_deadline_fast_ms', 'archive.max_concurrent', 'rate.per_sec', 'rate.burst', 'watch.hot_set_max', 'watch.full_threshold'].includes(key)) error = rangeError(key, String(values[key]))
    if (error) { setValidationError(error); return }
    const req = requestFor(group, values, group === 'oidc' ? secret : '')
    mutation.mutate({ section: group, req } as never, {
      onSuccess: (result: ApplyOutcome) => {
        setOutcome(result)
        const accepted = result.stored || result.applied || result.restart_required
        if (accepted) setBaselines((before) => ({ ...before, [group]: json(requestFor(group, values, '')) }))
        if (result.restart_required) setRestartOutcome(result)
        if (group === 'oidc' && accepted) setSecret('')
      },
      onError: (error) => setValidationError(describeApiError(error, t(`server.could_not_save_${group}_settings`)))
    })
  }
  function status(group: Group): ReactNode {
    if (activeGroup !== group) return null
    if (mutation.isPending) return <p className="sc-admin-section__status" role="status">{t('common.saving')}</p>
    if (validationError) return <p ref={errorRef} className="sc-admin-section__status sc-admin-section__status--error" role="alert" tabIndex={-1}>{validationError}</p>
    if (outcome && !(outcome.stored || outcome.applied || outcome.restart_required)) return <p className="sc-admin-section__status sc-admin-section__status--error" role="status">{t('common.could_not_save')}</p>
    if (isDirty(group)) return <p className="sc-admin-section__status" role="status">{t('settings.unsaved_changes')}</p>
    if (outcome) return <p className="sc-admin-section__status" role="status">{outcome.restart_required ? t('server.change_takes_full_effect_only') : outcome.applied ? t('server.change_took_effect_immediately') : outcome.stored ? t('server.change_takes_full_effect_only') : t('common.could_not_save')}</p>
    return null
  }
  function input(key: string, label: string, options: { type?: 'text' | 'number' | 'password'; min?: number; max?: number; placeholder?: string } = {}) {
    return <TextField label={label} value={key === 'oidc.secret' ? secret : String(values[key] ?? '')} type={options.type} min={options.min} max={options.max} placeholder={options.placeholder} onValueChange={(value) => key === 'oidc.secret' ? setSecret(value) : setValue(key, value)} />
  }
  function toggle(key: string, label: string) { return <Switch checked={Boolean(values[key])} label={label} onChange={(checked) => setValue(key, checked)} /> }
  function saveButton(group: Group) { return <>{status(group)}<Button onClick={() => submit(group)} loading={mutation.isPending && activeGroup === group}>{t('common.save')}</Button>{activeGroup === group && outcome ? findingList(outcome, t) : null}</> }
  function card(id: string, title: string, subtitle: string, children: ReactNode) { return <div className="sc-admin-card" id={id}><div className="sc-admin-card-head"><div className="sc-admin-card-icon"><Icon name="settings" /></div><div className="sc-admin-card-meta"><h4 className="sc-admin-card-title">{title}</h4><p className="sc-admin-card-subtitle">{subtitle}</p></div></div><div className="sc-server-settings__form">{children}</div></div> }
  async function copyEndpoint(uri: string, name: string) { try { await navigator.clipboard.writeText(uri); setAnnouncement(t('settings.oidc_endpoint_copied', { name })) } catch { /* selectable fallback */ } }
  if (settings.isPending) return <section className="sc-admin-section"><h3>{t('server.server_settings')}</h3><mdui-circular-progress></mdui-circular-progress></section>
  if (settings.isError || !snapshot) return <section className="sc-admin-section"><h3>{t('server.server_settings')}</h3><p className="sc-admin-section__error" role="alert">{t('server.could_not_load_server_settings')}</p></section>
  const hop = snapshot.hop
  const hopAddress = hop?.peer ?? hop?.client ?? ''
  const otherFields = snapshot.fields.filter((item) => !EDITABLE_KEYS.has(item.key) && !PATH_KEYS.includes(item.key) && item.key !== 'symlink_policy')
  const addObserved = () => { if (!hopAddress) return; const cidr = hopAddress.includes(':') ? `${hopAddress}/128` : `${hopAddress}/32`; setValue('trusted_proxies', [...split(String(values.trusted_proxies ?? '')), cidr].filter((item, index, all) => all.indexOf(item) === index).join(', ')) }
  return <>
    <section className="sc-admin-section"><h3>{t('server.server_settings')}</h3><p className="sc-admin-section__hint">{t('server.anything_settable_config_toml_can')}</p><nav className="sc-server-settings__nav" aria-label={t('admin.server_settings_navigation')}><div className="sc-server-settings__nav-items">{[['server-smb', 'admin.server_smb'], ['server-search', 'admin.server_search'], ['server-network', 'admin.server_network'], ['server-transfers', 'admin.server_transfers'], ['server-security', 'admin.server_security'], ['server-storage', 'admin.server_storage_paths']].map(([id, key]) => <button key={id} type="button" onClick={() => document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>{t(key)}</button>)}</div></nav>
      {snapshot.smb_public_bind_warning ? <p className="sc-admin-section__warning" role="alert">{t('server.smb_reachable_from_outside_private')}</p> : null}
      {snapshot.smb_overgrants?.length ? <div className="sc-admin-section__warning" role="alert"><p>{t('server.smb_grants_more_than_configured')}</p><ul>{snapshot.smb_overgrants.map((item) => <li key={`${item.share}-${item.user}-${item.key}`}>{t(item.key, { share: item.share, user: item.user, detail: item.detail.join(', ') })}</li>)}</ul></div> : null}
      {card('server-smb', 'SMB', t('server.all_apply_immediately_no_restart'), <>{toggle('smb.enabled', t('server.enable_smb'))}{input('smb.workgroup', t('server.workgroup'))}{input('smb.server_name', t('server.smb_server_name'))}{!String(values['smb.server_name'] ?? '').trim() && emptyNote(snapshot, 'smb.server_name', t) ? <p className="sc-server-settings__empty-note">{emptyNote(snapshot, 'smb.server_name', t)}</p> : null}{input('smb.service_user', t('server.service_account_name'))}{toggle('smb.allow_public_bind', t('server.allow_access_from_outside_private'))}<label>{t('server.smb_access_2fa_users')}<select value={String(values['smb.totp_policy'] ?? 'require_separate')} onChange={(event) => setValue('smb.totp_policy', event.currentTarget.value)}><option value="require_separate">{t('server.require_separate_smb_password_default')}</option><option value="block">{t('server.smb_not_allowed')}</option></select></label>{input('smb.service_gid', t('server.service_account_gid'), { type: 'number' })}{input('smb.interfaces', t('settings.smb_interfaces'))}<p className="sc-admin-section__hint">{t('settings.smb_interfaces_hint')}</p>{saveButton('smb')}{snapshot.smb_agent ? <div className={snapshot.smb_agent.ok ? 'sc-admin-section__hint' : 'sc-admin-section__warning'} role={snapshot.smb_agent.ok ? 'status' : 'alert'}><p>{t(snapshot.smb_agent.key, { shares: snapshot.smb_agent.shares?.length ?? 0, interfaces: snapshot.smb_agent.interfaces, smbd: snapshot.smb_agent.smbd, error: snapshot.smb_agent.detail ?? '' })}</p>{snapshot.smb_agent.missing_paths?.length ? <p>{t('smb.agent_missing_paths', { paths: snapshot.smb_agent.missing_paths.join(', ') })}</p> : null}{snapshot.smb_agent.missing_passdb?.length ? <p>{t('smb.agent_missing_passdb', { users: snapshot.smb_agent.missing_passdb.join(', ') })}</p> : null}</div> : null}</>)}
      {card('server-storage', t('server.storage_paths'), t('settings.paths_readonly_reason'), <><dl className="sc-server-settings__other">{PATH_KEYS.map((key) => <div key={key}><dt>{fieldLabel(t, key)}</dt><dd>{displayValue(t, fields[key])}</dd></div>)}<div><dt>{fieldLabel(t, 'symlink_policy')}</dt><dd><span className="sc-server-settings__reason">{t('settings.readonly_per_share_symlink_policy')}</span></dd></div></dl>{otherFields.length ? <><h5 className="sc-admin-section__subhead">{t('settings.settings_sections')}</h5><dl className="sc-server-settings__other">{otherFields.map((item) => <div key={item.key}><dt>{fieldLabel(t, item.key)}</dt><dd>{displayValue(t, item.value)}{item.readonly_reason_key ? <><br /><span className="sc-server-settings__reason">{serverKeyText(item.readonly_reason_key)}</span></> : null}</dd></div>)}</dl></> : null}</>)}
      {card('server-search', t('common.search'), t('server.all_apply_immediately_no_restart'), <>{input('search.max_concurrent_fast', t('server.concurrent_fast_searches'), { type: 'number' })}{input('search.walk_deadline_fast_ms', t('server.fast_search_timeout_ms'), { type: 'number' })}{saveButton('search')}</>)}
      {card('server-transfers', t('server.zip_download'), t('server.applies_immediately_no_restart_needed'), <>{input('archive.max_concurrent', t('server.concurrent_zip_streams'), { type: 'number' })}{saveButton('archive')}</>)}
      {card('server-thumbnail', t('server.thumbnail_settings'), t('server.thumbnail_enabled_description'), <>{toggle('thumbnail.enabled', t('server.thumbnail_enabled'))}<div className="sc-server-settings__path-row">{input('thumbnail.dir', t('server.thumbnail_storage_dir'))}<Button variant="outlined" onClick={() => setPathPicker({ mode: 'folder', key: 'thumbnail.dir' })}>{t('picker.browse_folder')}</Button></div>{!String(values['thumbnail.dir'] ?? '').trim() && emptyNote(snapshot, 'thumbnail.dir', t) ? <p className="sc-server-settings__empty-note">{emptyNote(snapshot, 'thumbnail.dir', t)}</p> : null}<p className="sc-admin-section__hint">{t('server.thumbnail_storage_dir_description')}</p>{saveButton('thumbnail')}</>)}
      {card('server-network', t('server.network'), t('server.applies_immediately_no_restart_needed'), <>{input('app_hosts', t('server.app_hosts_comma_separated'))}{!String(values.app_hosts ?? '').trim() && emptyNote(snapshot, 'app_hosts', t) ? <p className="sc-server-settings__empty-note">{emptyNote(snapshot, 'app_hosts', t)}</p> : null}<p className="sc-admin-section__hint">{t('server.app_hosts_hint')}</p>{input('trusted_proxies', t('server.trusted_proxies_comma_separated'))}{!String(values.trusted_proxies ?? '').trim() && emptyNote(snapshot, 'trusted_proxies', t) ? <p className="sc-server-settings__empty-note">{emptyNote(snapshot, 'trusted_proxies', t)}</p> : null}{hop ? <p className="sc-admin-section__hint">{t('server.requests_arriving_from', { address: hopAddress || t('server.unknown_address') })} {hop.peer_trusted ? t('server.peer_trusted') : t('server.peer_not_trusted')}</p> : null}{hop && !hop.peer_trusted && hop.forwarded_seen ? <div className="sc-admin-section__warning" role="alert"><p>{t('server.forwarding_headers_ignored_hint', { address: hopAddress })}</p><Button variant="text" onClick={addObserved} disabled={!hopAddress}>{t('server.add_observed_address_to_trusted', { address: hopAddress })}</Button></div> : null}{input('content_hosts', t('server.content_hosts_comma_separated'))}{input('allowed_origins', t('server.allowed_origins_cors_comma_separated'))}{input('compat_canonical_url', t('server.compat_canonical_url'))}{input('bind', t('server.bind_address'))}<p className="sc-admin-section__hint">{t('server.bind_address_hint')}</p>{saveButton('network')}</>)}
      {card('server-watch', t('server.file_watching'), t('server.what_file_watching_is_for'), <>{input('watch.hot_set_max', t('server.maximum_folders_watched_at_once'), { type: 'number' })}{input('watch.full_threshold', t('server.changes_before_a_full_rescan'), { type: 'number' })}<p className="sc-admin-section__hint">{t('settings.within_kernel_watch_limit', { limit: snapshot.fields.find((field) => field.key === 'watch.hot_set_max')?.range && 'max' in snapshot.fields.find((field) => field.key === 'watch.hot_set_max')!.range! ? (snapshot.fields.find((field) => field.key === 'watch.hot_set_max')!.range as { max: number }).max : '' })}</p>{saveButton('watch')}</>)}
      {card('server-homes', t('server.home_folders'), t('server.home_folders_hint'), <>{toggle('homes.enabled', t('server.enable_home_folders'))}<div className="sc-server-settings__path-row">{input('homes.root', t('server.homes_root_path'))}<Button variant="outlined" onClick={() => setPathPicker({ mode: 'folder', key: 'homes.root' })}>{t('picker.browse_folder')}</Button></div>{saveButton('homes')}</>)}
      {card('server-rate', t('server.request_rate'), t('server.what_the_request_rate_is_for'), <>{input('rate.per_sec', t('server.requests_per_second'), { type: 'number' })}{input('rate.burst', t('server.burst_allowance'), { type: 'number' })}{saveButton('rate')}</>)}
      {card('server-security', t('settings.single_sign_on'), t('server.single_sign_on_hint'), <>{toggle('oidc.enabled', t('settings.oidc_enable'))}{input('oidc.issuer', t('settings.oidc_issuer'))}{input('oidc.client_id', t('settings.oidc_client_id'))}{input('oidc.secret', t('common.password'), { type: 'password', placeholder: t('settings.secret_is_write_only') })}{toggle('oidc.public_client', t('settings.oidc_public_client'))}{input('oidc.scopes', t('settings.oidc_scopes'))}{input('oidc.display_name', t('settings.oidc_display_name'))}{toggle('oidc.allow_private_endpoints', t('settings.oidc_allow_private_endpoints'))}<div className="sc-server-settings__path-row">{input('oidc.ca_cert_file', t('field.oidc_ca_cert_file'))}<Button variant="outlined" onClick={() => setPathPicker({ mode: 'file', key: 'oidc.ca_cert_file' })}>{t('picker.browse_file')}</Button></div><p className="sc-admin-section__hint">{t('server.connected_accounts_cannot_use_smb')}</p>{saveButton('oidc')}{endpoints.data && (endpoints.data.redirect_uris.length || endpoints.data.post_logout_redirect_uris.length) ? <div className="sc-server-settings__endpoints"><p className="sc-admin-section__hint">{t('settings.oidc_endpoints_hint')}</p><h5 className="sc-admin-section__subhead">{t('settings.oidc_effective_redirect_uris')}</h5>{endpoints.data.redirect_uris.map((uri) => <div className="sc-server-settings__endpoint-row" key={uri}><code className="sc-server-settings__endpoint-uri">{uri}</code><Button variant="text" ariaLabel={t('common.copy_named', { name: uri })} onClick={() => void copyEndpoint(uri, t('settings.oidc_effective_redirect_uris'))}>{t('common.copy')}</Button></div>)}<h5 className="sc-admin-section__subhead">{t('settings.oidc_post_logout_redirect_uris')}</h5>{endpoints.data.post_logout_redirect_uris.map((uri) => <div className="sc-server-settings__endpoint-row" key={uri}><code className="sc-server-settings__endpoint-uri">{uri}</code><Button variant="text" ariaLabel={t('common.copy_named', { name: uri })} onClick={() => void copyEndpoint(uri, t('settings.oidc_post_logout_redirect_uris'))}>{t('common.copy')}</Button></div>)}<p className="sc-server-settings__announce" aria-live="polite">{announcement}</p></div> : null}</>)}
    </section>
    <RestartDialog open={restartOutcome !== null} outcome={restartOutcome} onclose={() => setRestartOutcome(null)} onrestarted={() => { setRestartOutcome(null); void queryClient.invalidateQueries({ queryKey: keys.adminSettings() }) }} />
    <PathPickerDialog open={pathPicker !== null} mode={pathPicker?.mode ?? 'folder'} start={pathPicker ? String(values[pathPicker.key] ?? '') : ''} onclose={() => setPathPicker(null)} onpick={(path) => { if (pathPicker) setValue(pathPicker.key, path); setPathPicker(null) }} />
  </>
}
