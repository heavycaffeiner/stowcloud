import { useState } from 'react'
import { overlay } from 'overlay-kit'
import { describeApiError } from '../../../api/error-text'
import { t } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { confirmAction, promptText } from '../../../ui/ActionDialog'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { Icon } from '../../../ui/Icon'
import { ProgressCircular } from '../../../ui/ProgressCircular'
import { Select } from '../../../ui/Select'
import { VirtualList } from '../../../ui/VirtualList'
import { ListItem } from '../../../ui/ListItem'
import { showGrants } from './GrantManagementSection'
import * as styles from './GroupManagementSection.css'
import * as adminStyles from './admin.css'
import {
  useAddGroupMember,
  useAdminGroups,
  useAdminUsers,
  useCreateGroup,
  useDeleteGroup,
  useRemoveGroupMember,
  useRenameGroup,
  type AdminGroup
} from '../api'
import { ApiError } from '../../../api/fetcher'

function groupErrorText(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.code === 'fs.conflict') return t('common.name_already_taken')
  return describeApiError(error, fallback)
}

const requireName = (name: string): string | true => name !== '' || t('group.enter_group_name')

export function GroupManagementSection() {
  const { t, tp } = useI18n()
  const groupsQuery = useAdminGroups()
  const usersQuery = useAdminUsers()
  const groups = groupsQuery.data ?? []
  const create = useCreateGroup()
  const rename = useRenameGroup()
  const remove = useDeleteGroup()
  // Keeps the row that opened a dialog mounted so focus can return to it.
  const [pinned, setPinned] = useState<number | null>(null)

  async function withPinned(group: AdminGroup, task: () => Promise<unknown>): Promise<void> {
    setPinned(group.id)
    await task()
    setPinned(null)
  }
  function addGroup(): void {
    void promptText({
      title: t('group.add_group'),
      label: t('group.group_name'),
      action: t('common.add'),
      validate: requireName,
      run: (name) => create.mutateAsync(name),
      describeError: (error) => groupErrorText(error, t('group.could_not_create_group'))
    })
  }
  function renameGroup(group: AdminGroup): Promise<void> {
    return withPinned(group, () =>
      promptText({
        title: t('group.rename_group'),
        label: t('group.group_name'),
        action: t('common.save'),
        initial: group.name,
        validate: requireName,
        run: (name) => rename.mutateAsync({ id: group.id, name }),
        describeError: (error) => groupErrorText(error, t('common.could_not_rename'))
      })
    )
  }
  function deleteGroup(group: AdminGroup): Promise<void> {
    return withPinned(group, () =>
      confirmAction({
        title: t('group.delete_group'),
        action: t('common.delete'),
        danger: true,
        body: <p>{t('group.permanently_deletes_group_its_member', { name: group.name })}</p>,
        run: () => remove.mutateAsync(group.id),
        describeError: (error) => describeApiError(error, t('common.could_not_delete'))
      })
    )
  }

  return (
    <section className={adminStyles.section}>
      <div className={adminStyles.sectionHeader}>
        <p className={adminStyles.sectionHint}>{t('group.create_group_grant_folder_permissions')}</p>
        <Button className={adminStyles.sectionHeaderAction} icon={<Icon name="add" />} onClick={addGroup}>
          {t('group.add_group')}
        </Button>
      </div>

      {groupsQuery.isPending || usersQuery.isPending ? (
        <ProgressCircular />
      ) : groupsQuery.error || usersQuery.error ? (
        <p className={adminStyles.sectionError} role="alert">
          {describeApiError(groupsQuery.error ?? usersQuery.error, t('group.could_not_load_group_list'))}
        </p>
      ) : groups.length === 0 ? (
        <div className={adminStyles.empty}>
          <Icon name="account_tree" />
          <p className={adminStyles.emptyText}>{t('group.no_groups_yet')}</p>
        </div>
      ) : (
        <VirtualList
          className={adminStyles.list}
          items={groups}
          itemKey={(group) => group.id}
          estimateSize={72}
          itemProps={() => ({ className: adminStyles.item })}
          pinnedKeys={pinned === null ? undefined : [pinned]}
          renderItem={(group) => (
            <ListItem
              headline={
                <>
                  <span className={adminStyles.rowName}>{group.name}</span>
                  <span className={adminStyles.chip}>{tp('group.members', group.members.length)}</span>
                </>
              }
              trailing={
                <div className={adminStyles.rowActions}>
                  <Button
                    variant="text"
                    square
                    ariaLabel={t('group.manage_members', { name: group.name })}
                    onClick={() => void withPinned(group, () => showGroupMembers(group.id))}
                  >
                    <Icon name="settings" />
                  </Button>
                  <Button
                    variant="text"
                    square
                    ariaLabel={t('common.manage_folders_visible', { name: group.name })}
                    onClick={() =>
                      void withPinned(group, () =>
                        showGrants({
                          title: t('group.folders_visible_group', { name: group.name }),
                          principal: { kind: 'group', id: group.id },
                          label: t('group.group', { name: group.name })
                        })
                      )
                    }
                  >
                    <Icon name="account_tree" />
                  </Button>
                  <Button
                    variant="text"
                    square
                    ariaLabel={t('group.rename', { name: group.name })}
                    onClick={() => void renameGroup(group)}
                  >
                    <Icon name="rename" />
                  </Button>
                  <Button
                    variant="text"
                    danger
                    square
                    ariaLabel={t('common.delete_2', { name: group.name })}
                    onClick={() => void deleteGroup(group)}
                  >
                    <Icon name="delete" />
                  </Button>
                </div>
              }
            />
          )}
        />
      )}
    </section>
  )
}

