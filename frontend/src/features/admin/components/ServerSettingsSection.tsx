import { Fragment, useState } from 'react'
import { useFormContext } from 'react-hook-form'
import { t } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { useCopyText } from '../../../hooks/use-copy-text'
import { Button } from '../../../ui/Button'
import { ProgressCircular } from '../../../ui/ProgressCircular'
import { ServerSettingsCard } from './ServerSettingsCard'
import { EmptyNote, SettingInput, SettingPath, SettingSwitch, SettingsGroupCard, split } from './ServerSettingsForm'
import * as styles from './ServerSettingsSection.css'
import * as adminStyles from './admin.css'
import { useAdminSettings, useOidcEndpoints, type Hop, type SmbAgentReport } from '../api'

/* i18n */ ;('admin.server_network')
/* i18n */ ;('admin.server_search')
/* i18n */ ;('admin.server_security')
/* i18n */ ;('admin.server_storage_paths')
/* i18n */ ;('admin.server_transfers')
/* i18n */ ;('field.data_dir')
/* i18n */ ;('field.security_hardening')
/* i18n */ ;('field.smb_agent_socket')
/* i18n */ ;('field.smb_config_dir')
/* i18n */ ;('field.thumbnail_dir')
/* i18n */ ;('field.thumbnail_enabled')
/* i18n */ ;('settings.dir_is_writable')
/* i18n */ ;('settings.dir_will_be_created')
/* i18n */ ;('settings.empty_app_hosts_first_boot')
/* i18n */ ;('settings.empty_default_thumbs_dir')
/* i18n */ ;('settings.empty_disables_netbios_name')
/* i18n */ ;('settings.empty_trusted_proxies')
/* i18n */ ;('smb.agent_applied')
/* i18n */ ;('smb.agent_applied_with_warnings')
/* i18n */ ;('smb.agent_daemon_failed')
/* i18n */ ;('smb.agent_problem')
/* i18n */ ;('smb.agent_unreachable')
/* i18n */ ;('smb.deny_below_root_ignored')
/* i18n */ ;('smb.write_list_grants_more')
/* i18n */ ;('settings.out_of_range')
/* i18n */ ;('settings.host_list_empty')
/* i18n */ ;('settings.would_lock_you_out')
/* i18n */ ;('settings.proxy_range_is_everything')
/* i18n */ ;('settings.gid_zero_is_root')
/* i18n */ ;('settings.smb_render_failed')
/* i18n */ ;('settings.smb_config_dir_unavailable')
/* i18n */ ;('settings.path_is_not_a_directory')
/* i18n */ ;('settings.above_kernel_watch_limit')
/* i18n */ ;('settings.invalid_host')
/* i18n */ ;('settings.invalid_cidr')
/* i18n */ ;('settings.path_must_be_absolute')
/* i18n */ ;('settings.unknown_totp_policy')
/* i18n */ ;('settings.must_be_at_least_one')
/* i18n */ ;('settings.invalid_bind_address')
/* i18n */ ;('settings.unknown_hardening_policy')
/* i18n */ ;('settings.guard_has_no_bound')
/* i18n */ ;('settings.required_when_enabled')
/* i18n */ ;('settings.issuer_must_be_https')
/* i18n */ ;('settings.oidc_client_secret_required')
/* i18n */ ;('settings.canonical_url_not_an_app_host')
/* i18n */ ;('settings.duplicate_host')
/* i18n */ ;('settings.host_role_conflict')
/* i18n */ ;('settings.invalid_origin')

