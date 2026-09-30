import type { OwnedShareLinkInfo } from '../../../lib/api/client'
import { useI18n } from '../../../hooks/use-i18n'
import { Icon } from '../../../ui/Icon'
import { isDropLink, isExpired, isExhausted, type LinkRow } from './hooks/use-link-management'
import { ProgressCircular } from '../../../ui/ProgressCircular'
import * as styles from './LinkRow.css'
import * as secondaryPageShellStyles from '../secondary/SecondaryPageShell.css'
import { cx } from '../../../ui/cx'
interface LinkRowProps {
  link: LinkRow
  mine: boolean
  resolving: boolean
  targetSummary: string
  onOpen: () => void
}

function isOwned(link: LinkRow): link is OwnedShareLinkInfo {
  return 'owner_name' in link
}

export function LinkListRow({ link, mine, resolving, targetSummary, onOpen }: LinkRowProps) {
  const { t } = useI18n()
  const meta = isOwned(link) ? `${t('links.owner')}: ${link.owner_name || t('common.user', { id: link.owner })} - ` : ''
  const usage = isDropLink(link)
    ? t('share.kind_drop')
    : t('share.used_times', { count: link.max_downloads ? `${link.downloads}/${link.max_downloads}` : link.downloads })
  const status = isExpired(link) ? t('links.expired') : isExhausted(link) ? t('links.exhausted') : null
  return (
    <>
      <button
        type="button"
        className={cx(secondaryPageShellStyles.row, styles.row, !mine && styles.rowReadonly)}
        aria-disabled={!mine || resolving ? 'true' : undefined}
        aria-busy={resolving ? 'true' : undefined}
        aria-label={
          mine
            ? `${t('links.manage_link', { path: link.path })}. ${targetSummary}`
            : t('links.owned_elsewhere', { path: link.path })
        }
        onClick={onOpen}
      >
        <span className={secondaryPageShellStyles.icon}>
          <Icon name={link.has_password ? 'lock' : 'link'} />
        </span>
        <span className={secondaryPageShellStyles.text}>
          <span className={secondaryPageShellStyles.name}>{link.path}</span>
          {mine ? <span className={secondaryPageShellStyles.path}>{targetSummary}</span> : null}
          <span className={styles.meta}>
            {meta}
            {usage} - {link.has_password ? t('links.password_protected') : t('links.no_password')}
          </span>
        </span>
        {resolving && mine ? <ProgressCircular className={styles.progress} /> : null}
        {status ? <span className={styles.flag}>{status}</span> : null}
      </button>
    </>
  )
}

export type { LinkRow }
