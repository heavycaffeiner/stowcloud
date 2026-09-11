<script lang="ts">
  // One lazily expandable node of FileTree.svelte.
  // Recursion is via self-import (Svelte 5 dropped `<svelte:self>`).
  //
  // The tree uses one roving focus target per visible branch. Children remain
  // lazy: only the first listing page is fetched on expansion, and a
  // continuation control fetches later pages on demand.
  import { onMount } from 'svelte'
  import { t } from '../i18n'
  import { createInfiniteQuery } from '@tanstack/svelte-query'
  import { joinPath } from '../api/path-utils'
  import { dirListQuery, dirViewOf } from '../query/files'
  import type { Sort } from '../query/keys'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'
  import FileTreeItem from './FileTreeItem.svelte'

  interface Props {
    path: string
    name: string
    depth: number
    currentPath: string
    onnavigate: (path: string) => void
    position?: number
    setSize?: number
    initial?: boolean
  }
  let {
    path,
    name,
    depth,
    currentPath,
    onnavigate,
    position,
    setSize,
    initial = false
  }: Props = $props()

  let expanded = $state(false)
  let liEl = $state<HTMLLIElement>()
  let labelEl = $state<HTMLButtonElement>()

  const isActive = $derived(currentPath === path)
  // Highlights ancestors of the active folder too, so the tree gives context
  // even for folders the user has not expanded down into yet.
  const isAncestor = $derived(!isActive && currentPath.startsWith(`${path}/`))
  const indent = $derived(depth * 16 + 8)
  const groupId = $derived(`sc-tree-group-${encodeURIComponent(path).replace(/%/g, '_')}`)

  // Name ascending: the tree wants a stable alphabetical order, and asking
  // the server for it up front means there is nothing left to re-sort here.
  const TREE_SORT: Sort = { key: 'name', order: 'asc' }

  // A collapsed node's query never runs. Expanding the node enables the first
  // page, while later pages are requested only through the continuation
  // control or when revealing the already selected destination.
  const listing = createInfiniteQuery(() => ({ ...dirListQuery(path, TREE_SORT), enabled: expanded }))
  const view = $derived(listing.data ? dirViewOf(listing.data.pages) : null)
  const children = $derived(view?.entries.filter((e) => e.kind === 'dir') ?? null)
  const childSetSize = $derived(view?.dirs)
  const hasMore = $derived(Boolean(listing.hasNextPage))
  const loadingMore = $derived(Boolean(listing.isFetchingNextPage))

  function treeRoot(): HTMLElement | null {
    return labelEl?.closest<HTMLElement>('[role="tree"]') ?? null
  }

  function labelsAndContinuation(): HTMLElement[] {
    const tree = treeRoot()
    return tree
      ? [...tree.querySelectorAll<HTMLElement>('[data-tree-label], [data-tree-more]')]
      : []
  }

  function makeRoving(target: HTMLElement): void {
    const tree = treeRoot()
    if (!tree) return
    for (const button of tree.querySelectorAll<HTMLElement>('[data-tree-label], [data-tree-more]')) {
      button.tabIndex = button === target ? 0 : -1
    }
  }

  function focusTreeTarget(target: HTMLElement | undefined): void {
    if (!target) return
    makeRoving(target)
    target.focus()
  }

  function parentLabel(): HTMLElement | undefined {
    const parentItem = liEl?.parentElement?.closest<HTMLElement>('[role="treeitem"]')
    const label = parentItem?.querySelector<HTMLElement>(':scope > .sc-tree-row [data-tree-label]')
    return label instanceof HTMLElement ? label : undefined
  }

  function childLabel(): HTMLElement | undefined {
    const label = liEl?.querySelector<HTMLElement>(':scope > ul[role="group"] [data-tree-label]')
    return label instanceof HTMLElement ? label : undefined
  }

  function onNodeKeydown(e: KeyboardEvent): void {
    const target = e.currentTarget as HTMLElement
    const items = labelsAndContinuation()
    const index = items.indexOf(target)
    const move = (next: number): void => {
      e.preventDefault()
      focusTreeTarget(items[Math.min(Math.max(next, 0), items.length - 1)])
    }

    if (e.key === 'ArrowDown') {
      if (index >= 0 && index < items.length - 1) move(index + 1)
      return
    }
    if (e.key === 'ArrowUp') {
      if (index > 0) move(index - 1)
      return
    }
    if (e.key === 'Home') {
      move(0)
      return
    }
    if (e.key === 'End') {
      move(items.length - 1)
      return
    }

    if (target === labelEl) {
      if (e.key === 'ArrowRight') {
        e.preventDefault()
        if (!expanded) {
          expanded = true
        } else {
          focusTreeTarget(childLabel())
        }
        return
      }
      if (e.key === 'ArrowLeft') {
        e.preventDefault()
        if (expanded) expanded = false
        else focusTreeTarget(parentLabel())
        return
      }
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault()
        open()
        return
      }
      if (e.key === '*') {
        e.preventDefault()
        const parent = liEl?.parentElement
        for (const item of parent?.children ?? []) {
          const toggleButton = (item as HTMLElement).querySelector<HTMLButtonElement>(
            ':scope > .sc-tree-row [data-tree-toggle][aria-expanded="false"]'
          )
          toggleButton?.click()
        }
      }
      return
    }

    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      void loadMore()
    }
  }

  function syncDomPosition(): void {
    if (!liEl || position !== undefined || setSize !== undefined) return
    const siblings = [...(liEl.parentElement?.children ?? [])].filter(
      (item) => item instanceof HTMLElement && item.getAttribute('role') === 'treeitem'
    )
    const index = siblings.indexOf(liEl)
    if (index >= 0) {
      liEl.setAttribute('aria-posinset', String(index + 1))
      liEl.setAttribute('aria-setsize', String(siblings.length))
    }
  }

  onMount(() => {
    const tree = treeRoot()
    if (initial || !tree?.querySelector('[data-tree-label][tabindex="0"]')) {
      if (labelEl) labelEl.tabIndex = 0
    }
    syncDomPosition()
  })

  // Auto expand ancestors of the active path so navigation via the file table
  // or breadcrumbs reveals the folder's position in the tree. If the selected
  // descendant is beyond page one, continue loading only this branch until it
  // can be shown.
  $effect(() => {
    if (isAncestor && !expanded) expanded = true
    if (!isAncestor || !expanded || !children || !hasMore || loadingMore) return
    const containsCurrent = children.some((child) => {
      const childPath = joinPath(path, child.name)
      return currentPath === childPath || currentPath.startsWith(`${childPath}/`)
    })
    if (!containsCurrent) void listing.fetchNextPage()
  })

  $effect(() => {
    // Page completion changes the sibling positions and may insert the
    // continuation control after the group.
    children
    hasMore
    queueMicrotask(syncDomPosition)
  })

  function toggle(): void {
    expanded = !expanded
    labelEl?.focus()
  }

  function open(): void {
    onnavigate(path)
    expanded = true
  }

  async function loadMore(): Promise<void> {
    if (!hasMore || loadingMore) return
    await listing.fetchNextPage()
  }