/** Shows a group's members for adding and removing. Settles once it has closed. */
function showGroupMembers(groupId: number): Promise<void> {
  return overlay.openAsync<void>(({ isOpen, close, unmount }) => (
    <GroupMembersDialog groupId={groupId} open={isOpen} onClose={() => close()} onClosed={unmount} />
  ))
}

interface GroupMembersDialogProps {
  groupId: number
  open: boolean
  onClose: () => void
  onClosed: () => void
}

function GroupMembersDialog({ groupId, open, onClose, onClosed }: GroupMembersDialogProps) {
  const { t } = useI18n()
  const group = useAdminGroups().data?.find((item) => item.id === groupId)
  const users = useAdminUsers().data ?? []
  const addMember = useAddGroupMember()
  const removeMember = useRemoveGroupMember()
  const [addMemberId, setAddMemberId] = useState('')
  const members = group?.members ?? []
  const availableUsers = users.filter((user) => !members.includes(user.id))
  const memberError = addMember.error
    ? describeApiError(addMember.error, t('group.could_not_add'))
    : removeMember.error
      ? describeApiError(removeMember.error, t('common.could_not_remove'))
      : null
  const memberBusyId =
    addMember.isPending && addMember.variables
      ? addMember.variables.userId
      : removeMember.isPending && removeMember.variables
        ? removeMember.variables.userId
        : null

  function userName(id: number): string {
    const user = users.find((item) => item.id === id)
    return user?.display_name || user?.name || t('common.user', { id })
  }
  function add(): void {
    if (addMemberId === '') return
    removeMember.reset()
    addMember.mutate({ groupId, userId: Number(addMemberId) }, { onSuccess: () => setAddMemberId('') })
  }
  function removeUser(userId: number): void {
    addMember.reset()
    removeMember.mutate({ groupId, userId })
  }

  return (
    <Dialog
      open={open}
      title={group ? t('group.members_2', { name: group.name }) : t('group.members_3')}
      onClose={onClose}
      onClosed={onClosed}
      actions={
        <Button variant="text" onClick={onClose}>
          {t('common.close')}
        </Button>
      }
    >
      <div className={adminStyles.form}>
        {members.length ? (
          <VirtualList
            className={styles.chips}
            items={members}
            itemKey={(id) => id}
            estimateSize={44}
            pinnedKeys={memberBusyId === null ? undefined : [memberBusyId]}
            renderItem={(id) => (
              <span className={adminStyles.chip}>
                {memberBusyId === id ? t('common.loading') : userName(id)}
                <Button
                  className={adminStyles.chipAction}
                  variant="text"
                  square
                  ariaLabel={t('group.remove_member', { name: userName(id) })}
                  disabled={memberBusyId === id}
                  onClick={() => removeUser(id)}
                >
                  <Icon name="close" size={14} />
                </Button>
              </span>
            )}
          />
        ) : (
          <p className={adminStyles.sectionFieldHint}>{t('group.no_members_yet')}</p>
        )}
        {availableUsers.length ? (
          <div className={styles.formRow}>
            <Select
              ariaLabel={t('group.add_member')}
              value={addMemberId}
              options={[
                { value: '', text: t('group.add_member') },
                ...availableUsers.map((user) => ({ value: String(user.id), text: user.display_name || user.name }))
              ]}
              onValueChange={setAddMemberId}
            />
            <Button variant="tonal" disabled={!addMemberId} loading={addMember.isPending} onClick={add}>
              {t('common.add')}
            </Button>
          </div>
        ) : null}
        {memberError ? (
          <p className={adminStyles.sectionError} role="alert">
            {memberError}
          </p>
        ) : null}
      </div>
    </Dialog>
  )
}
