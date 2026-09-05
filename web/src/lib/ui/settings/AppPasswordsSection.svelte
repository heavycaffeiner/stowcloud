<script lang="ts">
  // App passwords: List, issue, and revoke.
  // GET/POST/DELETE /api/auth/app-passwords[/:id].
  import { createMutation, createQuery } from '@tanstack/svelte-query'
  import { ApiError, type AppPasswordInfo } from '../../api/client'
  import { describeApiError } from '../../api/error-text'
  import { formatDateNs, t } from '../../i18n'
  import Button from '../Button.svelte'
  import TextField from '../TextField.svelte'
  import Checkbox from '../Checkbox.svelte'
  import Dialog from '../Dialog.svelte'
  import ListItem from '../ListItem.svelte'
  import IconButton from '../IconButton.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'
  import Chip from '../Chip.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import { appPasswordsQuery, createAppPasswordMutation, revokeAppPasswordMutation } from '../../query/account'

  const list = createQuery(() => appPasswordsQuery())
  const passwords = $derived(list.data ?? [])

  let createOpen = $state(false)
  let newName = $state('')
  let newReadOnly = $state(false)
  let newCurrent = $state('')
  let issuedToken = $state<string | null>(null)
  const create = createMutation(() => createAppPasswordMutation())
  const createError = $derived(
    create.error ? describeApiError(create.error, t('app_password.could_not_create_app_password')) : null
  )

  let revokeTarget = $state<AppPasswordInfo | null>(null)
  const revoke = createMutation(() => revokeAppPasswordMutation())

  let wipeTarget = $state<AppPasswordInfo | null>(null)
  const wipe = createMutation(() => revokeAppPasswordMutation())

  let actionError = $state<string | null>(null)

  /** Whether a credential's expiry has passed. The server writes epoch zero
   *  when a wipe is requested, which is how a wiped device reads as revoked
   *  rather than merely asked. Nanoseconds on the wire, milliseconds here. */
  function isExpired(p: AppPasswordInfo): boolean {
    if (!p.expires_ns) return false
    return Number(p.expires_ns) / 1e6 <= Date.now()
  }

  function openCreate(): void {
    newName = ''
    newReadOnly = false
    newCurrent = ''
    create.reset()
    createOpen = true
  }

  function closeCreate(): void {
    createOpen = false
  }

  function confirmCreate(): void {
    if (!newName.trim() || !newCurrent) return
    create.mutate(
      { name: newName.trim(), currentPassword: newCurrent, scope: newReadOnly ? { readOnly: true } : undefined },
      {
        onSuccess: (res) => {
          issuedToken = res.token
          createOpen = false
        }
      }
    )
  }

  function closeIssued(): void {
    issuedToken = null
  }

  async function copyToken(): Promise<void> {
    if (!issuedToken) return
    try {
      await navigator.clipboard.writeText(issuedToken)
    } catch {
      // clipboard API unavailable: the token is still selectable as text
    }
  }

  function askRevoke(p: AppPasswordInfo): void {
    actionError = null
    revokeTarget = p
  }

  function closeRevoke(): void {
    revokeTarget = null
    actionError = null
  }

  function confirmRevoke(): void {
    if (!revokeTarget) return
    actionError = null
    revoke.mutate(
      { id: revokeTarget.id, wipe: false },
      {
        onSuccess: () => {
          revokeTarget = null
        },
        onError: (err) => {
          // 404 just means it's already gone (e.g. revoked from another
          // tab): refresh the list silently instead of leaving a stale row.
          if (err instanceof ApiError && err.status === 404) {
            revokeTarget = null
            void list.refetch()
          } else {
            actionError = describeApiError(err, t('common.could_not_save_change'))
          }
        }
      }
    )
  }

  function askWipe(p: AppPasswordInfo): void {
    actionError = null
    wipeTarget = p
  }

  function closeWipe(): void {
    wipeTarget = null
    actionError = null
  }

  function confirmWipe(): void {
    if (!wipeTarget) return
    actionError = null
    wipe.mutate(
      { id: wipeTarget.id, wipe: true },
      {
        onSuccess: () => {
          wipeTarget = null
        },
        onError: (err) => {
          // Already gone (revoked from another tab): the device can no
          // longer reach us at all, so there is nothing left to ask it to do.
          if (err instanceof ApiError && err.status === 404) {
            wipeTarget = null
            void list.refetch()
          } else {
            actionError = describeApiError(err, t('common.could_not_save_change'))
          }
        }
      }
    )
  }
</script>

