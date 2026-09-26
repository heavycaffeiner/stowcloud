import type { ChangeEvent, MouseEvent as ReactMouseEvent, RefObject } from 'react'
import type { Entry, Perms } from '../../../lib/api/client'
import type { FileGridHandle } from '../../../features/files/FileGrid'
import type { FileViewHandle } from '../../../features/files/FileTable'
import { FileGrid } from '../../../features/files/FileGrid'
import { FileTable } from '../../../features/files/FileTable'
import { FileTree } from '../../../features/files/FileTree'
import { DetailsPanel } from '../../../features/files/DetailsPanel'
import { NewFolderDialog } from '../../../features/files/NewFolderDialog'
import { RenameDialog } from '../../../features/files/RenameDialog'
import { DeleteDialog } from '../../../features/files/DeleteDialog'
import { ConflictDialog } from '../../../features/files/ConflictDialog'
import { DestinationPickerDialog } from '../../../features/files/DestinationPickerDialog'
import { VirtualList } from '../../../lib/ui/VirtualList'
import { UnlockShareDialog } from '../../../features/shares/UnlockShareDialog'
import { Button } from '../../../lib/ui/Button'
import { Icon } from '../../../lib/ui/Icon'
import { ShareManageDialog } from '../../../features/shares/ShareManageDialog'
import { Menu } from '../../../lib/ui/Menu'
import { Breadcrumb } from '../../../features/files/Breadcrumb'
import { pickDirectory, filesFromWebkitDirectoryInput, supportsDirectoryPicker } from '../../../lib/upload/directory-picker'
import { joinPath } from '../../../lib/api/path-utils'
import type { BrowseState, BrowseFilterDate, BrowseFilterType } from './types'
import type { RowAction } from '../../../features/files/row-actions'

type Patch = (patch: Partial<BrowseState> | ((state: BrowseState) => Partial<BrowseState>)) => void
type Translate = (key: string, params?: Record<string, string | number>) => string

type BrowseToolbarModel = {
  compact: boolean
  details: boolean
  mode: 'list' | 'grid'
  canCreate: boolean
  filterType: BrowseFilterType
  filterDate: BrowseFilterDate
  filterTypeLabel: string
  filterDateLabel: string
  sortLabel: string
  crumbs: { label: string; path: string }[]
  external: boolean
  encrypted: boolean
  unlocked: boolean
  broken: boolean
}

type BrowseToolbarActions = {
  onNavigate: (path: string) => void
  onUnlock: () => void
  onFilter: (kind: 'type' | 'date', event: ReactMouseEvent<HTMLElement>) => void
  onRefresh: () => void
  onToggleView: () => void
  onToggleDetails: () => void
  onSort: (event: ReactMouseEvent<HTMLElement>) => void
  onNew: (event: ReactMouseEvent<HTMLElement>) => void
  onOverflow: (event: ReactMouseEvent<HTMLElement>) => void
}

type ToolbarProps = { model: BrowseToolbarModel; actions: BrowseToolbarActions; t: Translate }

