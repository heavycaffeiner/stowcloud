import type { AdminUser } from '../../lib/api/client'
import { Button } from '../../lib/ui/Button'
import { Icon } from '../../lib/ui/Icon'
import { ListItem } from '../../lib/ui/ListItem'

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

export function UserManagementRow({ user, t, locked, toggling, quotaLabel, onToggle, onQuota, onGrants, onOidc, onPassword, onDelete }: UserManagementRowProps) {
  return <ListItem
    headline={<><span className="sc-admin-row-name">{user.display_name || user.name}</span>{user.is_admin ? <span className="sc-admin-chip">{t('common.administrator')}</span> : null}{user.disabled ? <span className="sc-admin-chip sc-admin-chip-muted">{t('user.inactive')}</span> : null}</>}
    supporting={user.name}
    trailing={<><mdui-switch checked={!user.disabled} disabled={locked} title={locked ? t('user.last_active_administrator_cannot_deactivated') : undefined} aria-label={t('user.enable_account', { name: user.name })} onChange={onToggle} /><button className="sc-admin-chip sc-admin-chip-muted" type="button" onClick={onQuota}>{quotaLabel}</button><div className="sc-admin-row-actions"><Button variant="text" square ariaLabel={t('common.manage_folders_visible', { name: user.name })} onClick={onGrants}><Icon name="account_tree" /></Button><Button variant="text" square ariaLabel={t('oidc.manage_single_sign_connection', { name: user.name })} onClick={onOidc}><Icon name="link" /></Button><Button variant="text" square ariaLabel={t('password.change_password')} onClick={onPassword}><Icon name="lock" /></Button><Button variant="text" danger square ariaLabel={t('common.delete_2', { name: user.name })} disabled={locked || toggling} onClick={onDelete}><Icon name="delete" /></Button></div></>}
  />
}
