import { forwardRef, useCallback, useLayoutEffect, useRef, type ReactNode } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useI18n } from '../../../../hooks/use-i18n'
import { cssVarName } from '@/lib/css-var'
import { cx } from '@/shared/ui'
import { vars } from '@/shared/theme'
import { cellPos, sectionRows, verticalTarget } from '../../logic/grid-sections'
import { indicesInRect } from '../../logic/marquee'
import type { FileEntry } from '../../model/file-entry'
import { density as densityPref, type Density } from '../../model/view-prefs'
import { useFileView, useViewportMetrics, type FileViewHandle, type FileViewProps } from '../../hooks/use-file-view'
import { FileItem, FileItemSkeleton } from '../FileItem'
import * as viewStyles from '../file-view.css'
import * as styles from './FileGrid.css'

// Card sizes in pixels. The virtualizers need them as numbers, so they live here rather than in the stylesheet.
const CARD: Record<Density, { width: number; folder: number; file: number; columnGap: number; rowGap: number }> = {
  compact: { width: 192, folder: 44, file: 176, columnGap: 8, rowGap: 12 },
  comfortable: { width: 224, folder: 52, file: 208, columnGap: 12, rowGap: 16 },
  spacious: { width: 256, folder: 60, file: 244, columnGap: 16, rowGap: 20 }
}
const MIN_CARD_WIDTH = 120
const OVERSCAN = 3

export interface FileGridProps extends FileViewProps {
  /** A file card's preview. Cards without one show the file type's icon. */
  thumbnail?: (item: FileEntry) => ReactNode
}

