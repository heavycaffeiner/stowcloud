import { useEffect, useRef } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useI18n } from '../../../hooks/use-i18n'
import { t } from '../../../lib/i18n'
import { describeApiError } from '../../../api/error-text'
import { Button } from '../../../ui/Button'
import { ErrorBoundary } from '../../../ui/ErrorBoundary'
import { Icon } from '../../../ui/Icon'
import { MenuButton, MenuItem, MenuList } from '../../../ui/Menu'
import { ProgressCircular } from '../../../ui/ProgressCircular'
import { VirtualList } from '../../../ui/VirtualList'
import { useCompact } from '../../../ui/use-compact'
import { cx } from '../../../ui/cx'
import { useOpenSearch } from '../../search/state'
import { Breadcrumb } from '../Breadcrumb'
import { DetailsPanel } from '../DetailsPanel'
import { FileGrid } from '../FileGrid'
import { FileTable, type FileViewHandle } from '../FileTable'
import { FileTree } from '../FileTree'
import { notice, operation, treeOpen } from '../browse-page'
import { splatOf, type BrowseFilterDate, type BrowseFilterType } from '../browse-search'
import {
  chooseSort,
  cycleDensity,
  density,
  detailsPanel,
  sortKey,
  sortOrder,
  toggleDetails,
  toggleViewMode,
  viewMode
} from '../view-prefs'
import { useBrowseMarquee } from '../hooks/use-browse-marquee'
import { useBrowseSearch, useFocusParam } from '../hooks/use-browse-route-effects'
import { selection } from '../selection'
import type { BrowseActions, BrowseListing } from '../hooks/use-browse-actions'
import * as styles from './BrowseView.css'
import * as iconButtonStyles from '../../../ui/IconButton.css'
import * as utilitiesStyles from '../../../ui/utilities.css'
import type { SortKey } from '../api'

export interface BrowseSectionProps {
  path: string
  listing: BrowseListing
  actions: BrowseActions
}

type FilterOption<T extends string> = readonly [value: T, label: string]

const SORT_KEYS: readonly SortKey[] = ['name', 'size', 'mtime', 'kind']

const sortName = (key: SortKey): string =>
  ({
    name: t('browse.sort_by_name'),
    size: t('browse.sort_by_size'),
    mtime: t('browse.sort_by_modified'),
    kind: t('browse.sort_by_kind')
  })[key]

const typeOptions = (): readonly FilterOption<BrowseFilterType>[] => [
  ['all', t('browse.filter_all')],
  ['folders', t('browse.filter_folders')],
  ['documents', t('search.preset_document')],
  ['images', t('search.preset_image')],
  ['videos', t('search.preset_video')],
  ['audio', t('search.preset_audio')],
  ['archives', t('search.preset_archive')]
]

const dateOptions = (): readonly FilterOption<BrowseFilterDate>[] => [
  ['any', t('browse.date_any')],
  ['today', t('browse.date_today')],
  ['7days', t('browse.date_last_7_days')],
  ['30days', t('browse.date_last_30_days')],
  ['this_year', t('browse.date_this_year')]
]

function crumbsOf(path: string): { label: string; path: string }[] {
  const parts = path.split('/').filter(Boolean)
  return [
    { label: t('nav.files'), path: '/' },
    ...parts.map((part, index) => ({ label: part, path: `/${parts.slice(0, index + 1).join('/')}` }))
  ]
}

