<script lang="ts">
  // NavigationDrawer.svelte: the standard navigation surface and the compact
  // folder selector. Files is always a destination; folder switching is a
  // separately named control with the permitted roots beneath it.
  import { t } from '../i18n'
  import type { IconifyIcon } from '@iconify/types'
  import IconButton from './IconButton.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'
  import { createMutation } from '@tanstack/svelte-query'
  import { setRootOrderMutation } from '../query/account'
  import { describeApiError } from '../api/error-text'

  export interface NavItem {
    id: string
    label: string
    icon: IconifyIcon
    href?: string
  }
  export interface RootItem {
    id: string
    label: string
    icon: IconifyIcon
  }

  //   /* i18n */ 'nav.folders'

  interface Props {
    navItems?: NavItem[]
    activeNav?: string
    items?: RootItem[]
    active?: string
    onselect?: (item: RootItem) => void
    onnavselect?: (item: NavItem) => void
    overlay?: boolean
    onclose?: () => void
    folderSelectorOnly?: boolean
  }

  let {
    navItems = [],
    activeNav = 'files',
    items = [],
    active = '',
    onselect,
    onnavselect,
    overlay = false,
    onclose,
    folderSelectorOnly = false
  }: Props = $props()


  let dialogEl: HTMLDialogElement | undefined = $state()
  let foldersExpanded = $state(true)

  const setOrderMut = createMutation(() => setRootOrderMutation())

  /** True while the root list shows move buttons instead of the plain
   *  click-to-switch rows, so a person nudging the order can't also switch
   *  folders by mistake. */
  let reordering = $state(false)

  /** Non-null exactly while a local reorder is ahead of what `items` (the
   *  session's own order) currently reports: the window between an
   *  optimistic move and either the session catching up or the save
   *  failing. `displayRoots` is what the move buttons act on and paint. */
  let pendingOrder = $state<RootItem[] | null>(null)
  const displayRoots = $derived(pendingOrder ?? items)
  let orderError = $state<string | null>(null)

  $effect(() => {
    // The moment `items` reports a new order (this mutation's invalidation
    // landed, or another tab moved a root first), the override standing in
    // for it is stale: the session is the source of truth again.
    void items
    pendingOrder = null
  })

  $effect(() => {
    if (!overlay || !dialogEl) return
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
    dialogEl.showModal()
    return () => {
      dialogEl?.close()
      trigger?.focus()
    }
  })

  function onDialogClick(e: MouseEvent): void {
    if (e.target === dialogEl) onclose?.()
  }

  function toggleFolders(): void {
    foldersExpanded = !foldersExpanded
  }

  function handleFilesClick(): void {
    onnavselect?.({ id: 'files', label: t('nav.files'), icon: icons.home, href: '/b' })
    if (overlay) onclose?.()
  }

  function handleNavClick(item: NavItem): void {
    onnavselect?.(item)
    if (overlay) onclose?.()
  }

  function toggleReordering(): void {
    reordering = !reordering
  }

  /** Applies at once: the visible order changes before the request even
   *  starts, and only unwinds back to what the session reports if the save
   *  is refused. The server matches roots by label, so that is exactly what
   *  goes over the wire. */
  function moveRoot(index: number, direction: -1 | 1): void {
    const target = index + direction
    if (target < 0 || target >= displayRoots.length) return
    const next = displayRoots.slice()
    ;[next[index], next[target]] = [next[target], next[index]]
    pendingOrder = next
    orderError = null
    setOrderMut.mutate(next.map((r) => r.id), {
      onError: (err) => {
        pendingOrder = null
        orderError = describeApiError(err, t('nav.could_not_save_order'))
      }
    })
  }
</script>

