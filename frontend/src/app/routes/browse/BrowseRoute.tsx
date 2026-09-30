import { useEffect, useMemo, useRef } from 'react'
import { useNavigate, useParams, useLocation } from 'react-router-dom'
import { useI18n } from '../../../hooks/use-i18n'
import { ui, useUiStore } from '../../../lib/store/ui.store'
import { useViewStore, view } from '../../../lib/store/view.store'
import { selection } from '../../../lib/store/selection.store'
import { search, searchTarget } from '../../../lib/store/search.store'
import { joinPath, normalizePath } from '../../../lib/api/path-utils'
import { describeApiError } from '../../../lib/api/error-text'
import type { Entry, SortKey } from '../../../lib/api/client'
import type { FileGridHandle } from '../../../features/files/FileGrid'
import type { FileViewHandle } from '../../../features/files/FileTable'
import { PreviewDialog } from '../../../features/preview/PreviewDialog'
import { useDocumentTitle } from '../../hooks/use-document-title'
import { useBrowseState } from './hooks/use-browse-state'
import { useBrowseListing } from './hooks/use-browse-listing'
import { useBrowseActions } from './hooks/use-browse-actions'
import { useBrowseMarquee } from './hooks/use-browse-marquee'
import { useBrowseMenus } from './hooks/use-browse-menus'
import { useBrowsePathSelection, useBrowseRouteFocus } from './hooks/use-browse-route-effects'
import { BrowseSelectionBar } from './BrowseSelectionBar'
import { BrowseToolbar, BrowseContent, BrowseDialogs } from './BrowseView'
import '../../../styles/app/routes/browse.css.ts'
import '../../../styles/features/files/browse-ui.css.ts'

export function BrowseRoute() {
  const params = useParams()
  const path = normalizePath(`/${params['*'] ?? ''}`)
  return <BrowsePageContent key={path} path={path} />
}

