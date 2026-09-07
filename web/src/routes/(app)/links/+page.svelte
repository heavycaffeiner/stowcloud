<script lang="ts">
  // /links: every share link the account can see. An ordinary account sees
  // only its own (`GET /api/v1/links`, `sharesList`); an administrator sees
  // every link on the deployment, with an owner column, from the
  // admin-only `GET /api/v1/admin/links` (`adminListLinks`). Which query
  // runs is decided once, from the session, rather than fetching both and
  // discarding one: the admin route 403s for anyone else, and firing it
  // from every ordinary session for a response that is always thrown away
  // costs a request with no possible use.
  //
  // No create/edit/revoke here: minting and managing a link stays anchored
  // to the file it shares (`ShareManageDialog`, opened from a row's own
  // folder). This is the read-only overview the nav needed: "what have I
  // published" for anyone, "what is published from this server" for an
  // admin. Never a token: neither endpoint ever sends one, and showing an
  // address column for a link this screen cannot mint would imply it could
  // reopen a credential the server has already forgotten.
  //
  // A row opens the link's own manage dialog rather than navigating to the
  // folder holding it. Navigating was the wrong destination twice over: it
  // answered "where does this live" when the question a link list raises is
  // "change or revoke this", and it sent the browser at a path this listing
  // could not always spell, so an account whose grant projects a subfolder as
  // its root landed on a path that does not exist.
  //
  // The dialog is the same one the file browser opens, so edit and revoke
  // behave identically wherever they are reached from.
  import { goto } from '$app/navigation'
  import { createQuery } from '@tanstack/svelte-query'
  import { adminLinksQuery } from '../../../lib/query/admin'
  import { shareLinksQuery } from '../../../lib/query/shares'
  import { createSession } from '../../../lib/query/session'
  import type { OwnedShareLinkInfo, ShareLinkInfo } from '../../../lib/api/client'
  import { describeApiError } from '../../../lib/api/error-text'
  import { baseName, normalizePath } from '../../../lib/api/path-utils'
  import { formatDateNs, t } from '../../../lib/i18n'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../../lib/icons'
  import IconButton from '../../../lib/ui/IconButton.svelte'
  import ProgressCircular from '../../../lib/ui/ProgressCircular.svelte'
  import ShareManageDialog from '../../../lib/ui/ShareManageDialog.svelte'

  const session = createSession()
  const isAdmin = $derived(session.data?.user.is_admin ?? false)

  // `enabled` gates whichever query the caller is not: an ordinary session
  // never fires the admin-only route, and an admin session never fires the
  // per-account one this screen has no use for once the session is known.
  // Before the session resolves, neither fires and the screen shows its own
  // loading state instead of one request that would be thrown away the
  // moment `isAdmin` turns out to disagree with it.
  const own = createQuery(() => ({ ...shareLinksQuery(undefined), enabled: session.data !== undefined && !isAdmin }))
  const all = createQuery(() => ({ ...adminLinksQuery(), enabled: isAdmin }))

  const rows = $derived<(ShareLinkInfo | OwnedShareLinkInfo)[]>(isAdmin ? (all.data ?? []) : (own.data ?? []))
  const loading = $derived(session.isPending || (isAdmin ? all.isPending : own.isPending))
  const loadError = $derived.by(() => {
    const err = isAdmin ? all.error : own.error
    return err ? describeApiError(err, t('links.could_not_load')) : null
  })

  function isOwned(l: ShareLinkInfo | OwnedShareLinkInfo): l is OwnedShareLinkInfo {
    return 'owner_name' in l
  }

  /** A file-drop link, the same rule `ShareManageDialog` tests: create with
   *  neither read nor download, so it has no download count worth showing. */
  function isDropLink(link: ShareLinkInfo): boolean {
    return link.perms.create && !link.perms.read && !link.perms.download
  }

  function isExpired(link: ShareLinkInfo): boolean {
    return link.expires_ns !== null && Number(link.expires_ns) / 1e6 <= Date.now()
  }

  function isExhausted(link: ShareLinkInfo): boolean {
    return link.max_downloads !== null && link.downloads >= link.max_downloads
  }

  // Whether the caller owns a row, which decides whether it can be managed.
  // The owner's own listing is all theirs; the administrative one crosses
  // accounts, and the API refuses an edit or a revoke of somebody else's link
  // by design. A row that opens a dialog which then shows nothing and can
  // change nothing is worse than a row that does not open.
  function isMine(l: ShareLinkInfo | OwnedShareLinkInfo): boolean {
    return !isOwned(l) || l.owner === session.data?.user.id
  }

  // The link whose dialog is open, or null. The dialog manages every link at
  // one path, which is the unit it was built around, so the row hands it the
  // path rather than the link id.
  let managing = $state<ShareLinkInfo | OwnedShareLinkInfo | null>(null)

  // The dialog offers the file-drop option only for a directory, and the
  // listing carries no resourcetype. A drop link proves its target is one,
  // since the server refuses to create a drop over a file; anything else is
  // reported as a file, which costs a folder the option to gain a second,
  // drop-shaped link from this screen and never mis-offers one on a file.
  const managingIsDir = $derived(managing ? isDropLink(managing) : false)
