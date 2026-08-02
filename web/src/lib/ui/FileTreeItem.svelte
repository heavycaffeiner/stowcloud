<script lang="ts">
  // FileTreeItem.svelte — one lazily-expandable node of FileTree.svelte.
  // Recursion is via self-import (Svelte 5 dropped `<svelte:self>`).
  //
  // Deliberately NOT virtualized like FileTable/FileGrid: a directory tree's
  // *expanded* node count is bounded by how much the user has chosen to open,
  // not by the total file count, so the 100k-row concern DESIGN-FRONTEND.md
  // §5/§9 raises for the listing doesn't apply here. Per-node focus is a
  // plain native tab stop for the same reason (a small tree is not the "100k
  // tab stops" case §9 warns about).
  import { t } from '../i18n'
  import { api, type Entry } from '../api/client'
  import { joinPath } from '../api/path-utils'
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
  let children = $state<Entry[] | null>(null)
  let loading = $state(false)
  let errored = $state(false)

  const isActive = $derived(currentPath === path)
  // Highlights ancestors of the active folder too, so the tree gives context
  // even for folders the user hasn't expanded down into yet.
  const isAncestor = $derived(!isActive && (currentPath === path || currentPath.startsWith(`${path}/`)))
  const indent = $derived(depth * 16 + 8)

  async function loadChildren(): Promise<void> {
    if (children !== null || loading) return
    loading = true
    errored = false
    try {
      // 500 is a lazy-tree pragmatic cap, not a hard backend limit -- a
      // folder with more subfolders than that just shows its first 500.
      // There is no "list directories only" endpoint, so this filters a
      // normal listing page down to dirs after the fact.
      const res = await api.list(path, { limit: 500 })
      children = res.entries.filter((e) => e.kind === 'dir').sort((a, b) => a.name.localeCompare(b.name))
    } catch {
      errored = true
    } finally {
      loading = false
    }
  }

  function toggle(): void {
    expanded = !expanded
    if (expanded) void loadChildren()
  }

  function open(): void {
    onnavigate(path)
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
    {#if loading}
      <p class="sc-tree-row__status" style:padding-inline-start="{indent + 16}px">{t('common.loading')}</p>
    {:else if errored}
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
    height: 32px;
    border-radius: var(--m3-shape-small);
    color: var(--m3c-on-surface);
  }
  .sc-tree-row--ancestor {
    background: color-mix(in srgb, var(--m3c-secondary-container) 40%, transparent);
  }
  .sc-tree-row--active {
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
  }
  .sc-tree-row__twisty {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 20px;
    width: 20px;
    height: 20px;
    border: none;
    background: transparent;
    color: var(--m3c-on-surface-variant);
    cursor: pointer;
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
    padding-inline-end: 8px;
    border: none;
    background: transparent;
    color: inherit;
    @apply --m3-body-medium;
    text-align: left;
    cursor: pointer;
  }
  .sc-tree-row__name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    direction: rtl;
    text-align: left;
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
