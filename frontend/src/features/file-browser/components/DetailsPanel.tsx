import { useEffect, useMemo, useRef } from 'react'
import type { MouseEvent as ReactMouseEvent } from 'react'
import { joinPath } from '../../../lib/path-utils'
import { formatBytes } from '../../../lib/format/bytes'
import { formatEntrySize } from '../entry-size'
import { formatModifiedDateNs, t } from '../../../i18n'
import { useEventListener } from '../../../hooks/use-event-listener'
import { useI18n } from '../../../hooks/use-i18n'
import { useRestoreFocus } from '../../../hooks/use-restore-focus'
import { useCompact } from '@/hooks/use-compact'
import { cx, Icon, StowButton, StowIconButton, type IconName } from '@/shared/ui'
import { fileIcon } from './FileItem'
import * as styles from './DetailsPanel.css'
import { extensionOf, toFileEntry, type FileType } from '../model/file-entry'
import { ApiError } from '../../../api/fetcher'
import { useFolderSizes, type Entry } from '../api'

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

const KIND_KEYS: Record<FileType, string> = {
  folder: /* i18n */ 'details.folder',
  archive: /* i18n */ 'details.kind_archive',
  app: /* i18n */ 'details.kind_app',
  audio: /* i18n */ 'details.kind_audio',
  video: /* i18n */ 'details.kind_video',
  image: /* i18n */ 'details.kind_image',
  code: /* i18n */ 'details.kind_file',
  document: /* i18n */ 'details.kind_document',
  file: /* i18n */ 'details.kind_file'
}