export function BrowseToolbar(props: ToolbarProps) {
  const { model, actions, t } = props
  const { compact, details, mode, canCreate, filterType, filterDate, filterTypeLabel, filterDateLabel, sortLabel, crumbs, external, encrypted, unlocked, broken } = model
  const { onNavigate, onUnlock, onFilter, onRefresh, onToggleView, onToggleDetails, onSort, onNew, onOverflow } = actions
  return <header className={`sc-browse-toolbar${compact ? ' sc-browse-toolbar--compact' : ''}`}>
    <div className="sc-browse-folder-heading">
      <h1 className="sc-sr-only">{crumbs.at(-1)?.label ?? t('nav.files')}</h1>
      <Breadcrumb crumbs={crumbs} onNavigate={onNavigate} />
      {external ? <span className="sc-browse-external-badge"><Icon name="warning" size={14} />{t('common.shared_with_other_services')}</span> : null}
      {encrypted ? unlocked ? <span className="sc-browse-encrypted-badge"><Icon name="lock" size={14} />{t('browse.encrypted_badge')}</span> : <button type="button" className="sc-browse-encrypted-badge sc-browse-encrypted-badge--locked" onClick={onUnlock}><Icon name="lock" size={14} />{t('browse.encrypted_locked_badge')}</button> : null}
      {broken ? <span className="sc-browse-broken-badge"><Icon name="warning" size={14} />{t('browse.this_folder_is_unavailable')}</span> : null}
    </div>
    <div className="sc-browse-toolbar-actions">
      {!compact ? <>
        <button type="button" className={`sc-browse-filter-pill${filterType !== 'all' ? ' sc-browse-filter-pill--active' : ''}`} aria-label={t('browse.filter_type')} onClick={(event) => onFilter('type', event)}><span>{filterTypeLabel}</span><Icon name="arrow-drop-down" size={16} /></button>
        <button type="button" className={`sc-browse-filter-pill${filterDate !== 'any' ? ' sc-browse-filter-pill--active' : ''}`} aria-label={t('browse.filter_date')} onClick={(event) => onFilter('date', event)}><span>{filterDateLabel}</span><Icon name="arrow-drop-down" size={16} /></button>
        <button type="button" className="sc-browse-action-btn sc-icon-button" aria-label={t('common.refresh')} onClick={onRefresh}><Icon name="refresh" size={18} /></button>
        <button type="button" className="sc-browse-action-btn sc-icon-button" aria-label={mode === 'list' ? t('browse.grid_view') : t('browse.list_view')} onClick={onToggleView}><Icon name={mode === 'list' ? 'grid' : 'list'} size={18} /></button>
        <button type="button" className={`sc-browse-action-btn sc-icon-button${details ? ' is-active' : ''}`} aria-label={details ? t('details.hide') : t('details.show')} onClick={onToggleDetails}><Icon name="info" size={18} /></button>
        <button type="button" className="sc-browse-action-btn sc-icon-button" aria-label={sortLabel} onClick={onSort}><Icon name="sort" size={18} /></button>
      </> : <>
        {canCreate ? <button type="button" className="sc-browse-fab-btn" aria-label={t('browse.new')} onClick={onNew}><Icon name="add" size={20} /></button> : null}
        <button type="button" className="sc-browse-action-btn sc-icon-button" aria-label={sortLabel} onClick={onSort}><Icon name="sort" size={20} /></button>
        <button type="button" className="sc-browse-action-btn sc-icon-button" aria-label={t('browse.more')} onClick={onOverflow}><Icon name="more-vert" size={20} /></button>
      </>}
    </div>
  </header>
}

type BrowseContentProps = {
  path: string
  compact: boolean
  details: boolean
  mode: 'list' | 'grid'
  filteredEntries: readonly Entry[]
  directory: { total: number; dirs: number; perms: Perms }
  noShares: boolean
  isAdmin: boolean
  isPending: boolean
  isFetchingMore: boolean
  hasNextPage?: boolean
  error: unknown
  errorText: string
  encrypted: boolean
  dragOver: boolean
  marqueeRect: BrowseState['marqueeRect']
  marqueeScroll: BrowseState['marqueeScroll']
  treeOpen: boolean
  selected: readonly Entry[]
  tableRef: RefObject<FileViewHandle | null>
  gridRef: RefObject<FileGridHandle | null>
  onPointerDown: (event: React.PointerEvent) => void
  onEmptyClick: (event: React.MouseEvent) => void
  onBlankMenu: (event: React.MouseEvent) => void
  onOpen: (entry: Entry) => void
  onContextMenu: (entry: Entry, event: ReactMouseEvent) => void
  onRename: () => void
  onDelete: () => void
  onSearchFocus: () => void
  onRequestMore: () => void
  onTreeNavigate: (next: string) => void
  onTreeClose: () => void
  onDetailsClose: () => void
  onDownload: () => void
  onShare: () => void
  onDetailsContext: (event: ReactMouseEvent) => void
  onAddFolder: () => void
  t: Translate
  showingAll?: boolean
}

