<script lang="ts">
  // User management — admin-only screen built on top of's
  // role model (§8, `user.role`). GET/POST /api/admin/users, PATCH/DELETE
  // /api/admin/users/{id}.
  //
  // Last-admin protection: the server (`sc_auth::AdminGuardError::LastAdmin`)
  // has final say and rejects with 409 `admin.last_admin`. This screen does
  // two things, neither of which replaces the server-side defense: shows
  // that failure clearly, and pre-disables the toggle/delete buttons when
  // only one active admin remains, to cut down on accidental clicks.
  import { t } from '../../i18n'
  import { api, ApiError, type AdminUser } from '../../api/client'
  import { BYTES_PER_MB, bytesToMb, formatBytes } from '../../format/bytes'
  import { scorePasswordStrength } from '../../format/password-strength'
  import Button from '../Button.svelte'
  import Dialog from '../Dialog.svelte'
  import ListItem from '../ListItem.svelte'
  import IconButton from '../IconButton.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'
  import Switch from '../Switch.svelte'
  import TextField from '../TextField.svelte'
  import Chip from '../Chip.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import ProgressLinear from '../ProgressLinear.svelte'
  import GrantManagementSection from './GrantManagementSection.svelte'
  import UserOidcDialog from './UserOidcDialog.svelte'

  const MIN_PASSWORD_LEN = 10

  let users = $state<AdminUser[]>([])
  let loading = $state(true)
  let loadError = $state<string | null>(null)

  let createOpen = $state(false)
  let newName = $state('')
  let newPassword = $state('')
  let createError = $state<string | null>(null)
  let creating = $state(false)

  let deleteTarget = $state<AdminUser | null>(null)
  let deleting = $state(false)
  let deleteError = $state<string | null>(null)

  let togglingId = $state<number | null>(null)
  let toggleError = $state<string | null>(null)

  // Dialog that sets which folders a just-created (or picked-from-the-list)
  // user can see. "Create a user" has to lead straight into "and decide what
  // they see" — an account with zero grants just shows a blank screen on
  // login, which reads as a broken server, not a state anyone chose on
  // purpose.
  let grantsTarget = $state<AdminUser | null>(null)

  function openGrants(u: AdminUser): void {
    grantsTarget = u
  }

  function closeGrants(): void {
    grantsTarget = null
  }

  // Single sign-on, per account (`docs/proposals/stowcloud-0-oidc-login.md`
  // §5-1). Its own dialog: reading, attaching and detaching an identity is
  // three routes and a two-step confirmation, and none of it belongs in this
  // list's row.
  let oidcTarget = $state<AdminUser | null>(null)

  function openOidc(u: AdminUser): void {
    oidcTarget = u
  }

  function closeOidc(): void {
    oidcTarget = null
  }

  // Per-user quota: `quota_bytes` is the cap, enforced
  // on upload/copy/write against the running `usage_bytes` ledger
  // (`sc_core::quota`'s module doc) — a write that would exceed it is
  // refused with `507 quota.exceeded`, not just reported to NC clients.
  let quotaTarget = $state<AdminUser | null>(null)
  let quotaInput = $state('')
  let quotaError = $state<string | null>(null)
  let quotaSaving = $state(false)

  /** Chip text: "1.2 GB / 5 GB" when capped, "1.2 GB used" (unlimited) otherwise. */
  function quotaLabel(u: AdminUser): string {
    const used = formatBytes(Number(BigInt(u.usage_bytes)))
    if (!u.quota_bytes) return t('user.used', { used })
    return `${used} / ${formatBytes(Number(BigInt(u.quota_bytes)))}`
  }

  function openQuota(u: AdminUser): void {
    quotaError = null
    quotaInput = u.quota_bytes ? String(bytesToMb(Number(BigInt(u.quota_bytes)))) : ''
    quotaTarget = u
  }

  function closeQuota(): void {
    if (quotaSaving) return
    quotaTarget = null
  }

  async function submitQuota(e?: SubmitEvent): Promise<void> {
    e?.preventDefault()
    if (!quotaTarget) return
    quotaError = null
    const trimmed = quotaInput.trim()
    let bytes: number | null = null
    if (trimmed) {
      const mb = Number(trimmed)
      if (!Number.isFinite(mb) || mb <= 0) {
        quotaError = t('user.enter_number_greater_than_0')
        return
      }
      bytes = Math.round(mb * BYTES_PER_MB)
    }
    quotaSaving = true
    try {
      const updated = await api.adminSetUserQuota(quotaTarget.id, bytes)
      users = users.map((x) => (x.id === updated.id ? updated : x))
      quotaTarget = null
    } catch (err) {
      if (err instanceof ApiError && err.code === 'admin.invalid_quota') {
        quotaError = t('user.enter_number_greater_than_0')
      } else {
        quotaError = t('common.could_not_save')
      }
    } finally {
      quotaSaving = false
    }
  }

  const newPasswordStrength = $derived(scorePasswordStrength(newPassword))

  async function load(): Promise<void> {
    loading = true
    loadError = null
    try {
      users = await api.adminListUsers()
    } catch {
      loadError = t('user.could_not_load_user_list')
    } finally {
      loading = false
    }
  }

  load()

  const activeAdminCount = $derived(users.filter((u) => u.is_admin && !u.disabled).length)

  /** Would disabling/deleting this account leave zero active admins? The
   *  server has final say, but there's no reason to let anyone press a
   *  button that's guaranteed to be rejected. */
  function isLastActiveAdmin(u: AdminUser): boolean {
    return u.is_admin && !u.disabled && activeAdminCount <= 1
  }

  function openCreate(): void {
    newName = ''
    newPassword = ''
    createError = null
    createOpen = true
  }

  function closeCreate(): void {
    if (creating) return
    createOpen = false
  }

  async function submitCreate(e?: SubmitEvent): Promise<void> {
    e?.preventDefault()
    createError = null
    if (newPassword.length < MIN_PASSWORD_LEN) {
      createError = t('user.password_must_at_least_characters', { min: MIN_PASSWORD_LEN })
      return
    }
    creating = true
    try {
      const created = await api.adminCreateUser(newName, newPassword)
      users = [...users, created]
      createOpen = false
      // A new account has zero access by default — flow straight into the
      // screen that decides which folders it sees.
      openGrants(created)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'fs.conflict') {
        createError = t('common.name_already_taken')
      } else if (err instanceof ApiError && err.code === 'auth.weak_password') {
        const min = (err.detail?.min_length as number | undefined) ?? MIN_PASSWORD_LEN
        createError = t('user.password_must_at_least_characters', { min })
      } else {
        createError = t('user.could_not_create_user')
      }
    } finally {
      creating = false
    }
  }

  async function toggleDisabled(u: AdminUser, disabled: boolean): Promise<void> {
    toggleError = null
    togglingId = u.id
    try {
      const updated = await api.adminSetUserDisabled(u.id, disabled)
      users = users.map((x) => (x.id === u.id ? updated : x))
    } catch (err) {
      if (err instanceof ApiError && err.code === 'admin.last_admin') {
        toggleError = t('user.last_administrator_cannot_deactivated')
      } else {
        toggleError = t('common.could_not_save_change')
      }
    } finally {
      togglingId = null
    }
  }

  function askDelete(u: AdminUser): void {
    deleteError = null
    deleteTarget = u
  }

  function closeDelete(): void {
    if (deleting) return
    deleteTarget = null
  }

  async function confirmDelete(): Promise<void> {
    if (!deleteTarget) return
    deleting = true
    deleteError = null
    try {
      await api.adminDeleteUser(deleteTarget.id)
      users = users.filter((x) => x.id !== deleteTarget!.id)
      deleteTarget = null
    } catch (err) {
      if (err instanceof ApiError && err.code === 'admin.last_admin') {
        deleteError = t('user.last_administrator_cannot_deleted')
      } else {
        deleteError = t('common.could_not_delete')
      }
    } finally {
      deleting = false
    }
  }
