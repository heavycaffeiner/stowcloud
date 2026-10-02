import { forwardRef, useMemo } from 'react'
import { useCompact } from '@/hooks/use-compact'
import { useI18n } from '../../../../hooks/use-i18n'
import { computeScaleMapping, computeWindow, rowIndexToScrollTop } from '../../../../lib/virtual/windowing'
import { cx, StowCheckboxIndicator } from '@/shared/ui'
import { touchTarget } from '@/shared/theme'
import { indicesInRect } from '../../logic/marquee'
import { selection } from '../../model/selection'
import { chooseSort, SORT_LABEL_KEYS, sortKey, sortOrder } from '../../model/sorting'
import { density as densityPref } from '../../model/view-prefs'
import { useFileView, useViewportMetrics, type FileViewHandle, type FileViewProps } from '../../hooks/use-file-view'
import { FileItem, FileItemSkeleton } from '../FileItem'
import * as itemStyles from '../FileItem/FileItem.css'
import * as viewStyles from '../file-view.css'
import * as styles from './FileList.css'

const HEADER_HEIGHT = 40
const ROW_HEIGHT = { compact: 40, comfortable: 48, spacious: 56 }
// Below this width a phone shows two-line rows.
const MOBILE_ROW_HEIGHT = 64
const MOBILE_MAX_WIDTH = 600
const SORTABLE = ['name', 'size', 'mtime'] as const
const COLUMN = { name: itemStyles.colName, size: itemStyles.colSize, mtime: itemStyles.colModified }

const measure = (element: HTMLDivElement) => ({
  scroll: element.scrollTop,
  height: element.clientHeight,
  width: element.clientWidth
})

/** The folder as rows under sortable column headings. Only the rows in view are drawn. */
export const FileList = forwardRef<FileViewHandle, FileViewProps>(function FileList(props, ref) {
  const { items, total, loading } = props
  const { t } = useI18n()
  const compact = useCompact()
  const { viewport, metrics } = useViewportMetrics(measure, { scroll: 0, height: 0, width: 0 })
  const mobile = compact && metrics.width > 0 && metrics.width < MOBILE_MAX_WIDTH
  const rowHeight = mobile ? MOBILE_ROW_HEIGHT : ROW_HEIGHT[densityPref.value]
  const visibleHeight = Math.max(0, metrics.height - HEADER_HEIGHT)
  const win = computeWindow({
    scrollTop: metrics.scroll,
    viewportHeight: visibleHeight,
    rowHeight,
    itemCount: total,
    overscan: 8
  })

  const view = useFileView(
    props,
    ref,
    viewport,
    {
      step: (key, from) =>
        key === 'ArrowDown' ? (from === null ? 0 : from + 1) : key === 'ArrowUp' ? (from ?? total) - 1 : null,
      reveal(index) {
        const mapping = computeScaleMapping(total, rowHeight)
        const top = rowIndexToScrollTop(index, mapping, rowHeight)
        const bottom = rowIndexToScrollTop(index + 1, mapping, rowHeight)
        if (top < metrics.scroll) viewport.current?.scrollTo({ top })
        else if (bottom > metrics.scroll + visibleHeight) viewport.current?.scrollTo({ top: bottom - visibleHeight })
      },
      indicesInRect(rect) {
        const element = viewport.current
        if (!element) return []
        const box = element.getBoundingClientRect()
        return indicesInRect(rect, {
          top: box.top + window.scrollY + HEADER_HEIGHT - element.scrollTop,
          left: box.left + window.scrollX,
          rowHeight,
          cellHeight: rowHeight,
          columnPitch: 0,
          cellWidth: box.width,
          columns: 1,
          startIndex: 0,
          count: total
        })
      },
      rendered: (index) => index >= win.start && index < win.end,
      needed: win.end
    },
    'sc-row'
  )

  const chosen = useMemo(() => items.filter((item) => view.names.has(item.name)).length, [items, view.names])
  const allChosen = items.length > 0 && chosen === items.length
  const key = sortKey.value
  const order = sortOrder.value

  return (
    <div
      {...view.rootProps}
      className={cx(viewStyles.root, mobile && itemStyles.mobile, view.names.size > 0 && viewStyles.reserveSelection)}
      aria-rowcount={total + 1}
      aria-label={t('table.file_list')}
    >
      {total === 0 && !loading ? (
        <p className={viewStyles.empty}>{t('common.folder_empty')}</p>
      ) : (
        <>
          <div className={styles.header} role="row" aria-rowindex={1} style={{ blockSize: HEADER_HEIGHT }}>
            <span className={cx(itemStyles.cell, itemStyles.colSelect)} role="columnheader">
              <button
                type="button"
                className={cx(styles.selectAll, touchTarget)}
                role="checkbox"
                aria-checked={allChosen ? true : chosen > 0 ? 'mixed' : false}
                onClick={() => (allChosen ? selection.clear() : selection.all(items.map((item) => item.name)))}
              >
                <StowCheckboxIndicator
                  checked={allChosen}
                  indeterminate={chosen > 0 && !allChosen}
                  label={t('browse.select_all')}
                />
              </button>
            </span>
            {SORTABLE.map((column) => {
              const active = key === column
              const direction = order === 'asc' ? t('browse.sort_ascending') : t('browse.sort_descending')
              return (
                <span
                  key={column}
                  className={cx(itemStyles.cell, COLUMN[column])}
                  role="columnheader"
                  aria-sort={active ? (order === 'asc' ? 'ascending' : 'descending') : 'none'}
                >
                  <button
                    type="button"
                    className={cx(styles.sortButton, active && styles.sortActive)}
                    aria-label={active ? `${t(SORT_LABEL_KEYS[column])}, ${direction}` : t(SORT_LABEL_KEYS[column])}
                    onClick={() => chooseSort(column)}
                  >
                    {t(SORT_LABEL_KEYS[column])}
                    {active ? <span aria-hidden="true">{order === 'asc' ? '↑' : '↓'}</span> : null}
                  </button>
                </span>
              )
            })}
            <span className={cx(itemStyles.cell, itemStyles.colActions)} role="columnheader" />
          </div>
          <div className={viewStyles.spacer} style={{ blockSize: win.totalHeight }}>
            <div className={viewStyles.window} style={{ transform: `translate3d(0, ${win.padTop}px, 0)` }}>
              {Array.from({ length: win.count }, (_, offset) => {
                const index = win.start + offset
                const item = items[index]
                const style = { blockSize: rowHeight }
                return item ? (
                  <FileItem
                    key={item.id}
                    view="list"
                    position={index + 2}
                    style={style}
                    {...view.itemProps(item, index)}
                  />
                ) : (
                  <FileItemSkeleton key={`skeleton-${index}`} view="list" position={index + 2} style={style} />
                )
              })}
            </div>
          </div>
        </>
      )}
    </div>
  )
})
