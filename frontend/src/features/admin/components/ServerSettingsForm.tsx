import { useEffect, useRef, type ReactNode } from 'react'
import { Controller, FormProvider, get, set, useForm, useFormContext, useWatch } from 'react-hook-form'
import { describeApiError } from '../../../api/error-text'
import { BYTES_PER_MB, bytesToMb } from '../../../lib/format/bytes'
import { t } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { Button } from '../../../ui/Button'
import { FormTextField } from '../../../ui/FormTextField'
import { Switch } from '../../../ui/Switch'
import { VirtualList } from '../../../ui/VirtualList'
import { cx } from '../../../ui/cx'
import { pickPath } from '../../files/components/PathPickerDialog'
import { offerRestart } from './RestartDialog'
import { ServerSettingsCard } from './ServerSettingsCard'
import * as styles from './ServerSettingsSection.css'
import { useRefreshAdminSettings, useSaveSettings, type ApplyOutcome, type SettingsSnapshot } from '../api'

export type Group = 'smb' | 'search' | 'thumbnail' | 'archive' | 'network' | 'db' | 'homes' | 'watch' | 'rate' | 'oidc'
type Values = Record<string, unknown>
type Read = (key: string) => unknown

// A field key with a dot is a nested path in the form, so each group's values nest under its prefix.
const GROUP_KEYS: Record<Group, readonly string[]> = {
  smb: [
    'smb.enabled',
    'smb.workgroup',
    'smb.server_name',
    'smb.service_user',
    'smb.allow_public_bind',
    'smb.totp_policy',
    'smb.service_gid',
    'smb.interfaces'
  ],
  search: ['search.max_concurrent_fast', 'search.walk_deadline_fast_ms'],
  thumbnail: ['thumbnail.enabled', 'thumbnail.dir'],
  archive: ['archive.max_concurrent'],
  network: ['app_hosts', 'content_hosts', 'allowed_origins', 'compat_canonical_url', 'trusted_proxies', 'bind'],
  db: ['db.size_guard', 'db.max_bytes', 'db.min_free_bytes'],
  homes: ['homes.enabled', 'homes.root'],
  watch: ['watch.hot_set_max', 'watch.full_threshold'],
  rate: ['rate.per_sec', 'rate.burst'],
  oidc: [
    'oidc.enabled',
    'oidc.issuer',
    'oidc.client_id',
    'oidc.secret',
    'oidc.scopes',
    'oidc.display_name',
    'oidc.allow_private_endpoints',
    'oidc.ca_cert_file',
    'oidc.public_client'
  ]
}

const SAVE_FAILED: Record<Group, () => string> = {
  smb: () => t('server.could_not_save_smb_settings'),
  search: () => t('server.could_not_save_search_settings'),
  thumbnail: () => t('server.could_not_save_thumbnail_settings'),
  archive: () => t('server.could_not_save_archive_settings'),
  network: () => t('server.could_not_save_network_settings'),
  db: () => t('server.could_not_save_db_settings'),
  homes: () => t('server.could_not_save_home_folder'),
  watch: () => t('server.could_not_save_file_watch'),
  rate: () => t('server.could_not_save_rate_settings'),
  oidc: () => t('server.could_not_save_oidc_settings')
}

const RANGED_KEYS = new Set([
  'smb.service_gid',
  'search.max_concurrent_fast',
  'search.walk_deadline_fast_ms',
  'archive.max_concurrent',
  'rate.per_sec',
  'rate.burst',
  'watch.hot_set_max',
  'watch.full_threshold'
])

function asArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
}
function csv(value: unknown): string {
  return asArray(value).join(', ')
}
export function split(value: string): string[] {
  return value
    .split(',')
    .map((part) => part.trim())
    .filter(Boolean)
}

