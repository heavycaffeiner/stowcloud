<script lang="ts">
  // Folder share management: adding, renaming and removing shared folders.
  // GET/POST /api/admin/shares, PATCH/DELETE /api/admin/shares/{id}.
  //
  // There is one kind of share. This screen is where every one of them comes
  // from: no file declares any, so a deployment serves nothing until somebody
  // adds a folder here.
  //
  // This is a distinct, adjacent screen from `GrantManagementSection`: that
  // one decides *who* can see a share (or a subpath of it); this one decides
  // *which folders exist* as shares in the first place. A share still needs
  // a grant before anyone but an admin can see it.
  import { t } from '../../i18n'
  import { api, ApiError, type AdminShare, type SMBOutcome, type UpdateShareReq } from '../../api/client'
  import { describeApiError } from '../../api/error-text'
  import Button from '../Button.svelte'
  import { smbOutcomeText } from '../../api/smb-text'
  import Dialog from '../Dialog.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../icons'
  import IconButton from '../IconButton.svelte'
  import ListItem from '../ListItem.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import Switch from '../Switch.svelte'
  import TextField from '../TextField.svelte'

  let shares = $state<AdminShare[]>([])
  let loading = $state(true)
  let loadError = $state<string | null>(null)

  async function load(): Promise<void> {
    loading = true
    loadError = null
    try {
      shares = await api.adminListShares()
    } catch {
      loadError = t('folder_share.could_not_load_share_list')
    } finally {
      loading = false
    }
  }

  load()

  /** A bad host path refuses with `admin.share_rejected` and a `reason`
   *  argument (missing, unreadable, an unsupported filesystem);
   *  `describeApiError` renders the keyed message with that reason filled
   *  in. This used to reverse the server's English sentences back into keys
   *  by substring, which meant a one-word copy edit on the server silently
   *  dropped the screen to its generic fallback. */
  function describeError(err: unknown, fallback: string): string {
    if (err instanceof ApiError && err.code === 'fs.not_found') return t('common.share_no_longer_exists')
    return describeApiError(err, fallback)
  }

  // ── add share ──

  let addOpen = $state(false)
  let addName = $state('')
  let addHostPath = $state('')
  let addError = $state<string | null>(null)
  let adding = $state(false)

  function openAdd(): void {
    addName = ''
    addHostPath = ''
    addError = null
    addOpen = true
  }

  function closeAdd(): void {
    if (adding) return
    addOpen = false
  }

  async function submitAdd(): Promise<void> {
    addError = null
    if (addName.trim() === '') {
      addError = t('folder_share.enter_name')
      return
    }
    if (addHostPath.trim() === '') {
      addError = t('folder_share.enter_server_path')
      return
    }
    adding = true
    try {
      const created = await api.adminCreateShare({ name: addName.trim(), host: addHostPath.trim() })
      noteSMB(created)
      shares = [...shares, created]
      addOpen = false
    } catch (err) {
      addError = describeError(err, t('common.could_not_add_folder'))
    } finally {
      adding = false
    }
  }

  // ── edit share ──

  let editTarget = $state<AdminShare | null>(null)
  let editName = $state('')
  let editHostPath = $state('')
  let editError = $state<string | null>(null)
  let editing = $state(false)

  function openEdit(s: AdminShare): void {
    editTarget = s
    editName = s.name
    // Blank, and optional. The server never sends the current path back, so
    // there is nothing truthful to prefill; leaving it empty means "keep the
    // one it has". Requiring it made renaming a folder impossible without
    // retyping a disk path nothing on this screen can show.
    editHostPath = ''
    editError = null
  }

  function closeEdit(): void {
    if (editing) return
    editTarget = null
  }

  async function submitEdit(): Promise<void> {
    if (!editTarget) return
    editError = null
    if (editName.trim() === '') {
      editError = t('folder_share.enter_name')
      return
    }
    // Only the fields that changed. A patch naming nothing else leaves the
    // path as it is, which is what the server does with an absent one.
    const patch: UpdateShareReq = { name: editName.trim() }
    if (editHostPath.trim() !== '') patch.host = editHostPath.trim()

    editing = true
    try {
      const updated = await api.adminUpdateShare(editTarget.id, patch)
      noteSMB(updated)
      shares = shares.map((s) => (s.id === updated.id ? updated : s))
      editTarget = null
    } catch (err) {
      editError = describeError(err, t('common.could_not_save_change'))
    } finally {
      editing = false
    }
  }

  // ── trash toggle ──

  let trashTogglingId = $state<number | null>(null)
  let trashToggleError = $state<string | null>(null)

  async function toggleTrash(s: AdminShare, enabled: boolean): Promise<void> {
    trashToggleError = null
    trashTogglingId = s.id
    try {
      const updated = await api.adminUpdateShare(s.id, { trash_enabled: enabled })
      shares = shares.map((x) => (x.id === updated.id ? updated : x))
    } catch (err) {
      trashToggleError = describeError(err, t('folder_share.could_not_change_trash_setting'))
    } finally {
      trashTogglingId = null
    }
  }

  // What the SMB republish that every write here triggers did. It is reported
  // beside the list rather than swallowed: a share saved with the sidecar
  // stopped answered a clean success, and "saved here, not applied over
  // there" showed up only on the health page whenever somebody next looked.
  let smbNote = $state<string | null>(null)

  function noteSMB(share: { smb?: SMBOutcome }): void {
    smbNote = smbOutcomeText(share.smb)
  }

  // ── retry a broken share ──

  // A share whose backing folder is not there is listed, marked, with the
  // three things an administrator can do about it: retry, because the usual
  // repair is a remount that changes nothing about the share; edit, because
  // the folder may have moved; remove, because it may be gone for good.
  let retryingId = $state<number | null>(null)
  let retryError = $state<string | null>(null)

  async function retry(s: AdminShare): Promise<void> {
    retryError = null
    retryingId = s.id
    try {
      const healed = await api.adminRetryShare(s.id)
      noteSMB(healed)
      shares = shares.map((x) => (x.id === healed.id ? healed : x))
    } catch (err) {
      // Still broken. The message says why this attempt failed, which is not
      // necessarily the reason the row is already showing.
      retryError = describeError(err, t('folder_share.the_folder_is_still_unavailable'))
    } finally {
      retryingId = null
    }
  }

  // The catalogue renders the sentence; the server sends the token.
  /* i18n */ 'folder_share.broken_missing'
  /* i18n */ 'folder_share.broken_unreadable'
  /* i18n */ 'folder_share.broken_unavailable'
  function brokenText(reason: string): string {
    switch (reason) {
      case 'missing':
        return t('folder_share.broken_missing')
      case 'unreadable':
        return t('folder_share.broken_unreadable')
      default:
        return t('folder_share.broken_unavailable')
    }
  }

  // ── remove share ──

  let deleteTarget = $state<AdminShare | null>(null)
  let deleting = $state(false)
  let deleteError = $state<string | null>(null)

  function askDelete(s: AdminShare): void {
    deleteError = null
    deleteTarget = s
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
      smbNote = smbOutcomeText((await api.adminDeleteShare(deleteTarget.id)).smb)
      shares = shares.filter((s) => s.id !== deleteTarget!.id)
      deleteTarget = null
    } catch (err) {
      deleteError = describeError(err, t('common.could_not_remove'))
    } finally {
      deleting = false
    }
  }
