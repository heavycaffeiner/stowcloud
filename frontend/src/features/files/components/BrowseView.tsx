import { useEffect, useRef } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useI18n } from '../../../hooks/use-i18n'
import { t } from '../../../i18n'
import { describeApiError } from '../../../api/error-text'
import {
  cx,
  ErrorBoundary,
  Icon,
  StowBadge,
  StowButton,
  StowIconButton,
  StowMenuButton,
  StowMenuItem,
  StowProgressCircular,
  StowSnackbar,
  VirtualList,
  type IconName
} from '@/shared/ui'
import { useCompact } from '@/hooks/use-compact'
import { useOpenSearch } from '../../search/state'
import { Breadcrumb } from './Breadcrumb'
import { DetailsPanel } from './DetailsPanel'
import { FileGrid } from './FileGrid'
import { FileTable, type FileViewHandle } from './FileTable'
import { FileTree } from './FileTree'
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
import { srOnly } from '@/shared/theme'
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
  const sortLabel = t('browse.sort_by', { key: sortName(sortKey.value) })
  const sortButton = (
    <StowMenuButton label={sortLabel} align="end" menu={(close) => <SortMenu close={close} />}>
      <StowIconButton label={sortLabel} icon="sort" />
    </StowMenuButton>
  )
  return (
    <header className={cx(styles.toolbar, compact && styles.toolbarCompact)}>
      <div className={styles.folderHeading}>
        <h1 className={srOnly}>{crumbs.at(-1)?.label}</h1>
        <Breadcrumb crumbs={crumbs} onNavigate={(next) => void navigate({ to: '/b/$', params: splatOf(next) })} />
        {root?.shared_externally ? (
          <StowBadge tone="warning" icon="warning">
            {t('common.shared_with_other_services')}
          </StowBadge>
        ) : null}
        {encrypted ? (
          unlocked ? (
            <StowBadge icon="lock">{t('browse.encrypted_badge')}</StowBadge>
          ) : (
            <StowButton variant="tonal" icon={<Icon name="lock" size={18} />} onClick={actions.unlockFolder}>
              {t('browse.encrypted_locked_badge')}
            </StowButton>
          )
        ) : null}
        {root?.broken_reason ? (
          <StowBadge tone="danger" icon="warning">
            {t('browse.this_folder_is_unavailable')}
          </StowBadge>
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
            <StowIconButton label={t('common.refresh')} icon="refresh" onClick={() => void listing.listing.refetch()} />
            <StowIconButton
              label={mode === 'list' ? t('browse.grid_view') : t('browse.list_view')}
              icon={mode === 'list' ? 'grid' : 'list'}
              onClick={toggleViewMode}
            />
            <StowIconButton label={t('details.title')} icon="info" selected={details} onClick={toggleDetails} />
            {sortButton}
          </>
        ) : (
          <>
            {canCreate ? (
              <StowMenuButton label={t('browse.new')} menu={actions.createMenu}>
                <StowIconButton label={t('browse.new')} icon="add" variant="filled" />
              </StowMenuButton>
            ) : null}
            {sortButton}
            <StowMenuButton
              label={t('browse.more')}
              align="end"
              menu={(close) => <OverflowMenu close={close} onRefresh={() => void listing.listing.refetch()} />}
            >
              <StowIconButton label={t('browse.more')} icon="more-vert" />
            </StowMenuButton>
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
    <StowMenuButton
      label={name}
      menu={(close) =>
        options.map(([value, text]) => (
          <StowMenuItem
            key={value}
            checked={current === value}
            onClick={() => {
              onChoose(value)
              close()
            }}
          >
            {text}
          </StowMenuItem>
        ))
      }
    >
      <StowButton
        variant={idle ? 'outlined' : 'tonal'}
        endIcon={<Icon name="arrow-drop-down" size={18} />}
        aria-label={label === name ? name : `${name}: ${label}`}
      >
        {label}
      </StowButton>
    </StowMenuButton>
  )
}

function SortMenu({ close }: { close: () => void }) {
  const { t } = useI18n()
  const current = sortKey.value
  const direction = sortOrder.value === 'asc' ? t('browse.sort_ascending') : t('browse.sort_descending')
  return SORT_KEYS.map((key) => (
    <StowMenuItem
      key={key}
      checked={key === current}
      onClick={() => {
        chooseSort(key)
        close()
      }}
    >
      {key === current ? t('browse.sort_selected', { label: sortName(key), direction }) : sortName(key)}
    </StowMenuItem>
  ))
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
  const item = (icon: IconName, label: string, run: () => void) => (
    <StowMenuItem
      icon={icon}
      onClick={() => {
        close()
        run()
      }}
    >
      {label}
    </StowMenuItem>
  )
  const list = viewMode.value === 'list'
  return (
    <>
      {item(list ? 'grid' : 'list', list ? t('browse.grid_view') : t('browse.list_view'), toggleViewMode)}
      {item('info', t('details.show'), toggleDetails)}
      {item('refresh', t('common.refresh'), onRefresh)}
      {item('folder-tree', treeOpen.value ? t('browse.hide_folder_tree') : t('browse.show_folder_tree'), () => {
        treeOpen.value = !treeOpen.value
      })}
      {item('tune', t('browse.density', { density: densityName }), cycleDensity)}
      {item('trash', t('browse.open_trash'), () => void navigate({ to: '/trash' }))}
    </>
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
              <StowButton onClick={() => void navigate({ to: '/admin/{-$tab}', params: { tab: 'shares' } })}>
                {t('common.add_folder')}
              </StowButton>
            ) : null}
          </div>
        ) : query.isPending ? (
          <div className={styles.loading}>
            <StowProgressCircular size={40} />
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
                <StowProgressCircular size={40} />
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
        <StowButton variant="text" onClick={() => (operation.value = null)}>
          {t('common.close')}
        </StowButton>
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
  return <StowSnackbar className={styles.snackbar} message={notice.value} onDismiss={() => (notice.value = null)} />
}