export function BrowseContent(props: BrowseContentProps) {
  const { path, compact, details, mode, filteredEntries, directory, noShares, isAdmin, isPending, isFetchingMore, error, errorText, encrypted, dragOver, marqueeRect, marqueeScroll, treeOpen, selected, tableRef, gridRef, onPointerDown, onEmptyClick, onBlankMenu, onOpen, onContextMenu, onRename, onDelete, onSearchFocus, onRequestMore, onTreeNavigate, onTreeClose, onDetailsClose, onDownload, onShare, onDetailsContext, onAddFolder, t, showingAll, hasNextPage } = props
  const all = showingAll ?? filteredEntries.length === directory.total
  const dirs = all ? directory.dirs : filteredEntries.filter((entry) => entry.kind === 'dir').length
  return <div className="sc-browse-content">
    {treeOpen ? <FileTree currentPath={path} onNavigate={onTreeNavigate} overlay={compact} onClose={onTreeClose} /> : null}
    <div className={`sc-browse-table-wrap${dragOver ? ' sc-browse-table-wrap--dragover' : ''}${marqueeRect ? ' sc-browse-table-wrap--marquee' : ''}`} onPointerDown={onPointerDown} onContextMenu={onBlankMenu} onClick={onEmptyClick}>
      {noShares ? <div className="sc-browse-nothing"><div className="sc-browse-nothing-icon" aria-hidden="true"><Icon name="folder" size={40} /></div><h2 className="sc-browse-nothing-title">{t('browse.nothing_here')}</h2><p className="sc-browse-nothing-hint">{isAdmin ? t('browse.press_this_button_to_set_up_your_first_folder') : t('browse.ask_an_administrator_for_a_folder')}</p>{isAdmin ? <Button onClick={onAddFolder}>{t('common.add_folder')}</Button> : null}</div> : isPending ? <div className="sc-browse-loading"><mdui-circular-progress /></div> : error ? <p className="sc-browse-error" role="alert">{errorText}</p> : <div className="sc-browse-view">{mode === 'list' ? <FileTable ref={tableRef} entries={filteredEntries} total={all ? directory.total : filteredEntries.length} dirs={dirs} loading={isPending} loadingMore={isFetchingMore} requestMore={onRequestMore} perms={directory.perms} onOpen={onOpen} onContextMenu={onContextMenu} onRename={onRename} onDelete={onDelete} onSearchFocus={onSearchFocus} encrypted={encrypted} /> : <FileGrid ref={gridRef} entries={filteredEntries} total={all ? directory.total : filteredEntries.length} dirs={dirs} loading={isPending} loadingMore={isFetchingMore} requestMore={onRequestMore} perms={directory.perms} onOpen={onOpen} onContextMenu={onContextMenu} onRename={onRename} onDelete={onDelete} onSearchFocus={onSearchFocus} encrypted={encrypted} />}</div>}
      {dragOver ? <div className="sc-browse-drop-overlay">{t('browse.drop_here_upload')}</div> : null}
    </div>
    {marqueeRect ? <div className="sc-browse-marquee" aria-hidden="true" style={{ left: marqueeRect.left - marqueeScroll.x, top: marqueeRect.top - marqueeScroll.y, width: marqueeRect.right - marqueeRect.left, height: marqueeRect.bottom - marqueeRect.top }} /> : null}
    {details ? <DetailsPanel path={path} selected={selected} total={directory.total} dirs={directory.dirs} encrypted={encrypted} onClose={onDetailsClose} onDownload={onDownload} onShare={onShare} onContextMenu={onDetailsContext} /> : null}
  </div>
}