/** The folder as cards: folders in one section, files with previews in the next. Only the rows in view are drawn. */
export const FileGrid = forwardRef<FileViewHandle, FileGridProps>(function FileGrid({ thumbnail, ...props }, ref) {
  const { items, total, dirs, loading } = props
  const { t } = useI18n()
  const folderEl = useRef<HTMLDivElement>(null)
  const fileEl = useRef<HTMLDivElement>(null)
  const measure = useCallback(
    (element: HTMLDivElement) => ({
      width: element.clientWidth,
      foldersTop: folderEl.current?.offsetTop ?? 0,
      filesTop: fileEl.current?.offsetTop ?? 0,
      inlinePad: Number.parseFloat(getComputedStyle(element).getPropertyValue(cssVarName(vars.layout.contentPad))) || 0
    }),
    []
  )
  const { viewport, metrics } = useViewportMetrics(measure, {
    width: 0,
    foldersTop: 0,
    filesTop: 0,
    inlinePad: 0
  })

  const card = CARD[densityPref.value]
  const available = Math.max(card.width, metrics.width - metrics.inlinePad * 2)
  const columns = Math.max(1, Math.floor((available + card.columnGap) / (card.width + card.columnGap)))
  const cardWidth = Math.max(MIN_CARD_WIDTH, Math.floor((available - (columns - 1) * card.columnGap) / columns))
  const folderCount = Math.min(dirs, total)
  const fileCount = Math.max(0, total - folderCount)
  const folderRows = sectionRows(folderCount, columns)
  const fileRows = sectionRows(fileCount, columns)
  const folderRowHeight = card.folder + card.rowGap
  const fileRowHeight = card.file + card.rowGap
  // One virtualizer per section on the same scrolling element, each offset by where its section starts.
  const folderVirtualizer = useVirtualizer({
    count: folderRows,
    getScrollElement: () => viewport.current,
    estimateSize: () => folderRowHeight,
    overscan: OVERSCAN,
    scrollMargin: metrics.foldersTop
  })
  const fileVirtualizer = useVirtualizer({
    count: fileRows,
    getScrollElement: () => viewport.current,
    estimateSize: () => fileRowHeight,
    overscan: OVERSCAN,
    scrollMargin: metrics.filesTop
  })
  // The virtualizers keep their row offsets until told the estimate changed.
  useLayoutEffect(() => {
    folderVirtualizer.measure()
    fileVirtualizer.measure()
  }, [folderVirtualizer, fileVirtualizer, folderRowHeight, fileRowHeight])
  const sections = [
    {
      start: 0,
      count: folderCount,
      cardHeight: card.folder,
      top: metrics.foldersTop,
      rows: folderRows,
      rowHeight: folderRowHeight,
      virtualizer: folderVirtualizer,
      el: folderEl
    },
    {
      start: folderCount,
      count: fileCount,
      cardHeight: card.file,
      top: metrics.filesTop,
      rows: fileRows,
      rowHeight: fileRowHeight,
      virtualizer: fileVirtualizer,
      el: fileEl
    }
  ].map((section) => {
    const drawn = section.virtualizer.getVirtualItems()
    const first = drawn[0]?.index ?? 0
    const end = (drawn.at(-1)?.index ?? -1) + 1
    return { ...section, drawn, first, end }
  })
  const [folders, files] = sections

  const view = useFileView(
    props,
    ref,
    viewport,
    {
      step(key, from) {
        if (key === 'ArrowDown' || key === 'ArrowUp')
          return from === null ? 0 : verticalTarget(from, key === 'ArrowDown' ? 1 : -1, folderCount, total, columns)
        if (key === 'ArrowLeft' || key === 'ArrowRight')
          return from === null ? 0 : from + (key === 'ArrowRight' ? 1 : -1)
        return null
      },
      // scrollToIndex sends a section's last row to the bottom of the view, past the section below it.
      reveal(index) {
        const element = viewport.current
        if (!element) return
        const position = cellPos(index, folderCount, columns)
        const section = sections[position.section]
        const top = section.top + position.row * section.rowHeight
        const bottom = top + section.rowHeight
        if (top < element.scrollTop) element.scrollTo({ top })
        else if (bottom > element.scrollTop + element.clientHeight)
          element.scrollTo({ top: bottom - element.clientHeight })
      },
      indicesInRect(rect) {
        const left = (viewport.current?.getBoundingClientRect().left ?? 0) + window.scrollX + metrics.inlinePad
        return sections.flatMap((section) =>
          indicesInRect(rect, {
            left,
            top: (section.el.current?.getBoundingClientRect().top ?? 0) + window.scrollY,
            columnPitch: cardWidth + card.columnGap,
            cellWidth: cardWidth,
            columns,
            rowHeight: section.rowHeight,
            cellHeight: section.cardHeight,
            startIndex: section.start,
            count: section.count
          })
        )
      },
      rendered(index) {
        const position = cellPos(index, folderCount, columns)
        const { first, end } = sections[position.section]
        return position.row >= first && position.row < end
      },
      needed: Math.min(total, Math.max(folders.end * columns, folderCount + files.end * columns))
    },
    'sc-grid-cell'
  )

  const renderSection = (section: (typeof sections)[number], label: string, rowOffset: number) => (
    <>
      <p className={styles.group} aria-hidden="true">
        {label}
      </p>
      <div
        ref={section.el}
        className={viewStyles.spacer}
        role="rowgroup"
        aria-label={label}
        style={{ blockSize: section.virtualizer.getTotalSize() }}
      >
        <div
          className={cx(viewStyles.window, styles.window)}
          style={{ transform: `translate3d(0, ${section.first * section.rowHeight}px, 0)` }}
        >
          {section.drawn.map(({ index: row }) => {
            const first = section.start + row * columns
            const cells = Math.min(columns, section.start + section.count - first)
            return (
              <div
                key={row}
                className={styles.row}
                role="row"
                aria-rowindex={rowOffset + row + 1}
                style={{ blockSize: section.rowHeight, columnGap: card.columnGap }}
              >
                {Array.from({ length: cells }, (_, column) => {
                  const index = first + column
                  const item = items[index]
                  const style = { inlineSize: cardWidth, blockSize: section.cardHeight }
                  return item ? (
                    <FileItem
                      key={item.id}
                      view="grid"
                      position={column + 1}
                      style={style}
                      thumbnail={item.type === 'folder' ? undefined : thumbnail?.(item)}
                      {...view.itemProps(item, index)}
                    />
                  ) : (
                    <FileItemSkeleton
                      key={`skeleton-${index}`}
                      view="grid"
                      folder={section === folders}
                      position={column + 1}
                      style={style}
                    />
                  )
                })}
              </div>
            )
          })}
        </div>
      </div>
    </>
  )

  return (
    <div
      {...view.rootProps}
      className={cx(viewStyles.root, view.names.size > 0 && viewStyles.reserveSelection)}
      aria-rowcount={folders.rows + files.rows}
      aria-colcount={columns}
      aria-label={t('grid.file_grid')}
    >
      {total === 0 && !loading ? (
        <p className={viewStyles.empty}>{t('common.folder_empty')}</p>
      ) : (
        <>
          {folderCount > 0 ? renderSection(folders, t('grid.folders'), 0) : null}
          {fileCount > 0 ? renderSection(files, t('grid.files'), folders.rows) : null}
        </>
      )}
    </div>
  )
})