{#snippet content()}
  <!-- Top brand header (desktop: clean text title without home icon, overlay has close button) -->
  {#if !overlay}
    <div class="sc-nav-drawer__brand">
      <span class="sc-nav-drawer__app-name">Stowcloud</span>
    </div>
  {/if}

  <div class="sc-nav-drawer__body">

    {#if !folderSelectorOnly}
    <!-- Primary destination. Choosing Files never toggles this surface. -->
    <div class="sc-nav-drawer__section-title">{t('nav.files')}</div>
    <ul class="sc-nav-drawer__list">
      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          class:sc-nav-drawer__item--active={activeNav === 'files'}
          onclick={handleFilesClick}
          aria-current={activeNav === 'files' ? 'page' : undefined}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.home} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('nav.files')}</span>
        </button>
      </li>
    </ul>
    {/if}

    <!-- Folder switching is a separate, named control rather than a side
         effect of activating the Files destination. -->
    <div class="sc-nav-drawer__section-title">{t('nav.folders')}</div>
    <ul class="sc-nav-drawer__list" aria-label={t('nav.folder_selector')}>
      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          aria-expanded={foldersExpanded}
          aria-controls="sc-nav-drawer-folders"
          onclick={toggleFolders}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons['folder-tree']} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('nav.browse_folders')}</span>
          {#if displayRoots.length > 0}
            <span
              class="sc-nav-drawer__twisty-right"
              class:sc-nav-drawer__twisty-right--expanded={foldersExpanded}
              aria-hidden="true"
            >
              <Icon icon={icons['chevron-right']} size={16} />
            </span>
          {/if}
        </button>

        {#if foldersExpanded && displayRoots.length > 0}
          <ul id="sc-nav-drawer-folders" class="sc-nav-drawer__sublist">
            <li class="sc-nav-drawer__reorder-row">
              <button type="button" class="sc-nav-drawer__reorder-toggle" aria-pressed={reordering} onclick={toggleReordering}>
                {reordering ? t('nav.reorder_done') : t('nav.reorder')}
              </button>
            </li>
            {#each displayRoots as root, i (root.id)}
              <li>
                {#if reordering}
                  <div class="sc-nav-drawer__subitem sc-nav-drawer__subitem--reorder">
                    <span class="sc-nav-drawer__indent" aria-hidden="true"></span>
                    <span class="sc-nav-drawer__item-icon"><Icon icon={icons.folder} size={18} /></span>
                    <span class="sc-nav-drawer__subitem-label sc-filename">{root.label}</span>
                    <span class="sc-nav-drawer__reorder-actions">
                      <IconButton label={t('nav.move_up', { name: root.label })} disabled={i === 0} onclick={() => moveRoot(i, -1)}>
                        <span class="sc-nav-drawer__reorder-chevron sc-nav-drawer__reorder-chevron--up">
                          <Icon icon={icons['chevron-right']} size={16} />
                        </span>
                      </IconButton>
                      <IconButton
                        label={t('nav.move_down', { name: root.label })}
                        disabled={i === displayRoots.length - 1}
                        onclick={() => moveRoot(i, 1)}
                      >
                        <span class="sc-nav-drawer__reorder-chevron sc-nav-drawer__reorder-chevron--down">
                          <Icon icon={icons['chevron-right']} size={16} />
                        </span>
                      </IconButton>
                    </span>
                  </div>
                {:else}
                  <button
                    type="button"
                    class="sc-nav-drawer__subitem"
                    class:sc-nav-drawer__subitem--active={active === root.id}
                    aria-current={active === root.id ? 'page' : undefined}
                    onclick={() => {
                      onselect?.(root)
                      if (overlay) onclose?.()
                    }}
                  >
                    <span class="sc-nav-drawer__indent" aria-hidden="true"></span>
                    <span class="sc-nav-drawer__item-icon"><Icon icon={icons.folder} size={18} /></span>
                    <span class="sc-nav-drawer__subitem-label sc-filename">{root.label}</span>
                  </button>
                {/if}
              </li>
            {/each}
            {#if orderError}
              <li class="sc-nav-drawer__reorder-error" role="alert">{orderError}</li>
            {/if}
          </ul>
        {/if}
      </li>
    </ul>

    {#if !folderSelectorOnly}
    <ul class="sc-nav-drawer__list">
      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          class:sc-nav-drawer__item--active={activeNav === 'recent'}
          aria-current={activeNav === 'recent' ? 'page' : undefined}
          onclick={() => handleNavClick({ id: 'recent', label: t('nav.recent'), icon: icons.recent, href: '/recent' })}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.recent} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('nav.recent')}</span>
        </button>
      </li>

      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          class:sc-nav-drawer__item--active={activeNav === 'trash'}
          aria-current={activeNav === 'trash' ? 'page' : undefined}
          onclick={() => handleNavClick({ id: 'trash', label: t('common.trash'), icon: icons.trash, href: '/trash' })}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.trash} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('common.trash')}</span>
        </button>
      </li>

      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          class:sc-nav-drawer__item--active={activeNav === 'links'}
          aria-current={activeNav === 'links' ? 'page' : undefined}
          onclick={() => handleNavClick({ id: 'links', label: t('nav.links'), icon: icons.link, href: '/links' })}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.link} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('nav.links')}</span>
        </button>
      </li>
    </ul>
    {/if}

    {#if !folderSelectorOnly}
    <!-- Section 2: SYSTEM -->
    <div class="sc-nav-drawer__section-title">{t('common.settings')}</div>
    <ul class="sc-nav-drawer__list">
      {#if navItems.some((n) => n.id === 'admin')}
        <li class="sc-nav-drawer__entry">
          <button
            type="button"
            class="sc-nav-drawer__item"
            class:sc-nav-drawer__item--active={activeNav === 'admin'}
            aria-current={activeNav === 'admin' ? 'page' : undefined}
            onclick={() => handleNavClick({ id: 'admin', label: t('nav.admin'), icon: icons.admin, href: '/admin' })}
          >
            <span class="sc-nav-drawer__item-icon"><Icon icon={icons.admin} size={20} /></span>
            <span class="sc-nav-drawer__item-label">{t('nav.admin')}</span>
          </button>
        </li>
      {/if}

      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          class:sc-nav-drawer__item--active={activeNav === 'settings'}
          aria-current={activeNav === 'settings' ? 'page' : undefined}
          onclick={() => handleNavClick({ id: 'settings', label: t('common.settings'), icon: icons.settings, href: '/settings' })}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.settings} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('common.settings')}</span>
        </button>
      </li>
    </ul>
    {/if}
  </div>
{/snippet}