// Keys edited by a card below; the rest are listed read-only under storage.
const EDITABLE_KEYS = new Set([
  'bind',
  'app_hosts',
  'content_hosts',
  'allowed_origins',
  'compat_canonical_url',
  'trusted_proxies',
  'search.walk_deadline_fast_ms',
  'archive.max_concurrent',
  'db.size_guard',
  'db.max_bytes',
  'db.min_free_bytes',
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
const PATH_KEYS = ['data_dir', 'smb.config_dir']

const NAV = [
  ['server-smb', 'admin.server_smb'],
  ['server-search', 'admin.server_search'],
  ['server-network', 'admin.server_network'],
  ['server-transfers', 'admin.server_transfers'],
  ['server-security', 'admin.server_security'],
  ['server-storage', 'admin.server_storage_paths']
] as const

function fieldLabel(key: string): string {
  const name = `field.${key.replaceAll('.', '_')}`
  const value = t(name)
  return value === name ? key : value
}
function displayValue(value: unknown): string {
  if (value === null || value === undefined) return t('server.none')
  if (Array.isArray(value)) return value.length ? value.join(', ') : t('server.empty')
  if (typeof value === 'boolean') return value ? t('server.on') : t('server.off')
  return value === '' ? t('server.empty') : String(value)
}

export function ServerSettingsSection() {
  const { t } = useI18n()
  const settings = useAdminSettings()
  const snapshot = settings.data
  if (settings.isPending)
    return (
      <section className={adminStyles.section}>
        <h3 className={adminStyles.sectionTitle}>{t('server.server_settings')}</h3>
        <ProgressCircular size={40} />
      </section>
    )
  if (settings.isError || !snapshot)
    return (
      <section className={adminStyles.section}>
        <h3 className={adminStyles.sectionTitle}>{t('server.server_settings')}</h3>
        <p className={adminStyles.sectionError} role="alert">
          {t('server.could_not_load_server_settings')}
        </p>
      </section>
    )
  const stored = (key: string): unknown => snapshot.fields.find((field) => field.key === key)?.value
  const otherFields = snapshot.fields.filter(
    (item) => !EDITABLE_KEYS.has(item.key) && !PATH_KEYS.includes(item.key) && item.key !== 'symlink_policy'
  )
  const watchLimit = snapshot.fields.find((field) => field.key === 'watch.hot_set_max')?.range?.max ?? ''
  return (
    <section className={adminStyles.section}>
      <h3 className={adminStyles.sectionTitle}>{t('server.server_settings')}</h3>
      <p className={adminStyles.sectionHint}>{t('server.settings_stored_in_database')}</p>
      <nav className={styles.nav} aria-label={t('admin.server_settings_navigation')}>
        <div className={styles.navItems}>
          {NAV.map(([id, key]) => (
            <button
              key={id}
              type="button"
              className={styles.navButton}
              onClick={() => document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })}
            >
              {t(key)}
            </button>
          ))}
        </div>
      </nav>

      <SettingsGroupCard
        group="smb"
        id="server-smb"
        title="SMB"
        subtitle={t('server.all_apply_immediately_no_restart')}
        snapshot={snapshot}
        footer={snapshot.smb_agent ? <SmbAgentStatus report={snapshot.smb_agent} /> : null}
      >
        <SettingSwitch name="smb.enabled" label={t('server.enable_smb')} />
        <SettingInput name="smb.workgroup" label={t('server.workgroup')} />
        <SettingInput name="smb.server_name" label={t('server.smb_server_name')} />
        <EmptyNote name="smb.server_name" snapshot={snapshot} />
        <SettingInput name="smb.service_user" label={t('server.service_account_name')} />
        <SettingSwitch name="smb.allow_public_bind" label={t('server.allow_access_from_outside_private')} />
        <TotpPolicySelect />
        <SettingInput name="smb.service_gid" label={t('server.service_account_gid')} type="number" />
        <SettingInput name="smb.interfaces" label={t('settings.smb_interfaces')} />
        <p className={adminStyles.sectionHint}>{t('settings.smb_interfaces_hint')}</p>
      </SettingsGroupCard>

      <ServerSettingsCard
        id="server-storage"
        title={t('server.storage_paths')}
        subtitle={t('settings.paths_readonly_reason')}
      >
        <dl className={styles.other}>
          {PATH_KEYS.map((key) => (
            <div key={key}>
              <dt className={styles.otherLabel}>{fieldLabel(key)}</dt>
              <dd className={styles.otherValue}>{displayValue(stored(key))}</dd>
            </div>
          ))}
          <div>
            <dt className={styles.otherLabel}>{fieldLabel('symlink_policy')}</dt>
            <dd className={styles.otherValue}>
              <span className={styles.reason}>{t('settings.readonly_per_share_symlink_policy')}</span>
            </dd>
          </div>
        </dl>
        {otherFields.length ? (
          <>
            <h5 className={styles.adminSectionSubhead}>{t('settings.settings_sections')}</h5>
            <dl className={styles.other}>
              {otherFields.map((item) => (
                <div key={item.key}>
                  <dt className={styles.otherLabel}>{fieldLabel(item.key)}</dt>
                  <dd className={styles.otherValue}>{displayValue(item.value)}</dd>
                </div>
              ))}
            </dl>
          </>
        ) : null}
      </ServerSettingsCard>

      <SettingsGroupCard
        group="search"
        id="server-search"
        title={t('common.search')}
        subtitle={t('server.all_apply_immediately_no_restart')}
        snapshot={snapshot}
      >
        <SettingInput name="search.max_concurrent_fast" label={t('server.concurrent_fast_searches')} type="number" />
        <SettingInput name="search.walk_deadline_fast_ms" label={t('server.fast_search_timeout_ms')} type="number" />
      </SettingsGroupCard>

      <SettingsGroupCard
        group="archive"
        id="server-transfers"
        title={t('server.zip_download')}
        subtitle={t('server.applies_immediately_no_restart_needed')}
        snapshot={snapshot}
      >
        <SettingInput name="archive.max_concurrent" label={t('server.concurrent_zip_streams')} type="number" />
      </SettingsGroupCard>

      <SettingsGroupCard
        group="thumbnail"
        id="server-thumbnail"
        title={t('server.thumbnail_settings')}
        subtitle={t('server.thumbnail_enabled_description')}
        snapshot={snapshot}
      >
        <SettingSwitch name="thumbnail.enabled" label={t('server.thumbnail_enabled')} />
        <SettingPath name="thumbnail.dir" label={t('server.thumbnail_storage_dir')} mode="folder" />
        <EmptyNote name="thumbnail.dir" snapshot={snapshot} />
        <p className={adminStyles.sectionHint}>{t('server.thumbnail_storage_dir_description')}</p>
      </SettingsGroupCard>

      <SettingsGroupCard
        group="network"
        id="server-network"
        title={t('server.network')}
        subtitle={t('server.applies_immediately_no_restart_needed')}
        snapshot={snapshot}
      >
        <SettingInput name="app_hosts" label={t('server.app_hosts_comma_separated')} />
        <EmptyNote name="app_hosts" snapshot={snapshot} />
        <p className={adminStyles.sectionHint}>{t('server.app_hosts_hint')}</p>
        <SettingInput name="trusted_proxies" label={t('server.trusted_proxies_comma_separated')} />
        <EmptyNote name="trusted_proxies" snapshot={snapshot} />
        {snapshot.hop ? <HopHint hop={snapshot.hop} /> : null}
        <SettingInput name="content_hosts" label={t('server.content_hosts_comma_separated')} />
        <SettingInput name="allowed_origins" label={t('server.allowed_origins_cors_comma_separated')} />
        <SettingInput name="compat_canonical_url" label={t('server.compat_canonical_url')} />
        <SettingInput name="bind" label={t('server.bind_address')} />
        <p className={adminStyles.sectionHint}>{t('server.bind_address_hint')}</p>
      </SettingsGroupCard>

      <SettingsGroupCard
        group="db"
        id="server-db"
        title={t('server.database')}
        subtitle={t('settings.db_size_guard_hint')}
        snapshot={snapshot}
      >
        <SettingSwitch name="db.size_guard" label={t('settings.db_size_guard')} />
        <SettingInput name="db.max_bytes" label={t('settings.db_max_bytes')} type="number" />
        <SettingInput name="db.min_free_bytes" label={t('settings.db_min_free_bytes')} type="number" />
      </SettingsGroupCard>

      <SettingsGroupCard
        group="watch"
        id="server-watch"
        title={t('server.file_watching')}
        subtitle={t('server.what_file_watching_is_for')}
        snapshot={snapshot}
      >
        <SettingInput name="watch.hot_set_max" label={t('server.maximum_folders_watched_at_once')} type="number" />
        <SettingInput name="watch.full_threshold" label={t('server.changes_before_a_full_rescan')} type="number" />
        <p className={adminStyles.sectionHint}>{t('settings.within_kernel_watch_limit', { limit: watchLimit })}</p>
      </SettingsGroupCard>

      <SettingsGroupCard
        group="homes"
        id="server-homes"
        title={t('server.home_folders')}
        subtitle={t('server.home_folders_hint')}
        snapshot={snapshot}
      >
        <SettingSwitch name="homes.enabled" label={t('server.enable_home_folders')} />
        <SettingPath name="homes.root" label={t('server.homes_root_path')} mode="folder" />
      </SettingsGroupCard>

      <SettingsGroupCard
        group="rate"
        id="server-rate"
        title={t('server.request_rate')}
        subtitle={t('server.what_the_request_rate_is_for')}
        snapshot={snapshot}
      >
        <SettingInput name="rate.per_sec" label={t('server.requests_per_second')} type="number" />
        <SettingInput name="rate.burst" label={t('server.burst_allowance')} type="number" />
      </SettingsGroupCard>

      <SettingsGroupCard
        group="oidc"
        id="server-security"
        title={t('settings.single_sign_on')}
        subtitle={t('server.single_sign_on_hint')}
        snapshot={snapshot}
        footer={<OidcEndpoints />}
      >
        <SettingSwitch name="oidc.enabled" label={t('settings.oidc_enable')} />
        <SettingInput name="oidc.issuer" label={t('settings.oidc_issuer')} />
        <SettingInput name="oidc.client_id" label={t('settings.oidc_client_id')} />
        <SettingInput
          name="oidc.secret"
          label={t('common.password')}
          type="password"
          placeholder={t('settings.secret_is_write_only')}
        />
        <SettingSwitch name="oidc.public_client" label={t('settings.oidc_public_client')} />
        <SettingInput name="oidc.scopes" label={t('settings.oidc_scopes')} />
        <SettingInput name="oidc.display_name" label={t('settings.oidc_display_name')} />
        <SettingSwitch name="oidc.allow_private_endpoints" label={t('settings.oidc_allow_private_endpoints')} />
        <SettingPath name="oidc.ca_cert_file" label={t('field.oidc_ca_cert_file')} mode="file" />
        <p className={adminStyles.sectionHint}>{t('server.connected_accounts_cannot_use_smb')}</p>
      </SettingsGroupCard>
    </section>
  )
}

