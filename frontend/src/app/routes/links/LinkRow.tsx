import type { OwnedShareLinkInfo } from '../../../lib/api/client'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { Icon } from '../../../lib/ui/Icon'
import { isDropLink, isExpired, isExhausted, type LinkRow } from './use-link-management'
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
  const usage = isDropLink(link) ? t('share.kind_drop') : t('share.used_times', { count: link.max_downloads ? `${link.downloads}/${link.max_downloads}` : link.downloads })
  const status = isExpired(link) ? t('links.expired') : isExhausted(link) ? t('links.exhausted') : null
  return <>
    <button type="button" className={mine ? 'sc-secondary-page-row sc-links-row' : 'sc-secondary-page-row sc-links-row sc-links-row--readonly'} aria-disabled={!mine || resolving ? 'true' : undefined} aria-busy={resolving ? 'true' : undefined} aria-label={mine ? `${t('links.manage_link', { path: link.path })}. ${targetSummary}` : t('links.owned_elsewhere', { path: link.path })} onClick={onOpen}>
      <span className="sc-secondary-page-icon"><Icon name={link.has_password ? 'lock' : 'link'} /></span>
      <span className="sc-secondary-page-text"><span className="sc-secondary-page-name">{link.path}</span>{mine ? <span className="sc-secondary-page-path">{targetSummary}</span> : null}<span className="sc-links-meta">{meta}{usage} - {link.has_password ? t('links.password_protected') : t('links.no_password')}</span></span>
      {resolving && mine ? <mdui-circular-progress slot="end-icon"></mdui-circular-progress> : null}
      {status ? <span className="sc-links-flag sc-links-flag--warn">{status}</span> : null}
    </button>
  </>
}

export type { LinkRow }
