import { useEffect, useMemo, useRef } from 'react'
import type { MouseEvent as ReactMouseEvent } from 'react'
import { useQueries } from '@tanstack/react-query'
import { ApiError, type Entry } from '../../lib/api/types'
import { joinPath } from '../../lib/api/path-utils'
import { folderSizeQuery } from '../../lib/query/files'
import { formatBytes } from '../../lib/format/bytes'
import { formatEntrySize } from '../../lib/format/entry-size'
import { formatModifiedDateNs } from '../../lib/i18n'
import { useI18n } from '../../lib/i18n/use-i18n'
import { useStore } from '../../lib/store/use-store'
import { ui } from '../../lib/store/ui.store'
import { Button } from '../../lib/ui/Button'
import { IconButton } from '../../lib/ui/IconButton'
import { Icon } from '../../lib/ui/Icon'
import { getEntryIcon } from './FileRow'
import './browse-ui.css'

interface DetailsPanelProps {
  path: string
  selected: readonly Entry[]
  total: number
  dirs: number
  encrypted?: boolean
  onClose: () => void
  onDownload?: () => void
  onShare?: () => void
  onContextMenu?: (event: ReactMouseEvent) => void
}

function kindDescription(entry: Entry): string {
  if (entry.kind === 'dir') return '폴더'
  const dot = entry.name.lastIndexOf('.')
  const ext = dot > 0 ? entry.name.slice(dot + 1).toLowerCase() : ''
  if (['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'zst', 'iso'].includes(ext)) {
    return `${ext.toUpperCase()} 압축 아카이브`
  }
  if (ext === 'apk') return 'Android 애플리케이션 패키지'
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'heic', 'avif'].includes(ext)) {
    return `${ext.toUpperCase()} 이미지`
  }
  if (['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v'].includes(ext)) {
    return `${ext.toUpperCase()} 동영상`
  }
  if (['mp3', 'flac', 'wav', 'aac', 'ogg', 'm4a', 'opus', 'wma'].includes(ext)) {
    return `${ext.toUpperCase()} 오디오`
  }
  if (['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'txt', 'md'].includes(ext)) {
    return `${ext.toUpperCase()} 문서`
  }
  return ext ? `${ext.toUpperCase()} 파일` : '파일'
}

