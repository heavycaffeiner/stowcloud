<script lang="ts">
  // App passwords — List/issue/revoke.
  // `GET/POST/DELETE /api/auth/app-passwords[/:id]`.
  import { onMount } from 'svelte'
  import { api, ApiError, type AppPasswordInfo } from '../../api/client'
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

  let passwords = $state<AppPasswordInfo[]>([])
  let loading = $state(true)
  let loadError = $state<string | null>(null)

  let createOpen = $state(false)
  let newName = $state('')
  let newReadOnly = $state(false)
  // The server re-confirms the account password before minting a credential:
  // a session alone should not be enough to create one that outlives it.
  let newCurrent = $state('')
  let creating = $state(false)
  let createError = $state<string | null>(null)
  let issuedToken = $state<string | null>(null)

  let revokeTarget = $state<AppPasswordInfo | null>(null)
  let revoking = $state(false)

  let wipeTarget = $state<AppPasswordInfo | null>(null)
  let wiping = $state(false)

  async function load(): Promise<void> {
    loading = true
    loadError = null
    try {
      passwords = await api.listAppPasswords()
    } catch {
      loadError = t('common.could_not_load_list')
    } finally {
      loading = false
    }
  }

  onMount(load)

  function openCreate(): void {
    newName = ''
    newReadOnly = false
    newCurrent = ''
    createError = null
    createOpen = true
  }

  function closeCreate(): void {
    createOpen = false
  }

  async function confirmCreate(): Promise<void> {
    if (!newName.trim() || !newCurrent) return
    creating = true
    createError = null
    try {
      const res = newReadOnly
        ? await api.createScopedAppPassword(newName.trim(), newCurrent, { readOnly: true })
        : await api.createAppPassword(newName.trim(), newCurrent)
      issuedToken = res.token
      createOpen = false
      await load()
    } catch {
      createError = t('app_password.could_not_create_app_password')
    } finally {
      creating = false
    }
  }

  function closeIssued(): void {
    issuedToken = null
  }

  async function copyToken(): Promise<void> {
    if (!issuedToken) return
    try {
      await navigator.clipboard.writeText(issuedToken)
    } catch {
      // clipboard API unavailable — the token is still selectable as text
    }
  }

  function askRevoke(p: AppPasswordInfo): void {
    revokeTarget = p
  }

  function closeRevoke(): void {
    revokeTarget = null
  }

  async function confirmRevoke(): Promise<void> {
    if (!revokeTarget) return
    revoking = true
    try {
      await api.revokeAppPassword(revokeTarget.id)
      revokeTarget = null
      await load()
    } catch (err) {
      // 404 just means it's already gone (e.g. revoked from another tab) —
      // refresh silently instead of leaving a stale row on screen.
      if (err instanceof ApiError && err.status === 404) {
        revokeTarget = null
        await load()
      }
    } finally {
      revoking = false
    }
  }

  function askWipe(p: AppPasswordInfo): void {
    wipeTarget = p
  }

  function closeWipe(): void {
    wipeTarget = null
  }

  async function confirmWipe(): Promise<void> {
    if (!wipeTarget) return
    wiping = true
    try {
      await api.wipeAppPassword(wipeTarget.id)
      wipeTarget = null
      await load()
    } catch (err) {
      // Already gone (revoked from another tab): the device can no longer
      // reach us at all, so there is nothing left to ask it to do.
      if (err instanceof ApiError && err.status === 404) {
        wipeTarget = null
        await load()
      }
    } finally {
      wiping = false
    }
  }
</script>

<div class="sc-app-passwords">
  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-app-passwords__error">{loadError}</p>
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
              {#if p.last_used_ns}· {t('app_password.last_used', { date: formatDateNs(p.last_used_ns) })}{:else}· {t('app_password.never_used')}{/if}
            {/snippet}
            {#snippet trailing()}
              <IconButton label={t('app_password.wipe', { name: p.name })} onclick={() => askWipe(p)}><Icon icon={icons.warning} size={18} /></IconButton>
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
  <p>{t('app_password.use_one_where_your_account')}</p>
  <TextField label={t('common.name')} placeholder={t('app_password.e_g_rclone_backup')} bind:value={newName} error={createError} autofocus />
  <Checkbox bind:checked={newReadOnly} label={t('app_password.read_only_download_only_no')} />
  <TextField
    type="password"
    label={t('common.current_password')}
    bind:value={newCurrent}
    autocomplete="current-password"
  />
  <p class="sc-app-passwords__scope-hint">
    {t('app_password.read_only_recommended_anywhere_only')}
  </p>
  {#snippet actions()}
    <Button variant="text" onclick={closeCreate}>{t('common.cancel')}</Button>
    <Button variant="filled" disabled={!newName.trim() || !newCurrent} loading={creating} onclick={confirmCreate}>
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
  {#snippet actions()}
    <Button variant="text" onclick={closeRevoke}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmRevoke} loading={revoking}>{t('app_password.revoke_2')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!wipeTarget} title={t('app_password.wipe_device')} onclose={closeWipe}>
  <p>
    {t('app_password.next_time_that_device_erase', { name: wipeTarget?.name ?? '' })}
  </p>
  {#snippet actions()}
    <Button variant="text" onclick={closeWipe}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmWipe} loading={wiping}>{t('app_password.wipe_2')}</Button>
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
