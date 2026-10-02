import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { joinPath } from '../../../../lib/path-utils'
import { useSession } from '../../../auth/api'
import { useI18n } from '../../../../hooks/use-i18n'
import { FolderTreeItem } from './FolderTreeItem'
import { cx, StowDrawer, StowIconButton, VirtualList } from '@/shared/ui'
import * as styles from './FolderTree.css'
import { dirViewOf, useDirectory, type Entry } from '../../api'

export interface FolderTreeProps {
  currentPath: string
  onNavigate: (path: string) => void
  overlay?: boolean
  onClose?: () => void
}

interface TreeRoot {
  path: string
  name: string
}

interface DirectoryState {
  children: readonly Entry[]
  size: number
  pending: boolean
  error: boolean
  hasNextPage: boolean
  fetchingNextPage: boolean
  fetchNextPage: () => unknown
}

interface FolderRow extends TreeRoot {
  kind: 'folder'
  key: string
  parent: string | null
  depth: number
  position: number
  setSize: number
}

interface StatusRow {
  kind: 'status'
  key: string
  parent: string
  depth: number
  status: 'loading' | 'error' | 'empty'
}

interface MoreRow {
  kind: 'more'
  key: string
  parent: string
  depth: number
  fetching: boolean
}

type TreeRow = FolderRow | StatusRow | MoreRow

function ancestorPaths(path: string): string[] {
  const paths: string[] = []
  for (let end = path.lastIndexOf('/'); end > 0; end = path.lastIndexOf('/', end - 1)) paths.push(path.slice(0, end))
  return paths
}

// Query observers belong to expanded branches, not windowed rows. Scrolling a
// folder out of view must neither discard its children nor restart its loading.
function DirectoryBranch({
  path,
  currentPath,
  onChange
}: {
  path: string
  currentPath: string
  onChange: (path: string, state: DirectoryState) => void
}) {
  const query = useDirectory(path, { key: 'name', order: 'asc' })
  const directory = useMemo(() => dirViewOf(query.data?.pages), [query.data?.pages])
  const children = useMemo(() => directory.entries.filter((entry) => entry.kind === 'dir'), [directory.entries])
  const state = useMemo<DirectoryState>(
    () => ({
      children,
      size: directory.dirs,
      pending: query.isPending,
      error: query.isError,
      hasNextPage: query.hasNextPage,
      fetchingNextPage: query.isFetchingNextPage,
      fetchNextPage: query.fetchNextPage
    }),
    [
      children,
      directory.dirs,
      query.isPending,
      query.isError,
      query.hasNextPage,
      query.isFetchingNextPage,
      query.fetchNextPage
    ]
  )

  useEffect(() => onChange(path, state), [path, state, onChange])
  useEffect(() => {
    if (!currentPath.startsWith(`${path}/`) || !query.hasNextPage || query.isFetchingNextPage || query.isError) return
    const containsCurrent = children.some((child) => {
      const childPath = joinPath(path, child.name)
      return currentPath === childPath || currentPath.startsWith(`${childPath}/`)
    })
    if (!containsCurrent) void query.fetchNextPage()
  }, [path, currentPath, children, query.hasNextPage, query.isFetchingNextPage, query.isError, query.fetchNextPage])
  return null
}

