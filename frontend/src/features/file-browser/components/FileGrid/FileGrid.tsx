import { forwardRef, useCallback, useRef, type ReactNode } from 'react'
import { useI18n } from '../../../../hooks/use-i18n'
import { cssVarName } from '@/lib/css-var'
import { computeScaleMapping, computeWindow, rowIndexToScrollTop } from '../../../../lib/virtual/windowing'
import { cellPos, sectionRows, verticalTarget } from '../../../../lib/virtual/grid-sections'
import { cx } from '@/shared/ui'
import { vars } from '@/shared/theme'
import { indicesInRect } from '../../logic/marquee'
import type { FileEntry } from '../../model/file-entry'
import { density as densityPref, type Density } from '../../model/view-prefs'
import { useFileView, useViewportMetrics, type FileViewHandle, type FileViewProps } from '../../hooks/use-file-view'
import { FileItem, FileItemSkeleton } from '../FileItem'
import * as viewStyles from '../file-view.css'
import * as styles from './FileGrid.css'

// Card sizes in pixels. The window maths needs them as numbers, so they live here rather than in the stylesheet.
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
      scroll: element.scrollTop,
      height: element.clientHeight,
      foldersTop: folderEl.current?.offsetTop ?? 0,
      filesTop: fileEl.current?.offsetTop ?? 0,
      inlinePad: Number.parseFloat(getComputedStyle(element).getPropertyValue(cssVarName(vars.layout.contentPad))) || 0
    }),
    []
  )
  const { viewport, metrics } = useViewportMetrics(measure, {
    width: 0,
    scroll: 0,
    height: 0,
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
  const sections = [
    { start: 0, count: folderCount, cardHeight: card.folder, top: metrics.foldersTop, el: folderEl },
    { start: folderCount, count: fileCount, cardHeight: card.file, top: metrics.filesTop, el: fileEl }
  ].map((section) => {
    const rows = sectionRows(section.count, columns)
    const rowHeight = section.cardHeight + card.rowGap
    const win = computeWindow({
      scrollTop: Math.max(0, metrics.scroll - section.top),
      viewportHeight: metrics.height,
      rowHeight,
      itemCount: rows,
      overscan: OVERSCAN
    })
    return { ...section, rows, rowHeight, win }
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
      reveal(index) {
        const position = cellPos(index, folderCount, columns)
        const section = sections[position.section]
        const top =
          section.top +
          rowIndexToScrollTop(position.row, computeScaleMapping(section.rows, section.rowHeight), section.rowHeight)
        const bottom = top + section.rowHeight
        if (top < metrics.scroll) viewport.current?.scrollTo({ top })
        else if (bottom > metrics.scroll + metrics.height) viewport.current?.scrollTo({ top: bottom - metrics.height })
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
        const { win } = sections[position.section]
        return position.row >= win.start && position.row < win.end
      },
      needed: Math.min(total, Math.max(folders.win.end * columns, folderCount + files.win.end * columns))
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
        style={{ blockSize: section.win.totalHeight }}
      >
        <div
          className={cx(viewStyles.window, styles.window)}
          style={{ transform: `translate3d(0, ${section.win.padTop}px, 0)` }}
        >
          {Array.from({ length: section.win.count }, (_, offset) => {
            const row = section.win.start + offset
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