</script>

<section class="sc-user-mgmt">
  <div class="sc-user-mgmt__header">
    <p class="sc-user-mgmt__hint">
      {t('user.create_accounts_suspend_or_re')}
    </p>
    <Button variant="filled" onclick={openCreate}>
      {#snippet icon()}<Icon icon={icons.add} size={18} />{/snippet}
      {t('user.add_user')}
    </Button>
  </div>

  {#if toggleError}<p class="sc-user-mgmt__error" role="alert">{toggleError}</p>{/if}

  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-user-mgmt__error">{loadError}</p>
  {:else}
    <ul class="sc-user-mgmt__list">
      {#each users as u (u.id)}
        <li>
          <ListItem>
            {#snippet headline()}
              <span class="sc-user-mgmt__name">{u.display_name || u.name}</span>
              {#if u.is_admin}<Chip variant="filter" selected>{t('common.administrator')}</Chip>{/if}
              {#if u.disabled}<Chip variant="assist">{t('user.inactive')}</Chip>{/if}
            {/snippet}
            {#snippet supporting()}
              {u.name}
            {/snippet}
            {#snippet trailing()}
              <span
                class="sc-user-mgmt__switch"
                class:sc-user-mgmt__switch--locked={isLastActiveAdmin(u)}
                title={isLastActiveAdmin(u) ? t('user.last_active_administrator_cannot_deactivated') : undefined}
              >
                <Switch
                  checked={!u.disabled}
                  label={t('user.enable_account', { name: u.name })}
                  showLabel={false}
                  onchange={(checked) => toggleDisabled(u, !checked)}
                />
              </span>
              <button type="button" class="sc-user-mgmt__quota" onclick={() => openQuota(u)}>
                <Chip variant="assist">{quotaLabel(u)}</Chip>
              </button>
              <IconButton label={t('common.manage_folders_visible', { name: u.name })} onclick={() => openGrants(u)}>
                <Icon icon={icons['folder-tree']} size={18} />
              </IconButton>
              <IconButton label={t('oidc.manage_single_sign_connection', { name: u.name })} onclick={() => openOidc(u)}>
                <Icon icon={icons.link} size={18} />
              </IconButton>
              <IconButton
                label={t('common.delete_2', { name: u.name })}
                disabled={togglingId === u.id || isLastActiveAdmin(u)}
                onclick={() => askDelete(u)}
              >
                <Icon icon={icons.delete} size={18} />
              </IconButton>
            {/snippet}
          </ListItem>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<Dialog open={createOpen} title={t('user.add_user')} onclose={closeCreate}>
  <form class="sc-user-mgmt__form" onsubmit={submitCreate}>
    <TextField label={t('user.username')} bind:value={newName} autocomplete="off" autofocus />
    <TextField type="password" label={t('common.password')} bind:value={newPassword} autocomplete="new-password" />
    {#if newPassword}
      <div class="sc-user-mgmt__strength">
        <ProgressLinear
          value={newPasswordStrength.ratio}
          tone={newPasswordStrength.tier}
          label={t('common.password_strength', { level: newPasswordStrength.label })}
        />
        <span class="sc-user-mgmt__strength-label">{newPasswordStrength.label}</span>
      </div>
    {/if}
    <p class="sc-user-mgmt__form-hint">
      {t('user.at_least_characters_turning_smb', { min: MIN_PASSWORD_LEN })}
    </p>
    {#if createError}<p class="sc-user-mgmt__error" role="alert">{createError}</p>{/if}
  </form>
  {#snippet actions()}
    <Button variant="text" onclick={closeCreate} disabled={creating}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={() => submitCreate()} disabled={!newName || !newPassword} loading={creating}>
      {t('common.add')}
    </Button>
  {/snippet}
</Dialog>

<Dialog open={!!deleteTarget} title={t('user.delete_user')} onclose={closeDelete}>
  <p>
    {t('user.permanently_deletes_account_including_its', {
      name: deleteTarget?.name ?? ''
    })}
  </p>
  {#if deleteError}<p class="sc-user-mgmt__error" role="alert">{deleteError}</p>{/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeDelete} disabled={deleting}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmDelete} loading={deleting}>{t('common.delete')}</Button>
  {/snippet}
</Dialog>

<Dialog
  open={!!grantsTarget}
  title={grantsTarget ? t('user.folders_visible', { name: grantsTarget.display_name || grantsTarget.name }) : t('common.folder_permissions')}
  onclose={closeGrants}
>
  {#if grantsTarget}
    <GrantManagementSection
      principal={{ kind: 'user', id: grantsTarget.id }}
      label={grantsTarget.display_name || grantsTarget.name}
    />
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeGrants}>{t('common.close')}</Button>
  {/snippet}
</Dialog>

<UserOidcDialog user={oidcTarget} onclose={closeOidc} />

<Dialog
  open={!!quotaTarget}
  title={quotaTarget ? t('user.storage_quota', { name: quotaTarget.display_name || quotaTarget.name }) : t('user.storage_quota_2')}
  onclose={closeQuota}
>
  <form class="sc-user-mgmt__form" onsubmit={submitQuota}>
    <TextField label={t('user.storage_quota_mb')} bind:value={quotaInput} placeholder={t('user.empty_means_unlimited')} autofocus />
    <p class="sc-user-mgmt__form-hint">
      {#if quotaTarget}{t('user.currently_using', { used: formatBytes(Number(BigInt(quotaTarget.usage_bytes))) })}{/if}
      {t('user.empty_means_unlimited_uploads_copies')}
    </p>
    {#if quotaError}<p class="sc-user-mgmt__error" role="alert">{quotaError}</p>{/if}
  </form>
  {#snippet actions()}
    <Button variant="text" onclick={closeQuota} disabled={quotaSaving}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={() => submitQuota()} loading={quotaSaving}>{t('common.save')}</Button>
  {/snippet}
</Dialog>

<style>
  /* Named container so the header can respond to the space it actually has
   * (this section's own width) rather than the viewport — consistent with
   * how FileTable/FileTree already do compact-width layout switches
   * ( window-size classes via container queries). */
  .sc-user-mgmt {
    container-name: sc-user-mgmt;
    container-type: inline-size;
  }
  .sc-user-mgmt__header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 24px;
    margin-bottom: 16px;
  }
  /* Compact (<600px): a row that fits the button
   * beside 3-4 lines of body text at 1280px does not have to survive at
   * 390px — stack instead of squeezing the hint into a 6-line, half-width
   * column next to the button. */
  @container sc-user-mgmt (max-width: 599.98px) {
    .sc-user-mgmt__header {
      flex-direction: column;
      align-items: flex-start;
    }
  }
  .sc-user-mgmt__hint {
    max-width: 560px;
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-user-mgmt__list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-user-mgmt__list li + li {
    border-top: 1px solid var(--m3c-outline-variant);
  }
  /* The name is the one flex child that should give up space — Korean text
   * has a line-break opportunity after every syllable, so without an
   * explicit min-width:0 + nowrap it doesn't overflow, it *wraps*
   * character-by-character inside whatever sliver of width it's squeezed
   * to (the same defect, worked around here as well as at its root in
   * Chip.svelte, which no longer yields width to a sibling at all). */
  .sc-user-mgmt__name {
    overflow: hidden;
    min-width: 0;
    flex: 1 1 auto;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-user-mgmt__list :global(.sc-list-item__trailing) {
    gap: 8px;
  }
  .sc-user-mgmt__error {
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-user-mgmt__switch--locked {
    opacity: 0.5;
    pointer-events: none;
  }
  .sc-user-mgmt__quota {
    border: none;
    background: none;
    padding: 0;
    cursor: pointer;
  }
  .sc-user-mgmt__form {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: min(320px, 80vw);
  }
  .sc-user-mgmt__form-hint {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-user-mgmt__strength {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: calc(-1 * 8px);
  }
  .sc-user-mgmt__strength-label {
    flex: 0 0 auto;
    min-width: 48px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
</style>
