import type { SearchHit } from '../../lib/api/client'
import { formatBytes } from '../../lib/format/bytes'
import { formatModifiedDateNs } from '../../lib/i18n'
import { parentOf } from '../../lib/api/path-utils'
import { extensionOf } from '../../lib/search/filters'
import { computeWindow, type WindowResult } from '../../lib/virtual/windowing'
import { Icon } from '../../lib/ui/Icon'

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
  readonly t: (key: string, values?: Record<string, string>) => string
}

function getHitIcon(hit: SearchHit): { name: string; color?: string } {
  if (hit.entry.kind === 'dir') return { name: 'folder', color: 'var(--sc-icon-color)' }
  const ext = extensionOf(hit.entry.name).toLowerCase()
  if (['zip', 'rar', '7z', 'tar', 'gz', 'bz2', 'xz', 'zst', 'iso'].includes(ext)) return { name: 'folder-zip', color: 'var(--sc-icon-color)' }
  if (ext === 'apk') return { name: 'android', color: 'var(--sc-icon-color)' }
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'heic', 'avif'].includes(ext)) return { name: 'image', color: 'var(--sc-icon-color)' }
  if (['mp4', 'mkv', 'mov', 'avi', 'webm', 'wmv', 'm4v', 'mpg', 'mpeg', 'flv'].includes(ext)) return { name: 'movie', color: 'var(--sc-icon-color)' }
  if (['mp3', 'flac', 'wav', 'aac', 'ogg', 'oga', 'm4a', 'opus', 'wma'].includes(ext)) return { name: 'audio-file', color: 'var(--sc-icon-color)' }
  if (['js', 'ts', 'tsx', 'jsx', 'go', 'rs', 'py', 'java', 'c', 'cpp', 'h', 'cs', 'rb', 'php', 'sh', 'sql', 'json', 'yaml', 'yml', 'toml', 'xml', 'html', 'css'].includes(ext)) return { name: 'code', color: 'var(--sc-icon-color)' }
  if (['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'txt', 'md', 'rtf', 'odt', 'ods', 'odp', 'hwp', 'hwpx', 'csv'].includes(ext)) return { name: 'description', color: 'var(--sc-icon-color)' }
  return { name: 'draft', color: 'var(--sc-icon-color)' }
}

export function SearchResults({ ran, running, view, rows, windowed, activeFilters, onOpen, onScroll, resultsRef, t }: SearchResultsProps) {
  return (
    <div className="sc-search-results" ref={resultsRef} onScroll={(event) => onScroll(event.currentTarget.scrollTop)} tabIndex={-1}>
      {!ran ? (
        <p className="sc-search-note">{t('search.type_and_press_enter')}</p>
      ) : view.length === 0 && !running ? (
        <p className="sc-search-note">
          {t('search.no_results')} {activeFilters ? <span className="sc-search-hint">{t('search.filtered_by', { filters: activeFilters })}</span> : null}
        </p>
      ) : (
        <div className="sc-search-spacer" style={{ height: windowed.totalHeight }}>
          <ul className="sc-search-rows" style={{ transform: `translate3d(0, ${windowed.padTop}px, 0)` }}>
            {rows.map((hit) => {
              const hitIcon = getHitIcon(hit)
              return (
                <li key={hit.path}>
                  <button type="button" className="sc-search-row" onClick={() => onOpen(hit)}>
                    <span className="sc-search-row-icon" style={{ color: hitIcon.color }}><Icon name={hitIcon.name} size={20} /></span>
                    <span className="sc-search-text">
                      <span className="sc-search-name">{hit.entry.name}</span>
                      <span className="sc-search-folder">{parentOf(hit.path)}</span>
                    </span>
                    <span className="sc-search-cell">
                      {hit.entry.kind !== 'dir' ? <span className="sc-search-size">{formatBytes(hit.entry.size)}</span> : null}
                      <span className="sc-search-date">{formatModifiedDateNs(hit.entry.mtime_ns)}</span>
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
