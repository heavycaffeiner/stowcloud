<script lang="ts">
  // Group management (`FEATURES.md` #48) — create/rename/delete groups and
  // manage membership, then hand a group principal to `GrantManagementSection`
  // the exact same way `UserManagementSection` hands it a user principal.
  // GET/POST /api/admin/groups, PATCH/DELETE /api/admin/groups/{id},
  // POST /api/admin/groups/{id}/members, DELETE .../members/{user}.
  import { t } from '../../i18n'
  import { api, ApiError, type AdminGroup, type AdminUser } from '../../api/client'
  import Button from '../Button.svelte'
  import Chip from '../Chip.svelte'
  import Dialog from '../Dialog.svelte'
  import { Icon, SelectOutlined } from 'm3-svelte'
  import { icons } from '../../icons'
  import IconButton from '../IconButton.svelte'
  import ListItem from '../ListItem.svelte'
  import ProgressCircular from '../ProgressCircular.svelte'
  import TextField from '../TextField.svelte'
  import GrantManagementSection from './GrantManagementSection.svelte'

  let groups = $state<AdminGroup[]>([])
  let users = $state<AdminUser[]>([])
  let loading = $state(true)
  let loadError = $state<string | null>(null)

  async function load(): Promise<void> {
    loading = true
    loadError = null
    try {
      const [g, u] = await Promise.all([api.adminListGroups(), api.adminListUsers()])
      groups = g
      users = u
    } catch {
      loadError = t('group.could_not_load_group_list')
    } finally {
      loading = false
    }
  }

  load()

  function userName(id: number): string {
    const u = users.find((u) => u.id === id)
    return u ? u.display_name || u.name : t('common.user', { id })
  }

  // ── create ──

  let createOpen = $state(false)
  let newName = $state('')
  let createError = $state<string | null>(null)
  let creating = $state(false)

  function openCreate(): void {
    newName = ''
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
    if (!newName.trim()) {
      createError = t('group.enter_group_name')
      return
    }
    creating = true
    try {
      const created = await api.adminCreateGroup({ name: newName.trim() })
      groups = [...groups, created]
      createOpen = false
    } catch (err) {
      if (err instanceof ApiError && err.code === 'fs.conflict') {
        createError = t('common.name_already_taken')
      } else {
        createError = t('group.could_not_create_group')
      }
    } finally {
      creating = false
    }
  }

  // ── rename ──

  let renameTarget = $state<AdminGroup | null>(null)
  let renameName = $state('')
  let renameError = $state<string | null>(null)
  let renaming = $state(false)

  function openRename(g: AdminGroup): void {
    renameTarget = g
    renameName = g.name
    renameError = null
  }

  function closeRename(): void {
    if (renaming) return
    renameTarget = null
  }

  async function submitRename(e?: SubmitEvent): Promise<void> {
    e?.preventDefault()
    if (!renameTarget) return
    renameError = null
    if (!renameName.trim()) {
      renameError = t('group.enter_group_name')
      return
    }
    renaming = true
    try {
      const updated = await api.adminRenameGroup(renameTarget.id, { name: renameName.trim() })
      groups = groups.map((g) => (g.id === updated.id ? updated : g))
      renameTarget = null
    } catch (err) {
      if (err instanceof ApiError && err.code === 'fs.conflict') {
        renameError = t('common.name_already_taken')
      } else {
        renameError = t('common.could_not_rename')
      }
    } finally {
      renaming = false
    }
  }

  // ── delete ──

  let deleteTarget = $state<AdminGroup | null>(null)
  let deleting = $state(false)
  let deleteError = $state<string | null>(null)

  function askDelete(g: AdminGroup): void {
    deleteError = null
    deleteTarget = g
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
      await api.adminDeleteGroup(deleteTarget.id)
      groups = groups.filter((g) => g.id !== deleteTarget!.id)
      deleteTarget = null
    } catch {
      deleteError = t('common.could_not_delete')
    } finally {
      deleting = false
    }
  }

  // ── members ──

  let membersTarget = $state<AdminGroup | null>(null)
  let memberError = $state<string | null>(null)
  let memberBusyId = $state<number | null>(null)
  // String-valued: m3-svelte's Select is a real `<select>`. '' = nothing picked.
  let addMemberId = $state('')

  /** Accounts not already in this group — the only ones the add picker offers. */
  const availableUsers = $derived(users.filter((u) => !membersTarget?.members.includes(u.id)))
  const memberOptions = $derived(
    availableUsers.map((u) => ({ value: String(u.id), text: u.display_name || u.name }))
  )

  function openMembers(g: AdminGroup): void {
    membersTarget = g
    memberError = null
    addMemberId = ''
  }

  function closeMembers(): void {
    membersTarget = null
  }

  async function addMember(): Promise<void> {
    if (!membersTarget || addMemberId === '') return
    memberError = null
    const id = Number(addMemberId)
    memberBusyId = id
    try {
      await api.adminAddGroupMember(membersTarget.id, id)
      const updated: AdminGroup = { ...membersTarget, members: [...membersTarget.members, id] }
      groups = groups.map((g) => (g.id === updated.id ? updated : g))
      membersTarget = updated
      addMemberId = ''
    } catch {
      memberError = t('group.could_not_add')
    } finally {
      memberBusyId = null
    }
  }

  async function removeMember(userId: number): Promise<void> {
    if (!membersTarget) return
    memberError = null
    memberBusyId = userId
    try {
      await api.adminRemoveGroupMember(membersTarget.id, userId)
      const updated: AdminGroup = { ...membersTarget, members: membersTarget.members.filter((m) => m !== userId) }
      groups = groups.map((g) => (g.id === updated.id ? updated : g))
      membersTarget = updated
    } catch {
      memberError = t('common.could_not_remove')
    } finally {
      memberBusyId = null
    }
  }

  // ── grants ──

  let grantsTarget = $state<AdminGroup | null>(null)

  function openGrants(g: AdminGroup): void {
    grantsTarget = g
  }

  function closeGrants(): void {
    grantsTarget = null
  }