type BrowseDialogsModel = {
  state: BrowseState
  path: string
  root: { shared_externally?: boolean; trash_enabled?: boolean } | undefined
  selectedCount: number
  rowActions: readonly RowAction[]
  fileInputRef: RefObject<HTMLInputElement | null>
  dirInputRef: RefObject<HTMLInputElement | null>
  compact: boolean
  mode: 'list' | 'grid'
  densityLabel: string
  sortKey: string
  sortOrder: 'asc' | 'desc'
  treeOpen: boolean
  canCreate: boolean
}

type BrowseDialogsActions = {
  onPatch: Patch
  onCloseSort: () => void
  onCloseNew: () => void
  onCloseOverflow: () => void
  onToggleView: () => void
  onToggleDetails: () => void
  onToggleTree: () => void
  onCycleDensity: () => void
  onNavigateTrash: () => void
  onRefresh: () => void
  onChooseSort: (key: 'name' | 'size' | 'mtime' | 'kind') => void
  onUploadFiles: (files: FileList | File[]) => void
  onUploadEntries: (entries: readonly { file: File; relativePath: string }[]) => void
  onCreateFolder: (name: string) => void
  onRename: (name: string) => void
  onDelete: () => void
  onTransfer: (dest: string, kind: 'move' | 'copy') => void
  onDownloadEntry: (entry: Entry) => void
  onEdit: (entry: Entry) => void
}

type BrowseDialogsProps = { model: BrowseDialogsModel; actions: BrowseDialogsActions; t: Translate }

