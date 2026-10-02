import { formatBytes } from '../../../lib/format/bytes'
import { formatModifiedDateNs } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { parentOf } from '../../../lib/path-utils'
import { extensionOf } from '../logic/filters'
import type { WindowResult } from '../../../lib/virtual/windowing'
import { Icon, type IconName } from '@/shared/ui'
import * as styles from './SearchResults.css'
import { vars } from '@/shared/theme'
import type { SearchHit } from '../api'

export interface SearchResultsProps {
  readonly ran: boolean
  readonly running: boolean
  readonly view: readonly SearchHit[]
  readonly rows: readonly SearchHit[]
  readonly windowed: WindowResult
  readonly activeFilters: string
  readonly onOpen: (hit: SearchHit) => void
  readonly onScroll: (scrollTop: number) => void
  readonly resultsRef: (node: HTMLDivElement | null) => void
}

function getHitIcon(hit: SearchHit): { name: IconName; color?: string } {
  if (hit.entry.kind === 'dir') return { name: 'folder', color: vars.color.text.icon }
  const ext = extensionOf(hit.entry.name).toLowerCase()
  if (['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'zst', 'iso'].includes(ext))
    return { name: 'folder-zip', color: vars.color.text.icon }
  if (ext === 'apk') return { name: 'android', color: vars.color.text.icon }
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'heic', 'avif'].includes(ext))
    return { name: 'image', color: vars.color.text.icon }
  if (['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v', 'mpg', 'mpeg', 'flv'].includes(ext))
    return { name: 'movie', color: vars.color.text.icon }
  if (['mp3', 'flac', 'wav', 'aac', 'ogg', 'oga', 'm4a', 'opus', 'wma'].includes(ext))
    return { name: 'audio-file', color: vars.color.text.icon }
  if (
    [
      'js',
      'ts',
      'tsx',
      'jsx',
      'go',
      'rs',
      'py',
      'java',
      'c',
      'cpp',
      'h',
      'cs',
      'rb',
      'php',
      'sh',
      'sql',
      'json',
      'yaml',
      'yml',
      'toml',
      'xml',
      'html',
      'css'
    ].includes(ext)
  )
    return { name: 'code', color: vars.color.text.icon }
  if (
    [
      'pdf',
      'doc',
      'docx',
      'xls',
      'xlsx',
      'ppt',
      'pptx',
      'txt',
      'md',
      'rtf',
      'odt',
      'ods',
      'odp',
      'hwp',
      'hwpx',
      'csv'
    ].includes(ext)
  )
    return { name: 'description', color: vars.color.text.icon }
  return { name: 'draft', color: vars.color.text.icon }
}

export function SearchResults({
  ran,
  running,
  view,
  rows,
  windowed,
  activeFilters,
  onOpen,
  onScroll,
  resultsRef
}: SearchResultsProps) {
  const { t } = useI18n()
  return (
    <div
      className={styles.results}
      ref={resultsRef}
      onScroll={(event) => onScroll(event.currentTarget.scrollTop)}
      tabIndex={-1}
    >
      {!ran ? (
        <p className={styles.note}>{t('search.type_and_press_enter')}</p>
      ) : view.length === 0 && !running ? (
        <p className={styles.note}>
          {t('search.no_results')}{' '}
          {activeFilters ? <span>{t('search.filtered_by', { filters: activeFilters })}</span> : null}
        </p>
      ) : (
        <div className={styles.spacer} style={{ height: windowed.totalHeight }}>
          <ul className={styles.rows} style={{ transform: `translate3d(0, ${windowed.padTop}px, 0)` }}>
            {rows.map((hit) => {
              const hitIcon = getHitIcon(hit)
              return (
                <li key={hit.path}>
                  <button type="button" className={styles.row} onClick={() => onOpen(hit)}>
                    <span className={styles.rowIcon} style={{ color: hitIcon.color }}>
                      <Icon name={hitIcon.name} size={20} />
                    </span>
                    <span className={styles.text}>
                      <span className={styles.name}>{hit.entry.name}</span>
                      <span className={styles.folder}>{parentOf(hit.path)}</span>
                    </span>
                    <span className={styles.cell}>
                      {hit.entry.kind !== 'dir' ? (
                        <span className={styles.size}>{formatBytes(hit.entry.size)}</span>
                      ) : null}
                      <span className={styles.date}>{formatModifiedDateNs(hit.entry.mtime_ns)}</span>
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>
        </div>
      )}
    </div>
  )
}
