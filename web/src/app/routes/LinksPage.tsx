import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import type { Entry, OwnedShareLinkInfo, Perms, ShareLinkInfo } from '../../lib/api/client'
import { describeApiError } from '../../lib/api/error-text'
import { baseName, normalizePath } from '../../lib/api/path-utils'
import { formatDateNs } from '../../lib/i18n'
import { useI18n } from '../../lib/i18n/use-i18n'
import { adminLinksQuery } from '../../lib/query/admin'
import { queryClient } from '../../lib/query/client'
import { keys } from '../../lib/query/keys'
import { statQuery } from '../../lib/query/files'
import { sessionQuery } from '../../lib/query/session'
import { shareLinksQuery } from '../../lib/query/shares'
import { useDocumentTitle } from '../use-document-title'
import { ShareManageDialog } from '../../lib/ui/ShareManageDialog'
import { Icon } from '../../lib/ui/Icon'
import { VirtualList } from '../../lib/ui/VirtualList'
import './simple-pages.css'

type LinkRow = ShareLinkInfo | OwnedShareLinkInfo

const CAPABILITY_KEYS: readonly (keyof Perms)[] = ['read', 'download', 'write', 'create', 'delete', 'rename', 'move', 'share']

function isOwned(link: LinkRow): link is OwnedShareLinkInfo {
  return 'owner_name' in link
}

function isDropLink(link: ShareLinkInfo): boolean {
  return link.perms.create && !link.perms.read && !link.perms.download
}

function isExpired(link: ShareLinkInfo): boolean {
  return link.expires_ns !== null && Number(link.expires_ns) / 1e6 <= Date.now()
}

function isExhausted(link: ShareLinkInfo): boolean {
  return link.max_downloads !== null && link.downloads >= link.max_downloads
}