export function BrowseDialogs(props: BrowseDialogsProps) {
  const { model, actions, t } = props
  const { state, path, root, selectedCount, rowActions, fileInputRef, dirInputRef, compact, mode, densityLabel, sortKey, sortOrder, treeOpen, canCreate } = model
  const { onPatch, onCloseSort, onCloseNew, onCloseOverflow, onToggleView, onToggleDetails, onToggleTree, onCycleDensity, onNavigateTrash, onRefresh, onChooseSort, onUploadFiles, onUploadEntries, onCreateFolder, onRename, onDelete, onTransfer, onDownloadEntry, onEdit } = actions
  const { contextMenu, blankMenu, menuTrigger, sortMenuOpen, sortMenuPosition, newMenuOpen, newMenuPosition, overflowOpen, overflowPosition, typeMenuOpen, typeMenuPosition, dateMenuOpen, dateMenuPosition, newFolderOpen, renameTarget, deleteOpen, destOpen, destSources, destCanCopy, destCanMove, conflictOpen, conflictName, conflictRetry, unlockTarget, shareTarget, snackbar, operation } = state
  const closeContext = () => { onPatch({ contextMenu: null }); menuTrigger?.focus(); onPatch({ menuTrigger: null }) }
  const folderUpload = async () => { if (supportsDirectoryPicker()) { try { onUploadEntries(await pickDirectory()) } catch { /* canceled */ } } else dirInputRef.current?.click() }
  const sortName = (key: string) => key === 'name' ? t('browse.sort_by_name') : key === 'size' ? t('browse.sort_by_size') : key === 'mtime' ? t('browse.sort_by_modified') : t('browse.sort_by_kind')
  return <>
    <Menu compact={compact} open={contextMenu !== null} onClose={closeContext} x={contextMenu?.x} y={contextMenu?.y}><div className="sc-browse-new-menu" role="menu">{rowActions.map((action) => <button key={action.key} type="button" role="menuitem" onClick={() => { onPatch({ contextMenu: null }); action.run() }}>{action.label}</button>)}</div></Menu>
    {operation ? <section className="sc-browse-operation" role="status" aria-live="polite"><div className="sc-browse-operation-heading"><h2>{operation.kind === 'delete' ? t('common.delete') : operation.kind === 'move' ? t('common.move') : t('common.copy')}</h2><button type="button" className="sc-browse-operation-close" onClick={() => onPatch({ operation: null })}>{t('common.close')}</button></div><VirtualList items={operation.results} itemKey={(result) => result.path} estimateSize={48} itemProps={(result) => ({ className: !result.ok ? 'sc-browse-operation-error' : undefined })} renderItem={(result) => <><span>{result.destination ? `${result.path} to ${result.destination}` : result.path}</span><span>{result.ok ? result.skipped ? t('browse.items_skipped_name_taken', { count: 1 }) : t('common.done') : t('error.internal')}</span></>} />{operation.results.some((result) => result.skipped) ? <p>{t('browse.items_skipped_name_taken', { count: operation.results.filter((result) => result.skipped).length })}</p> : null}</section> : null}
    <Menu compact={compact} open={blankMenu !== null} onClose={() => { onPatch({ blankMenu: null }); menuTrigger?.focus(); onPatch({ menuTrigger: null }) }} x={blankMenu?.x} y={blankMenu?.y}><div className="sc-browse-new-menu" role="menu">{canCreate ? <><button type="button" role="menuitem" onClick={() => onPatch({ blankMenu: null, newFolderOpen: true })}>{t('common.new_folder')}</button><button type="button" role="menuitem" onClick={() => { onPatch({ blankMenu: null }); fileInputRef.current?.click() }}>{t('common.upload')}</button><button type="button" role="menuitem" onClick={() => { onPatch({ blankMenu: null }); void folderUpload() }}>{t('browse.upload_folder')}</button></> : null}</div></Menu>
    <Menu compact={compact} open={sortMenuOpen} onClose={onCloseSort} x={sortMenuPosition.x} y={sortMenuPosition.y} align="end"><div className="sc-browse-new-menu" role="menu">{(['name', 'size', 'mtime', 'kind'] as const).map((key) => <button type="button" role="menuitem" key={key} onClick={() => onChooseSort(key)}>{sortKey === key ? t('browse.sort_selected', { label: sortName(key), direction: sortOrder === 'asc' ? t('browse.sort_ascending') : t('browse.sort_descending') }) : sortName(key)}</button>)}</div></Menu>
    {canCreate ? <Menu compact={compact} open={newMenuOpen} onClose={onCloseNew} x={newMenuPosition.x} y={newMenuPosition.y} align={newMenuPosition.align}><div className="sc-browse-new-menu" role="menu"><button type="button" role="menuitem" onClick={() => { onCloseNew(); onPatch({ newFolderOpen: true }) }}>{t('common.new_folder')}</button><button type="button" role="menuitem" onClick={() => { onCloseNew(); fileInputRef.current?.click() }}>{t('common.upload')}</button><button type="button" role="menuitem" onClick={() => { onCloseNew(); void folderUpload() }}>{t('browse.upload_folder')}</button></div></Menu> : null}
    <Menu compact={compact} open={overflowOpen} onClose={onCloseOverflow} x={overflowPosition.x} y={overflowPosition.y} align="end"><div className="sc-browse-new-menu" role="menu">{compact ? <><button type="button" role="menuitem" onClick={() => { onToggleView(); onCloseOverflow() }}>{mode === 'list' ? t('browse.grid_view') : t('browse.list_view')}</button><button type="button" role="menuitem" onClick={() => { onToggleDetails(); onCloseOverflow() }}>{t('details.show')}</button><button type="button" role="menuitem" onClick={() => { onCloseOverflow(); onRefresh() }}>{t('common.refresh')}</button></> : null}<button type="button" role="menuitem" onClick={() => { onToggleTree(); onCloseOverflow() }}>{treeOpen ? t('browse.hide_folder_tree') : t('browse.show_folder_tree')}</button><button type="button" role="menuitem" onClick={() => { onCycleDensity(); onCloseOverflow() }}>{t('browse.density', { density: densityLabel })}</button><button type="button" role="menuitem" onClick={onNavigateTrash}>{t('browse.open_trash')}</button></div></Menu>
    <Menu compact={compact} open={typeMenuOpen} onClose={() => onPatch({ typeMenuOpen: false })} x={typeMenuPosition.x} y={typeMenuPosition.y}><div className="sc-browse-new-menu" role="menu">{([['all', /* i18n */ 'browse.filter_all'], ['folders', 'browse.filter_folders'], ['documents', 'search.preset_document'], ['images', 'search.preset_image'], ['videos', 'search.preset_video'], ['audio', 'search.preset_audio'], ['archives', 'search.preset_archive']] as const).map(([value, key]) => <button key={value} type="button" role="menuitemradio" aria-checked={state.filterType === value} onClick={() => onPatch({ filterType: value, typeMenuOpen: false })}>{state.filterType === value ? '✓ ' : ''}{t(key)}</button>)}</div></Menu>
    <Menu compact={compact} open={dateMenuOpen} onClose={() => onPatch({ dateMenuOpen: false })} x={dateMenuPosition.x} y={dateMenuPosition.y}><div className="sc-browse-new-menu" role="menu">{([['any', 'browse.date_any'], ['today', 'browse.date_today'], ['7days', 'browse.date_last_7_days'], ['30days', 'browse.date_last_30_days'], ['this_year', 'browse.date_this_year']] as const).map(([value, key]) => <button key={value} type="button" role="menuitemradio" aria-checked={state.filterDate === value} onClick={() => onPatch({ filterDate: value, dateMenuOpen: false })}>{state.filterDate === value ? '✓ ' : ''}{t(key)}</button>)}</div></Menu>
    <input ref={fileInputRef} type="file" hidden multiple aria-label={t('browse.choose_files_upload')} onChange={(event: ChangeEvent<HTMLInputElement>) => { if (event.currentTarget.files) onUploadFiles(event.currentTarget.files); event.currentTarget.value = '' }} />
    <input ref={dirInputRef} type="file" hidden aria-label={t('browse.choose_folder_upload')} {...{ webkitdirectory: '', directory: '' }} onChange={(event: ChangeEvent<HTMLInputElement>) => { onUploadEntries(filesFromWebkitDirectoryInput(event.currentTarget)); event.currentTarget.value = '' }} />
    <NewFolderDialog open={newFolderOpen} onClose={() => onPatch({ newFolderOpen: false })} onCreate={onCreateFolder} />
    <RenameDialog open={renameTarget !== null} currentName={renameTarget?.name ?? ''} onClose={() => onPatch({ renameTarget: null })} onRename={onRename} />
    <DeleteDialog open={deleteOpen} count={selectedCount} externalShare={root?.shared_externally} trashEnabled={root?.trash_enabled} onClose={() => onPatch({ deleteOpen: false })} onConfirm={onDelete} />
    <DestinationPickerDialog open={destOpen} sources={destSources} canCopy={destCanCopy} canMove={destCanMove} onClose={() => onPatch({ destOpen: false })} onPick={(dest, kind) => { onPatch({ destOpen: false }); onTransfer(dest, kind) }} />
    <ConflictDialog open={conflictOpen} name={conflictName} onClose={() => onPatch({ conflictOpen: false })} onKeepBoth={() => { onPatch({ conflictOpen: false }); conflictRetry?.('rename') }} onOverwrite={() => { onPatch({ conflictOpen: false }); conflictRetry?.('overwrite') }} onSkip={() => { onPatch({ conflictOpen: false }); conflictRetry?.('skip') }} />
    <UnlockShareDialog open={unlockTarget !== null} salt={unlockTarget?.salt ?? ''} verifier={unlockTarget?.verifier ?? ''} onUnlock={() => { const retry = unlockTarget?.retry; onPatch({ unlockTarget: null }); retry?.() }} onClose={() => onPatch({ unlockTarget: null })} />
    {shareTarget ? <ShareManageDialog open path={joinPath(path, shareTarget.name)} targetName={shareTarget.name} targetIsDir={shareTarget.kind === 'dir'} onClose={() => onPatch({ shareTarget: null })} /> : null}
    {snackbar ? <div role="status" className="sc-snackbar" onClick={() => onPatch({ snackbar: null })}>{snackbar}</div> : null}
  </>
}