function TotpPolicySelect() {
  const { t } = useI18n()
  const { register } = useFormContext()
  return (
    <label className={styles.selectLabel}>
      {t('server.smb_access_2fa_users')}
      <select className={styles.select} {...register('smb.totp_policy')}>
        <option value="require_separate">{t('server.require_separate_smb_password_default')}</option>
        <option value="block">{t('server.smb_not_allowed')}</option>
      </select>
    </label>
  )
}

function SmbAgentStatus({ report }: { report: SmbAgentReport }) {
  const { t } = useI18n()
  return (
    <div className={report.ok ? adminStyles.sectionHint : adminStyles.warning} role={report.ok ? 'status' : 'alert'}>
      <p>
        {t(report.key, {
          shares: report.shares?.length ?? 0,
          interfaces: report.interfaces,
          smbd: report.smbd,
          error: report.detail ?? ''
        })}
      </p>
      {report.missing_paths?.length ? (
        <p>{t('smb.agent_missing_paths', { paths: report.missing_paths.join(', ') })}</p>
      ) : null}
      {report.missing_passdb?.length ? (
        <p>{t('smb.agent_missing_passdb', { users: report.missing_passdb.join(', ') })}</p>
      ) : null}
    </div>
  )
}

/** Where requests come from, and a way to trust that hop when it forwards headers that are ignored. */
function HopHint({ hop }: { hop: Hop }) {
  const { t } = useI18n()
  const { getValues, setValue } = useFormContext()
  const address = hop.peer ?? hop.client ?? ''
  function trustObserved(): void {
    const cidr = address.includes(':') ? `${address}/128` : `${address}/32`
    const proxies = new Set([...split(String(getValues('trusted_proxies') ?? '')), cidr])
    setValue('trusted_proxies', [...proxies].join(', '), { shouldDirty: true })
  }
  return (
    <>
      <p className={adminStyles.sectionHint}>
        {t('server.requests_arriving_from', { address: address || t('server.unknown_address') })}{' '}
        {hop.peer_trusted ? t('server.peer_trusted') : t('server.peer_not_trusted')}
      </p>
      {!hop.peer_trusted && hop.forwarded_seen ? (
        <div className={adminStyles.warning} role="alert">
          <p>{t('server.forwarding_headers_ignored_hint', { address })}</p>
          <Button variant="text" onClick={trustObserved} disabled={!address}>
            {t('server.add_observed_address_to_trusted', { address })}
          </Button>
        </div>
      ) : null}
    </>
  )
}

