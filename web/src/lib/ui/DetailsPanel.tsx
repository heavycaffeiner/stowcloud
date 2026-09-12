import { useEffect, useMemo, useRef } from 'react'
import { useQueries } from '@tanstack/react-query'
import { ApiError, type Entry } from '../api/types'
import { joinPath } from '../api/path-utils'
import { folderSizeQuery } from '../query/files'
import { formatBytes } from '../format/bytes'
import { formatEntrySize } from '../format/entry-size'
import { formatDateNs } from '../i18n'
import { useI18n } from '../i18n/use-i18n'
import { useStore } from '../store/use-store'
import { ui } from '../store/ui.store'
import { Button } from './Button'
import { IconButton } from './IconButton'
import { Icon } from './Icon'
import './browse-ui.css'

interface DetailsPanelProps {
  path: string
  selected: readonly Entry[]
  total: number
  dirs: number
  encrypted?: boolean
  onClose: () => void
}

export function DetailsPanel({ path, selected, total, dirs, encrypted = false, onClose }: DetailsPanelProps) {
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
        { label: t('details.type'), value: one.kind === 'dir' ? t('details.folder') : t('details.file') },
        ...(one.kind !== 'dir' ? [{ label: t('details.size'), value: formatEntrySize(one.size, encrypted) }] : []),
        { label: t('details.modified'), value: formatDateNs(one.mtime_ns) },
        { label: t('details.location'), value: location },
        ...(one.link ? [{ label: t('details.symlink_target'), value: one.link.target }] : []),
        { label: t('details.permissions'), value: permissionSummary(one) }
      ]
    }
    return [{ label: t('details.type'), value: t('details.folder') }, { label: t('grid.folders'), value: String(dirs) }, { label: t('grid.files'), value: String(Math.max(0, total - dirs)) }, { label: t('details.location'), value: location }]
  }, [many, one, selected.length, location, encrypted, dirs, total, t])

  const title = many ? t('details.multiple_selected', { count: selected.length }) : one?.name ?? (path.split('/').filter(Boolean).at(-1) ?? t('browse.home'))
  return (
    <aside ref={panel} className={`sc-details${compact ? ' sc-details--sheet' : ''}`} role={compact ? 'dialog' : 'complementary'} aria-modal={compact ? 'true' : undefined} aria-label={t('details.title')}>
      <header className="sc-details__head">
        <span className="sc-details__head-icon" aria-hidden="true">
          <Icon name={many ? 'check' : one?.kind === 'dir' || !one ? 'folder' : 'draft'} />
        </span>
        <h2 className="sc-details__title"><bdi>{title}</bdi></h2>
        <IconButton label={t('common.close')} onClick={onClose}>
          <Icon name="close" />
        </IconButton>
      </header>
      {one?.confusable ? <p className="sc-details__warning"><Icon name="warning" size={16} /><span>{t('common.look_alike_characters')}</span></p> : null}
      <dl className="sc-details__fields">{fields.map((field) => <div key={field.label}><dt>{field.label}</dt><dd><bdi>{field.value}</bdi></dd></div>)}{measured !== 'idle' ? <div><dt>{many ? t('details.download_size') : t('details.total_size')}</dt><dd>{measured === 'done' ? <>{formatBytes(bytes)} <small>{t('details.size_file_count', { count: files })}</small></> : measured === 'failed' ? <><small role="alert">{queries.some((query) => query.error instanceof ApiError && query.error.code === 'fs.denied') ? t('details.size_hidden_by_permissions') : t('details.could_not_measure')}</small><Button variant="text" onClick={() => queries.forEach((query) => void query.refetch())}>{t('common.retry')}</Button></> : <small role="status">{t('details.measuring')}</small>}</dd></div> : null}</dl>
    </aside>
  )
}
