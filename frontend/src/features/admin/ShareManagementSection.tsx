import { useMemo, useState } from 'react'
import { useI18n } from '../../hooks/use-i18n'
import { useCopyText } from '../../hooks/use-copy-text'
import { describeApiError } from '../../api/error-text'
import { confirmAction } from '../../ui/ActionDialog'
import { Button } from '../../ui/Button'
import { Icon } from '../../ui/Icon'
import { ProgressCircular } from '../../ui/ProgressCircular'
import { askEnableEncryption, askShare, shareErrorText } from './ShareDialogs'
import { ShareManagementList } from './ShareManagementList'
import * as styles from './ShareManagementSection.css'
import * as adminStyles from './admin.css'
import {
  useAdminShares,
  useDeleteShare,
  useDisableShareEncryption,
  useRetryShare,
  useUpdateShare,
  type AdminShare
} from './api'
import { useShareEncryptions } from '../shares/api'

export function ShareManagementSection() {
  const { t } = useI18n()
  const sharesQuery = useAdminShares()
  const encryptionQuery = useShareEncryptions()
  const encryptionByShare = useMemo(
    () => new Map((encryptionQuery.data ?? []).map((entry) => [entry.share, entry])),
    [encryptionQuery.data]
  )
  const remove = useDeleteShare()
  const trash = useUpdateShare()
  const retry = useRetryShare()
  const disableEncryption = useDisableShareEncryption()
  const copy = useCopyText()
  // Keeps the row that opened a dialog mounted so focus can return to it.
  const [pinned, setPinned] = useState<number | null>(null)
  const [announcement, setAnnouncement] = useState('')
  const trashTogglingId = trash.isPending ? (trash.variables?.id ?? null) : null
  const retryingId = retry.isPending ? (retry.variables ?? null) : null

  async function withPinned<T>(share: AdminShare, task: () => Promise<T>): Promise<T> {
    setPinned(share.id)
    try {
      return await task()
    } finally {
      setPinned(null)
    }
  }
  function removeShare(share: AdminShare): Promise<unknown> {
    return withPinned(share, () =>
      confirmAction({
        title: t('folder_share.remove_share'),
        action: t('common.remove_2'),
        danger: true,
        body: <p>{t('folder_share.removes_every_user_permission_granted', { name: share.name })}</p>,
        run: () => remove.mutateAsync(share.id),
        describeError: (error) => shareErrorText(error, t('common.could_not_remove'))
      })
    )
  }
  async function enableEncryption(share: AdminShare): Promise<void> {
    if (await withPinned(share, () => askEnableEncryption(share)))
      setAnnouncement(t('encryption.enabled_for', { name: share.name }))
  }
  async function turnOffEncryption(share: AdminShare): Promise<void> {
    const done = await withPinned(share, () =>
      confirmAction({
        title: t('encryption.disable_title', { name: share.name }),
        action: t('encryption.disable'),
        body: <p>{t('encryption.disable_hint')}</p>,
        run: () => disableEncryption.mutateAsync(share.id).then(() => true),
        describeError: (error) => describeApiError(error, t('encryption.could_not_disable'))
      })
    )
    if (done) setAnnouncement(t('encryption.disabled_for', { name: share.name }))
  }

  return (
    <section className={adminStyles.section}>
      <h2 className={adminStyles.sectionTitle}>{t('folder_share.folder_shares')}</h2>
      <p className={adminStyles.hint}>{t('folder_share.registers_real_folder_on_server')}</p>
      {sharesQuery.isPending ? (
        <ProgressCircular />
      ) : sharesQuery.error ? (
        <p className={adminStyles.error} role="alert">
          {describeApiError(sharesQuery.error, t('folder_share.could_not_load_share_list'))}
        </p>
      ) : (
        <>
          <ShareManagementList
            shares={sharesQuery.data}
            encryptionByShare={encryptionByShare}
            encryptionLoaded={Boolean(encryptionQuery.data)}
            pinnedKeys={[pinned, trashTogglingId, retryingId].filter((id): id is number => id !== null)}
            trashTogglingId={trashTogglingId}
            retryingId={retryingId}
            onEnableEncryption={(share) => void enableEncryption(share)}
            onDisableEncryption={(share) => void turnOffEncryption(share)}
            onCopySalt={(salt, name) =>
              copy.mutate(salt, { onSuccess: () => setAnnouncement(t('encryption.salt_copied', { name })) })
            }
            onToggleTrash={(share, enabled) => trash.mutate({ id: share.id, patch: { trash_enabled: enabled } })}
            onRetry={(share) => retry.mutate(share.id)}
            onEdit={(share) => void withPinned(share, () => askShare(share))}
            onDelete={(share) => void removeShare(share)}
          />
          {trash.error ? (
            <p className={adminStyles.error} role="alert">
              {shareErrorText(trash.error, t('folder_share.could_not_change_trash_setting'))}
            </p>
          ) : null}
          {retry.error ? (
            <p className={adminStyles.error} role="alert">
              {shareErrorText(retry.error, t('folder_share.the_folder_is_still_unavailable'))}
            </p>
          ) : null}
          {encryptionQuery.error ? (
            <p className={adminStyles.error} role="alert">
              {describeApiError(encryptionQuery.error, t('encryption.could_not_load_status'))}
            </p>
          ) : null}
          <p className={styles.encAnnounce} aria-live="polite">
            {announcement}
          </p>
          <Button variant="tonal" icon={<Icon name="add" />} onClick={() => void askShare()}>
            {t('common.add_folder')}
          </Button>
        </>
      )}
    </section>
  )
}
