import { ALL_GRANT_PERMS, type GrantPermName } from '../../lib/api/client'
import { Checkbox } from '../../lib/ui/Checkbox'

type Translate = (key: string, params?: Record<string, string | number>) => string

export interface PermissionSets {
  allow: Set<GrantPermName>
  deny: Set<GrantPermName>
}

export interface GrantPermissionGridProps extends PermissionSets {
  setAllow: (next: Set<GrantPermName>) => void
  setDeny: (next: Set<GrantPermName>) => void
  permLabel: Record<GrantPermName, string>
  t: Translate
}

export function GrantPermissionGrid({ allow, deny, setAllow, setDeny, permLabel, t }: GrantPermissionGridProps) {
  return (
    <div className="sc-admin-permgrid">
      <div className="sc-admin-permgrid-head"><span /> <span>{t('grant.allow')}</span><span>{t('grant.deny')}</span></div>
      {ALL_GRANT_PERMS.map((permission) => (
        <div className="sc-admin-permgrid-row" key={permission}>
          <span>{permLabel[permission]}</span>
          <span className="sc-admin-permgrid-cell">
            <Checkbox checked={allow.has(permission)} hideLabel label={t('grant.allow_2', { perm: permLabel[permission] })} onChange={(checked) => setAllow(togglePermission(allow, permission, checked))} />
          </span>
          <span className="sc-admin-permgrid-cell">
            <Checkbox checked={deny.has(permission)} hideLabel label={t('grant.deny_2', { perm: permLabel[permission] })} onChange={(checked) => setDeny(togglePermission(deny, permission, checked))} />
          </span>
        </div>
      ))}
    </div>
  )
}

function togglePermission(set: Set<GrantPermName>, permission: GrantPermName, checked: boolean): Set<GrantPermName> {
  const next = new Set(set)
  if (checked) next.add(permission)
  else next.delete(permission)
  return next
}
