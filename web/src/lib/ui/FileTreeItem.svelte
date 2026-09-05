<script lang="ts">
  // One lazily-expandable node of FileTree.svelte.
  // Recursion is via self-import (Svelte 5 dropped `<svelte:self>`).
  //
  // Deliberately NOT virtualized like FileTable/FileGrid: a directory tree's
  // *expanded* node count is bounded by how much the user has chosen to open,
  // not by the total file count, so the 100k-row concern
  // §5/§9 raises for the listing doesn't apply here. Per-node focus is a
  // plain native tab stop for the same reason (a small tree is not the "100k
  // tab stops" case §9 warns about).
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
  }
  let { path, name, depth, currentPath, onnavigate }: Props = $props()

  let expanded = $state(false)

  const isActive = $derived(currentPath === path)
  // Highlights ancestors of the active folder too, so the tree gives context
  // even for folders the user hasn't expanded down into yet.
  const isAncestor = $derived(!isActive && (currentPath === path || currentPath.startsWith(`${path}/`)))
  const indent = $derived(depth * 16 + 8)

  // Name-ascending: the tree wants a stable alphabetical order, and asking
  // the server for it up front means there is nothing left to re-sort here.
  const TREE_SORT: Sort = { key: 'name', order: 'asc' }

  // `enabled` in the thunk is what makes this lazy: a collapsed node's query
  // never runs, and toggling `expanded` is the only thing that turns it on.
  const listing = createInfiniteQuery(() => ({ ...dirListQuery(path, TREE_SORT), enabled: expanded }))
  // 200 (`PAGE_LIMIT`) rows is a lazy-tree pragmatic cap, not a hard backend
  // limit -- a folder with more subfolders than that just shows its first
  // page; nothing here ever calls `fetchNextPage`. There is no "list
  // directories only" endpoint, so this filters a normal listing page down
  // to dirs after the fact.
  const children = $derived(
    listing.data ? dirViewOf(listing.data.pages).entries.filter((e) => e.kind === 'dir') : null
  )

  // Auto-expand ancestors of the active path so navigating via the file table
  // or breadcrumbs immediately reveals the folder's position in the tree.
  $effect(() => {
    if (isAncestor && !expanded) expanded = true
  })

  function toggle(): void {
    expanded = !expanded
  }

  function open(): void {
    onnavigate(path)
    expanded = true
  }
</script>

<li role="treeitem" aria-expanded={expanded} aria-selected={isActive}>
  <div
    class="sc-tree-row m3-layer"
    class:sc-tree-row--active={isActive}
    class:sc-tree-row--ancestor={isAncestor}
    style:padding-inline-start="{indent}px"
  >
    <button class="sc-tree-row__twisty" onclick={toggle} aria-label={expanded ? t('tree.collapse', { name }) : t('tree.expand', { name })}>
      <span class="sc-tree-row__twisty-icon" class:sc-tree-row__twisty-icon--expanded={expanded}>
        <Icon icon={icons['chevron-right']} size={16} />
      </span>
    </button>
    <button class="sc-tree-row__label" onclick={open}>
      <Icon icon={icons.folder} size={16} />
      <span class="sc-tree-row__name">{name}</span>
    </button>
  </div>
  {#if expanded}
    {#if listing.isPending}
      <p class="sc-tree-row__status" style:padding-inline-start="{indent + 16}px">{t('common.loading')}</p>
    {:else if listing.isError}
      <p class="sc-tree-row__status sc-tree-row__status--error" style:padding-inline-start="{indent + 16}px">
        {t('tree.could_not_load_subfolders')}
      </p>
    {:else if children && children.length > 0}
      <ul role="group">
        {#each children as child (child.name)}
          <FileTreeItem
            path={joinPath(path, child.name)}
            name={child.name}
            depth={depth + 1}
            {currentPath}
            {onnavigate}
          />
        {/each}
      </ul>
    {:else if children}
      <p class="sc-tree-row__status" style:padding-inline-start="{indent + 16}px">{t('tree.no_subfolders')}</p>
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
  ul[role='group'] {
    list-style: none;
    margin: 0;
    padding: 0;
  }
</style>