function visibleTree(
  roots: readonly TreeRoot[],
  expanded: ReadonlySet<string>,
  directories: ReadonlyMap<string, DirectoryState>
) {
  const rows: TreeRow[] = []
  const branches: string[] = []
  const stack: TreeRow[] = []
  for (let index = roots.length - 1; index >= 0; index--) {
    const root = roots[index]
    stack.push({
      ...root,
      kind: 'folder',
      key: root.path,
      parent: null,
      depth: 0,
      position: index + 1,
      setSize: roots.length
    })
  }
  while (stack.length) {
    const row = stack.pop()!
    rows.push(row)
    if (row.kind !== 'folder' || !expanded.has(row.path)) continue
    branches.push(row.path)
    const directory = directories.get(row.path)
    const depth = row.depth + 1
    if (!directory || directory.pending || directory.error) {
      stack.push({
        kind: 'status',
        key: `status:${row.path}`,
        parent: row.path,
        depth,
        status: directory?.error ? 'error' : 'loading'
      })
      continue
    }
    if (directory.hasNextPage) {
      stack.push({
        kind: 'more',
        key: `more:${row.path}`,
        parent: row.path,
        depth,
        fetching: directory.fetchingNextPage
      })
    }
    if (!directory.children.length) {
      if (!directory.hasNextPage)
        stack.push({ kind: 'status', key: `status:${row.path}`, parent: row.path, depth, status: 'empty' })
      continue
    }
    for (let index = directory.children.length - 1; index >= 0; index--) {
      const child = directory.children[index]
      const path = joinPath(row.path, child.name)
      stack.push({
        kind: 'folder',
        key: path,
        path,
        name: child.name,
        parent: row.path,
        depth,
        position: index + 1,
        setSize: directory.size
      })
    }
  }
  return { rows, branches }
}