{#if overlay}
  <dialog
    bind:this={dialogEl}
    class="sc-nav-drawer sc-nav-drawer--overlay"
    aria-label={folderSelectorOnly ? t('nav.folder_selector') : t('common.main_menu')}
    onclick={onDialogClick}
    onclose={() => onclose?.()}
    oncancel={() => onclose?.()}
  >
    <div class="sc-nav-drawer__overlay-header">
      <span class="sc-nav-drawer__app-name">{folderSelectorOnly ? t('nav.browse_folders') : 'Stowcloud'}</span>
      <IconButton label={t('common.close')} onclick={() => onclose?.()}><Icon icon={icons.close} /></IconButton>
    </div>
    {@render content()}
  </dialog>
{:else}
  <nav class="sc-nav-drawer" aria-label={t('common.main_menu')}>
    {@render content()}
  </nav>
{/if}

<style>
  .sc-nav-drawer {
    width: var(--sc-nav-drawer-width);
    overflow-y: auto;
    background: var(--m3c-surface-container-low);
    border-right: 1px solid var(--m3c-outline-variant);
    position: fixed;
    top: 0;
    left: 0;
    height: 100vh;
    height: 100dvh;
    z-index: 10;
    display: flex;
    flex-direction: column;
  }
  .sc-nav-drawer__brand {
    display: flex;
    align-items: center;
    height: 56px;
    padding-inline: 16px;
    flex: none;
  }
  .sc-nav-drawer__app-name {
    @apply --m3-title-medium;
    font-weight: 600;
    color: var(--m3c-on-surface);
  }
  .sc-nav-drawer__overlay-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 56px;
    padding-inline: 16px;
    box-shadow: 0 1px 0 var(--m3c-outline-variant);
    background: var(--m3c-surface-container-low);
    flex: none;
  }
  .sc-nav-drawer__body {
    flex: 1;
    display: flex;
    flex-direction: column;
    padding: 0;
  }
  .sc-nav-drawer__section-title {
    margin: 16px 16px 8px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--m3c-on-surface-variant);
  }
  .sc-nav-drawer__section-title:first-child {
    margin-top: 0;
  }
  .sc-nav-drawer__list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .sc-nav-drawer__entry {
    margin: 0;
  }
  .sc-nav-drawer__item {
    height: 40px;
    margin: 0 12px;
    padding: 0 12px;
    border-radius: var(--m3-shape-small);
    display: flex;
    align-items: center;
    gap: 12px;
    border: none;
    background: transparent;
    cursor: pointer;
    width: calc(100% - 24px);
    color: var(--m3c-on-surface-variant);
    font-weight: 500;
    font-size: 14px;
    text-align: left;
    position: relative;
    overflow: hidden;
    transition: background var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-nav-drawer__item:hover {
    background: var(--m3c-surface-container);
    color: var(--m3c-on-surface);
  }
  .sc-nav-drawer__item--active {
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
    font-weight: 600;
  }
  .sc-nav-drawer__item--active:hover {
    background: var(--m3c-surface-container-high);
  }
  .sc-nav-drawer__twisty-right {
    display: flex;
    align-items: center;
    justify-content: center;
    margin-left: auto;
    color: inherit;
    flex: none;
    transition: rotate var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-nav-drawer__twisty-right--expanded {
    rotate: 90deg;
  }
  .sc-nav-drawer__item-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: none;
  }
  .sc-nav-drawer__item-label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-nav-drawer__sublist {
    list-style: none;
    margin: 4px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .sc-nav-drawer__indent {
    width: 16px;
    flex: none;
  }
  .sc-nav-drawer__subitem {
    height: 36px;
    margin: 0 12px;
    padding: 0 12px;
    border-radius: var(--m3-shape-small);
    display: flex;
    align-items: center;
    gap: 8px;
    border: none;
    background: transparent;
    cursor: pointer;
    width: calc(100% - 24px);
    color: var(--m3c-on-surface-variant);
    font-size: 13px;
    font-weight: 500;
    text-align: left;
    transition: background var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-nav-drawer__subitem:hover {
    background: var(--m3c-surface-container);
    color: var(--m3c-on-surface);
  }
  .sc-nav-drawer__subitem--active {
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
    font-weight: 600;
  }
  .sc-nav-drawer__subitem-label {
    flex: 1;
    min-width: 0;
  }
  .sc-nav-drawer__reorder-row {
    margin: 0;
  }
  .sc-nav-drawer__reorder-toggle {
    height: 28px;
    margin: 0 12px;
    padding: 0 12px;
    width: calc(100% - 24px);
    border: none;
    border-radius: var(--m3-shape-small);
    background: transparent;
    color: var(--m3c-primary);
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: flex-end;
    font-size: 12px;
    font-weight: 600;
    transition: background var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-nav-drawer__reorder-toggle:hover {
    background: var(--m3c-surface-container);
  }
  .sc-nav-drawer__subitem--reorder {
    cursor: default;
  }
  .sc-nav-drawer__reorder-actions {
    display: flex;
    align-items: center;
    gap: 4px;
    margin-left: auto;
    flex: none;
  }
  .sc-nav-drawer__reorder-chevron {
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .sc-nav-drawer__reorder-chevron--up {
    rotate: -90deg;
  }
  .sc-nav-drawer__reorder-chevron--down {
    rotate: 90deg;
  }
  .sc-nav-drawer__reorder-error {
    margin: 0 12px;
    padding: 4px 12px;
    font-size: 12px;
    color: var(--m3c-error);
  }
  .sc-nav-drawer--overlay {
    position: fixed;
    top: 0;
    bottom: calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px));
    height: auto;
    inset-inline-start: 0;
    margin: 0;
    max-width: min(var(--sc-nav-drawer-width), calc(100vw - 56px));
    width: 100%;
    padding: 0;
    border: none;
    box-shadow: var(--m3-elevation-3);
    translate: 0 0;
    transition:
      translate var(--m3-duration) var(--m3-easing),
      display var(--m3-duration) allow-discrete,
      overlay var(--m3-duration) allow-discrete;
  }
  .sc-nav-drawer--overlay .sc-nav-drawer__item {
    height: 44px;
  }
  .sc-nav-drawer--overlay .sc-nav-drawer__subitem {
    height: 40px;
  }
  .sc-nav-drawer--overlay:not([open]) {
    translate: -100% 0;
  }
  @starting-style {
    .sc-nav-drawer--overlay[open] {
      translate: -100% 0;
    }
  }
  .sc-nav-drawer--overlay::backdrop {
    background: color-mix(in srgb, var(--m3c-scrim) 32%, transparent);
    transition:
      background-color var(--m3-duration) var(--m3-easing),
      display var(--m3-duration) allow-discrete,
      overlay var(--m3-duration) allow-discrete;
  }
  .sc-nav-drawer--overlay:not([open])::backdrop {
    background: color-mix(in srgb, var(--m3c-scrim) 0%, transparent);
  }
  @starting-style {
    .sc-nav-drawer--overlay[open]::backdrop {
      background: color-mix(in srgb, var(--m3c-scrim) 0%, transparent);
    }
  }
</style>