/** The kind of item in words, naming the format by its extension. */
function kindDescription(entry: Entry): string {
  const { type } = toFileEntry(entry)
  const ext = extensionOf(entry.name).toUpperCase()
  if (!ext && type !== 'folder') return t('details.file')
  return t(KIND_KEYS[type], { ext })
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
  const compact = useCompact()
  const panel = useRef<HTMLElement>(null)
  const one = selected.length === 1 ? selected[0] : null
  const many = selected.length > 1
  const location = path.startsWith('/') ? path : `/${path}`
  const targets =
    selected.length === 0
      ? path === '/'
        ? []
        : [path]
      : selected.filter((entry) => entry.kind === 'dir').map((entry) => joinPath(path, entry.name))
  const base = many ? selected.filter((entry) => entry.kind !== 'dir').reduce((sum, entry) => sum + entry.size, 0) : 0
  const queries = useFolderSizes(targets)
  const measured = queries.some((query) => query.isError)
    ? 'failed'
    : queries.some((query) => query.isPending)
      ? 'measuring'
      : targets.length || base
        ? 'done'
        : 'idle'
  const bytes = base + queries.reduce((sum, query) => sum + (query.data?.bytes ?? 0), 0)
  const files =
    (many ? selected.filter((entry) => entry.kind !== 'dir').length : 0) +
    queries.reduce((sum, query) => sum + (query.data?.files ?? 0), 0)

  useRestoreFocus(compact)
  useEffect(() => {
    if (compact) queueMicrotask(() => panel.current?.querySelector<HTMLElement>('button')?.focus())
  }, [compact])
  // In compact mode the panel covers the list, so Escape closes it and Tab stays inside it.
  useEventListener(compact ? panel : null, 'keydown', (event) => {
    if (event.key === 'Escape') {
      event.stopPropagation()
      onClose()
      return
    }
    if (event.key !== 'Tab' || !panel.current) return
    const focusable = [
      ...panel.current.querySelectorAll<HTMLElement>(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
      )
    ].filter((element) => !element.hasAttribute('disabled'))
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
  })

  const permissionSummary = (entry: Entry) => {
    const granted = [
      entry.perms.download && t('common.download'),
      entry.perms.rename && t('details.perm_rename'),
      entry.perms.move && t('details.perm_move'),
      entry.perms.delete && t('details.perm_delete'),
      entry.perms.share && t('details.perm_share')
    ].filter(Boolean)
    return granted.length > 0 ? granted.join(', ') : t('details.perm_read_only')
  }

  const fields = useMemo(() => {
    if (many)
      return [
        { label: t('details.items'), value: String(selected.length) },
        { label: t('details.location'), value: location }
      ]
    if (one) {
      return [
        { label: t('details.type'), value: one.kind === 'dir' ? t('details.folder') : kindDescription(one) },
        ...(one.kind !== 'dir'
          ? [
              {
                label: t('details.size'),
                value: t('details.size_with_bytes', {
                  size: formatEntrySize(one.size, encrypted),
                  bytes: one.size.toLocaleString()
                })
              }
            ]
          : []),
        { label: t('details.modified'), value: formatModifiedDateNs(one.mtime_ns) },
        { label: t('details.location'), value: joinPath(location, one.name) },
        { label: t('details.permissions'), value: permissionSummary(one) }
      ]
    }
    return [
      { label: t('details.type'), value: t('details.folder') },
      { label: t('grid.folders'), value: String(dirs) },
      { label: t('grid.files'), value: String(Math.max(0, total - dirs)) },
      { label: t('details.location'), value: location }
    ]
  }, [many, one, selected.length, location, encrypted, dirs, total, t])

  const title = many
    ? t('details.multiple_selected', { count: selected.length })
    : (one?.name ?? path.split('/').filter(Boolean).at(-1) ?? t('nav.files'))
  const heroIcon: IconName = one ? fileIcon(toFileEntry(one).type) : many ? 'check' : 'folder'
  const heroDesc = one ? kindDescription(one) : many ? formatBytes(bytes) : t('details.folder')

  return (
    <aside
      ref={panel}
      className={cx(styles.root, compact && styles.sheet)}
      role={compact ? 'dialog' : 'complementary'}
      aria-modal={compact ? 'true' : undefined}
      aria-label={t('details.title')}
    >
      <header className={styles.head}>
        <span className={styles.headIcon} aria-hidden="true">
          <Icon name={heroIcon} size={20} />
        </span>
        <h2 className={styles.title}>
          <bdi>{title}</bdi>
        </h2>
        <StowIconButton label={t('common.close')} icon="close" onClick={onClose} />
      </header>

      <div className={styles.summary}>
        <span className={styles.summaryIcon} aria-hidden="true">
          <Icon name={heroIcon} size={24} />
        </span>
        <div>
          <div className={styles.summaryTitle}>
            <bdi>{title}</bdi>
          </div>
          <div className={styles.summaryDesc}>{heroDesc}</div>
        </div>
      </div>

      {one || many ? (
        <div className={styles.actions}>
          {onDownload ? (
            <StowButton className={styles.action} icon={<Icon name="download" size={18} />} onClick={onDownload}>
              {t('common.download')}
            </StowButton>
          ) : null}
          {onShare && one ? <StowIconButton label={t('details.perm_share')} onClick={onShare} icon="link" /> : null}
          {onContextMenu && one ? (
            <StowIconButton label={t('browse.more')} onClick={onContextMenu} icon="more-vert" />
          ) : null}
        </div>
      ) : null}

      <div className={styles.sectionHeading}>{t('details.title')}</div>

      <dl className={styles.fields}>
        {fields.map((field) => (
          <div key={field.label} className={styles.field}>
            <dt className={styles.fieldLabel}>{field.label}</dt>
            <dd className={styles.fieldValue}>
              <bdi>{field.value}</bdi>
            </dd>
          </div>
        ))}
        {measured !== 'idle' ? (
          <div className={styles.field}>
            <dt className={styles.fieldLabel}>{many ? t('details.download_size') : t('details.total_size')}</dt>
            <dd className={styles.fieldValue}>
              {measured === 'done' ? (
                <>
                  {formatBytes(bytes)}{' '}
                  <small className={styles.fieldNote}>{t('details.size_file_count', { count: files })}</small>
                </>
              ) : measured === 'failed' ? (
                <>
                  <small className={styles.fieldNote} role="alert">
                    {queries.some((query) => query.error instanceof ApiError && query.error.code === 'fs.denied')
                      ? t('details.size_hidden_by_permissions')
                      : t('details.could_not_measure')}
                  </small>
                  <StowButton variant="text" onClick={() => queries.forEach((query) => void query.refetch())}>
                    {t('common.retry')}
                  </StowButton>
                </>
              ) : (
                <small className={styles.fieldNote} role="status">
                  {t('details.measuring')}
                </small>
              )}
            </dd>
          </div>
        ) : null}
      </dl>
    </aside>
  )
}
