<script lang="ts">
  // Folder share management — adding, renaming and removing shared folders
  // without editing config.toml or restarting the server.
  // GET/POST /api/admin/shares, PATCH/DELETE /api/admin/shares/{id}.
  //
  // A share declared in config.toml (`config_defined`) is editable here like
  // any other — the backend keeps the new name/path as an override in
  // `shares.db` and reapplies it at startup, so nothing is reverted. Only
  // *delete* is hidden for it: the config entry would re-declare the share on
  // the next restart, and `sc_core::Core::delete_share` refuses accordingly.
  //
  // This is a distinct, adjacent screen from `GrantManagementSection`: that
  // one decides *who* can see a share (or a subpath of it); this one decides
  // *which folders exist* as shares in the first place. A share still needs
  // a grant before anyone but an admin can see it.
  import { t } from '../../i18n'
  import { api, ApiError, type AdminShare } from '../../api/client'
  import { describeApiError } from '../../api/error-text'
  import Button from '../Button.svelte'
  import Chip from '../Chip.svelte'
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

  /** `sc_core::Core::create_share`/`update_share` name each refusal with a
   *  catalogue key (`share.*`); `describeApiError` renders it. This used to
   *  reverse the server's English sentences back into keys by substring,
   *  which meant a one-word copy edit on the server silently dropped the
   *  screen to its generic fallback. */
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
      const created = await api.adminCreateShare({ name: addName.trim(), host_path: addHostPath.trim() })
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
    editHostPath = s.host_path
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
    if (editHostPath.trim() === '') {
      editError = t('folder_share.enter_server_path')
      return
    }
    editing = true
    try {
      const updated = await api.adminUpdateShare(editTarget.id, { name: editName.trim(), host_path: editHostPath.trim() })
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
      await api.adminDeleteShare(deleteTarget.id)
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
                {#if s.config_defined}<Chip variant="assist">{t('folder_share.config_file')}</Chip>{/if}
              {/snippet}
              {#snippet supporting()}
                <span class="sc-shares__path">{s.host_path}</span>
              {/snippet}
              {#snippet trailing()}
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
                <IconButton label={t('common.edit', { name: s.name })} onclick={() => openEdit(s)}>
                  <Icon icon={icons.rename} size={18} />
                </IconButton>
                <!-- Everything else about a config-file share is editable;
                     only removing it is not, because the config entry would
                     declare it again on the next restart. -->
                {#if !s.config_defined}
                  <IconButton label={t('common.remove', { name: s.name })} onclick={() => askDelete(s)}>
                    <Icon icon={icons.delete} size={18} />
                  </IconButton>
                {/if}
              {/snippet}
            </ListItem>
          </li>
        {/each}
      </ul>
    {/if}
    {#if trashToggleError}<p class="sc-shares__error" role="alert">{trashToggleError}</p>{/if}

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
      <TextField label={t('folder_share.server_path')} bind:value={editHostPath} autocomplete="off" />
      {#if editTarget.config_defined}
        <p class="sc-shares__field-hint">{t('folder_share.config_share_edit_wins_over_the_file')}</p>
      {/if}
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
  .sc-shares__path {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    font-family: var(--sc-font-mono, monospace);
  }
  .sc-shares__trash {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    /* `ListItem`'s trailing group has no gap of its own — every other caller
       puts only icon buttons there, which carry their own padding. */
    margin-inline-end: 8px;
  }
  .sc-shares__trash-label {
    color: var(--m3c-on-surface-variant);
    white-space: nowrap;
    @apply --m3-label-large;
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