<div class="sc-app-passwords">
  {#if list.isPending}
    <ProgressCircular />
  {:else if list.isError}
    <p class="sc-app-passwords__error">{t('common.could_not_load_list')}</p>
  {:else if passwords.length === 0}
    <p class="sc-app-passwords__empty">{t('app_password.no_app_passwords_issued_yet')}</p>
  {:else}
    <ul class="sc-app-passwords__list">
      {#each passwords as p (p.id)}
        <li>
          <ListItem>
            {#snippet headline()}
              <span class="sc-app-passwords__name">{p.name}</span>
              {#if p.read_only}<Chip variant="filter" selected>{t('common.read_only')}</Chip>{/if}
            {/snippet}
            {#snippet supporting()}
              {t('app_password.issued', { date: formatDateNs(p.created_ns) })}
              {#if p.last_used_ns}- {t('app_password.last_used', { date: formatDateNs(p.last_used_ns) })}{:else}- {t('app_password.never_used')}{/if}
              <!-- A requested wipe revokes the credential in the same
                   statement (the server moves its expiry to the epoch), so a
                   row that said nothing about expiry read as live after the
                   device had already been cut off. -->
              {#if isExpired(p)}- {t('app_password.expired')}{:else if p.expires_ns}- {t('app_password.expires', { date: formatDateNs(p.expires_ns) })}{/if}
            {/snippet}
            {#snippet trailing()}
              <!-- Nothing to ask of a credential that can no longer connect,
                   so the wipe request goes; revoking still clears the row. -->
              {#if !isExpired(p)}
                <IconButton label={t('app_password.wipe', { name: p.name })} onclick={() => askWipe(p)}><Icon icon={icons.warning} size={18} /></IconButton>
              {/if}
              <IconButton label={t('app_password.revoke', { name: p.name })} onclick={() => askRevoke(p)}><Icon icon={icons.delete} size={18} /></IconButton>
            {/snippet}
          </ListItem>
        </li>
      {/each}
    </ul>
  {/if}

  <div class="sc-app-passwords__actions">
    <Button variant="outlined" onclick={openCreate}>
      {#snippet icon()}<Icon icon={icons.add} size={18} />{/snippet}
      {t('app_password.new_app_password')}
    </Button>
  </div>
</div>
<Dialog open={createOpen} title={t('app_password.new_app_password')} onclose={closeCreate}>
  {#if createError}
    <p class="sc-app-passwords__error">{createError}</p>
  {/if}
  <p>{t('app_password.use_one_where_your_account')}</p>
  <TextField
    label={t('common.name')}
    bind:value={newName}
    placeholder={t('app_password.e_g_rclone_backup')}
  />
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={newCurrent}
    autocomplete="current-password"
  />
  <Checkbox
    label={t('app_password.read_only_download_only_no')}
    bind:checked={newReadOnly}
  />
  <p class="sc-app-passwords__scope-hint">
    {t('app_password.read_only_recommended_anywhere_only')}
  </p>
  {#snippet actions()}
    <Button variant="text" onclick={closeCreate}>{t('common.cancel')}</Button>
    <Button
      variant="filled"
      disabled={!newName.trim() || !newCurrent}
      loading={create.isPending}
      onclick={confirmCreate}
    >
      {t('common.create')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={!!issuedToken} title={t('app_password.app_password_issued')} onclose={closeIssued}>
  <p>
    <Icon icon={icons.warning} size={16} />
    {t('app_password.once_you_close_cannot_shown')}
  </p>
  <div class="sc-app-passwords__token-row">
    <code class="sc-app-passwords__token">{issuedToken}</code>
    <Button variant="text" onclick={copyToken}>{t('common.copy')}</Button>
  </div>
  {#snippet actions()}
    <Button variant="filled" onclick={closeIssued}>{t('common.saved')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!revokeTarget} title={t('app_password.revoke_app_password')} onclose={closeRevoke}>
  <p>
    {t('app_password.everything_using_disconnected_at_once', { name: revokeTarget?.name ?? '' })}
  </p>
  {#if actionError}
    <p class="sc-app-passwords__error" role="alert">{actionError}</p>
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeRevoke}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmRevoke} loading={revoke.isPending}>{t('app_password.revoke_2')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!wipeTarget} title={t('app_password.wipe_device')} onclose={closeWipe}>
  <p>
    {t('app_password.next_time_that_device_erase', { name: wipeTarget?.name ?? '' })}
  </p>
  {#snippet actions()}
  {#if actionError}
    <p class="sc-app-passwords__error" role="alert">{actionError}</p>
  {/if}
    <Button variant="text" onclick={closeWipe}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmWipe} loading={wipe.isPending}>{t('app_password.wipe_2')}</Button>
  {/snippet}
</Dialog>

<style>
  .sc-app-passwords__list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-app-passwords__list li + li {
    border-top: 1px solid var(--m3c-outline-variant);
  }
  /* Same defect as the admin user list: without this, the "Read-only" chip
   * is the one that gets squeezed and wraps syllable-by-syllable instead of
   * the name (which can't shrink below its own content, since it has no
   * CJK break opportunities of its own to give up). Chip.svelte now refuses
   * to shrink at all; this makes the name the side that ellipsizes. */
  .sc-app-passwords__name {
    overflow: hidden;
    min-width: 0;
    flex: 1 1 auto;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-app-passwords__empty,
  .sc-app-passwords__error {
    color: var(--m3c-on-surface-variant);
  }
  .sc-app-passwords__actions {
    margin-top: 16px;
  }
  .sc-app-passwords__scope-hint {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-app-passwords__token-row {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-block: 8px;
  }
  .sc-app-passwords__token {
    padding: 8px 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface-container-highest);
    @apply --m3-body-medium;
    overflow-wrap: anywhere;
    user-select: all;
  }
</style>