function initialValue(key: string, read: Read): unknown {
  const value = read(key)
  switch (key) {
    case 'smb.enabled':
    case 'smb.allow_public_bind':
    case 'db.size_guard':
    case 'homes.enabled':
    case 'oidc.enabled':
    case 'oidc.allow_private_endpoints':
    case 'oidc.public_client':
      return Boolean(value)
    case 'thumbnail.enabled':
      return value !== false
    case 'smb.totp_policy':
      return String(value ?? 'require_separate')
    case 'smb.service_gid':
      return String(value ?? '1000')
    case 'smb.interfaces':
    case 'app_hosts':
    case 'content_hosts':
    case 'allowed_origins':
    case 'trusted_proxies':
    case 'oidc.scopes':
      return csv(value)
    case 'db.max_bytes':
    case 'db.min_free_bytes':
      return String(bytesToMb(Number(value ?? 0)))
    default:
      return String(value ?? '')
  }
}

function formValuesOf(group: Group, snapshot: SettingsSnapshot): Values {
  const stored = new Map(snapshot.fields.map((field) => [field.key, field.value]))
  const values: Values = {}
  for (const key of GROUP_KEYS[group])
    set(
      values,
      key,
      initialValue(key, (name) => stored.get(name))
    )
  return values
}

function requestFor(group: Group, read: Read): Values {
  const text = (key: string): string => String(read(key) ?? '')
  const flag = (key: string): boolean => Boolean(read(key))
  const number = (key: string): number => Number(read(key))
  switch (group) {
    case 'smb':
      return {
        enabled: flag('smb.enabled'),
        workgroup: text('smb.workgroup'),
        server_name: text('smb.server_name'),
        service_user: text('smb.service_user'),
        allow_public_bind: flag('smb.allow_public_bind'),
        totp_policy: read('smb.totp_policy') === 'block' ? 'block' : 'require_separate',
        service_gid: number('smb.service_gid'),
        interfaces: split(text('smb.interfaces'))
      }
    case 'search':
      return {
        max_concurrent_fast: number('search.max_concurrent_fast'),
        walk_deadline_fast_ms: number('search.walk_deadline_fast_ms')
      }
    case 'thumbnail':
      return { enabled: flag('thumbnail.enabled'), dir: text('thumbnail.dir').trim() || undefined }
    case 'archive':
      return { max_concurrent: number('archive.max_concurrent') }
    case 'network':
      return {
        app_hosts: split(text('app_hosts')),
        content_hosts: split(text('content_hosts')),
        allowed_origins: split(text('allowed_origins')),
        compat_canonical_url: text('compat_canonical_url').trim(),
        trusted_proxies: split(text('trusted_proxies')),
        bind: text('bind').trim() || undefined
      }
    case 'db':
      return {
        size_guard: flag('db.size_guard'),
        max_bytes: Math.round(number('db.max_bytes') * BYTES_PER_MB),
        min_free_bytes: Math.round(number('db.min_free_bytes') * BYTES_PER_MB)
      }
    case 'homes':
      return { enabled: flag('homes.enabled'), root: text('homes.root').trim() || null }
    case 'watch':
      return { hot_set_max: number('watch.hot_set_max'), full_threshold: number('watch.full_threshold') }
    case 'rate':
      return { per_sec: number('rate.per_sec'), burst: number('rate.burst') }
    case 'oidc':
      return {
        enabled: flag('oidc.enabled'),
        issuer: text('oidc.issuer').trim(),
        client_id: text('oidc.client_id').trim(),
        scopes: split(text('oidc.scopes')),
        display_name: text('oidc.display_name'),
        allow_private_endpoints: flag('oidc.allow_private_endpoints'),
        ca_cert_file: text('oidc.ca_cert_file').trim(),
        public_client: flag('oidc.public_client'),
        smb_policy: 'block',
        // The secret is write-only: blank keeps the stored one.
        ...(text('oidc.secret').trim() ? { client_secret: text('oidc.secret').trim() } : {})
      }
  }
}

const wholeAtLeast = (value: unknown, min: number): boolean => Number.isInteger(Number(value)) && Number(value) >= min