export function DetailsPanel({
  path,
  selected,
  total,
  dirs,
  encrypted = false,
  onClose,
  onDownload,
  onShare,
  onContextMenu
}: DetailsPanelProps) {
  const { t } = useI18n()
  const compact = useStore(ui, (state) => state.compact)
  const panel = useRef<HTMLElement>(null)
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose
  const one = selected.length === 1 ? selected[0] : null
  const many = selected.length > 1
  const location = path.startsWith('/') ? path : `/${path}`
  const targets = selected.length === 0 ? (path === '/' ? [] : [path]) : selected.filter((entry) => entry.kind === 'dir').map((entry) => joinPath(path, entry.name))
  const base = many ? selected.filter((entry) => entry.kind !== 'dir').reduce((sum, entry) => sum + entry.size, 0) : 0
  const queries = useQueries({ queries: targets.map((target) => folderSizeQuery(target)) })
  const measured = queries.some((query) => query.isError) ? 'failed' : queries.some((query) => query.isPending) ? 'measuring' : targets.length || base ? 'done' : 'idle'
  const bytes = base + queries.reduce((sum, query) => sum + (query.data?.bytes ?? 0), 0)
  const files = (many ? selected.filter((entry) => entry.kind !== 'dir').length : 0) + queries.reduce((sum, query) => sum + (query.data?.files ?? 0), 0)

  useEffect(() => {
    if (!compact || !panel.current) return
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    queueMicrotask(() => panel.current?.querySelector<HTMLElement>('button')?.focus())
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation()
        onCloseRef.current()
        return
      }
      if (event.key !== 'Tab' || !panel.current) return
      const focusable = [...panel.current.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')].filter((element) => !element.hasAttribute('disabled'))
      if (focusable.length === 0) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    panel.current.addEventListener('keydown', onKeyDown)
    return () => {
      panel.current?.removeEventListener('keydown', onKeyDown)
      previous?.focus()
    }
  }, [compact])

  const permissionSummary = (entry: Entry) => {
    const granted = [entry.perms.download && t('common.download'), entry.perms.rename && t('details.perm_rename'), entry.perms.move && t('details.perm_move'), entry.perms.delete && t('details.perm_delete'), entry.perms.share && t('details.perm_share')].filter(Boolean)
    return granted.length > 0 ? granted.join(', ') : t('details.perm_read_only')
  }

  const fields = useMemo(() => {
    if (many) return [{ label: t('details.items'), value: String(selected.length) }, { label: t('details.location'), value: location }]
    if (one) {
      return [
        { label: t('details.type'), value: one.kind === 'dir' ? t('details.folder') : kindDescription(one) },
        ...(one.kind !== 'dir' ? [{ label: t('details.size'), value: `${formatEntrySize(one.size, encrypted)} (${one.size.toLocaleString()} 바이트)` }] : []),
        { label: t('details.modified'), value: formatModifiedDateNs(one.mtime_ns) },
        { label: t('details.location'), value: joinPath(location, one.name) },
        ...(one.link ? [{ label: t('details.symlink_target'), value: one.link.target }] : []),
        { label: t('details.permissions'), value: permissionSummary(one) }
      ]
    }
    return [{ label: t('details.type'), value: t('details.folder') }, { label: t('grid.folders'), value: String(dirs) }, { label: t('grid.files'), value: String(Math.max(0, total - dirs)) }, { label: t('details.location'), value: location }]
  }, [many, one, selected.length, location, encrypted, dirs, total, t])

  const title = many ? t('details.multiple_selected', { count: selected.length }) : one?.name ?? (path.split('/').filter(Boolean).at(-1) ?? t('nav.files'))
  const heroIcon = one ? getEntryIcon(one) : many ? { name: 'check', color: 'var(--sc-icon-color)' } : { name: 'folder', color: 'var(--sc-icon-color)' }
  const heroDesc = one ? kindDescription(one) : many ? formatBytes(bytes) : t('details.folder')

  return (
    <aside ref={panel} className={`sc-details${compact ? ' sc-details--sheet' : ''}`} role={compact ? 'dialog' : 'complementary'} aria-modal={compact ? 'true' : undefined} aria-label={t('details.title')}>
      <header className="sc-details__head">
        <span className="sc-details__head-icon" aria-hidden="true" style={{ color: heroIcon.color }}>
          <Icon name={heroIcon.name} size={20} />
        </span>
        <h2 className="sc-details__title"><bdi>{title}</bdi></h2>
        <IconButton label={t('common.close')} onClick={onClose}>
          <Icon name="close" />
        </IconButton>
      </header>

      <div className="sc-details__summary">
        <span className="sc-details__summary-icon" aria-hidden="true" style={{ color: heroIcon.color }}>
          <Icon name={heroIcon.name} size={24} />
        </span>
        <div>
          <div className="sc-details__summary-title"><bdi>{title}</bdi></div>
          <div className="sc-details__summary-desc">{heroDesc}</div>
        </div>
      </div>

      {(one || many) ? (
        <div className="sc-details__actions">
          {onDownload ? (
            <Button icon={<Icon name="download" size={18} />} onClick={onDownload}>{t('common.download')}</Button>
          ) : null}
          {onShare && one ? (
            <IconButton label={t('details.perm_share')} onClick={onShare}><Icon name="link" /></IconButton>
          ) : null}
          {onContextMenu && one ? (
            <IconButton label={t('browse.more')} onClick={onContextMenu}><Icon name="more-vert" /></IconButton>
          ) : null}
        </div>
      ) : null}

      {one?.confusable ? <p className="sc-details__warning"><Icon name="warning" size={16} /><span>{t('common.look_alike_characters')}</span></p> : null}

      <div className="sc-details__section-heading">{t('details.title')}</div>

      <dl className="sc-details__fields">
        {fields.map((field) => (
          <div key={field.label}>
            <dt>{field.label}</dt>
            <dd><bdi>{field.value}</bdi></dd>
          </div>
        ))}
        {measured !== 'idle' ? (
          <div>
            <dt>{many ? t('details.download_size') : t('details.total_size')}</dt>
            <dd>
              {measured === 'done' ? (
                <>{formatBytes(bytes)} <small>{t('details.size_file_count', { count: files })}</small></>
              ) : measured === 'failed' ? (
                <><small role="alert">{queries.some((query) => query.error instanceof ApiError && query.error.code === 'fs.denied') ? t('details.size_hidden_by_permissions') : t('details.could_not_measure')}</small><Button variant="text" onClick={() => queries.forEach((query) => void query.refetch())}>{t('common.retry')}</Button></>
              ) : (
                <small role="status">{t('details.measuring')}</small>
              )}
            </dd>
          </div>
        ) : null}
      </dl>
    </aside>
  )
}