</script>

<section class="sc-group-mgmt">
  <div class="sc-group-mgmt__header">
    <p class="sc-group-mgmt__hint">
      {t('group.create_group_grant_folder_permissions')}
    </p>
    <Button variant="filled" onclick={openCreate}>
      {#snippet icon()}<Icon icon={icons.add} size={18} />{/snippet}
      {t('group.add_group')}
    </Button>
  </div>

  {#if loading}
    <ProgressCircular />
  {:else if loadError}
    <p class="sc-group-mgmt__error">{loadError}</p>
  {:else if groups.length === 0}
    <div class="sc-group-mgmt__empty">
      <Icon icon={icons['folder-tree']} size={28} />
      <p>{t('group.no_groups_yet')}</p>
    </div>
  {:else}
    <ul class="sc-group-mgmt__list">
      {#each groups as g (g.id)}
        <li>
          <ListItem>
            {#snippet headline()}
              <span class="sc-group-mgmt__name">{g.name}</span>
              <Chip variant="assist">{t('group.members', { count: g.members.length })}</Chip>
            {/snippet}
            {#snippet trailing()}
              <IconButton label={t('group.manage_members', { name: g.name })} onclick={() => openMembers(g)}>
                <Icon icon={icons.settings} size={18} />
              </IconButton>
              <IconButton label={t('common.manage_folders_visible', { name: g.name })} onclick={() => openGrants(g)}>
                <Icon icon={icons['folder-tree']} size={18} />
              </IconButton>
              <IconButton label={t('group.rename', { name: g.name })} onclick={() => openRename(g)}>
                <Icon icon={icons.rename} size={18} />
              </IconButton>
              <IconButton label={t('common.delete_2', { name: g.name })} onclick={() => askDelete(g)}>
                <Icon icon={icons.delete} size={18} />
              </IconButton>
            {/snippet}
          </ListItem>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<Dialog open={createOpen} title={t('group.add_group')} onclose={closeCreate}>
  <form class="sc-group-mgmt__form" onsubmit={submitCreate}>
    <TextField label={t('group.group_name')} bind:value={newName} autocomplete="off" autofocus />
    {#if createError}<p class="sc-group-mgmt__error" role="alert">{createError}</p>{/if}
  </form>
  {#snippet actions()}
    <Button variant="text" onclick={closeCreate} disabled={creating}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={() => submitCreate()} disabled={!newName.trim()} loading={creating}>{t('common.add')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!renameTarget} title={t('group.rename_group')} onclose={closeRename}>
  <form class="sc-group-mgmt__form" onsubmit={submitRename}>
    <TextField label={t('group.group_name')} bind:value={renameName} autocomplete="off" autofocus />
    {#if renameError}<p class="sc-group-mgmt__error" role="alert">{renameError}</p>{/if}
  </form>
  {#snippet actions()}
    <Button variant="text" onclick={closeRename} disabled={renaming}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={() => submitRename()} loading={renaming}>{t('common.save')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!deleteTarget} title={t('group.delete_group')} onclose={closeDelete}>
  <p>
    {t('group.permanently_deletes_group_its_member', {
      name: deleteTarget?.name ?? ''
    })}
  </p>
  {#if deleteError}<p class="sc-group-mgmt__error" role="alert">{deleteError}</p>{/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeDelete} disabled={deleting}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={confirmDelete} loading={deleting}>{t('common.delete')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!membersTarget} title={membersTarget ? t('group.members_2', { name: membersTarget.name }) : t('group.members_3')} onclose={closeMembers}>
  {#if membersTarget}
    <div class="sc-group-mgmt__members">
      {#if membersTarget.members.length === 0}
        <p class="sc-group-mgmt__form-hint">{t('group.no_members_yet')}</p>
      {:else}
        <ul class="sc-group-mgmt__chips">
          {#each membersTarget.members as uid (uid)}
            <li>
              <Chip onremove={() => removeMember(uid)}>
                {memberBusyId === uid ? '...' : userName(uid)}
              </Chip>
            </li>
          {/each}
        </ul>
      {/if}

      {#if availableUsers.length > 0}
        <div class="sc-group-mgmt__addmember">
          <div class="sc-group-mgmt__addmember-row">
            <SelectOutlined label={t('group.add_member')} width="100%" options={memberOptions} bind:value={addMemberId} />
            <Button variant="tonal" onclick={addMember} disabled={addMemberId === ''}>{t('common.add')}</Button>
          </div>
        </div>
      {/if}

      {#if memberError}<p class="sc-group-mgmt__error" role="alert">{memberError}</p>{/if}
    </div>
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeMembers}>{t('common.close')}</Button>
  {/snippet}
</Dialog>

<Dialog open={!!grantsTarget} title={grantsTarget ? t('group.folders_visible_group', { name: grantsTarget.name }) : t('common.folder_permissions')} onclose={closeGrants}>
  {#if grantsTarget}
    <GrantManagementSection principal={{ kind: 'group', id: grantsTarget.id }} label={t('group.group', { name: grantsTarget.name })} />
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={closeGrants}>{t('common.close')}</Button>
  {/snippet}
</Dialog>

<style>
  .sc-group-mgmt {
    container-name: sc-group-mgmt;
    container-type: inline-size;
  }
  .sc-group-mgmt__header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 24px;
    margin-bottom: 16px;
  }
  @container sc-group-mgmt (max-width: 599.98px) {
    .sc-group-mgmt__header {
      flex-direction: column;
      align-items: flex-start;
    }
  }
  .sc-group-mgmt__hint {
    max-width: 560px;
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-group-mgmt__empty {
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
  .sc-group-mgmt__empty p {
    margin: 0;
    @apply --m3-body-medium;
  }
  .sc-group-mgmt__list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--m3c-outline-variant);
    border-radius: var(--m3-shape-medium);
    overflow: hidden;
  }
  .sc-group-mgmt__list li + li {
    border-top: 1px solid var(--m3c-outline-variant);
  }
  .sc-group-mgmt__list :global(.sc-list-item__trailing) {
    gap: 8px;
  }
  .sc-group-mgmt__name {
    overflow: hidden;
    min-width: 0;
    flex: 1 1 auto;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sc-group-mgmt__error {
    color: var(--m3c-error);
    @apply --m3-body-small;
  }
  .sc-group-mgmt__form {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: min(320px, 80vw);
  }
  .sc-group-mgmt__form-hint {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-group-mgmt__members {
    display: flex;
    flex-direction: column;
    gap: 16px;
    min-width: min(360px, 80vw);
  }
  .sc-group-mgmt__chips {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .sc-group-mgmt__addmember {
    display: flex;
    flex-direction: column;
    gap: 4px;
    border-top: 1px solid var(--m3c-outline-variant);
    padding-top: 16px;
  }
  .sc-group-mgmt__addmember-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  /* `:has(> select)`, not bare `.m3-container` — m3-svelte gives that class to
     the Button next to the picker too, so the unqualified rule let the button
     grow and take half the row. */
  .sc-group-mgmt__addmember-row :global(.m3-container:has(> select)) {
    flex: 1 1 auto;
    min-width: 0;
  }
</style>