function ruleProblem(group: Group, read: Read): string | null {
  const blank = (key: string): boolean => !String(read(key) ?? '').trim()
  switch (group) {
    case 'smb':
      if (blank('smb.workgroup') || blank('smb.server_name') || blank('smb.service_user'))
        return t('server.enter_workgroup_server_name_service_account')
      return wholeAtLeast(read('smb.service_gid'), 0) ? null : t('server.gid_must_integer_0')
    case 'network':
      return split(String(read('app_hosts') ?? '')).length ? null : t('server.enter_at_least_one_app_host')
    case 'homes':
      return read('homes.enabled') && blank('homes.root') ? t('server.enter_root_path_enable_home') : null
    case 'oidc':
      return read('oidc.enabled') && (blank('oidc.issuer') || blank('oidc.client_id'))
        ? t('settings.oidc_issuer_client_required')
        : null
    case 'db':
      return wholeAtLeast(read('db.max_bytes'), 0) && wholeAtLeast(read('db.min_free_bytes'), 0)
        ? null
        : t('server.every_value_must_integer_0')
    case 'rate':
      return wholeAtLeast(read('rate.per_sec'), 1) && wholeAtLeast(read('rate.burst'), 1)
        ? null
        : t('server.must_integer_1_or_more')
    case 'watch':
      return wholeAtLeast(read('watch.hot_set_max'), 1) && wholeAtLeast(read('watch.full_threshold'), 1)
        ? null
        : t('server.watch_limit_must_integer_1')
    default:
      return null
  }
}

function rangeProblem(snapshot: SettingsSnapshot, key: string, raw: string): string | null {
  const range = snapshot.fields.find((field) => field.key === key)?.range
  if (range?.kind !== 'int' || range.min === undefined || range.max === undefined) return null
  const number = Number(raw)
  return Number.isInteger(number) && number >= range.min && number <= range.max
    ? null
    : t('server.enter_a_value_between', { min: range.min, max: range.max })
}

function groupProblem(group: Group, read: Read, snapshot: SettingsSnapshot): string | null {
  return (
    ruleProblem(group, read) ??
    GROUP_KEYS[group]
      .filter((key) => RANGED_KEYS.has(key))
      .map((key) => rangeProblem(snapshot, key, String(read(key) ?? '')))
      .find((problem) => problem !== null) ??
    null
  )
}

const accepted = (outcome: ApplyOutcome): boolean => outcome.stored || outcome.applied || outcome.restart_required

function outcomeText(outcome: ApplyOutcome): string {
  if (outcome.applied && !outcome.restart_required) return t('server.change_took_effect_immediately')
  return accepted(outcome) ? t('server.change_takes_full_effect_only') : t('common.could_not_save')
}

function Findings({ outcome }: { outcome: ApplyOutcome }) {
  const { t } = useI18n()
  const findings = outcome.findings.filter((finding) => finding.reason !== 'settings.check_passed')
  if (!findings.length) return null
  return (
    <VirtualList
      className={styles.findings}
      items={findings}
      itemKey={(finding, index) => `${finding.section}-${finding.field ?? ''}-${finding.reason}-${index}`}
      estimateSize={72}
      itemProps={(finding) => ({
        className: cx(styles.finding, finding.blocking ? styles.findingBlock : styles.findingOk)
      })}
      renderItem={(finding) => (
        <>
          <strong className={styles.findingKind}>
            {finding.blocking ? t('settings.finding_blocking') : t('settings.finding_advisory')}
          </strong>
          {finding.field ? <code className={styles.findingField}>{finding.field}</code> : null}
          {t(finding.reason, finding.args ?? {})}
        </>
      )}
    />
  )
}

interface SettingsGroupCardProps {
  group: Group
  id: string
  title: ReactNode
  subtitle: ReactNode
  snapshot: SettingsSnapshot
  children: ReactNode
  /** Shown below the save button and its findings. */
  footer?: ReactNode
}