</script>

<section class="sc-shares">
  <h2>{t('folder_share.folder_shares')}</h2>
  <p class="sc-shares__hint">
    {t('folder_share.registers_real_folder_on_server')}
  </p>

  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-shares__error">{loadError}</p>
  {:else}
    {#if shares.length === 0}
      <div class="sc-shares__empty">
        <Icon icon={icons['folder-tree']} size={28} />
        <p>{t('folder_share.no_shares_registered_add_folder')}</p>
      </div>
    {:else}
      <ul class="sc-shares__list">
        {#each shares as s (s.id)}
          <li>
            <ListItem>
              {#snippet leading()}<Icon icon={icons.folder} size={20} />{/snippet}
              {#snippet headline()}
                <span class="sc-shares__name">{s.name}</span>
              {/snippet}
              {#snippet supporting()}
                <!-- No host path. The server does not send where a share lives
                     on its disk: that is the layout of the machine, and it is
                     the first thing worth knowing to anyone trying to reach
                     past the shares they were given. Editing one sends a new
                     path rather than correcting what was echoed back. -->
                {#if s.broken_reason}
                  <!-- The row stays, with the reason on it. A share dropped
                       from the list is indistinguishable from one somebody
                       deleted, which is the worst thing a missing disk can
                       look like. -->
                  <span class="sc-shares__broken">{brokenText(s.broken_reason)}</span>
                {/if}
              {/snippet}
              {#snippet trailing()}
                <!-- Where the share came from is a fact about the row, like the
                     trash switch beside it, so it belongs on the row's line. On
                     the headline it sat 8px above every control here, because a
                     two-line list item centres its controls on the row and its
                     headline on the first line. No placeholder is needed on the
                     rows that lack it: this group is right-aligned, so dropping
                     its leading item shortens the group from the left and moves
                     nothing else. -->
                <!-- The switch used to stand here bare, named only by a
                     `title` — nothing on screen said which setting it was.
                     The visible word is the label now; the switch keeps the
                     longer, per-share sentence as its accessible name. -->
                <span class="sc-shares__trash" title={trashTogglingId === s.id ? t('folder_share.applying') : undefined}>
                  <span class="sc-shares__trash-label">{t('folder_share.use_trash')}</span>
                  <Switch
                    checked={s.trash_enabled}
                    label={t('folder_share.trash', { name: s.name })}
                    showLabel={false}
                    onchange={(checked) => toggleTrash(s, checked)}
                  />
                </span>
                {#if s.broken_reason}
                  <Button variant="tonal" onclick={() => retry(s)} loading={retryingId === s.id}>
                    {t('folder_share.retry')}
                  </Button>
                {/if}
                <IconButton label={t('common.edit', { name: s.name })} onclick={() => openEdit(s)}>
                  <Icon icon={icons.rename} size={18} />
                </IconButton>
                <IconButton label={t('common.remove', { name: s.name })} onclick={() => askDelete(s)}>
                  <Icon icon={icons.delete} size={18} />
                </IconButton>
              {/snippet}
            </ListItem>
          </li>
        {/each}
      </ul>
    {/if}
    {#if trashToggleError}<p class="sc-shares__error" role="alert">{trashToggleError}</p>{/if}
    {#if retryError}<p class="sc-shares__error" role="alert">{retryError}</p>{/if}
    {#if smbNote}<p class="sc-shares__smb" role="status">{smbNote}</p>{/if}

    <Button variant="tonal" onclick={openAdd}>
      {#snippet icon()}<Icon icon={icons.add} size={18} />{/snippet}
      {t('common.add_folder')}
    </Button>
  {/if}
</section>

<Dialog open={addOpen} title={t('common.add_folder')} onclose={closeAdd}>
  <form class="sc-shares__form" onsubmit={(e) => (e.preventDefault(), submitAdd())}>
    <TextField label={t('common.name')} bind:value={addName} placeholder={t('folder_share.e_g_photos')} autocomplete="off" />
    <TextField label={t('folder_share.server_path')} bind:value={addHostPath} placeholder={t('folder_share.e_g_srv_photos')} autocomplete="off" />
    <p class="sc-shares__field-hint">{t('folder_share.enter_path_folder_already_exists')}</p>
    {#if addError}<p class="sc-shares__error" role="alert">{addError}</p>{/if}
  </form>
  {#snippet actions()}
    <Button variant="text" onclick={closeAdd} disabled={adding}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submitAdd} loading={adding}>{t('common.add')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!editTarget} title={t('folder_share.edit_folder')} onclose={closeEdit}>
  {#if editTarget}
    <form class="sc-shares__form" onsubmit={(e) => (e.preventDefault(), submitEdit())}>
      <TextField label={t('common.name')} bind:value={editName} autocomplete="off" />
      <TextField
        label={t('folder_share.server_path_optional')}
        bind:value={editHostPath}
        autocomplete="off"
      />
      {#if editError}<p class="sc-shares__error" role="alert">{editError}</p>{/if}
    </form>
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeEdit} disabled={editing}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submitEdit} loading={editing}>{t('common.save')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!deleteTarget} title={t('folder_share.remove_share')} onclose={closeDelete}>
  <p>
    {t('folder_share.removes_every_user_permission_granted', {
      name: deleteTarget?.name ?? ''
    })}
  </p>
  {#if deleteError}<p class="sc-shares__error" role="alert">{deleteError}</p>{/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeDelete} disabled={deleting}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmDelete} loading={deleting}>{t('common.remove_2')}</Button>
  {/snippet}
</Dialog>

<style>
  .sc-shares {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .sc-shares h2 {
    margin: 0;
    @apply --m3-title-large;
  }
  .sc-shares__hint {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-shares__empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 32px 16px;
    color: var(--m3c-on-surface-variant);
    text-align: center;
    border: 1px dashed var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
  }
  .sc-shares__empty p {
    margin: 0;
    @apply --m3-body-medium;
  }
  .sc-shares__list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-shares__list li + li {
    border-top: 1px solid var(--m3c-outline-variant);
  }
  /* The name yields width, not the chip beside it — a Korean share name has a
     break opportunity after every syllable, so squeezed it wraps character by
     character instead of ellipsizing (same as the user and app-password lists). */
  .sc-shares__name {
    overflow: hidden;
    min-width: 0;
    flex: 1 1 auto;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
    color: var(--m3c-on-surface);
  }
  /* The error colour, plus the word itself: a row marked only by colour says
     nothing to a reader who cannot see the difference. */
  .sc-shares__smb {
    margin: 0;
    padding: 12px 16px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-on-surface);
    @apply --m3-body-small;
  }
  .sc-shares__broken {
    display: block;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  /* `ListItem`'s trailing group has no gap of its own: every other caller puts
     only icon buttons there, which carry their own padding. This one holds a
     chip and a labelled switch as well, so it needs one. It replaces the
     margin `.sc-shares__trash` used to carry for the same reason, which only
     ever spaced the one thing that came after it. */
  .sc-shares__list :global(.sc-list-item__trailing) {
    gap: 8px;
  }
  .sc-shares__trash {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .sc-shares__trash-label {
    color: var(--m3c-on-surface-variant);
    white-space: nowrap;
    @apply --m3-label-large;
  }
  /* The trailing group cannot shrink -- it holds a switch and 40px icon
     buttons -- so on a 360px screen it took more room than the row had
     and ran past the card's edge. The word goes; the switch keeps the whole
     per-share sentence as its accessible name, so nothing is lost to a screen
     reader and the visible label returns as soon as there is room for it. */
  @media (max-width: 599px) {
    .sc-shares__trash-label {
      display: none;
    }
  }
  .sc-shares__error {
    margin: 0;
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-shares__form {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: min(360px, 80vw);
  }
  .sc-shares__field-hint {
    margin: calc(-1 * 8px) 0 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
</style>
