import { Button } from '../../ui/Button'
import { Icon } from '../../ui/Icon'
import { ListItem } from '../../ui/ListItem'
import { Switch } from '../../ui/Switch'
import * as styles from './UserManagementRow.css'
import * as adminStyles from './admin.css'
import { cx } from '../../ui/cx'
import type { AdminUser } from './api'

type Translator = (key: string, params?: Record<string, string | number>) => string

interface UserManagementRowProps {
  user: AdminUser
  t: Translator
  locked: boolean
  toggling: boolean
  quotaLabel: string
  onToggle: () => void
  onQuota: () => void
  onGrants: () => void
  onOidc: () => void
  onPassword: () => void
  onDelete: () => void
}

export function UserManagementRow({
  user,
  t,
  locked,
  toggling,
  quotaLabel,
  onToggle,
  onQuota,
  onGrants,
  onOidc,
  onPassword,
  onDelete
}: UserManagementRowProps) {
  return (
    <ListItem
      headline={
        <>
          <span className={adminStyles.rowName}>{user.display_name || user.name}</span>
          {user.is_admin ? <span className={adminStyles.chip}>{t('common.administrator')}</span> : null}
          {user.disabled ? <span className={cx(adminStyles.chip, styles.chipMuted)}>{t('user.inactive')}</span> : null}
        </>
      }
      supporting={user.name}
      trailing={
        <>
          <span title={locked ? t('user.last_active_administrator_cannot_deactivated') : undefined}>
            <Switch
              checked={!user.disabled}
              disabled={locked}
              label={t('user.enable_account', { name: user.name })}
              showLabel={false}
              onChange={onToggle}
            />
          </span>
          <button className={cx(adminStyles.chip, styles.chipMuted)} type="button" onClick={onQuota}>
            {quotaLabel}
          </button>
          <div className={adminStyles.rowActions}>
            <Button
              variant="text"
              square
              ariaLabel={t('common.manage_folders_visible', { name: user.name })}
              onClick={onGrants}
            >
              <Icon name="account_tree" />
            </Button>
            <Button
              variant="text"
              square
              ariaLabel={t('oidc.manage_single_sign_connection', { name: user.name })}
              onClick={onOidc}
            >
              <Icon name="link" />
            </Button>
            <Button variant="text" square ariaLabel={t('password.change_password')} onClick={onPassword}>
              <Icon name="lock" />
            </Button>
            <Button
              variant="text"
              danger
              square
              ariaLabel={t('common.delete_2', { name: user.name })}
              disabled={locked || toggling}
              onClick={onDelete}
            >
              <Icon name="delete" />
            </Button>
          </div>
        </>
      }
    />
  )
}