</script>

<svelte:head><title>{t('links.title_stowcloud')}</title></svelte:head>

<div class="sc-links">
  <div class="sc-links__inner">
    <header class="sc-links__header">
      <IconButton label={t('trash.go_back')} onclick={() => goto('/b')}>
        <Icon icon={icons['chevron-left']} />
      </IconButton>
      <h1>{t('nav.links')}</h1>
      <IconButton
        label={t('common.refresh')}
        onclick={() => (isAdmin ? all.refetch() : own.refetch())}
      >
        <Icon icon={icons.refresh} />
      </IconButton>
    </header>

    {#if loading}
      <div class="sc-links__loading"><ProgressCircular /></div>
    {:else if loadError}
      <p class="sc-links__error" role="alert">{loadError}</p>
    {:else if rows.length === 0}
      <p class="sc-links__empty">{t('links.empty')}</p>
    {:else}
      <ul class="sc-links__list">
        {#each rows as link (link.id)}
          <li>
            <button
              type="button"
              class="sc-links__row"
              class:sc-links__row--readonly={!isMine(link)}
              aria-disabled={!isMine(link)}
              aria-label={isMine(link)
                ? t('links.manage_link', { path: link.path })
                : t('links.owned_elsewhere', { path: link.path })}
              onclick={() => {
                if (isMine(link)) managing = link
              }}
            >
              <span class="sc-links__icon">
                <Icon icon={icons[link.has_password ? 'lock' : 'link']} size={20} />
              </span>
              <span class="sc-links__text">
                <span class="sc-links__path">{link.path}</span>
                <span class="sc-links__meta">
                  {#if isOwned(link)}
                    {t('links.owner')}: {link.owner_name || t('common.user', { id: link.owner })}
                    -
                  {/if}
                  {#if isDropLink(link)}
                    {t('share.kind_drop')}
                  {:else}
                    {t('share.used_times', { count: link.max_downloads ? `${link.downloads}/${link.max_downloads}` : link.downloads })}
                  {/if}
                  - {link.has_password ? t('links.password_protected') : t('links.no_password')}
                  - {#if link.expires_ns}{t('share.expires', { date: formatDateNs(link.expires_ns) })}{:else}{t('share.never_expires')}{/if}
                </span>
              </span>
              {#if isExpired(link)}
                <span class="sc-links__flag sc-links__flag--warn">{t('links.expired')}</span>
              {:else if isExhausted(link)}
                <span class="sc-links__flag sc-links__flag--warn">{t('links.exhausted')}</span>
              {/if}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
</div>

{#if managing}
  <ShareManageDialog
    open={true}
    path={normalizePath(managing.path)}
    targetName={baseName(managing.path) || managing.path}
    targetIsDir={managingIsDir}
    onclose={() => (managing = null)}
  />
{/if}

<style>
  .sc-links {
    height: 100%;
    overflow-y: auto;
    word-break: keep-all;
  }
  .sc-links__inner {
    max-width: min(860px, 100%);
    margin-inline: auto;
    padding: var(--sc-page-pad);
  }
  .sc-links__header {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 16px;
  }
  .sc-links__header h1 {
    flex: 1;
    @apply --m3-headline-small;
  }
  .sc-links__list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .sc-links__row {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    padding: 8px 16px;
    border: none;
    border-bottom: 1px solid var(--m3c-outline-variant);
    background: none;
    color: inherit;
    font: inherit;
    text-align: start;
    cursor: pointer;
  }
  .sc-links__row:hover {
    background: var(--m3c-surface-container);
  }
  /* A link somebody else published: shown because an operator needs to see
     what this server is serving, not actionable because the API refuses an
     edit or a revoke from anyone but its owner. */
  .sc-links__row--readonly {
    cursor: default;
  }
  .sc-links__row--readonly:hover {
    background: none;
  }
  .sc-links__row:focus-visible {
    outline: 2px solid var(--m3c-primary);
    outline-offset: -2px;
  }
  .sc-links__icon {
    display: inline-flex;
    flex: none;
    color: var(--m3c-on-surface-variant);
  }
  .sc-links__text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .sc-links__path {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-links__meta {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-links__flag {
    flex-shrink: 0;
    @apply --m3-label-small;
    padding: 4px 8px;
    border-radius: var(--m3-shape-full);
  }
  .sc-links__flag--warn {
    color: var(--m3c-on-error-container);
    background: var(--m3c-error-container);
  }
  .sc-links__loading {
    display: flex;
    justify-content: center;
    padding: 32px;
  }
  .sc-links__error {
    color: var(--m3c-error);
    padding: 16px;
  }
  .sc-links__empty {
    color: var(--m3c-on-surface-variant);
    padding: 32px;
    text-align: center;
  }

  /* Same rule the recent and trash lists follow: the meta line disappears
     before the path is squeezed into per-character wrapping. */
  @media (max-width: 480px) {
    .sc-links__meta {
      display: none;
    }
  }
</style>