/** The redirect URIs to register with the identity provider. */
function OidcEndpoints() {
  const { t } = useI18n()
  const endpoints = useOidcEndpoints().data
  const copy = useCopyText()
  const [announcement, setAnnouncement] = useState('')
  if (!endpoints || !(endpoints.redirect_uris.length || endpoints.post_logout_redirect_uris.length)) return null
  const lists = [
    [t('settings.oidc_effective_redirect_uris'), endpoints.redirect_uris],
    [t('settings.oidc_post_logout_redirect_uris'), endpoints.post_logout_redirect_uris]
  ] as const
  return (
    <div className={styles.endpoints}>
      <p className={adminStyles.sectionHint}>{t('settings.oidc_endpoints_hint')}</p>
      {lists.map(([name, uris]) => (
        <Fragment key={name}>
          <h5 className={styles.adminSectionSubhead}>{name}</h5>
          {uris.map((uri) => (
            <div className={styles.endpointRow} key={uri}>
              <code className={styles.endpointUri}>{uri}</code>
              <Button
                variant="text"
                ariaLabel={t('common.copy_named', { name: uri })}
                onClick={() =>
                  copy.mutate(uri, { onSuccess: () => setAnnouncement(t('settings.oidc_endpoint_copied', { name })) })
                }
              >
                {t('common.copy')}
              </Button>
            </div>
          ))}
        </Fragment>
      ))}
      <p className={styles.announce} aria-live="polite">
        {announcement}
      </p>
    </div>
  )
}