export function BrowseToolbar({ path, listing, actions }: BrowseSectionProps) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const compact = useCompact()
  const { search, update } = useBrowseSearch()
  const { root, encrypted, unlocked, canCreate } = listing
  const crumbs = crumbsOf(path)
  const mode = viewMode.value
  const details = detailsPanel.value === 'open'
  const sortButton = (
    <MenuButton
      label={t('browse.sort_by', { key: sortName(sortKey.value) })}
      className={cx(styles.actionBtn, iconButtonStyles.root)}
      align="end"
      menu={(close) => <SortMenu close={close} />}
    >
      <Icon name="sort" size={compact ? 20 : 18} />
    </MenuButton>
  )
  return (
    <header className={cx(styles.toolbar, compact && styles.toolbarCompact)}>
      <div className={styles.folderHeading}>
        <h1 className={utilitiesStyles.srOnly}>{crumbs.at(-1)?.label}</h1>
        <Breadcrumb crumbs={crumbs} onNavigate={(next) => void navigate({ to: '/b/$', params: splatOf(next) })} />
        {root?.shared_externally ? (
          <span className={styles.externalBadge}>
            <Icon name="warning" size={14} />
            {t('common.shared_with_other_services')}
          </span>
        ) : null}
        {encrypted ? (
          unlocked ? (
            <span className={styles.encryptedBadge}>
              <Icon name="lock" size={14} />
              {t('browse.encrypted_badge')}
            </span>
          ) : (
            <button
              type="button"
              className={cx(styles.encryptedBadge, styles.encryptedBadgeLocked)}
              onClick={actions.unlockFolder}
            >
              <Icon name="lock" size={14} />
              {t('browse.encrypted_locked_badge')}
            </button>
          )
        ) : null}
        {root?.broken_reason ? (
          <span className={styles.brokenBadge}>
            <Icon name="warning" size={14} />
            {t('browse.this_folder_is_unavailable')}
          </span>
        ) : null}
      </div>
      <div className={styles.toolbarActions}>
        {!compact ? (
          <>
            <FilterPill
              name={t('browse.filter_type')}
              idleLabel={t('browse.filter_type')}
              value={search.type ?? 'all'}
              options={typeOptions()}
              onChoose={(type) => update({ type: type === 'all' ? undefined : type })}
            />
            <FilterPill
              name={t('browse.filter_date')}
              value={search.date ?? 'any'}
              options={dateOptions()}
              onChoose={(date) => update({ date: date === 'any' ? undefined : date })}
            />
            <button
              type="button"
              className={cx(styles.actionBtn, iconButtonStyles.root)}
              aria-label={t('common.refresh')}
              onClick={() => void listing.listing.refetch()}
            >
              <Icon name="refresh" size={18} />
            </button>
            <button
              type="button"
              className={cx(styles.actionBtn, iconButtonStyles.root)}
              aria-label={mode === 'list' ? t('browse.grid_view') : t('browse.list_view')}
              onClick={toggleViewMode}
            >
              <Icon name={mode === 'list' ? 'grid' : 'list'} size={18} />
            </button>
            <button
              type="button"
              className={cx(styles.actionBtn, iconButtonStyles.root, details && styles.actionBtnActive)}
              aria-label={details ? t('details.hide') : t('details.show')}
              onClick={toggleDetails}
            >
              <Icon name="info" size={18} />
            </button>
            {sortButton}
          </>
        ) : (
          <>
            {canCreate ? (
              <MenuButton label={t('browse.new')} className={styles.fabBtn} menu={actions.createMenu}>
                <Icon name="add" size={20} />
              </MenuButton>
            ) : null}
            {sortButton}
            <MenuButton
              label={t('browse.more')}
              className={cx(styles.actionBtn, iconButtonStyles.root)}
              align="end"
              menu={(close) => <OverflowMenu close={close} onRefresh={() => void listing.listing.refetch()} />}
            >
              <Icon name="more-vert" size={20} />
            </MenuButton>
          </>
        )}
      </div>
    </header>
  )
}

/** A filter dropdown. The first option means no filter; `idleLabel` replaces its label on the pill. */
function FilterPill<T extends string>({
  name,
  idleLabel,
  value: current,
  options,
  onChoose
}: {
  name: string
  idleLabel?: string
  value: T
  options: readonly FilterOption<T>[]
  onChoose: (value: T) => void
}) {
  const idle = current === options[0][0]
  const label = idle && idleLabel ? idleLabel : options.find(([value]) => value === current)?.[1]
  return (
    <MenuButton
      label={name}
      className={cx(styles.filterPill, !idle && styles.filterPillActive)}
      menu={(close) => (
        <MenuList>
          {options.map(([value, text]) => (
            <MenuItem
              key={value}
              checked={current === value}
              onClick={() => {
                onChoose(value)
                close()
              }}
            >
              {current === value ? <span aria-hidden="true">✓ </span> : null}
              {text}
            </MenuItem>
          ))}
        </MenuList>
      )}
    >
      <span>{label}</span>
      <Icon name="arrow-drop-down" size={16} />
    </MenuButton>
  )
}