/** One settings group as its own form, saved on its own. */
export function SettingsGroupCard({ group, id, title, subtitle, snapshot, children, footer }: SettingsGroupCardProps) {
  const { t } = useI18n()
  const save = useSaveSettings()
  const refreshSettings = useRefreshAdminSettings()
  // A refetched snapshot replaces only the fields the admin has not edited.
  const form = useForm<Values>({
    values: formValuesOf(group, snapshot),
    resetOptions: { keepDirtyValues: true }
  })
  const { handleSubmit, setError, reset, formState } = form
  const errorRef = useRef<HTMLParagraphElement>(null)
  const outcome = save.data
  const error =
    formState.errors.root?.message ?? (save.error ? describeApiError(save.error, SAVE_FAILED[group]()) : null)

  useEffect(() => {
    if (error) errorRef.current?.focus()
  }, [error])

  const submit = handleSubmit((values) => {
    if (save.isPending) return
    const read: Read = (key) => get(values, key)
    const problem = groupProblem(group, read, snapshot)
    if (problem) {
      setError('root', { message: problem })
      return
    }
    save.mutate(
      { section: group, req: requestFor(group, read) },
      {
        onSuccess: (result) => {
          if (!accepted(result)) return
          reset(group === 'oidc' ? { ...values, oidc: { ...(values.oidc as Values), secret: '' } } : values)
          if (result.restart_required) void offerRestart(result).then((restarted) => restarted && refreshSettings())
        }
      }
    )
  })

  return (
    <ServerSettingsCard id={id} title={title} subtitle={subtitle} onSubmit={(event) => void submit(event)}>
      <FormProvider {...form}>
        {children}
        {save.isPending ? (
          <p className={styles.adminSectionStatus} role="status">
            {t('common.saving')}
          </p>
        ) : error ? (
          <p
            ref={errorRef}
            className={cx(styles.adminSectionStatus, styles.adminSectionStatusError)}
            role="alert"
            tabIndex={-1}
          >
            {error}
          </p>
        ) : outcome && !accepted(outcome) ? (
          <p className={cx(styles.adminSectionStatus, styles.adminSectionStatusError)} role="status">
            {t('common.could_not_save')}
          </p>
        ) : formState.isDirty ? (
          <p className={styles.adminSectionStatus} role="status">
            {t('settings.unsaved_changes')}
          </p>
        ) : outcome ? (
          <p className={styles.adminSectionStatus} role="status">
            {outcomeText(outcome)}
          </p>
        ) : null}
        <Button type="submit" loading={save.isPending}>
          {t('common.save')}
        </Button>
        {outcome ? <Findings outcome={outcome} /> : null}
        {footer}
      </FormProvider>
    </ServerSettingsCard>
  )
}

interface SettingInputProps {
  name: string
  label: string
  type?: 'text' | 'number' | 'password'
  placeholder?: string
}

export function SettingInput({ name, label, type, placeholder }: SettingInputProps) {
  const { control } = useFormContext<Values>()
  return (
    <FormTextField
      control={control}
      name={name}
      className={styles.field}
      label={label}
      type={type}
      placeholder={placeholder}
    />
  )
}

export function SettingSwitch({ name, label }: { name: string; label: string }) {
  const { control } = useFormContext<Values>()
  return (
    <Controller
      control={control}
      name={name}
      render={({ field }) => <Switch checked={Boolean(field.value)} label={label} onChange={field.onChange} />}
    />
  )
}

export function SettingPath({ name, label, mode }: { name: string; label: string; mode: 'folder' | 'file' }) {
  const { t } = useI18n()
  const { getValues, setValue } = useFormContext<Values>()
  async function browse(): Promise<void> {
    const picked = await pickPath({ mode, start: String(getValues(name) ?? '') })
    if (picked !== null) setValue(name, picked, { shouldDirty: true })
  }
  return (
    <div className={styles.pathRow}>
      <SettingInput name={name} label={label} />
      <Button className={styles.pathButton} variant="outlined" onClick={() => void browse()}>
        {mode === 'folder' ? t('picker.browse_folder') : t('picker.browse_file')}
      </Button>
    </div>
  )
}

/** What leaving the field empty does, shown while it is empty. */
export function EmptyNote({ name, snapshot }: { name: string; snapshot: SettingsSnapshot }) {
  const { t } = useI18n()
  const { control } = useFormContext<Values>()
  const value = useWatch({ control, name })
  const key = snapshot.fields.find((field) => field.key === name)?.empty_means_key
  return key && !String(value ?? '').trim() ? <p className={styles.emptyNote}>{t(key)}</p> : null
}
