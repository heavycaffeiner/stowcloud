import type { ReactNode } from 'react'
import type { ServerSettingsGroup } from './hooks/server-settings-state'
import { ServerSettingsCard } from './ServerSettingsCard'
import * as styles from './ServerSmbCard.css'
import * as adminStyles from './admin.css'
import type { SettingsSnapshot } from './api'
type Translator = (key: string, params?: Record<string, string | number>) => string
type Values = Record<string, unknown>
type Input = (
  key: string,
  label: string,
  options?: { type?: 'text' | 'number' | 'password'; min?: number; max?: number; placeholder?: string }
) => ReactNode
type Toggle = (key: string, label: string) => ReactNode
type SaveButton = (group: ServerSettingsGroup) => ReactNode

interface ServerSmbCardProps {
  t: Translator
  values: Values
  snapshot: SettingsSnapshot
  input: Input
  toggle: Toggle
  saveButton: SaveButton
  emptyNote: (key: string) => ReactNode
  onValueChange: (key: string, value: unknown) => void
}

export function ServerSmbCard({
  t,
  values,
  snapshot,
  input,
  toggle,
  saveButton,
  emptyNote,
  onValueChange
}: ServerSmbCardProps) {
  return (
    <ServerSettingsCard id="server-smb" title="SMB" subtitle={t('server.all_apply_immediately_no_restart')}>
      {toggle('smb.enabled', t('server.enable_smb'))}
      {input('smb.workgroup', t('server.workgroup'))}
      {input('smb.server_name', t('server.smb_server_name'))}
      {!String(values['smb.server_name'] ?? '').trim() ? emptyNote('smb.server_name') : null}
      {input('smb.service_user', t('server.service_account_name'))}
      {toggle('smb.allow_public_bind', t('server.allow_access_from_outside_private'))}
      <label className={styles.selectLabel}>
        {t('server.smb_access_2fa_users')}
        <select
          className={styles.select}
          value={String(values['smb.totp_policy'] ?? 'require_separate')}
          onChange={(event) => onValueChange('smb.totp_policy', event.currentTarget.value)}
        >
          <option value="require_separate">{t('server.require_separate_smb_password_default')}</option>
          <option value="block">{t('server.smb_not_allowed')}</option>
        </select>
      </label>
      {input('smb.service_gid', t('server.service_account_gid'), { type: 'number' })}
      {input('smb.interfaces', t('settings.smb_interfaces'))}
      <p className={adminStyles.sectionHint}>{t('settings.smb_interfaces_hint')}</p>
      {saveButton('smb')}
      {snapshot.smb_agent ? (
        <div
          className={snapshot.smb_agent.ok ? adminStyles.sectionHint : adminStyles.warning}
          role={snapshot.smb_agent.ok ? 'status' : 'alert'}
        >
          <p>
            {t(snapshot.smb_agent.key, {
              shares: snapshot.smb_agent.shares?.length ?? 0,
              interfaces: snapshot.smb_agent.interfaces,
              smbd: snapshot.smb_agent.smbd,
              error: snapshot.smb_agent.detail ?? ''
            })}
          </p>
          {snapshot.smb_agent.missing_paths?.length ? (
            <p>{t('smb.agent_missing_paths', { paths: snapshot.smb_agent.missing_paths.join(', ') })}</p>
          ) : null}
          {snapshot.smb_agent.missing_passdb?.length ? (
            <p>{t('smb.agent_missing_passdb', { users: snapshot.smb_agent.missing_passdb.join(', ') })}</p>
          ) : null}
        </div>
      ) : null}
    </ServerSettingsCard>
  )
}
