import { useState } from 'react'
import { formatBytes, bytesToMb, BYTES_PER_MB } from '../../../lib/format/bytes'
import { t } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { describeApiError } from '../../../api/error-text'
import { confirmAction, Icon, promptText, StowButton, StowProgressCircular, VirtualList } from '@/shared/ui'
import { showGrants } from './GrantManagementSection'
import { askNewUser, askUserPassword } from './UserDialogs'
import { showUserOidc } from './UserOidcDialog'
import { UserManagementRow } from './UserManagementRow'
import * as adminStyles from './admin.css'
import { ApiError } from '../../../api/fetcher'
import { useAdminUsers, useDeleteUser, useSetUserDisabled, useSetUserQuota, type AdminUser } from '../api'

function lastAdminText(error: unknown, lastAdmin: string, fallback: string): string {
  return error instanceof ApiError && error.code === 'admin.last_admin' ? lastAdmin : describeApiError(error, fallback)
}

const validQuota = (value: string): string | true =>
  value === '' || (Number.isFinite(Number(value)) && Number(value) > 0) || t('user.enter_number_greater_than_0')

function userGrants(user: AdminUser): Promise<void> {
  const name = user.display_name || user.name
  return showGrants({
    title: t('user.folders_visible', { name }),
    principal: { kind: 'user', id: user.id },
    label: name
  })
}

export function UserManagementSection() {
  const { t } = useI18n()
  const usersQuery = useAdminUsers()
  const users = usersQuery.data ?? []
  const toggle = useSetUserDisabled()
  const remove = useDeleteUser()
  const quota = useSetUserQuota()
  // Keeps the row that opened a dialog mounted so focus can return to it.
  const [pinned, setPinned] = useState<number | null>(null)
  const activeAdminCount = users.filter((user) => user.is_admin && !user.disabled).length
  const togglingId = toggle.isPending && toggle.variables ? toggle.variables.id : null

  async function withPinned(user: AdminUser, task: () => Promise<unknown>): Promise<void> {
    setPinned(user.id)
    await task()
    setPinned(null)
  }
  async function addUser(): Promise<void> {
    const created = await askNewUser()
    if (created) await userGrants(created)
  }
  function editQuota(user: AdminUser): Promise<void> {
    return withPinned(user, () =>
      promptText({
        title: t('user.storage_quota', { name: user.display_name || user.name }),
        label: t('user.storage_quota_mb'),
        action: t('common.save'),
        placeholder: t('user.empty_means_unlimited'),
        initial: user.quota_bytes ? String(bytesToMb(Number(BigInt(user.quota_bytes)))) : '',
        hint: (
          <p className={adminStyles.sectionFieldHint}>
            {t('user.currently_using', { used: formatBytes(Number(BigInt(user.usage_bytes))) })}
            {t('user.empty_means_unlimited_uploads_copies')}
          </p>
        ),
        validate: validQuota,
        run: (value) =>
          quota.mutateAsync({ id: user.id, quotaBytes: value ? Math.round(Number(value) * BYTES_PER_MB) : null }),
        describeError: (error) => describeApiError(error, t('common.could_not_save'))
      })
    )
  }
  function deleteUser(user: AdminUser): Promise<void> {
    return withPinned(user, () =>
      confirmAction({
        title: t('user.delete_user'),
        action: t('common.delete'),
        danger: true,
        body: <p>{t('user.permanently_deletes_account_including_its', { name: user.name })}</p>,
        run: () => remove.mutateAsync(user.id),
        describeError: (error) =>
          lastAdminText(error, t('user.last_administrator_cannot_deleted'), t('common.could_not_delete'))
      })
    )
  }

  return (
    <section className={adminStyles.section}>
      <div className={adminStyles.sectionHeader}>
        <p className={adminStyles.hint}>{t('user.create_accounts_suspend_or_re')}</p>
        <StowButton
          className={adminStyles.sectionHeaderAction}
          icon={<Icon name="add" />}
          onClick={() => void addUser()}
        >
          {t('user.add_user')}
        </StowButton>
      </div>
      {toggle.error ? (
        <p className={adminStyles.sectionError} role="alert">
          {lastAdminText(
            toggle.error,
            t('user.last_administrator_cannot_deactivated'),
            t('common.could_not_save_change')
          )}
        </p>
      ) : null}
      {usersQuery.isPending ? (
        <StowProgressCircular size={40} />
      ) : usersQuery.error ? (
        <p className={adminStyles.sectionError} role="alert">
          {describeApiError(usersQuery.error, t('user.could_not_load_user_list'))}
        </p>
      ) : (
        <VirtualList
          className={adminStyles.list}
          items={users}
          itemKey={(user) => user.id}
          estimateSize={80}
          itemProps={() => ({ className: adminStyles.item })}
          pinnedKeys={[pinned, togglingId].filter((id): id is number => id !== null)}
          renderItem={(user) => (
            <UserManagementRow
              user={user}
              locked={user.is_admin && !user.disabled && activeAdminCount <= 1}
              toggling={togglingId === user.id}
              onToggle={() => toggle.mutate({ id: user.id, disabled: !user.disabled })}
              onQuota={() => void editQuota(user)}
              onGrants={() => void withPinned(user, () => userGrants(user))}
              onOidc={() => void withPinned(user, () => showUserOidc(user))}
              onPassword={() => void withPinned(user, () => askUserPassword(user))}
              onDelete={() => void deleteUser(user)}
            />
          )}
        />
      )}
    </section>
  )
}
