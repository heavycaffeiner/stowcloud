<script lang="ts">
  // NavigationDrawer.svelte: Google Drive style unified navigation sidebar.
  // Combines brand title, "+ New" action button, primary destinations,
  // and expandable root shares into one clean Material 3 drawer.
  import { t } from '../i18n'
  import type { IconifyIcon } from '@iconify/types'
  import IconButton from './IconButton.svelte'
  import { Icon, MenuItem } from 'm3-svelte'
  import Menu from './Menu.svelte'
  import { icons } from '../icons'
  import { goto } from '$app/navigation'
  import { page } from '$app/state'

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
  }

  let {
    navItems = [],
    activeNav = 'files',
    items = [],
    active = '',
    onselect,
    onnavselect,
    overlay = false,
    onclose
  }: Props = $props()

  let dialogEl: HTMLDialogElement | undefined = $state()
  let filesExpanded = $state(true)
  let newMenuOpen = $state(false)
  let newMenuX = $state(0)
  let newMenuY = $state(0)

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

  function toggleNewMenu(e: MouseEvent): void {
    if (newMenuOpen) {
      newMenuOpen = false
      return
    }
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
    newMenuX = rect.left
    newMenuY = rect.bottom + 4
    newMenuOpen = true
  }

  function triggerNew(action: 'folder' | 'file' | 'upload-folder'): void {
    newMenuOpen = false
    if (overlay) onclose?.()
    if (!page.url.pathname.startsWith('/b')) {
      void goto('/b').then(() => {
        setTimeout(() => window.dispatchEvent(new CustomEvent(`sc:${action}`)), 100)
      })
    } else {
      window.dispatchEvent(new CustomEvent(`sc:${action}`))
    }
  }

  function handleFilesClick(): void {
    if (items.length > 0 && !active) {
      onselect?.(items[0])
    } else {
      onnavselect?.({ id: 'files', label: t('nav.files'), icon: icons.home, href: '/b' })
    }
    if (overlay) onclose?.()
  }

  function handleNavClick(item: NavItem): void {
    onnavselect?.(item)
    if (overlay) onclose?.()
  }
</script>

