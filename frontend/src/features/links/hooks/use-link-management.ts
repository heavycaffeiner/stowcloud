import { useState } from 'react'
import { describeApiError } from '../../../api/error-text'
import { t } from '../../../lib/i18n'
import { baseName, normalizePath } from '../../../lib/path-utils'
import { useFileCache } from '../../files/api'
import { openShareManager } from '../../shares/ShareManageDialog'
import type { OwnedShareLinkInfo, ShareLinkInfo } from '../api'
import type { Perms } from '../../files/perms'

type LinkRow = ShareLinkInfo | OwnedShareLinkInfo
export type { LinkRow }

const CAPABILITY_KEYS: readonly (keyof Perms)[] = [
  'read',
  'download',
  'write',
  'create',
  'delete',
  'rename',
  'move',
  'share'
]

const capabilityLabel = (key: keyof Perms): string =>
  ({
    read: t('links.can_read'),
    download: t('links.can_download'),
    write: t('links.can_write'),
    create: t('links.can_create'),
    delete: t('links.can_delete'),
    rename: t('links.can_rename'),
    move: t('links.can_move'),
    share: t('links.can_share')
  })[key]

function isOwned(link: LinkRow): link is OwnedShareLinkInfo {
  return 'owner_name' in link
}

export function useLinkManagement(userId: number | undefined) {
  const files = useFileCache()
  const [resolvingPath, setResolvingPath] = useState<string | null>(null)
  const [targetError, setTargetError] = useState<{ path: string; message: string } | null>(null)
  // The link being managed stays rendered, so focus can go back to its row.
  const [managingId, setManagingId] = useState<LinkRow['id'] | null>(null)

  const isMine = (link: LinkRow): boolean => !isOwned(link) || link.owner === userId

  const targetSummary = (link: LinkRow): string => {
    const target = files.cachedStat(normalizePath(link.path))
    if (!target) return t('links.target_unknown')
    const capabilities = CAPABILITY_KEYS.filter((key) => link.perms[key]).map(capabilityLabel)
    const mutating =
      link.perms.write ||
      link.perms.create ||
      link.perms.delete ||
      link.perms.rename ||
      link.perms.move ||
      link.perms.share
    const permissionSummary =
      !mutating && capabilities.length > 0
        ? `${t('links.read_only')} (${capabilities.join(', ')})`
        : capabilities.join(', ') || t('links.target_unknown')
    return `${target.kind === 'dir' ? t('links.target_folder') : t('links.target_file')} - ${t('links.capabilities')}: ${permissionSummary}`
  }

  /** Looks the link's target up, then opens the share manager on it. */
  const openManagement = async (link: LinkRow): Promise<void> => {
    if (!isMine(link) || resolvingPath !== null) return
    const path = normalizePath(link.path)
    setTargetError(null)
    if (link.path.trim() === '') {
      setTargetError({ path, message: t('links.target_unknown') })
      return
    }
    setResolvingPath(path)
    let targetIsDir: boolean
    try {
      targetIsDir = (await files.fetchStat(path, false)).kind === 'dir'
    } catch (error) {
      setTargetError({ path, message: describeApiError(error, t('links.target_unknown')) })
      return
    } finally {
      setResolvingPath(null)
    }
    setManagingId(link.id)
    await openShareManager({ path, targetName: baseName(link.path) || link.path, targetIsDir })
    setManagingId(null)
  }

  return { resolvingPath, targetError, managingId, isMine, targetSummary, openManagement }
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
