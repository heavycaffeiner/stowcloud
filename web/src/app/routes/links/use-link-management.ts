import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { Entry, OwnedShareLinkInfo, Perms, ShareLinkInfo } from '../../../lib/api/client'
import { describeApiError } from '../../../lib/api/error-text'
import { normalizePath } from '../../../lib/api/path-utils'
import { queryClient } from '../../../lib/query/client'
import { keys } from '../../../lib/query/keys'
import { statQuery } from '../../../lib/query/files'
import { useRouteStore } from '../../use-route-store'

type LinkRow = ShareLinkInfo | OwnedShareLinkInfo
export type { LinkRow }

const CAPABILITY_KEYS: readonly (keyof Perms)[] = ['read', 'download', 'write', 'create', 'delete', 'rename', 'move', 'share']

type Translator = (key: string, params?: Record<string, string | number>) => string

export interface LinkManagementState {
  managing: LinkRow | null
  managingTarget: Entry | null
  resolvingPath: string | null
  targetErrorPath: string | null
  targetError: string | null
}

function isOwned(link: LinkRow): link is OwnedShareLinkInfo {
  return 'owner_name' in link
}

export function useLinkManagement(userId: number | undefined, t: Translator) {
  const [state, setState] = useRouteStore<LinkManagementState>({ managing: null, managingTarget: null, resolvingPath: null, targetErrorPath: null, targetError: null })
  const query = useQueryClient()

  const isMine = useCallback((link: LinkRow): boolean => !isOwned(link) || link.owner === userId, [userId])

  const targetOf = useCallback((link: LinkRow): Entry | undefined => {
    const path = normalizePath(link.path)
    const cached = queryClient.getQueryState<Entry>(keys.pathStat(path))
    return cached?.isInvalidated ? undefined : cached?.data
  }, [])

  const capabilityLabel = useCallback((key: keyof Perms): string => {
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
  }, [t])

  const targetSummary = useCallback((link: LinkRow): string => {
    const target = targetOf(link)
    if (!target) return t('links.target_unknown')
    const capabilities = CAPABILITY_KEYS.filter((key) => link.perms[key]).map(capabilityLabel)
    const mutating = link.perms.write || link.perms.create || link.perms.delete || link.perms.rename || link.perms.move || link.perms.share
    const permissionSummary = !mutating && capabilities.length > 0 ? `${t('links.read_only')} (${capabilities.join(', ')})` : capabilities.join(', ') || t('links.target_unknown')
    return `${target.kind === 'dir' ? t('links.target_folder') : t('links.target_file')} - ${t('links.capabilities')}: ${permissionSummary}`
  }, [capabilityLabel, t, targetOf])

  const openManagement = useCallback(async (link: LinkRow): Promise<void> => {
    if (!isMine(link) || state.resolvingPath !== null) return
    const path = normalizePath(link.path)
    setState({ targetErrorPath: null, targetError: null })
    if (link.path.trim() === '') {
      setState({ targetErrorPath: path, targetError: t('links.target_unknown') })
      return
    }
    setState({ resolvingPath: path, managing: null, managingTarget: null })
    try {
      const target = await query.fetchQuery({ ...statQuery(path), staleTime: Infinity })
      setState({ managingTarget: target, managing: link })
    } catch (error) {
      setState({ targetErrorPath: path, targetError: describeApiError(error, t('links.target_unknown')) })
    } finally {
      setState({ resolvingPath: null })
    }
  }, [isMine, query, setState, state.resolvingPath, t])

  return { ...state, setState, isMine, targetSummary, openManagement }
}

export function isDropLink(link: ShareLinkInfo): boolean {
  return link.perms.create && !link.perms.read && !link.perms.download
}

export function isExpired(link: ShareLinkInfo): boolean {
  return link.expires_ns !== null && Number(link.expires_ns) / 1e6 <= Date.now()
}

export function isExhausted(link: ShareLinkInfo): boolean {
  return link.max_downloads !== null && link.downloads >= link.max_downloads
}