{#snippet content()}
  <!-- Top brand header (desktop only, overlay has its own header) -->
  {#if !overlay}
    <div class="sc-nav-drawer__brand">
      <span class="sc-nav-drawer__logo"><Icon icon={icons.home} size={24} /></span>
      <span class="sc-nav-drawer__app-name">Stowcloud</span>
    </div>
  {/if}

  <!-- Google Drive "+ New" Extended FAB button -->
  <div class="sc-nav-drawer__new-wrap">
    <button
      type="button"
      class="sc-nav-drawer__new-btn"
      onclick={toggleNewMenu}
      aria-haspopup="true"
      aria-expanded={newMenuOpen}
    >
      <span class="sc-nav-drawer__new-icon"><Icon icon={icons.add} size={24} /></span>
      <span class="sc-nav-drawer__new-text">{t('common.add')}</span>
    </button>
  </div>

  <Menu open={newMenuOpen} onclose={() => (newMenuOpen = false)} x={newMenuX} y={newMenuY}>
    <MenuItem icon={icons.folder} onclick={() => triggerNew('folder')}>
      {t('common.new_folder')}
    </MenuItem>
    <MenuItem icon={icons.upload} onclick={() => triggerNew('file')}>
      {t('common.upload')}
    </MenuItem>
    <MenuItem icon={icons['upload-folder']} onclick={() => triggerNew('upload-folder')}>
      {t('browse.upload_folder')}
    </MenuItem>
  </Menu>

  <!-- Main Navigation Destinations -->
  <ul class="sc-nav-drawer__list">
    <!-- Files destination with expandable root shares -->
    <li class="sc-nav-drawer__entry">
      <div
        class="sc-nav-drawer__item"
        class:sc-nav-drawer__item--active={activeNav === 'files'}
      >
        {#if items.length > 0}
          <button
            type="button"
            class="sc-nav-drawer__twisty"
            class:sc-nav-drawer__twisty--expanded={filesExpanded}
            aria-label={t('nav.switch_folder')}
            onclick={(e) => {
              e.stopPropagation()
              filesExpanded = !filesExpanded
            }}
          >
            <Icon icon={icons['chevron-right']} size={16} />
          </button>
        {/if}
        <button
          type="button"
          class="sc-nav-drawer__item-btn"
          class:sc-nav-drawer__item-btn--with-twisty={items.length > 0}
          onclick={handleFilesClick}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.home} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('nav.files')}</span>
        </button>
      </div>

      {#if filesExpanded && items.length > 0}
        <ul class="sc-nav-drawer__sublist">
          {#each items as root (root.id)}
            <li>
              <button
                type="button"
                class="sc-nav-drawer__subitem"
                class:sc-nav-drawer__subitem--active={active === root.id}
                onclick={() => {
                  onselect?.(root)
                  if (overlay) onclose?.()
                }}
              >
                <span class="sc-nav-drawer__indent" aria-hidden="true"></span>
                <span class="sc-nav-drawer__item-icon"><Icon icon={icons.folder} size={18} /></span>
                <span class="sc-nav-drawer__subitem-label sc-filename">{root.label}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </li>

    <!-- Recent -->
    <li class="sc-nav-drawer__entry">
      <div
        class="sc-nav-drawer__item"
        class:sc-nav-drawer__item--active={activeNav === 'recent'}
      >
        <button
          type="button"
          class="sc-nav-drawer__item-btn"
          onclick={() => handleNavClick({ id: 'recent', label: t('nav.recent'), icon: icons.recent, href: '/recent' })}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.recent} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('nav.recent')}</span>
        </button>
      </div>
    </li>

    <!-- Trash -->
    <li class="sc-nav-drawer__entry">
      <div
        class="sc-nav-drawer__item"
        class:sc-nav-drawer__item--active={activeNav === 'trash'}
      >
        <button
          type="button"
          class="sc-nav-drawer__item-btn"
          onclick={() => handleNavClick({ id: 'trash', label: t('common.trash'), icon: icons.trash, href: '/trash' })}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.trash} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('common.trash')}</span>
        </button>
      </div>
    </li>

    <li class="sc-nav-drawer__divider-entry" role="separator">
      <hr class="sc-nav-drawer__divider" />
    </li>

    <!-- Admin (if present in navItems) -->
    {#if navItems.some((n) => n.id === 'admin')}
      <li class="sc-nav-drawer__entry">
        <div
          class="sc-nav-drawer__item"
          class:sc-nav-drawer__item--active={activeNav === 'admin'}
        >
          <button
            type="button"
            class="sc-nav-drawer__item-btn"
            onclick={() => handleNavClick({ id: 'admin', label: t('nav.admin'), icon: icons.admin, href: '/admin' })}
          >
            <span class="sc-nav-drawer__item-icon"><Icon icon={icons.admin} size={20} /></span>
            <span class="sc-nav-drawer__item-label">{t('nav.admin')}</span>
          </button>
        </div>
      </li>
    {/if}

    <!-- Settings -->
    <li class="sc-nav-drawer__entry">
      <div
        class="sc-nav-drawer__item"
        class:sc-nav-drawer__item--active={activeNav === 'settings'}
      >
        <button
          type="button"
          class="sc-nav-drawer__item-btn"
          onclick={() => handleNavClick({ id: 'settings', label: t('common.settings'), icon: icons.settings, href: '/settings' })}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.settings} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('common.settings')}</span>
        </button>
      </div>
    </li>
  </ul>
{/snippet}