function SortMenu({ close }: { close: () => void }) {
  const { t } = useI18n()
  const current = sortKey.value
  const direction = sortOrder.value === 'asc' ? t('browse.sort_ascending') : t('browse.sort_descending')
  return (
    <MenuList>
      {SORT_KEYS.map((key) => (
        <MenuItem
          key={key}
          onClick={() => {
            chooseSort(key)
            close()
          }}
        >
          {key === current ? t('browse.sort_selected', { label: sortName(key), direction }) : sortName(key)}
        </MenuItem>
      ))}
    </MenuList>
  )
}

/** The compact layout's overflow: view controls the wide toolbar shows as buttons, then page options. */
function OverflowMenu({ close, onRefresh }: { close: () => void; onRefresh: () => void }) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const densityName = {
    compact: t('browse.compact'),
    comfortable: t('browse.comfortable'),
    spacious: t('browse.spacious')
  }[density.value]
  const item = (label: string, run: () => void) => (
    <MenuItem
      onClick={() => {
        close()
        run()
      }}
    >
      {label}
    </MenuItem>
  )
  return (
    <MenuList>
      {item(viewMode.value === 'list' ? t('browse.grid_view') : t('browse.list_view'), toggleViewMode)}
      {item(t('details.show'), toggleDetails)}
      {item(t('common.refresh'), onRefresh)}
      {item(treeOpen.value ? t('browse.hide_folder_tree') : t('browse.show_folder_tree'), () => {
        treeOpen.value = !treeOpen.value
      })}
      {item(t('browse.density', { density: densityName }), cycleDensity)}
      {item(t('browse.open_trash'), () => void navigate({ to: '/trash' }))}
    </MenuList>
  )
}

