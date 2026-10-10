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
import { parentOf } from '../../../lib/path-utils'
import { useOpenSearch } from '../../search/state'
import { BreadcrumbPath } from './BreadcrumbPath'
import { DetailsPanel } from './DetailsPanel'
import { FileGrid } from './FileGrid'
import { fileIcon } from './FileItem'
import { FileList } from './FileList'
import { FolderTree } from './FolderTree'
import { ParentFolderItem } from './ParentFolderItem'
import { notice, operation, treeOpen } from '../browse-page'
import { splatOf } from '../browse-search'
import type { BrowseFilterDate, BrowseFilterType } from '../model/filtering'
import { cycleDensity, density, detailsPanel, toggleDetails, toggleViewMode, viewMode } from '../model/view-prefs'
import { chooseSort, SORT_KEYS, SORT_LABEL_KEYS, sortKey, sortOrder } from '../model/sorting'
import type { FileEntry } from '../model/file-entry'
import type { FileViewHandle } from '../hooks/use-file-view'
import { Thumbnail } from '../../preview/components/Thumbnail'
import { useBrowseMarquee } from '../hooks/use-browse-marquee'
import { useBrowseSearch, useFocusParam } from '../hooks/use-browse-route-effects'
import { selection } from '../model/selection'
import type { BrowseActions, BrowseListing } from '../hooks/use-browse-actions'
import type { RowMenuAnchor } from '../logic/row-actions'
import * as styles from './BrowseView.css'
import { srOnly } from '@/shared/theme'

export interface BrowseSectionProps {
  path: string
  listing: BrowseListing
  actions: BrowseActions
}

type FilterOption<T extends string> = readonly [value: T, label: string]

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

export function BrowseToolbar({ path, listing, actions }: BrowseSectionProps) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const compact = useCompact()
  const { search, update } = useBrowseSearch()
  const { root, encrypted, unlocked, canCreate } = listing
  const mode = viewMode.value
  const details = detailsPanel.value === 'open'
  const sortLabel = t('browse.sort_by', { key: t(SORT_LABEL_KEYS[sortKey.value]) })
  const sortButton = (
    <StowMenuButton label={sortLabel} align="end" menu={(close) => <SortMenu close={close} />}>
      <StowIconButton label={sortLabel} icon="sort" />
    </StowMenuButton>
  )
  return (
    <header className={cx(styles.toolbar, compact && styles.toolbarCompact)}>
      <div className={styles.folderHeading}>
        <h1 className={srOnly}>{path.split('/').filter(Boolean).at(-1) ?? t('nav.files')}</h1>
        <BreadcrumbPath path={path} onNavigate={(next) => void navigate({ to: '/b/$', params: splatOf(next) })} />
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
      {key === current
        ? t('browse.sort_selected', { label: t(SORT_LABEL_KEYS[key]), direction })
        : t(SORT_LABEL_KEYS[key])}
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
  const { listing: query, directory, items, entryOf, selected, noShares, encrypted, session } = listing
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = query
  useFocusParam(view, query)
  const all = !listing.filtered
  const isAdmin = Boolean(session.data?.user.is_admin)
  const parent = parentOf(path)

  // A filter has to see the whole folder, so every page loads while one is on.
  useEffect(() => {
    if (!all && hasNextPage && !isFetchingNextPage) void fetchNextPage()
  }, [all, hasNextPage, isFetchingNextPage, fetchNextPage])

  const viewProps = {
    items,
    total: all ? directory.total : items.length,
    dirs: all ? directory.dirs : items.filter((item) => item.type === 'folder').length,
    loading: query.isPending,
    loadingMore: isFetchingNextPage,
    requestMore: () => {
      if (hasNextPage && !isFetchingNextPage) void fetchNextPage()
    },
    onOpen: (item: FileEntry) => {
      const entry = entryOf(item)
      if (entry) actions.open(entry)
    },
    onContextMenu: (item: FileEntry, anchor: RowMenuAnchor) => {
      const entry = entryOf(item)
      if (entry) actions.openRowMenu(entry, anchor)
    },
    onRename: () => actions.runOnSelection('rename'),
    onDelete: () => actions.runOnSelection('delete'),
    onSearchFocus: () => openSearch(path === '/' ? '' : path),
    onNavigateParent: parent !== '/' ? () => void navigate({ to: '/b/$', params: splatOf(parent) }) : undefined,
    encrypted
  }
  return (
    <div className={styles.content}>
      {treeOpen.value ? (
        <FolderTree
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
        {parent !== '/' && (query.isPending || query.error) ? (
          <ParentFolderItem onNavigate={() => void navigate({ to: '/b/$', params: splatOf(parent) })} />
        ) : null}
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
              <FileList ref={view} {...viewProps} />
            ) : (
              <FileGrid
                ref={view}
                {...viewProps}
                thumbnail={(item) => {
                  const entry = entryOf(item)
                  return entry ? (
                    <Thumbnail entry={entry} dim={512} fallback={fileIcon(item.type)} iconSize={40} />
                  ) : null
                }}
              />
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