{#if overlay}
  <dialog
    bind:this={dialogEl}
    class="sc-nav-drawer sc-nav-drawer--overlay"
    aria-label={t('common.main_menu')}
    onclick={onDialogClick}
    onclose={() => onclose?.()}
    oncancel={() => onclose?.()}
  >
    <div class="sc-nav-drawer__overlay-header">
      <div class="sc-nav-drawer__brand">
        <span class="sc-nav-drawer__logo"><Icon icon={icons.home} size={24} /></span>
        <span class="sc-nav-drawer__app-name">Stowcloud</span>
      </div>
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
    gap: 12px;
    height: 64px;
    padding-inline: 16px;
    flex: none;
  }
  .sc-nav-drawer__logo {
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--m3c-primary);
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
    height: 64px;
    padding-inline: 16px;
    box-shadow: 0 1px 0 var(--m3c-outline-variant);
    background: var(--m3c-surface-container-low);
    flex: none;
  }
  .sc-nav-drawer__new-wrap {
    padding: 16px;
    flex: none;
  }
  .sc-nav-drawer__new-btn {
    height: 56px;
    padding: 0 16px;
    border-radius: 16px;
    border: none;
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
    box-shadow: var(--m3-elevation-1);
    display: inline-flex;
    align-items: center;
    gap: 12px;
    cursor: pointer;
    @apply --m3-label-large;
    font-weight: 600;
    font-size: 14px;
    transition: background var(--m3-duration-fast) var(--m3-easing), box-shadow var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-nav-drawer__new-btn:hover {
    background: var(--m3c-surface-container-highest);
    box-shadow: var(--m3-elevation-2);
  }
  .sc-nav-drawer__new-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--m3c-primary);
  }
  .sc-nav-drawer__new-text {
    white-space: nowrap;
  }
  .sc-nav-drawer__list {
    list-style: none;
    margin: 0;
    padding: 8px 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
    flex: 1;
  }
  .sc-nav-drawer__entry {
    margin: 0;
  }
  .sc-nav-drawer__item {
    height: 48px;
    margin: 0 12px;
    border-radius: var(--m3-shape-full);
    display: flex;
    align-items: center;
    position: relative;
    overflow: hidden;
    color: var(--m3c-on-surface-variant);
    transition: background var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-nav-drawer__item:hover {
    background: var(--m3c-surface-container-highest);
  }
  .sc-nav-drawer__item--active {
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
    font-weight: 600;
  }
  .sc-nav-drawer__item--active:hover {
    background: var(--m3c-secondary-container);
  }
  .sc-nav-drawer__twisty {
    width: 32px;
    height: 32px;
    margin: 0;
    padding: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    border: none;
    background: transparent;
    border-radius: var(--m3-shape-full);
    cursor: pointer;
    color: inherit;
    flex: none;
    transition: rotate var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-nav-drawer__twisty--expanded {
    rotate: 90deg;
  }
  .sc-nav-drawer__item-btn {
    flex: 1;
    height: 100%;
    border: none;
    background: transparent;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 0 16px;
    border-radius: var(--m3-shape-full);
    cursor: pointer;
    color: inherit;
    text-align: left;
    @apply --m3-label-large;
    overflow: hidden;
  }
  .sc-nav-drawer__item-btn--with-twisty {
    padding: 0 12px;
  }
  .sc-nav-drawer__item-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: none;
  }
  .sc-nav-drawer__item-label {
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
    height: 40px;
    margin: 0 12px;
    padding: 0 12px;
    border-radius: var(--m3-shape-full);
    display: flex;
    align-items: center;
    gap: 8px;
    border: none;
    background: transparent;
    cursor: pointer;
    width: calc(100% - 24px);
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-medium;
    text-align: left;
    transition: background var(--m3-duration-fast) var(--m3-easing);
  }
  .sc-nav-drawer__subitem:hover {
    background: var(--m3c-surface-container-highest);
  }
  .sc-nav-drawer__subitem--active {
    background: var(--m3c-primary-container-subtle);
    color: var(--m3c-primary);
    font-weight: 600;
  }
  .sc-nav-drawer__subitem-label {
    flex: 1;
    min-width: 0;
  }
  .sc-nav-drawer__divider-entry {
    margin: 8px 0;
  }
  .sc-nav-drawer__divider {
    width: 224px;
    margin: 0 auto;
    border: none;
    border-top: 1px solid var(--m3c-outline-variant);
  }
  .sc-nav-drawer--overlay {
    position: fixed;
    top: 0;
    bottom: calc(var(--sc-nav-bar-height) + env(safe-area-inset-bottom, 0px));
    inset-inline-start: 0;
    margin: 0;
    max-width: min(var(--sc-nav-drawer-width), calc(100vw - 56px));
    width: 100%;
    padding: 0;
    border: none;
    box-shadow: var(--m3-elevation-2);
    translate: 0 0;
    transition:
      translate var(--m3-duration) var(--m3-easing),
      display var(--m3-duration) allow-discrete,
      overlay var(--m3-duration) allow-discrete;
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