export function BrowseContent({ path, listing, actions, dragOver }: BrowseSectionProps & { dragOver: boolean }) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const compact = useCompact()
  const openSearch = useOpenSearch()
  const view = useRef<FileViewHandle>(null)
  const marquee = useBrowseMarquee(view)
  const { listing: query, directory, filteredEntries, selected, noShares, encrypted, session } = listing
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = query
  useFocusParam(view, query)
  const all = !listing.filtered
  const dirs = all ? directory.dirs : filteredEntries.filter((entry) => entry.kind === 'dir').length
  const isAdmin = Boolean(session.data?.user.is_admin)

  // A filter has to see the whole folder, so every page loads while one is on.
  useEffect(() => {
    if (!all && hasNextPage && !isFetchingNextPage) void fetchNextPage()
  }, [all, hasNextPage, isFetchingNextPage, fetchNextPage])

  const viewProps = {
    entries: filteredEntries,
    total: all ? directory.total : filteredEntries.length,
    dirs,
    loading: query.isPending,
    loadingMore: isFetchingNextPage,
    requestMore: () => {
      if (hasNextPage && !isFetchingNextPage) void fetchNextPage()
    },
    perms: directory.perms,
    onOpen: actions.open,
    onContextMenu: actions.openRowMenu,
    onRename: () => actions.runOnSelection('rename'),
    onDelete: () => actions.runOnSelection('delete'),
    onSearchFocus: () => openSearch(path === '/' ? '' : path),
    encrypted
  }
  return (
    <div className={styles.content}>
      {treeOpen.value ? (
        <FileTree
          currentPath={path}
          onNavigate={(next) => void navigate({ to: '/b/$', params: splatOf(next) })}
          overlay={compact}
          onClose={() => (treeOpen.value = false)}
        />
      ) : null}
      <div
        className={cx(styles.tableWrap, dragOver && styles.tableWrapDragover, marquee.box && styles.tableWrapMarquee)}
        onPointerDown={marquee.onPointerDown}
        onClick={marquee.onClick}
        onContextMenu={(event) => {
          event.preventDefault()
          selection.clear()
          actions.openCreateMenu({ x: event.clientX, y: event.clientY, trigger: event.currentTarget })
        }}
      >
        {noShares ? (
          <div className={styles.nothing}>
            <div className={styles.nothingIcon} aria-hidden="true">
              <Icon name="folder" size={40} />
            </div>
            <h2 className={styles.nothingTitle}>{t('browse.nothing_here')}</h2>
            <p className={styles.nothingHint}>
              {isAdmin
                ? t('browse.press_this_button_to_set_up_your_first_folder')
                : t('browse.ask_an_administrator_for_a_folder')}
            </p>
            {isAdmin ? (
              <Button onClick={() => void navigate({ to: '/admin/{-$tab}', params: { tab: 'shares' } })}>
                {t('common.add_folder')}
              </Button>
            ) : null}
          </div>
        ) : query.isPending ? (
          <div className={styles.loading}>
            <ProgressCircular size={40} />
          </div>
        ) : query.error ? (
          <p className={styles.error} role="alert">
            {describeApiError(query.error, t('browse.this_folder_could_not_be_opened'))}
          </p>
        ) : (
          <div className={styles.view}>
            {viewMode.value === 'list' ? (
              <FileTable ref={view} {...viewProps} />
            ) : (
              <FileGrid ref={view} {...viewProps} />
            )}
            {isFetchingNextPage ? (
              <div className={styles.loadingMore} role="status" aria-live="polite">
                <ProgressCircular size={40} />
                {t('common.loading')}
              </div>
            ) : null}
          </div>
        )}
        {dragOver ? <div className={styles.dropOverlay}>{t('browse.drop_here_upload')}</div> : null}
      </div>
      {marquee.box ? <div className={styles.marquee} aria-hidden="true" style={marquee.box} /> : null}
      {detailsPanel.value === 'open' ? (
        <ErrorBoundary resetKey={path}>
          <DetailsPanel
            path={path}
            selected={selected}
            total={directory.total}
            dirs={directory.dirs}
            encrypted={encrypted}
            onClose={toggleDetails}
            onDownload={() => void actions.download(selected)}
            onShare={() => {
              if (selected[0]) actions.share(selected[0])
            }}
            onContextMenu={(event) => {
              if (selected[0]) actions.openRowMenu(selected[0], event)
            }}
          />
        </ErrorBoundary>
      ) : null}
    </div>
  )
}

/** Per-item results of the last delete, move or copy. */
export function BrowseOperation() {
  const { t } = useI18n()
  const current = operation.value
  if (!current) return null
  const skipped = current.results.filter((result) => result.skipped).length
  return (
    <section className={styles.operation} role="status" aria-live="polite">
      <div className={styles.operationHeading}>
        <h2 className={styles.operationTitle}>
          {current.kind === 'delete'
            ? t('common.delete')
            : current.kind === 'move'
              ? t('common.move')
              : t('common.copy')}
        </h2>
        <button type="button" className={styles.operationClose} onClick={() => (operation.value = null)}>
          {t('common.close')}
        </button>
      </div>
      <VirtualList
        className={styles.operationList}
        items={current.results}
        itemKey={(result) => result.path}
        estimateSize={48}
        itemProps={(result) => ({ className: cx(styles.operationItem, !result.ok && styles.operationError) })}
        renderItem={(result) => (
          <>
            <span>{result.destination ? `${result.path} to ${result.destination}` : result.path}</span>
            <span>
              {result.ok
                ? result.skipped
                  ? t('browse.items_skipped_name_taken', { count: 1 })
                  : t('common.done')
                : t('error.internal')}
            </span>
          </>
        )}
      />
      {skipped ? <p>{t('browse.items_skipped_name_taken', { count: skipped })}</p> : null}
    </section>
  )
}

export function BrowseNotice() {
  const message = notice.value
  if (!message) return null
  return (
    <div role="status" className={styles.snackbar} onClick={() => (notice.value = null)}>
      {message}
    </div>
  )
}
