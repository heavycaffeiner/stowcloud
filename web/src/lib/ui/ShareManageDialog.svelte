<script lang="ts">
  // ShareManageDialog.svelte: owner-side share-link CRUD for one file/folder
  // path (/api/shares[/:id]). Creation
  // is the only piece of this surface any part of the app touched before.
  // up zero create/view/edit/revoke UI, only the unrelated public `/s/{token}`
  // viewer in `api/share.ts` and the admin storage report). `GET
  // /api/shares/{id}`, `PATCH /api/shares/{id}`, and `DELETE
  // /api/shares/{id}` existed and worked server-side with nothing in the
  // app ever calling them; this dialog is the first caller.
  import { createMutation, createQuery } from '@tanstack/svelte-query'
  import { type PermsReq, type ShareLinkInfo } from '../api/client'
  import { describeApiError } from '../api/error-text'
  import { shareCreateMutation, shareDeleteMutation, shareLinksQuery, shareUpdateMutation } from '../query/shares'
  import { formatDateNs, t } from '../i18n'
  import Button from './Button.svelte'
  import Checkbox from './Checkbox.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import Dialog from './Dialog.svelte'
  import { Icon, SelectOutlined } from 'm3-svelte'
  import { icons } from '../icons'
  import IconButton from './IconButton.svelte'
  import ProgressCircular from './ProgressCircular.svelte'
  import TextField from './TextField.svelte'

  interface Props {
    open: boolean
    /** Absolute vpath of the shared item, e.g. `/Documents/report.pdf`. */
    path: string
    targetName: string
    /** Gates the file-drop option: `sc-core::links::create_link` refuses a
     *  drop link whose target is not a directory ("a file-drop link must
     *  target a directory"), so offering it on a file would only produce a
     *  422 the user can do nothing about. */
    targetIsDir: boolean
    onclose: () => void
  }
  let { open, path, targetName, targetIsDir, onclose }: Props = $props()

  /** A file-drop link, by the same rule the server applies
   *  (`ShareLink::is_drop`): may create, may not read or download. */
  function isDropLink(link: ShareLinkInfo): boolean {
    return link.perms.create && !link.perms.read && !link.perms.download
  }

  const sharesQuery = createQuery(() => shareLinksQuery(path, open))
  const links = $derived(sharesQuery.data ?? [])
  const loading = $derived(sharesQuery.isPending)

  const createMut = createMutation(() => shareCreateMutation())
  const updateMut = createMutation(() => shareUpdateMutation())
  const deleteMut = createMutation(() => shareDeleteMutation())

  const PRESET_DAYS: Record<string, number> = { '1d': 1, '7d': 7, '30d': 30 }

  const NEW_EXPIRY_OPTIONS = [
    { value: 'none', text: t('share.none') },
    { value: '1d', text: t('share.1_day') },
    { value: '7d', text: t('share.7_days') },
    { value: '30d', text: t('share.30_days_default') },
    { value: 'custom', text: t('share.pick_a_date') }
  ]
  const EDIT_EXPIRY_OPTIONS = [
    { value: 'keep', text: t('share.leave_unchanged') },
    { value: 'none', text: t('share.none') },
    { value: '1d', text: t('share.1_day') },
    { value: '7d', text: t('share.7_days') },
    { value: '30d', text: t('share.30_days') },
    { value: 'custom', text: t('share.pick_a_date') }
  ]

  /** `YYYY-MM-DD` (what `<input type="date">` binds) to wire nanoseconds, at
   *  the *end* of that local day: a link the owner dated the 10th that stops
   *  working at 00:00 on the 10th does not last the day it names. */
  function isoToExpiresNs(iso: string): string | undefined {
    const [y, m, d] = iso.split('-').map(Number)
    if (!y || !m || !d) return undefined
    return String(BigInt(new Date(y, m - 1, d, 23, 59, 59, 999).getTime()) * 1_000_000n)
  }

  function expiryToNs(choice: string, iso: string): string | undefined {
    if (choice === 'custom') return isoToExpiresNs(iso)
    if (choice === 'none') return undefined
    return String(BigInt(Date.now() + PRESET_DAYS[choice] * 86_400_000) * 1_000_000n)
  }

  /** A date already past would create a link that is dead on arrival: the
   *  server takes it, it just never opens. */
  function expiryUnusable(choice: string, iso: string): boolean {
    if (choice !== 'custom') return false
    const ns = isoToExpiresNs(iso)
    return ns === undefined || BigInt(ns) <= BigInt(Date.now()) * 1_000_000n
  }

  // ── create form ──
  let creatingOpen = $state(false)
  /** `string`, not a union, for the same reason `newExpiry` is one: the m3
   *  Select binds a plain `<select>` value. Only ever `download` or `drop`. */
  let newKind = $state('download')
  const KIND_OPTIONS = $derived(
    targetIsDir
      ? [
          { value: 'download', text: t('share.kind_download') },
          { value: 'drop', text: t('share.kind_drop') }
        ]
      : [{ value: 'download', text: t('share.kind_download') }]
  )
  let newRead = $state(true)
  let newDownload = $state(true)
  let newPassword = $state('')
  // `string`, not `ExpiryPreset`: m3-svelte's Select is a real `<select>` and
  // binds a plain string. The value can only ever be one of the options above.
  let newExpiry = $state('30d')
  /** `YYYY-MM-DD`, only read when `newExpiry === 'custom'`. */
  let newExpiryDate = $state('')
  const newExpiryBad = $derived(expiryUnusable(newExpiry, newExpiryDate))
  let newMaxDownloads = $state('')
  let newLabel = $state('')
  /** Client-side validation the mutation itself never sees, e.g. a
   *  non-integer download limit. Distinct from `createMut.error`, which is
   *  the server's own refusal. */
  let createValidation = $state<string | null>(null)
  const creating = $derived(createMut.isPending)
  const createError = $derived(
    createValidation ?? (createMut.error ? describeApiError(createMut.error, t('share.could_not_create_share_link')) : null)
  )
  /** The one moment the plaintext token/url are ever available: the server
   *  never returns them again after this response ("plaintext is not stored"), so this has to be shown now or lost, same
   *  rule the app-password/recovery-code create flows already follow. */
  let justCreated = $state<ShareLinkInfo | null>(null)

  // ── inline edit ──
  let editingId = $state<number | null>(null)
  let editRead = $state(true)
  let editDownload = $state(true)
  let editClearPassword = $state(false)
  let editNewPassword = $state('')
  let editExpiry = $state('keep')
  let editExpiryDate = $state('')
  const editExpiryBad = $derived(expiryUnusable(editExpiry, editExpiryDate))
  let editMaxDownloads = $state('')
  let editLabel = $state('')
  const saving = $derived(updateMut.isPending)

  // ── revoke confirm ──
  let revokeTarget = $state<ShareLinkInfo | null>(null)

  let copiedId = $state<number | null>(null)

  // The banner above the list doubles as the "could not read" state and the
  // "an edit or a revoke just failed" state, same slot the old imperative
  // `load()` reported every one of those failures through.
  const loadError = $derived(
    sharesQuery.error
      ? describeApiError(sharesQuery.error, t('share.could_not_load_share_links'))
      : updateMut.error
        ? describeApiError(updateMut.error, t('share.could_not_update_share_link'))
        : deleteMut.error
          ? describeApiError(deleteMut.error, t('share.could_not_revoke_share_link'))
          : null
  )

  $effect(() => {
    if (open) {
      justCreated = null
      creatingOpen = false
      editingId = null
    }
  })

  function resetCreateForm(): void {
    newKind = 'download'
    newRead = true
    newDownload = true
    newPassword = ''
    newExpiry = '30d'
    newExpiryDate = ''
    newMaxDownloads = ''
    newLabel = ''
    createValidation = null
    createMut.reset()
  }

  function openCreate(): void {
    resetCreateForm()
    creatingOpen = true
  }

  async function submitCreate(): Promise<void> {
    createValidation = null
    // A drop link is exactly `create` without `read` or `download`, the
    // shape `ShareLink::is_drop` tests for. Granting read alongside it would
    // silently turn it back into an ordinary link.
    const isDrop = newKind === 'drop'
    const perms: PermsReq = isDrop ? { create: true } : { read: newRead, download: newDownload }
    const maxDownloads = !isDrop && newMaxDownloads.trim() ? Number(newMaxDownloads.trim()) : undefined
    if (maxDownloads !== undefined && (!Number.isInteger(maxDownloads) || maxDownloads <= 0)) {
      createValidation = t('share.download_limit_must_integer_1')
      return
    }
    try {
      const created = await createMut.mutateAsync({
        path,
        perms,
        password: newPassword.trim() || undefined,
        expires_ns: expiryToNs(newExpiry, newExpiryDate),
        max_downloads: maxDownloads,
        label: newLabel.trim() || undefined
      })
      justCreated = created
      creatingOpen = false
    } catch {
      // `createError` reads `createMut.error`; the form stays open to retry.
    }
  }

  async function copy(text: string, id: number): Promise<void> {
    try {
      await navigator.clipboard.writeText(text)
      copiedId = id
      setTimeout(() => {
        if (copiedId === id) copiedId = null
      }, 2000)
    } catch {
      // clipboard API unavailable, the value is still selectable as text
    }
  }

  function openEdit(link: ShareLinkInfo): void {
    editingId = link.id
    editRead = link.perms.read
    editDownload = link.perms.download
    editClearPassword = false
    editNewPassword = ''
    editExpiry = 'keep'
    editExpiryDate = ''
    editMaxDownloads = link.max_downloads !== null ? String(link.max_downloads) : ''
    editLabel = link.label ?? ''
    updateMut.reset()
  }

  function closeEdit(): void {
    editingId = null
  }

  async function submitEdit(link: ShareLinkInfo): Promise<void> {
    const maxDownloads = editMaxDownloads.trim() ? Number(editMaxDownloads.trim()) : null
    try {
      await updateMut.mutateAsync({
        id: link.id,
        patch: {
          perms: { read: editRead, download: editDownload },
          // Absent (`undefined`) leaves the password alone; `null` clears it;
          // a non-empty value replaces it. `JSON.stringify` drops `undefined`
          // keys on its own; see `ShareLinkPatchReq`'s doc comment.
          password: editClearPassword ? null : editNewPassword.trim() || undefined,
          expires_ns: editExpiry === 'keep' ? undefined : (expiryToNs(editExpiry, editExpiryDate) ?? null),
          max_downloads: maxDownloads,
          label: editLabel.trim() || null
        }
      })
      editingId = null
    } catch {
      // `loadError` reads `updateMut.error`; the form stays open to retry.
    }
  }

  async function confirmRevoke(): Promise<void> {
    if (!revokeTarget) return
    const id = revokeTarget.id
    revokeTarget = null
    // The banner offers to copy a link that no longer exists once its own
    // link is revoked, so it goes with it.
    if (justCreated?.id === id) justCreated = null
    try {
      await deleteMut.mutateAsync(id)
    } catch {
      // `loadError` reads `deleteMut.error`.
    }
  }