export function LinksPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const session = useQuery(sessionQuery())
  const isAdmin = session.data?.user.is_admin === true
  const own = useQuery({ ...shareLinksQuery(undefined), enabled: session.data !== undefined && !isAdmin })
  const all = useQuery({ ...adminLinksQuery(), enabled: isAdmin })
  const [managing, setManaging] = useState<LinkRow | null>(null)
  const [managingTarget, setManagingTarget] = useState<Entry | null>(null)
  const [resolvingPath, setResolvingPath] = useState<string | null>(null)
  const [targetErrorPath, setTargetErrorPath] = useState<string | null>(null)
  const [targetError, setTargetError] = useState<string | null>(null)
  const rows: LinkRow[] = isAdmin ? all.data ?? [] : own.data ?? []
  const activeQuery = isAdmin ? all : own
  const loading = session.isPending || activeQuery.isPending
  useDocumentTitle(t('links.title_stowcloud'))

  function isMine(link: LinkRow): boolean {
    return !isOwned(link) || link.owner === session.data?.user.id
  }

  function targetOf(link: LinkRow): Entry | undefined {
    const path = normalizePath(link.path)
    const state = queryClient.getQueryState<Entry>(keys.pathStat(path))
    return state?.isInvalidated ? undefined : state?.data
  }

  function capabilityLabel(key: keyof Perms): string {
    const labels: Record<keyof Perms, string> = {
      read: t('links.can_read'),
      download: t('links.can_download'),
      write: t('links.can_write'),
      create: t('links.can_create'),
      delete: t('links.can_delete'),
      rename: t('links.can_rename'),
      move: t('links.can_move'),
      share: t('links.can_share')
    }
    return labels[key]
  }

  function targetSummary(link: LinkRow): string {
    const target = targetOf(link)
    if (!target) return t('links.target_unknown')
    const capabilities = CAPABILITY_KEYS.filter((key) => link.perms[key]).map(capabilityLabel)
    const mutating = link.perms.write || link.perms.create || link.perms.delete || link.perms.rename || link.perms.move || link.perms.share
    const permissionSummary = !mutating && capabilities.length > 0 ? `${t('links.read_only')} (${capabilities.join(', ')})` : capabilities.join(', ') || t('links.target_unknown')
    return `${target.kind === 'dir' ? t('links.target_folder') : t('links.target_file')} - ${t('links.capabilities')}: ${permissionSummary}`
  }

  async function openManagement(link: LinkRow): Promise<void> {
    if (!isMine(link) || resolvingPath !== null) return
    const path = normalizePath(link.path)
    setTargetErrorPath(null)
    setTargetError(null)
    if (link.path.trim() === '') {
      setTargetErrorPath(path)
      setTargetError(t('links.target_unknown'))
      return
    }
    setResolvingPath(path)
    setManaging(null)
    setManagingTarget(null)
    try {
      const target = await queryClient.fetchQuery({ ...statQuery(path), staleTime: Infinity })
      setManagingTarget(target)
      setManaging(link)
    } catch (error) {
      setTargetErrorPath(path)
      setTargetError(describeApiError(error, t('links.target_unknown')))
    } finally {
      setResolvingPath(null)
    }
  }

  return (
    <section className="sc-links sc-secondary-page">
      <div className="sc-secondary-page__inner">
        <header className="sc-secondary-page__header">
          <button type="button" className="sc-route-back" aria-label={t('trash.go_back')} onClick={() => void navigate('/b')}><Icon name="chevron_left" /></button>
          <h1>{t('nav.links')}</h1>
          <button type="button" className="sc-route-icon-button" aria-label={t('common.refresh')} onClick={() => void activeQuery.refetch()}><Icon name="refresh" /></button>
        </header>
        {loading ? <div className="sc-secondary-page__loading"><mdui-circular-progress></mdui-circular-progress></div> : null}
        {activeQuery.error ? <p className="sc-secondary-page__error" role="alert">{describeApiError(activeQuery.error, t('links.could_not_load'))}</p> : null}
        {!loading && !activeQuery.error && rows.length === 0 ? <p className="sc-secondary-page__empty">{t('links.empty')}</p> : null}
        {rows.length > 0 ? <VirtualList className="sc-secondary-page__list sc-links__list" items={rows} itemKey={(link) => link.id} estimateSize={80} pinnedKeys={managing ? [managing.id] : undefined} renderItem={(link) => {
          const mine = isMine(link)
          const path = normalizePath(link.path)
          return <>
            <button type="button" className={mine ? 'sc-secondary-page__row sc-links__row' : 'sc-secondary-page__row sc-links__row sc-links__row--readonly'} aria-disabled={!mine || resolvingPath !== null ? 'true' : undefined} aria-busy={resolvingPath === path ? 'true' : undefined} aria-label={mine ? `${t('links.manage_link', { path: link.path })}. ${targetSummary(link)}` : t('links.owned_elsewhere', { path: link.path })} onClick={() => void openManagement(link)}>
              <span className="sc-secondary-page__icon"><Icon name={link.has_password ? 'lock' : 'link'} /></span>
              <span className="sc-secondary-page__text"><span className="sc-secondary-page__name">{link.path}</span>{mine ? <span className="sc-secondary-page__path">{targetSummary(link)}</span> : null}<span className="sc-links__meta">{isOwned(link) ? `${t('links.owner')}: ${link.owner_name || t('common.user', { id: link.owner })} - ` : ''}{isDropLink(link) ? t('share.kind_drop') : t('share.used_times', { count: link.max_downloads ? `${link.downloads}/${link.max_downloads}` : link.downloads })} - {link.has_password ? t('links.password_protected') : t('links.no_password')}</span></span>
              {resolvingPath === path && mine ? <mdui-circular-progress slot="end-icon"></mdui-circular-progress> : null}
              {isExpired(link) ? <span className="sc-links__flag sc-links__flag--warn">{t('links.expired')}</span> : isExhausted(link) ? <span className="sc-links__flag sc-links__flag--warn">{t('links.exhausted')}</span> : null}
            </button>
            {targetErrorPath === path && targetError ? <p className="sc-links__target-error" role="alert">{targetError}</p> : null}
          </>
        }} /> : null}
      </div>
      {managing ? <ShareManageDialog open path={normalizePath(managing.path)} targetName={baseName(managing.path) || managing.path} targetIsDir={managingTarget?.kind === 'dir'} onclose={() => { setManaging(null); setManagingTarget(null) }} /> : null}
    </section>
  )
}