function BrowsePageContent({ path }: { path: string }) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const location = useLocation()
  const compact = useUiStore((state) => state.compact)
  const details = useUiStore((state) => state.details)
  const mode = useViewStore((state) => state.mode)
  const density = useViewStore((state) => state.density)
  const sortKey = useViewStore((state) => state.sortKey)
  const sortOrder = useViewStore((state) => state.sortOrder)
  const [state, stateActions] = useBrowseState()
  const { patch } = stateActions
  const { filterType, filterDate, previewIndex } = state
  const listing = useBrowseListing(path, filterType, filterDate)
  const {
    session,
    listing: query,
    directory,
    entries,
    filteredEntries,
    selected,
    selectedNames,
    encryption,
    encrypted,
    unlocked,
    root,
    noShares,
    selectionBytes,
    canCreate
  } = listing
  const tableRef = useRef<FileViewHandle>(null)
  const gridRef = useRef<FileGridHandle>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const dirInputRef = useRef<HTMLInputElement>(null)
  const actions = useBrowseActions({
    path,
    entries,
    selected,
    contextEntry: state.contextEntry,
    renameTarget: state.renameTarget,
    canCreate,
    navigate,
    t,
    patch
  })
  const menus = useBrowseMenus({ state, patch, canCreate, t })
  const marquee = useBrowseMarquee({ mode, selectedNames, patch, tableRef, gridRef })
  const crumbs = useMemo(() => {
    const parts = path.split('/').filter(Boolean)
    const result = [{ label: t('nav.files'), path: '/' }]
    let current = ''
    for (const part of parts) {
      current += `/${part}`
      result.push({ label: part, path: current })
    }
    return result
  }, [path, t])
  const focusName = new URLSearchParams(location.search).get('focus')
  useDocumentTitle(`${path.split('/').filter(Boolean).at(-1) ?? 'Stowcloud'} - Stowcloud`)

  useBrowsePathSelection(path)
  useBrowseRouteFocus({
    focusName,
    isPending: query.isPending,
    hasNextPage: query.hasNextPage,
    isFetchingNextPage: query.isFetchingNextPage,
    mode,
    pathname: location.pathname,
    search: location.search,
    navigate,
    tableRef,
    gridRef,
    fetchNextPage: query.fetchNextPage
  })

  const densityLabel =
    density === 'compact'
      ? t('browse.compact')
      : density === 'spacious'
        ? t('browse.spacious')
        : t('browse.comfortable')
  const sortLabel =
    sortKey === 'name'
      ? t('browse.sort_by_name')
      : sortKey === 'size'
        ? t('browse.sort_by_size')
        : sortKey === 'mtime'
          ? t('browse.sort_by_modified')
          : t('browse.sort_by_kind')
  const filterTypeLabel =
    filterType === 'folders'
      ? t('browse.filter_folders')
      : filterType === 'documents'
        ? t('search.preset_document')
        : filterType === 'images'
          ? t('search.preset_image')
          : filterType === 'videos'
            ? t('search.preset_video')
            : filterType === 'audio'
              ? t('search.preset_audio')
              : filterType === 'archives'
                ? t('search.preset_archive')
                : t('browse.filter_type')
  const filterDateLabel =
    filterDate === 'today'
      ? t('browse.date_today')
      : filterDate === '7days'
        ? t('browse.date_last_7_days')
        : filterDate === '30days'
          ? t('browse.date_last_30_days')
          : filterDate === 'this_year'
            ? t('browse.date_this_year')
            : t('browse.date_any')
  const previewEntry = previewIndex >= 0 ? (entries[previewIndex] ?? null) : null
  const hasPreviewNeighbour = (delta: number) => {
    let index = previewIndex + delta
    while (index >= 0 && index < entries.length) {
      if (entries[index].kind !== 'dir') return true
      index += delta
    }
    return false
  }
  const stepPreview = (delta: number) => {
    let index = previewIndex + delta
    while (index >= 0 && index < entries.length) {
      if (entries[index].kind !== 'dir') {
        patch({ previewIndex: index })
        return
      }
      index += delta
    }
  }
  const openSearchForPath = () => {
    const target = searchTarget(compact, path)
    if (target) void navigate(target)
    else search.openSheet(path)
  }
  const actionTarget = selected[0] ?? state.contextEntry
  // A keyboard shortcut runs only what the row menu would offer for the selection.
  const runRowAction = (key: string) => actions.actions.find((action) => action.key === key)?.run()
  const openContextFor = (entry: Entry, event: React.MouseEvent) => {
    if (!selectedNames.has(entry.name)) selection.only(entry.name, entries.indexOf(entry))
    patch({
      contextEntry: entry,
      blankMenu: null,
      menuTrigger: event.currentTarget as HTMLElement,
      contextMenu: { x: event.clientX, y: event.clientY }
    })
  }
  const onDetailsContext = (event: React.MouseEvent) => {
    if (actionTarget) openContextFor(actionTarget, event)
  }
  const onToggleView = () => view.setMode(mode === 'list' ? 'grid' : 'list')
  const onToggleDetails = () => ui.setDetails(!details)
  const onToggleTree = () => patch((current) => ({ treeOpen: !current.treeOpen }))
  const onCycleDensity = () => {
    const order = ['compact', 'comfortable', 'spacious'] as const
    view.setDensity(order[(order.indexOf(density) + 1) % order.length])
  }
  const onChooseSort = (key: SortKey) => {
    view.setSort(key, sortKey === key && sortOrder === 'asc' ? 'desc' : 'asc')
    menus.closeSort()
  }
  const onUnlock = () => {
    if (encryption.data)
      patch({
        unlockTarget: { salt: encryption.data.salt, verifier: encryption.data.verifier, retry: () => undefined }
      })
  }
  if (path === '/' && session.data?.roots[0])
    return <NavigateToRoot path={session.data.roots[0].label} search={location.search} />
  const showingAll = filterType === 'all' && filterDate === 'any'
  return (
    <div
      className="sc-browse"
      role="region"
      aria-label={t('browse.file_browser')}
      onDragOver={(event) => {
        event.preventDefault()
        patch({ dragOver: canCreate })
      }}
      onDragLeave={() => patch({ dragOver: false })}
      onDrop={actions.onDrop}
    >
      <BrowseToolbar
        model={{
          compact,
          details,
          mode,
          canCreate,
          filterType,
          filterDate,
          filterTypeLabel,
          filterDateLabel,
          sortLabel: t('browse.sort_by', { key: sortLabel }),
          crumbs,
          external: Boolean(root?.shared_externally),
          encrypted,
          unlocked,
          broken: Boolean(root?.broken_reason)
        }}
        actions={{
          onNavigate: (next) => void navigate(`/b${next}`),
          onUnlock,
          onFilter: menus.openFilterMenu,
          onRefresh: () => void query.refetch(),
          onToggleView,
          onToggleDetails,
          onSort: menus.openSort,
          onNew: menus.openNewMenu,
          onOverflow: menus.openOverflow
        }}
        t={t}
      />
      {selected.length ? (
        <BrowseSelectionBar
          state={{ compact, details, count: selected.length, bytes: selectionBytes }}
          actions={{ onClear: selection.clear, onToggleDetails, actions: actions.actions }}
          t={t}
        />
      ) : null}
      <BrowseContent
        path={path}
        compact={compact}
        details={details}
        mode={mode}
        filteredEntries={filteredEntries}
        directory={directory}
        noShares={noShares}
        isAdmin={Boolean(session.data?.user.is_admin)}
        isPending={query.isPending}
        isFetchingMore={query.isFetchingNextPage}
        hasNextPage={Boolean(query.hasNextPage)}
        error={query.error}
        errorText={query.error ? describeApiError(query.error, t('browse.this_folder_could_not_be_opened')) : ''}
        encrypted={encrypted}
        dragOver={state.dragOver}
        marqueeRect={state.marqueeRect}
        marqueeScroll={state.marqueeScroll}
        treeOpen={state.treeOpen}
        selected={selected}
        tableRef={tableRef}
        gridRef={gridRef}
        onPointerDown={marquee.onPointerDown}
        onEmptyClick={marquee.onEmptyAreaClick}
        onBlankMenu={marquee.openBlankMenu}
        onOpen={actions.onOpen}
        onContextMenu={openContextFor}
        onRename={() => runRowAction('rename')}
        onDelete={() => runRowAction('delete')}
        onSearchFocus={openSearchForPath}
        onRequestMore={() => {
          if (query.hasNextPage && !query.isFetchingNextPage) void query.fetchNextPage()
        }}
        onTreeNavigate={(next) => void navigate(`/b${next}`)}
        onTreeClose={() => patch({ treeOpen: false })}
        onDetailsClose={onToggleDetails}
        onDownload={actions.downloadSelection}
        onShare={() => {
          const entry = selected[0] ?? state.contextEntry
          if (entry) patch({ shareTarget: entry })
        }}
        onDetailsContext={onDetailsContext}
        onAddFolder={() => void navigate('/admin#shares')}
        t={t}
        showingAll={showingAll}
      />
      <BrowseDialogs
        model={{
          state,
          path,
          root,
          selectedCount: selected.length || (state.contextEntry ? 1 : 0),
          rowActions: actions.actions,
          fileInputRef,
          dirInputRef,
          compact,
          mode,
          densityLabel,
          sortKey,
          sortOrder,
          treeOpen: state.treeOpen,
          canCreate
        }}
        actions={{
          onPatch: patch,
          onCloseSort: menus.closeSort,
          onCloseNew: menus.closeNewMenu,
          onCloseOverflow: menus.closeOverflow,
          onToggleView,
          onToggleDetails,
          onToggleTree,
          onCycleDensity,
          onNavigateTrash: () => void navigate('/trash'),
          onRefresh: () => void query.refetch(),
          onChooseSort,
          onUploadFiles: actions.handleUploadFiles,
          onUploadEntries: actions.handleUploadEntries,
          onCreateFolder: actions.createFolder,
          onRename: actions.doRename,
          onDelete: actions.doDelete,
          onTransfer: (dest, kind) => void actions.transfer(state.destSources, dest, kind, 'fail'),
          onDownloadEntry: actions.downloadEntry,
          onEdit: (entry) => void actions.openEditor(entry)
        }}
        t={t}
      />
      <PreviewDialog
        open={state.previewOpen && previewEntry !== null}
        entry={previewEntry}
        path={previewEntry ? joinPath(path, previewEntry.name) : ''}
        hasPrev={hasPreviewNeighbour(-1)}
        hasNext={hasPreviewNeighbour(1)}
        onClose={() => patch({ previewOpen: false })}
        onPrev={() => stepPreview(-1)}
        onNext={() => stepPreview(1)}
        onDownload={actions.downloadEntry}
        onEdit={(entry) => void actions.openEditor(entry)}
      />
    </div>
  )
}
function NavigateToRoot({ path, search }: { path: string; search: string }) {
  const navigate = useNavigate()
  useEffect(() => {
    void navigate(`/b/${encodeURIComponent(path)}${search}`, { replace: true })
  }, [navigate, path, search])
  return null
}