</script>

<li
  bind:this={liEl}
  role="treeitem"
  aria-expanded={expanded}
  aria-selected={isActive}
  aria-current={isActive ? 'page' : undefined}
  aria-level={depth + 1}
  aria-posinset={position}
  aria-setsize={setSize}
  aria-busy={listing.isPending || loadingMore ? 'true' : undefined}
  data-tree-node={path}
>
  <div
    class="sc-tree-row m3-layer"
    class:sc-tree-row--active={isActive}
    class:sc-tree-row--ancestor={isAncestor}
    style:padding-inline-start="{indent}px"
  >
    <button
      type="button"
      class="sc-tree-row__twisty"
      data-tree-toggle
      tabindex="-1"
      aria-expanded={expanded}
      aria-controls={expanded ? groupId : undefined}
      aria-label={expanded ? t('tree.collapse', { name }) : t('tree.expand', { name })}
      onclick={toggle}
    >
      <span class="sc-tree-row__twisty-icon" class:sc-tree-row__twisty-icon--expanded={expanded}>
        <Icon icon={icons['chevron-right']} size={16} />
      </span>
    </button>
    <button
      bind:this={labelEl}
      type="button"
      class="sc-tree-row__label"
      data-tree-label
      tabindex={initial ? 0 : -1}
      onclick={open}
      onkeydown={onNodeKeydown}
      onfocus={() => labelEl && makeRoving(labelEl)}
    >
      <Icon icon={icons.folder} size={16} />
      <span class="sc-tree-row__name">{name}</span>
    </button>
  </div>
  {#if expanded}
    {#if listing.isPending}
      <p
        class="sc-tree-row__status"
        role="status"
        aria-live="polite"
        style:padding-inline-start="{indent + 16}px"
      >
        {t('common.loading')}
      </p>
    {:else if listing.isError}
      <p class="sc-tree-row__status sc-tree-row__status--error" role="alert" style:padding-inline-start="{indent + 16}px">
        {t('tree.could_not_load_subfolders')}
      </p>
    {:else if children}
      {#if children.length > 0}
        <ul id={groupId} role="group" aria-busy={loadingMore ? 'true' : undefined}>
          {#each children as child, i (child.name)}
            <FileTreeItem
              path={joinPath(path, child.name)}
              name={child.name}
              depth={depth + 1}
              {currentPath}
              {onnavigate}
              position={i + 1}
              setSize={childSetSize}
            />
          {/each}
        </ul>
      {:else}
        <p class="sc-tree-row__status" style:padding-inline-start="{indent + 16}px">{t('tree.no_subfolders')}</p>
      {/if}
      {#if hasMore}
        <button
          type="button"
          class="sc-tree-row__more"
          data-tree-more
          tabindex="-1"
          aria-busy={loadingMore ? 'true' : undefined}
          style:padding-inline-start="{indent + 16}px"
          onclick={() => void loadMore()}
          onkeydown={onNodeKeydown}
        >
          {loadingMore ? t('common.loading') : t('common.continue')}
        </button>
      {/if}
    {/if}
  {/if}
</li>

<style>
  .sc-tree-row {
    display: flex;
    align-items: center;
    height: 40px;
    border-radius: var(--m3-shape-full);
    color: var(--m3c-on-surface);
    transition: background-color var(--m3-duration-fast) var(--m3-easing), color var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-tree-row--ancestor {
    background: color-mix(in srgb, var(--m3c-secondary-container) 40%, transparent);
  }
  .sc-tree-row--active {
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
    font-weight: 600;
  }
  .sc-tree-row--active :global(svg) {
    color: var(--m3c-primary);
  }
  .sc-tree-row__twisty {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 24px;
    width: 24px;
    height: 24px;
    border: none;
    border-radius: var(--m3-shape-full);
    background: transparent;
    color: var(--m3c-on-surface-variant);
    cursor: pointer;
    transition: background-color var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-tree-row__twisty:hover {
    background: color-mix(in srgb, currentColor 8%, transparent);
  }
  .sc-tree-row__twisty-icon {
    display: inline-flex;
    transition: transform var(--m3-easing-fast);
  }
  .sc-tree-row__twisty-icon--expanded {
    transform: rotate(90deg);
  }
  .sc-tree-row__label {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 1;
    min-width: 0;
    height: 100%;
    /* Both sides, not just the end: a <button>'s UA padding is `1px 6px`, so
       declaring only the end left a 6px inset on the start that nothing here
       asked for and that is not on the spacing scale. */
    padding-inline: 0 8px;
    border: none;
    background: transparent;
    color: inherit;
    @apply --m3-body-medium;
    text-align: left;
    cursor: pointer;
  }
  /* `direction: rtl` used to live here to ellipsize from the left. It also
     hands the string to the bidi algorithm as right-to-left paragraph text,
     which reorders any run the string starts with that is not a strong LTR
     character: a folder called `2026-iceland` rendered as `iceland-2026`,
     with no truncation involved and nothing on screen to suggest the name
     shown was not the name on disk. This column holds one folder name, not a
     path, so there is no tail worth preserving at the cost of that. */
  .sc-tree-row__name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-tree-row__status {
    margin: 0;
    height: 28px;
    display: flex;
    align-items: center;
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-tree-row__status--error {
    color: var(--m3c-error);
  }
  .sc-tree-row__more {
    display: flex;
    align-items: center;
    width: 100%;
    min-height: 36px;
    border: 0;
    background: transparent;
    padding-block: 4px;
    @apply --m3-body-small;
    text-align: left;
    cursor: pointer;
  }
  ul[role='group'] {
    list-style: none;
    margin: 0;
    padding: 0;
  }
</style>
