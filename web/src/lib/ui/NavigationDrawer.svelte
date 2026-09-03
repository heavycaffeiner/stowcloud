<script lang="ts">
  // NavigationDrawer.svelte: Google AI Studio / Material 3 style clean navigation sidebar.
  // Features structured sections (FILES / ACTIONS / SYSTEM), 8px rounded rectangle items,
  // integrated root share exploration, direct action triggers, and refined typography.
  import { t } from '../i18n'
  import type { IconifyIcon } from '@iconify/types'
  import IconButton from './IconButton.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'
  import { goto } from '$app/navigation'

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

  function triggerAction(action: 'folder' | 'file' | 'upload-folder'): void {
    if (overlay) onclose?.()
    if (!location.pathname.startsWith('/b')) {
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
  <!-- Top brand header (desktop: clean text title without home icon, overlay has close button) -->
  {#if !overlay}
    <div class="sc-nav-drawer__brand">
      <span class="sc-nav-drawer__app-name">Stowcloud</span>
    </div>
  {/if}

  <div class="sc-nav-drawer__body">
    <!-- Section 1: FILES -->
    <div class="sc-nav-drawer__section-title">{t('nav.files')}</div>
    <ul class="sc-nav-drawer__list">
      <li class="sc-nav-drawer__entry">
        <div
          class="sc-nav-drawer__item"
          class:sc-nav-drawer__item--active={activeNav === 'files' && !active}
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

      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          class:sc-nav-drawer__item--active={activeNav === 'recent'}
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
          onclick={() => handleNavClick({ id: 'trash', label: t('common.trash'), icon: icons.trash, href: '/trash' })}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.trash} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('common.trash')}</span>
        </button>
      </li>
    </ul>

    <!-- Section 2: ACTIONS -->
    <div class="sc-nav-drawer__section-title">{t('common.add')}</div>
    <ul class="sc-nav-drawer__list">
      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          onclick={() => triggerAction('folder')}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.add} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('common.new_folder')}</span>
        </button>
      </li>
      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          onclick={() => triggerAction('file')}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.upload} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('common.upload')}</span>
        </button>
      </li>
      <li class="sc-nav-drawer__entry">
        <button
          type="button"
          class="sc-nav-drawer__item"
          onclick={() => triggerAction('upload-folder')}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons['upload-folder']} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('browse.upload_folder')}</span>
        </button>
      </li>
    </ul>

    <!-- Section 3: MANAGE -->
    <div class="sc-nav-drawer__section-title">{t('common.settings')}</div>
    <ul class="sc-nav-drawer__list">
      {#if navItems.some((n) => n.id === 'admin')}
        <li class="sc-nav-drawer__entry">
          <button
            type="button"
            class="sc-nav-drawer__item"
            class:sc-nav-drawer__item--active={activeNav === 'admin'}
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
          onclick={() => handleNavClick({ id: 'settings', label: t('common.settings'), icon: icons.settings, href: '/settings' })}
        >
          <span class="sc-nav-drawer__item-icon"><Icon icon={icons.settings} size={20} /></span>
          <span class="sc-nav-drawer__item-label">{t('common.settings')}</span>
        </button>
      </li>
    </ul>
  </div>
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
      <span class="sc-nav-drawer__app-name">Stowcloud</span>
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
  .sc-nav-drawer__twisty {
    width: 24px;
    height: 24px;
    margin: 0;
    padding: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    border: none;
    background: transparent;
    border-radius: var(--m3-shape-small);
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
    padding: 0;
    border-radius: var(--m3-shape-small);
    cursor: pointer;
    color: inherit;
    text-align: left;
    font-weight: inherit;
    font-size: 14px;
    overflow: hidden;
  }
  .sc-nav-drawer__item-btn--with-twisty {
    padding-inline-start: 0;
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