export function FolderTreeList({
  roots,
  currentPath,
  onNavigate,
  rowSize = 40,
  autoFocus = false,
  'aria-label': ariaLabel
}: {
  roots: readonly TreeRoot[]
  currentPath: string
  onNavigate: (path: string) => void
  rowSize?: number
  /** Marks the row that holds focus as the one a surrounding dialog focuses first. */
  autoFocus?: boolean
  'aria-label'?: string
}) {
  const { t } = useI18n()
  const container = useRef<HTMLDivElement>(null)
  const pendingFocus = useRef<string | null>(null)
  const focusWithin = useRef(false)
  const loadingFocus = useRef<{ parent: string; childCount: number } | null>(null)
  const [treeState, setTreeState] = useState<{
    expanded: Set<string>
    directories: ReadonlyMap<string, DirectoryState>
    focusKey: string | null
  }>({ expanded: new Set(ancestorPaths(currentPath)), directories: new Map(), focusKey: null })
  const { expanded, directories, focusKey } = treeState
  const updateDirectory = useCallback(
    (path: string, state: DirectoryState) => {
      setTreeState((previous) => ({
        ...previous,
        directories:
          previous.directories.get(path) === state
            ? previous.directories
            : new Map(previous.directories).set(path, state)
      }))
    },
    [setTreeState]
  )

  useEffect(() => {
    const ancestors = ancestorPaths(currentPath)
    setTreeState((previous) => ({
      ...previous,
      expanded: ancestors.every((path) => previous.expanded.has(path))
        ? previous.expanded
        : new Set([...previous.expanded, ...ancestors])
    }))
  }, [currentPath, setTreeState])

  const model = useMemo(() => visibleTree(roots, expanded, directories), [roots, expanded, directories])
  const focusable = useMemo(
    () => model.rows.filter((row): row is FolderRow | MoreRow => row.kind !== 'status'),
    [model.rows]
  )
  const focusIndexes = useMemo(() => new Map(focusable.map((row, index) => [row.key, index])), [focusable])
  const fallbackKey =
    focusKey?.startsWith('more:') && focusIndexes.has(focusKey.slice(5)) ? focusKey.slice(5) : undefined
  const focusedKey =
    focusKey && focusIndexes.has(focusKey)
      ? focusKey
      : (fallbackKey ?? (focusIndexes.has(currentPath) ? currentPath : focusable[0]?.key))
  const pinnedKeys = useMemo(() => (focusedKey ? [focusedKey] : []), [focusedKey])

  const focusMounted = (key: string): boolean => {
    const row = container.current?.querySelector<HTMLElement>(`[data-tree-key="${CSS.escape(key)}"]`)
    const target = row?.querySelector<HTMLElement>('[data-tree-label], [data-tree-more]')
    if (!target) return false
    target.focus({ preventScroll: true })
    target.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    return true
  }
  const focusRow = (key: string) => {
    pendingFocus.current = key
    setTreeState((previous) => ({ ...previous, focusKey: key }))
    if (focusMounted(key)) pendingFocus.current = null
  }

  useLayoutEffect(() => {
    const loading = loadingFocus.current
    if (loading && !directories.get(loading.parent)?.fetchingNextPage) {
      const directory = directories.get(loading.parent)
      const child = directory?.children[loading.childCount]
      if (child || !focusIndexes.has(`more:${loading.parent}`)) {
        loadingFocus.current = null
        const next = child ? joinPath(loading.parent, child.name) : loading.parent
        if (focusWithin.current && focusIndexes.has(next)) {
          focusRow(next)
          return
        }
      }
    }
    const key = pendingFocus.current
    if (key && focusIndexes.has(key) && focusMounted(key)) pendingFocus.current = null
    if (focusKey && !focusIndexes.has(focusKey) && focusedKey && focusWithin.current) {
      const activeElement = document.activeElement
      if (activeElement === document.body || container.current?.contains(activeElement)) focusRow(focusedKey)
    }
  })

  const toggle = (path: string) => {
    setTreeState((previous) => ({
      ...previous,
      expanded: (() => {
        const next = new Set(previous.expanded)
        if (next.has(path)) next.delete(path)
        else next.add(path)
        return next
      })()
    }))
  }
  const navigate = (path: string) => {
    setTreeState((previous) => ({
      ...previous,
      expanded: previous.expanded.has(path) ? previous.expanded : new Set(previous.expanded).add(path)
    }))
    onNavigate(path)
  }

  const keyDown = (event: React.KeyboardEvent<HTMLUListElement>) => {
    const target = event.target as HTMLElement
    const key = target.closest<HTMLElement>('[data-tree-key]')?.dataset.treeKey ?? focusedKey
    const index = key ? focusIndexes.get(key) : undefined
    if (index === undefined) return
    const row = focusable[index]
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp' || event.key === 'Home' || event.key === 'End') {
      event.preventDefault()
      const next =
        event.key === 'Home'
          ? 0
          : event.key === 'End'
            ? focusable.length - 1
            : Math.max(0, Math.min(index + (event.key === 'ArrowDown' ? 1 : -1), focusable.length - 1))
      focusRow(focusable[next].key)
    } else if (event.key === 'ArrowLeft') {
      event.preventDefault()
      if (row.kind === 'folder' && expanded.has(row.path)) toggle(row.path)
      else if (row.parent) focusRow(row.parent)
    } else if (event.key === 'ArrowRight' && row.kind === 'folder') {
      event.preventDefault()
      if (!expanded.has(row.path))
        setTreeState((previous) => ({ ...previous, expanded: new Set(previous.expanded).add(row.path) }))
      else if (focusable[index + 1]?.parent === row.path) focusRow(focusable[index + 1].key)
    } else if (event.key === '*' && row.kind === 'folder') {
      event.preventDefault()
      setTreeState((previous) => {
        const next = new Set(previous.expanded)
        for (const sibling of focusable)
          if (sibling.kind === 'folder' && sibling.parent === row.parent) next.add(sibling.path)
        return { ...previous, expanded: next }
      })
    } else if ((event.key === 'Enter' || event.key === ' ') && row.kind === 'folder') {
      event.preventDefault()
      navigate(row.path)
    }
  }

  return (
    <div ref={container} className={styles.list}>
      {model.branches.map((path) => (
        <DirectoryBranch key={path} path={path} currentPath={currentPath} onChange={updateDirectory} />
      ))}
      <VirtualList
        className={styles.tree}
        role="tree"
        aria-label={ariaLabel ?? t('tree.folder_tree')}
        tabIndex={focusedKey ? -1 : 0}
        items={model.rows}
        itemKey={(row) => row.key}
        estimateSize={rowSize}
        pinnedKeys={pinnedKeys}
        onKeyDown={keyDown}
        onFocus={(event) => {
          focusWithin.current = true
          if (event.target === event.currentTarget) {
            if (focusedKey) focusRow(focusedKey)
            return
          }
          const key = (event.target as HTMLElement).closest<HTMLElement>('[data-tree-key]')?.dataset.treeKey
          if (key) {
            if (loadingFocus.current && key !== `more:${loadingFocus.current.parent}`) loadingFocus.current = null
            setTreeState((previous) => ({ ...previous, focusKey: key }))
          }
        }}
        onBlur={(event) => {
          if (!event.currentTarget.contains(event.relatedTarget as Node | null)) focusWithin.current = false
        }}
        itemProps={(row) => ({
          role: row.kind === 'status' ? 'none' : 'treeitem',
          'data-tree-key': row.key,
          'data-tree-node': row.kind === 'folder' ? row.path : undefined,
          'aria-level': row.kind === 'status' ? undefined : row.depth + 1,
          'aria-posinset': row.kind === 'folder' ? row.position : undefined,
          'aria-setsize': row.kind === 'folder' ? row.setSize : undefined,
          'aria-expanded': row.kind === 'folder' ? expanded.has(row.path) : undefined,
          'aria-selected': row.kind === 'folder' ? currentPath === row.path : undefined,
          'aria-current': row.kind === 'folder' && currentPath === row.path ? 'page' : undefined,
          'aria-busy':
            row.kind === 'folder' &&
            expanded.has(row.path) &&
            (!directories.has(row.path) ||
              directories.get(row.path)?.pending ||
              directories.get(row.path)?.fetchingNextPage)
              ? true
              : undefined
        })}
        renderItem={(row) =>
          row.kind === 'folder' ? (
            <FolderTreeItem
              path={row.path}
              name={row.name}
              depth={row.depth}
              active={currentPath === row.path}
              ancestor={currentPath.startsWith(`${row.path}/`)}
              expanded={expanded.has(row.path)}
              tabIndex={focusedKey === row.key ? 0 : -1}
              autoFocus={autoFocus && focusedKey === row.key}
              onNavigate={navigate}
              onToggle={toggle}
            />
          ) : row.kind === 'more' ? (
            <button
              type="button"
              className={styles.treeRowMore}
              style={{ paddingInlineStart: row.depth * 16 + 8 }}
              data-tree-more
              data-autofocus={(autoFocus && focusedKey === row.key) || undefined}
              tabIndex={focusedKey === row.key ? 0 : -1}
              aria-busy={row.fetching || undefined}
              onClick={() => {
                if (row.fetching) return
                const directory = directories.get(row.parent)
                if (!directory) return
                loadingFocus.current = { parent: row.parent, childCount: directory.children.length }
                directory.fetchNextPage()
              }}
            >
              {row.fetching ? t('common.loading') : t('common.continue')}
            </button>
          ) : (
            <p
              className={cx(styles.treeRowStatus, row.status === 'error' && styles.treeRowStatusError)}
              style={{ paddingInlineStart: row.depth * 16 + 8 }}
              role={row.status === 'error' ? 'alert' : row.status === 'loading' ? 'status' : undefined}
            >
              {row.status === 'error'
                ? t('tree.could_not_load_subfolders')
                : row.status === 'loading'
                  ? t('common.loading')
                  : t('tree.no_subfolders')}
            </p>
          )
        }
      />
    </div>
  )
}

export function FolderTree({ currentPath, onNavigate, overlay = false, onClose }: FolderTreeProps) {
  const { t } = useI18n()
  const session = useSession()
  const roots = useMemo(
    () => (session.data?.roots ?? []).map((root) => ({ path: `/${root.label}`, name: root.label })),
    [session.data?.roots]
  )

  const tree = (
    <FolderTreeList
      roots={roots}
      currentPath={currentPath}
      onNavigate={onNavigate}
      rowSize={overlay ? 44 : 40}
      autoFocus={overlay}
    />
  )
  if (!overlay)
    return (
      <nav className={styles.root} aria-label={t('tree.folder_tree')}>
        {tree}
      </nav>
    )
  return (
    <StowDrawer open label={t('tree.folder_tree')} className={styles.overlay} onClose={() => onClose?.()}>
      <div className={styles.overlayHeader}>
        <StowIconButton label={t('tree.close_folder_tree')} icon="close" onClick={onClose} />
      </div>
      <nav aria-label={t('tree.folder_tree')}>{tree}</nav>
    </StowDrawer>
  )
}