</script>

<Dialog {open} title={t('share.share_links', { name: targetName })} onclose={onclose}>
  {#if justCreated}
    <div class="sc-share__issued">
      <p class="sc-share__issued-note">
        {t('share.link_shown_only_now_cannot')}
      </p>
      <div class="sc-share__url-row">
        <code class="sc-share__url">{justCreated.url}</code>
        <IconButton label={t('share.copy_link')} onclick={() => justCreated && copy(justCreated.url ?? '', -1)}>
          <Icon icon={icons.copy} />
        </IconButton>
      </div>
      <Button variant="text" onclick={() => (justCreated = null)}>{t('common.close')}</Button>
    </div>
  {/if}

  {#if loading}
    <div class="sc-share__loading"><ProgressCircular /></div>
  {:else if loadError}
    <p class="sc-share__error" role="alert">{loadError}</p>
  {:else}
    {#if links.length === 0 && !creatingOpen}
      <p class="sc-share__empty">{t('share.no_share_links_item')}</p>
    {/if}

    <ul class="sc-share__list">
      {#each links as link (link.id)}
        <li class="sc-share__item">
          {#if editingId === link.id}
            <div class="sc-share__edit-form">
              <div class="sc-share__perm-row">
                <Checkbox checked={editRead} label={t('share.read_view')} onchange={(v) => (editRead = v)} />
                <Checkbox checked={editDownload} label={t('common.download')} onchange={(v) => (editDownload = v)} />
              </div>
              <SelectOutlined label={t('share.expiry')} width="100%" options={EDIT_EXPIRY_OPTIONS} bind:value={editExpiry} />
              {#if editExpiry === 'custom'}
                <TextField
                  type="date"
                  label={t('share.expiry_date')}
                  bind:value={editExpiryDate}
                  error={editExpiryDate && editExpiryBad ? t('share.expiry_date_must_be_in_the_future') : null}
                />
              {/if}
              <TextField bind:value={editMaxDownloads} label={t('share.download_limit_optional')} placeholder={t('share.no_limit')} />
              <TextField bind:value={editLabel} label={t('share.label_optional')} />
              {#if link.has_password && !editClearPassword}
                <Checkbox checked={editClearPassword} label={t('share.remove_password')} onchange={(v) => (editClearPassword = v)} />
              {/if}
              {#if !editClearPassword}
                <TextField bind:value={editNewPassword} type="password" label={t('share.new_password_optional')} />
              {/if}
              <div class="sc-share__edit-actions">
                <Button variant="text" onclick={closeEdit}>{t('common.cancel')}</Button>
                <Button variant="filled" disabled={saving || editExpiryBad} onclick={() => submitEdit(link)}>{t('common.save')}</Button>
              </div>
            </div>
          {:else}
            <div class="sc-share__item-row">
              <Icon icon={icons[link.has_password ? 'lock' : 'link']} size={18} />
              <div class="sc-share__item-main">
                <span class="sc-share__item-label">{link.label || t('share.no_label')}</span>
                <span class="sc-share__item-meta">
                  {#if isDropLink(link)}
                    <!-- No download count: this link can't serve one. -->
                    {t('share.kind_drop')}
                  {:else}
                    {link.perms.read ? t('common.read') : ''}{link.perms.read && link.perms.download ? ' - ' : ''}{link.perms.download ? t('common.download') : ''}
                  - {t('share.used_times', { count: link.max_downloads ? `${link.downloads}/${link.max_downloads}` : link.downloads })}
                  {/if}
                  {#if link.expires_ns}- {t('share.expires', { date: formatDateNs(link.expires_ns) })}{:else}- {t('share.never_expires')}{/if}
                </span>
                <span class="sc-share__item-meta">{t('share.created', { date: formatDateNs(link.created_ns) })}</span>
              </div>
              <div class="sc-share__item-actions">
                {#if link.url}
                  <IconButton label={copiedId === link.id ? t('share.copied') : t('share.copy_link')} onclick={() => copy(link.url ?? '', link.id)}>
                    <Icon icon={icons[copiedId === link.id ? 'check' : 'copy']} />
                  </IconButton>
                {/if}
                <IconButton label={t('share.edit')} onclick={() => openEdit(link)}><Icon icon={icons.rename} /></IconButton>
                <IconButton label={t('share.revoke')} onclick={() => (revokeTarget = link)}><Icon icon={icons.close} /></IconButton>
              </div>
            </div>
          {/if}
        </li>
      {/each}
    </ul>

    {#if creatingOpen}
      <div class="sc-share__create-form">
        <h3>{t('share.create_new_link')}</h3>
        <SelectOutlined label={t('share.kind_label')} width="100%" options={KIND_OPTIONS} bind:value={newKind} />
        {#if newKind === 'drop'}
          <p class="sc-share__hint">{t('share.drop_hint')}</p>
        {:else}
          <div class="sc-share__perm-row">
            <Checkbox checked={newRead} label={t('share.read_view')} onchange={(v) => (newRead = v)} />
            <Checkbox checked={newDownload} label={t('common.download')} onchange={(v) => (newDownload = v)} />
          </div>
        {/if}
        <SelectOutlined label={t('share.expiry')} width="100%" options={NEW_EXPIRY_OPTIONS} bind:value={newExpiry} />
        {#if newExpiry === 'custom'}
          <TextField
            type="date"
            label={t('share.expiry_date')}
            bind:value={newExpiryDate}
            error={newExpiryDate && newExpiryBad ? t('share.expiry_date_must_be_in_the_future') : null}
          />
        {/if}
        <TextField bind:value={newPassword} type="password" label={t('share.password_optional')} />
        <!-- A drop link never serves a download, so a download limit on one
             would count something that can't happen. -->
        {#if newKind !== 'drop'}
          <TextField bind:value={newMaxDownloads} label={t('share.download_limit_optional')} placeholder={t('share.no_limit')} />
        {/if}
        <TextField bind:value={newLabel} label={t('share.label_optional')} placeholder={targetName} />
        {#if createError}<p class="sc-share__error" role="alert">{createError}</p>{/if}
        <div class="sc-share__edit-actions">
          <Button variant="text" onclick={() => (creatingOpen = false)}>{t('common.cancel')}</Button>
          <Button variant="filled" disabled={creating || newExpiryBad || (newKind !== 'drop' && !newRead && !newDownload)} onclick={submitCreate}>{t('common.create')}</Button>
        </div>
      </div>
    {:else}
      <Button variant="tonal" onclick={openCreate}>
        {#snippet icon()}<Icon icon={icons.add} size={18} />{/snippet}
        {t('share.create_new_link')}
      </Button>
    {/if}
  {/if}

  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{t('common.close')}</Button>
  {/snippet}
</Dialog>

<ConfirmDialog
  open={revokeTarget !== null}
  title={t('share.revoke_share_link')}
  message={t('share.link_stops_working_cannot_undone')}
  confirmLabel={t('share.revoke')}
  onclose={() => (revokeTarget = null)}
  onconfirm={confirmRevoke}
/>

<style>
  .sc-share__issued {
    padding: 16px;
    margin-bottom: 16px;
    border-radius: var(--m3-shape-medium);
    background: var(--m3c-secondary-container);
    color: var(--m3c-on-secondary-container);
  }
  .sc-share__issued-note {
    margin: 0 0 8px;
  }
  .sc-share__url-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .sc-share__url {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    padding: 8px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface);
    color: var(--m3c-on-surface);
  }
  .sc-share__loading {
    display: flex;
    justify-content: center;
    padding: 24px;
  }
  .sc-share__error {
    color: var(--m3c-error);
  }
  .sc-share__empty {
    color: var(--m3c-on-surface-variant);
  }
  .sc-share__list {
    list-style: none;
    margin: 0 0 16px;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .sc-share__item {
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    padding: 12px;
  }
  .sc-share__item-row {
    display: flex;
    align-items: flex-start;
    gap: 12px;
  }
  .sc-share__item-main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .sc-share__item-label {
    font-weight: 500;
    color: var(--m3c-on-surface);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-share__item-meta {
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-share__item-actions {
    display: flex;
    align-items: center;
    flex-shrink: 0;
  }
  .sc-share__create-form,
  .sc-share__edit-form {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 12px;
    border-radius: var(--m3-shape-medium);
    background: var(--m3c-surface-container-low);
  }
  .sc-share__create-form h3 {
    margin: 0;
    @apply --m3-title-medium;
    color: var(--m3c-on-surface);
  }
  .sc-share__perm-row {
    display: flex;
    gap: 16px;
  }
  .sc-share__hint {
    margin: 0;
    @apply --m3-body-small;
    color: var(--m3c-on-surface-variant);
  }
  .sc-share__edit-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
