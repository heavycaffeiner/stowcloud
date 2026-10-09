import { formatBytes } from '../../../lib/format/bytes'
import { useI18n } from '../../../hooks/use-i18n'
import { StowBadge, StowButton, StowIconButton, StowListItem, StowSwitch } from '@/shared/ui'
import * as adminStyles from './admin.css'
import type { AdminUser } from '../api'

interface UserManagementRowProps {
  user: AdminUser
  locked: boolean
  toggling: boolean
  onToggle: () => void
  onQuota: () => void
  onGrants: () => void
  onOidc: () => void
  onPassword: () => void
  onDelete: () => void
}

export function UserManagementRow({
  user,
  locked,
  toggling,
  onToggle,
  onQuota,
  onGrants,
  onOidc,
  onPassword,
  onDelete
}: UserManagementRowProps) {
  const { t } = useI18n()
  const used = formatBytes(Number(BigInt(user.usage_bytes)))
  return (
    <StowListItem
      headline={
        <>
          <span className={adminStyles.rowName}>{user.display_name || user.name}</span>
          {user.is_admin ? <StowBadge>{t('common.administrator')}</StowBadge> : null}
          {user.disabled ? <StowBadge>{t('user.inactive')}</StowBadge> : null}
          {user.home?.enabled ? (
            <StowBadge tone={user.home.ready ? undefined : 'warning'}>
              {user.home.ready ? t('user.home_ready') : t('user.home_not_ready')}
            </StowBadge>
          ) : null}
        </>
      }
      supporting={user.name}
      trailing={
        <>
          <span title={locked ? t('user.last_active_administrator_cannot_deactivated') : undefined}>
            <StowSwitch
              checked={!user.disabled}
              disabled={locked}
              label={t('user.enable_account', { name: user.name })}
              hideLabel
              onChange={onToggle}
            />
          </span>
          <StowButton variant="outlined" onClick={onQuota}>
            {user.quota_bytes ? `${used} / ${formatBytes(Number(BigInt(user.quota_bytes)))}` : t('user.used', { used })}
          </StowButton>
          <div className={adminStyles.rowActions}>
            <StowIconButton
              label={t('common.manage_folders_visible', { name: user.name })}
              onClick={onGrants}
              icon="account_tree"
            />
            <StowIconButton
              label={t('oidc.manage_single_sign_connection', { name: user.name })}
              onClick={onOidc}
              icon="link"
            />
            <StowIconButton label={t('password.change_password')} onClick={onPassword} icon="lock" />
            <StowIconButton
              danger
              label={t('common.delete_2', { name: user.name })}
              disabled={locked || toggling}
              onClick={onDelete}
              icon="delete"
            />
          </div>
        </>
      }
    />
  )
}
