import { useMutation, useQuery } from '@tanstack/react-query'
import { useComponentState } from '../../lib/store/use-component-state'
import type { AdminGroup } from '../../lib/api/client'
import { ApiError } from '../../lib/api/client'
import { describeApiError } from '../../lib/api/error-text'
import { useI18n } from '../../lib/i18n/use-i18n'
import { adminGroupMutation, adminGroupsQuery, adminUsersQuery } from '../../lib/query/admin'
import { Button } from '../../lib/ui/Button'
import { Dialog } from '../../lib/ui/Dialog'
import { Icon } from '../../lib/ui/Icon'
import { ProgressCircular } from '../../lib/ui/ProgressCircular'
import { Select } from '../../lib/ui/Select'
import { TextField } from '../../lib/ui/TextField'
import { VirtualList } from '../../lib/ui/VirtualList'
import { ListItem } from '../../lib/ui/ListItem'
import { GrantManagementSection } from './GrantManagementSection'
import './admin.css'

type Translate = (key: string, params?: Record<string, string | number>) => string

export function GroupManagementSection() {
  const { t, tp } = useI18n()
  const groupsQuery = useQuery(adminGroupsQuery())
  const usersQuery = useQuery(adminUsersQuery())
  const groups = groupsQuery.data ?? []
  const users = usersQuery.data ?? []
  const loading = groupsQuery.isPending || usersQuery.isPending

  type GroupState = { createOpen: boolean; newName: string; createValidation: string | null; renameTarget: AdminGroup | null; renameName: string; renameValidation: string | null; deleteTarget: AdminGroup | null; membersTargetId: number | null; addMemberId: string; grantsTarget: AdminGroup | null }
  const [state, setState] = useComponentState<GroupState>({ createOpen: false, newName: '', createValidation: null, renameTarget: null, renameName: '', renameValidation: null, deleteTarget: null, membersTargetId: null, addMemberId: '', grantsTarget: null })
  const { createOpen, newName, createValidation, renameTarget, renameName, renameValidation, deleteTarget, membersTargetId, addMemberId, grantsTarget } = state
  const patchState = (patch: Partial<GroupState>): void => setState((current) => ({ ...current, ...patch }))
  const setCreateOpen = (value: boolean): void => patchState({ createOpen: value })
  const setNewName = (value: string): void => patchState({ newName: value })
  const setCreateValidation = (value: string | null): void => patchState({ createValidation: value })
  const setRenameTarget = (value: AdminGroup | null): void => patchState({ renameTarget: value })
  const setRenameName = (value: string): void => patchState({ renameName: value })
  const setRenameValidation = (value: string | null): void => patchState({ renameValidation: value })
  const setDeleteTarget = (value: AdminGroup | null): void => patchState({ deleteTarget: value })
  const setMembersTargetId = (value: number | null): void => patchState({ membersTargetId: value })
  const setAddMemberId = (value: string): void => patchState({ addMemberId: value })
  const setGrantsTarget = (value: AdminGroup | null): void => patchState({ grantsTarget: value })
  const create = useMutation(adminGroupMutation())
  const rename = useMutation(adminGroupMutation())
  const remove = useMutation(adminGroupMutation())
  const addMember = useMutation(adminGroupMutation())
  const removeMember = useMutation(adminGroupMutation())

  const membersTarget = membersTargetId === null ? null : groups.find((group) => group.id === membersTargetId) ?? null
  const availableUsers = users.filter((user) => !membersTarget?.members.includes(user.id))
  const createError = createValidation ?? (create.error ? groupError(create.error, t('group.could_not_create_group'), t) : null)
  const renameError = renameValidation ?? (rename.error ? groupError(rename.error, t('common.could_not_rename'), t) : null)
  const deleteError = remove.error ? describeApiError(remove.error, t('common.could_not_delete')) : null
  const memberError = addMember.error
    ? describeApiError(addMember.error, t('group.could_not_add'))
    : removeMember.error
      ? describeApiError(removeMember.error, t('common.could_not_remove'))
      : null
  const memberBusyId = addMember.isPending && addMember.variables?.kind === 'add-member'
    ? addMember.variables.userId
    : removeMember.isPending && removeMember.variables?.kind === 'remove-member'
      ? removeMember.variables.userId
      : null

  function userName(id: number): string {
    const user = users.find((item) => item.id === id)
    return user?.display_name || user?.name || t('common.user', { id })
  }

  function openCreate(): void {
    create.reset()
    setCreateValidation(null)
    setNewName('')
    setCreateOpen(true)
  }

  function closeCreate(): void {
    if (!create.isPending) setCreateOpen(false)
  }

  function submitCreate(): void {
    setCreateValidation(null)
    if (!newName.trim()) {
      setCreateValidation(t('group.enter_group_name'))
      return
    }
    create.mutate({ kind: 'create', req: { name: newName.trim() } }, { onSuccess: () => setCreateOpen(false) })
  }

  function openRename(group: AdminGroup): void {
    rename.reset()
    setRenameTarget(group)
    setRenameName(group.name)
    setRenameValidation(null)
  }

  function closeRename(): void {
    if (!rename.isPending) setRenameTarget(null)
  }

  function submitRename(): void {
    if (!renameTarget) return
    setRenameValidation(null)
    if (!renameName.trim()) {
      setRenameValidation(t('group.enter_group_name'))
      return
    }
    rename.mutate({ kind: 'rename', id: renameTarget.id, patch: { name: renameName.trim() } }, { onSuccess: () => setRenameTarget(null) })
  }

  function askDelete(group: AdminGroup): void {
    remove.reset()
    setDeleteTarget(group)
  }

  function closeDelete(): void {
    if (!remove.isPending) setDeleteTarget(null)
  }

  function submitDelete(): void {
    if (!deleteTarget) return
    remove.mutate({ kind: 'delete', id: deleteTarget.id }, { onSuccess: () => setDeleteTarget(null) })
  }

  function openMembers(group: AdminGroup): void {
    setMembersTargetId(group.id)
    setAddMemberId('')
    addMember.reset()
    removeMember.reset()
  }

  function closeMembers(): void {
    setMembersTargetId(null)
  }

  function submitAddMember(): void {
    if (!membersTarget || addMemberId === '') return
    addMember.mutate({ kind: 'add-member', id: membersTarget.id, userId: Number(addMemberId) }, { onSuccess: () => setAddMemberId('') })
  }

  function submitRemoveMember(userId: number): void {
    if (!membersTarget) return
    removeMember.mutate({ kind: 'remove-member', id: membersTarget.id, userId })
  }

  function openGrants(group: AdminGroup): void {
    setGrantsTarget(group)
  }

  function closeGrants(): void {
    setGrantsTarget(null)
  }

  return (
    <section className="sc-admin-section sc-group-mgmt">
      <div className="sc-admin-section__header">
        <p className="sc-admin-section__hint">{t('group.create_group_grant_folder_permissions')}</p>
        <Button icon={<Icon name="add" />} onClick={openCreate}>{t('group.add_group')}</Button>
      </div>

      {loading ? <ProgressCircular /> : groupsQuery.error || usersQuery.error ? (
        <p className="sc-admin-section__error" role="alert">{describeApiError(groupsQuery.error ?? usersQuery.error, t('group.could_not_load_group_list'))}</p>
      ) : groups.length === 0 ? (
        <div className="sc-admin-empty">
          <Icon name="account_tree" />
          <p>{t('group.no_groups_yet')}</p>
        </div>
      ) : (
        <VirtualList
          className="sc-admin-list"
          items={groups}
          itemKey={(group) => group.id}
          estimateSize={72}
          pinnedKeys={[renameTarget?.id, deleteTarget?.id, membersTargetId, grantsTarget?.id].filter((id): id is number => id != null)}
          renderItem={(group) => (
            <ListItem
              headline={<><span className="sc-admin-row__name">{group.name}</span><span className="sc-admin-chip">{tp('group.members', group.members.length)}</span></>}
              trailing={<div className="sc-admin-row__actions">
                <Button variant="text" square ariaLabel={t('group.manage_members', { name: group.name })} onClick={() => openMembers(group)}><Icon name="settings" /></Button>
                <Button variant="text" square ariaLabel={t('common.manage_folders_visible', { name: group.name })} onClick={() => openGrants(group)}><Icon name="account_tree" /></Button>
                <Button variant="text" square ariaLabel={t('group.rename', { name: group.name })} onClick={() => openRename(group)}><Icon name="rename" /></Button>
                <Button variant="text" danger square ariaLabel={t('common.delete_2', { name: group.name })} onClick={() => askDelete(group)}><Icon name="delete" /></Button>
              </div>}
            />
          )}
        />
      )}

      <Dialog open={createOpen} title={t('group.add_group')} onClose={closeCreate} actions={
        <>
          <Button variant="text" disabled={create.isPending} onClick={closeCreate}>{t('common.cancel')}</Button>
          <Button disabled={!newName.trim()} loading={create.isPending} onClick={submitCreate}>{t('common.add')}</Button>
        </>
      }>
        <form className="sc-admin-form" onSubmit={(event) => { event.preventDefault(); submitCreate() }}>
          <TextField label={t('group.group_name')} value={newName} autoComplete="off" autoFocus onValueChange={setNewName} />
          {createError ? <p className="sc-admin-section__error" role="alert">{createError}</p> : null}
        </form>
      </Dialog>

      <Dialog open={!!renameTarget} title={t('group.rename_group')} onClose={closeRename} actions={
        <>
          <Button variant="text" disabled={rename.isPending} onClick={closeRename}>{t('common.cancel')}</Button>
          <Button loading={rename.isPending} onClick={submitRename}>{t('common.save')}</Button>
        </>
      }>
        <form className="sc-admin-form" onSubmit={(event) => { event.preventDefault(); submitRename() }}>
          <TextField label={t('group.group_name')} value={renameName} autoComplete="off" autoFocus onValueChange={setRenameName} />
          {renameError ? <p className="sc-admin-section__error" role="alert">{renameError}</p> : null}
        </form>
      </Dialog>

      <Dialog open={!!deleteTarget} title={t('group.delete_group')} onClose={closeDelete} actions={
        <>
          <Button variant="text" disabled={remove.isPending} onClick={closeDelete}>{t('common.cancel')}</Button>
          <Button danger loading={remove.isPending} onClick={submitDelete}>{t('common.delete')}</Button>
        </>
      }>
        <p>{t('group.permanently_deletes_group_its_member', { name: deleteTarget?.name ?? '' })}</p>
        {deleteError ? <p className="sc-admin-section__error" role="alert">{deleteError}</p> : null}
      </Dialog>

      <Dialog open={!!membersTarget} title={membersTarget ? t('group.members_2', { name: membersTarget.name }) : t('group.members_3')} onClose={closeMembers} actions={<Button variant="text" onClick={closeMembers}>{t('common.close')}</Button>}>
        {membersTarget ? (
          <div className="sc-admin-form">
            {membersTarget.members.length ? (
              <VirtualList
                className="sc-admin-chips"
                items={membersTarget.members}
                itemKey={(id) => id}
                estimateSize={44}
                pinnedKeys={memberBusyId === null ? [] : [memberBusyId]}
                renderItem={(id) => (
                    <span className="sc-admin-chip">
                      {memberBusyId === id ? t('common.loading') : userName(id)}
                      <Button variant="text" square ariaLabel={t('group.remove_member', { name: userName(id) })} disabled={memberBusyId === id} onClick={() => submitRemoveMember(id)}><Icon name="close" /></Button>
                    </span>
                )}
              />
            ) : <p className="sc-admin-section__field-hint">{t('group.no_members_yet')}</p>}

            {availableUsers.length ? (
              <div className="sc-admin-form__row">
                <Select
                  ariaLabel={t('group.add_member')}
                  value={addMemberId}
                  options={[{ value: '', text: t('group.add_member') }, ...availableUsers.map((user) => ({ value: String(user.id), text: user.display_name || user.name }))]}
                  onValueChange={setAddMemberId}
                />
                <Button variant="tonal" disabled={!addMemberId} loading={addMember.isPending} onClick={submitAddMember}>{t('common.add')}</Button>
              </div>
            ) : null}

            {memberError ? <p className="sc-admin-section__error" role="alert">{memberError}</p> : null}
          </div>
        ) : null}
      </Dialog>

      <Dialog open={!!grantsTarget} title={grantsTarget ? t('group.folders_visible_group', { name: grantsTarget.name }) : t('common.folder_permissions')} onClose={closeGrants} actions={<Button variant="text" onClick={closeGrants}>{t('common.close')}</Button>}>
        {grantsTarget ? <GrantManagementSection principal={{ kind: 'group', id: grantsTarget.id }} label={t('group.group', { name: grantsTarget.name })} /> : null}
      </Dialog>
    </section>
  )
}

function groupError(error: unknown, fallback: string, translate: Translate): string {
  if (error instanceof ApiError && error.code === 'fs.conflict') return translate('common.name_already_taken')
  return describeApiError(error, fallback)
}
