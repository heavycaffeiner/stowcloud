import { useI18n } from '../../../hooks/use-i18n'
import { Checkbox } from '../../../ui/Checkbox'
import * as styles from './GrantPermissionGrid.css'
import { ALL_GRANT_PERMS, type GrantPermName } from '../api'

export function usePermLabels(): Record<GrantPermName, string> {
  const { t } = useI18n()
  return {
    read: t('common.read'),
    write: t('grant.write'),
    create: t('grant.create'),
    delete: t('common.delete'),
    rename: t('grant.rename'),
    move: t('common.move'),
    share: t('common.share_links'),
    download: t('common.download')
  }
}

interface GrantPermissionGridProps {
  allow: readonly GrantPermName[]
  deny: readonly GrantPermName[]
  onAllowChange: (next: GrantPermName[]) => void
  onDenyChange: (next: GrantPermName[]) => void
}

export function GrantPermissionGrid({ allow, deny, onAllowChange, onDenyChange }: GrantPermissionGridProps) {
  const { t } = useI18n()
  const permLabel = usePermLabels()
  return (
    <div className={styles.root}>
      <div className={styles.head}>
        <span /> <span>{t('grant.allow')}</span>
        <span>{t('grant.deny')}</span>
      </div>
      {ALL_GRANT_PERMS.map((permission) => (
        <div className={styles.row} key={permission}>
          <span>{permLabel[permission]}</span>
          <span className={styles.cell}>
            <Checkbox
              checked={allow.includes(permission)}
              hideLabel
              label={t('grant.allow_2', { perm: permLabel[permission] })}
              onChange={(checked) => onAllowChange(togglePermission(allow, permission, checked))}
            />
          </span>
          <span className={styles.cell}>
            <Checkbox
              checked={deny.includes(permission)}
              hideLabel
              label={t('grant.deny_2', { perm: permLabel[permission] })}
              onChange={(checked) => onDenyChange(togglePermission(deny, permission, checked))}
            />
          </span>
        </div>
      ))}
    </div>
  )
}

function togglePermission(
  list: readonly GrantPermName[],
  permission: GrantPermName,
  checked: boolean
): GrantPermName[] {
  const rest = list.filter((item) => item !== permission)
  return checked ? [...rest, permission] : rest
}
