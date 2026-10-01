import { useState } from 'react'
import { describeApiError } from '../../lib/api/error-text'
import { formatDateNs } from '../../lib/i18n'
import { useI18n } from '../../hooks/use-i18n'
import { useAppPasswords, useCreateAppPassword, useRevokeAppPassword } from './api'
import { Button } from '../../ui/Button'
import { Switch } from '../../ui/Switch'
import { TextField } from '../../ui/TextField'
import { VirtualList } from '../../ui/VirtualList'
import { SettingsDialog } from './SettingsDialog'
import * as styles from './AppPasswordsSection.css'
import * as settingsCardStyles from './SettingsCard.css'
import { ApiError } from '../../api/fetcher'
import type { AppPasswordInfo } from './api'

export function AppPasswordsSection() {
  const { t } = useI18n()
  const list = useAppPasswords()
  const create = useCreateAppPassword()
  const revoke = useRevokeAppPassword()
  const wipe = useRevokeAppPassword()
  type AppPasswordState = {
    createOpen: boolean
    newName: string
    newCurrent: string
    newReadOnly: boolean
    issuedToken: string | null
    issuedAcknowledged: boolean
    tokenCopyState: 'idle' | 'copied' | 'failed'
    revokeTarget: AppPasswordInfo | null
    wipeTarget: AppPasswordInfo | null
    actionError: string | null
  }
  const [state, setState] = useState<AppPasswordState>({
    createOpen: false,
    newName: '',
    newCurrent: '',
    newReadOnly: false,
    issuedToken: null,
    issuedAcknowledged: false,
    tokenCopyState: 'idle',
    revokeTarget: null,
    wipeTarget: null,
    actionError: null
  })
  const {
    createOpen,
    newName,
    newCurrent,
    newReadOnly,
    issuedToken,
    issuedAcknowledged,
    tokenCopyState,
    revokeTarget,
    wipeTarget,
    actionError
  } = state
  const patchState = (patch: Partial<AppPasswordState>): void => setState((current) => ({ ...current, ...patch }))
  const setCreateOpen = (value: boolean): void => patchState({ createOpen: value })
  const setNewName = (value: string): void => patchState({ newName: value })
  const setNewCurrent = (value: string): void => patchState({ newCurrent: value })
  const setNewReadOnly = (value: boolean): void => patchState({ newReadOnly: value })
  const setIssuedToken = (value: string | null): void => patchState({ issuedToken: value })
  const setIssuedAcknowledged = (value: boolean): void => patchState({ issuedAcknowledged: value })
  const setTokenCopyState = (value: AppPasswordState['tokenCopyState']): void => patchState({ tokenCopyState: value })
  const setRevokeTarget = (value: AppPasswordInfo | null): void => patchState({ revokeTarget: value })
  const setWipeTarget = (value: AppPasswordInfo | null): void => patchState({ wipeTarget: value })
  const setActionError = (value: string | null): void => patchState({ actionError: value })

  function isExpired(item: AppPasswordInfo): boolean {
    return item.expires_ns ? Number(item.expires_ns) / 1e6 <= Date.now() : false
  }
  function openCreate(): void {
    setNewName('')
    setNewCurrent('')
    setNewReadOnly(false)
    create.reset()
    setActionError(null)
    setCreateOpen(true)
  }
  function confirmCreate(): void {
    if (!newName.trim() || !newCurrent) return
    create.mutate(
      { name: newName.trim(), currentPassword: newCurrent, scope: newReadOnly ? { readOnly: true } : undefined },
      {
        onSuccess: (token) => {
          setCreateOpen(false)
          setIssuedToken(token)
          setIssuedAcknowledged(false)
          setTokenCopyState('idle')
          void list.refetch()
        },
        onError: (error) => setActionError(describeApiError(error, t('app_password.could_not_create_app_password')))
      }
    )
  }
  function closeIssued(): void {
    if (issuedToken && !issuedAcknowledged) return
    setIssuedToken(null)
  }
  function acknowledgeIssued(): void {
    setIssuedAcknowledged(true)
    setIssuedToken(null)
  }
  async function copyToken(): Promise<void> {
    if (!issuedToken) return
    try {
      await navigator.clipboard.writeText(issuedToken)
      setTokenCopyState('copied')
    } catch {
      setTokenCopyState('failed')
    }
  }
  function confirmRevoke(): void {
    if (!revokeTarget) return
    setActionError(null)
    revoke.mutate(
      { id: revokeTarget.id, wipe: false },
      {
        onSuccess: () => setRevokeTarget(null),
        onError: (error) => {
          if (error instanceof ApiError && error.status === 404) {
            setRevokeTarget(null)
            void list.refetch()
          } else setActionError(describeApiError(error, t('common.could_not_save_change')))
        }
      }
    )
  }
  function confirmWipe(): void {
    if (!wipeTarget) return
    setActionError(null)
    wipe.mutate(
      { id: wipeTarget.id, wipe: true },
      {
        onSuccess: () => setWipeTarget(null),
        onError: (error) => {
          if (error instanceof ApiError && error.status === 404) {
            setWipeTarget(null)
            void list.refetch()
          } else setActionError(describeApiError(error, t('common.could_not_save_change')))
        }
      }
    )
  }

  return (
    <div className={styles.root}>
      {list.isPending ? (
        <p className={settingsCardStyles.text}>{t('common.loading')}</p>
      ) : list.isError ? (
        <p className={styles.error}>{t('common.could_not_load_list')}</p>
      ) : (list.data ?? []).length === 0 ? (
        <p className={settingsCardStyles.text}>{t('app_password.no_app_passwords_issued_yet')}</p>
      ) : (
        <VirtualList
          className={styles.list}
          items={list.data ?? []}
          itemKey={(item) => item.id}
          estimateSize={96}
          itemProps={() => ({ className: styles.item })}
          renderItem={(item) => (
            <>
              <div className={styles.itemMain}>
                <strong className={styles.name}>{item.name}</strong>
                {item.read_only ? <span className={settingsCardStyles.badge}>{t('common.read_only')}</span> : null}
                <p className={styles.detail}>
                  {t('app_password.issued', { date: formatDateNs(item.created_ns) })} -{' '}
                  {item.last_used_ns
                    ? t('app_password.last_used', { date: formatDateNs(item.last_used_ns) })
                    : t('app_password.never_used')}
                  {isExpired(item)
                    ? ` - ${t('app_password.expired')}`
                    : item.expires_ns
                      ? ` - ${t('app_password.expires', { date: formatDateNs(item.expires_ns) })}`
                      : ''}
                </p>
              </div>
              <div className={styles.itemActions}>
                {!isExpired(item) ? (
                  <Button
                    variant="text"
                    ariaLabel={t('app_password.wipe', { name: item.name })}
                    onClick={() => {
                      setActionError(null)
                      setWipeTarget(item)
                    }}
                  >
                    {t('app_password.wipe_2')}
                  </Button>
                ) : null}
                <Button
                  variant="text"
                  ariaLabel={t('app_password.revoke', { name: item.name })}
                  onClick={() => {
                    setActionError(null)
                    setRevokeTarget(item)
                  }}
                >
                  {t('app_password.revoke_2')}
                </Button>
              </div>
            </>
          )}
        />
      )}
      <div className={styles.actions}>
        <Button variant="outlined" onClick={openCreate}>
          {t('app_password.new_app_password')}
        </Button>
      </div>
      <SettingsDialog
        open={createOpen}
        title={t('app_password.new_app_password')}
        onClose={() => setCreateOpen(false)}
        actions={
          <>
            <Button variant="text" onClick={() => setCreateOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmCreate} disabled={!newName.trim() || !newCurrent} loading={create.isPending}>
              {t('common.create')}
            </Button>
          </>
        }
      >
        {actionError ? (
          <p className={styles.error} role="alert">
            {actionError}
          </p>
        ) : null}
        <p className={settingsCardStyles.text}>{t('app_password.use_one_where_your_account')}</p>
        <TextField
          label={t('common.name')}
          placeholder={t('app_password.e_g_rclone_backup')}
          value={newName}
          onValueChange={setNewName}
        />
        <TextField
          type="password"
          label={t('common.current_password')}
          autoComplete="current-password"
          value={newCurrent}
          onValueChange={setNewCurrent}
        />
        <Switch checked={newReadOnly} label={t('app_password.read_only_download_only_no')} onChange={setNewReadOnly} />
        <p className={settingsCardStyles.text}>{t('app_password.read_only_recommended_anywhere_only')}</p>
      </SettingsDialog>
      <SettingsDialog
        open={!!issuedToken}
        title={t('app_password.app_password_issued')}
        onClose={closeIssued}
        dismissible={false}
        actions={<Button onClick={acknowledgeIssued}>{t('app_password.acknowledge_saved')}</Button>}
      >
        <p className={settingsCardStyles.text}>{t('app_password.once_you_close_cannot_shown')}</p>
        <div className={styles.tokenRow}>
          <input
            className={styles.tokenInput}
            readOnly
            value={issuedToken ?? ''}
            aria-label={t('app_password.app_password_issued')}
          />
          <Button variant="text" onClick={() => void copyToken()}>
            {tokenCopyState === 'copied' ? t('common.copied') : t('common.copy')}
          </Button>
        </div>
        {tokenCopyState === 'failed' ? (
          <p className={styles.copyFeedback} role="alert">
            {t('app_password.copy_failed')}
          </p>
        ) : null}
      </SettingsDialog>
      <SettingsDialog
        open={!!revokeTarget}
        title={t('app_password.revoke_app_password')}
        onClose={() => setRevokeTarget(null)}
        actions={
          <>
            <Button variant="text" onClick={() => setRevokeTarget(null)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmRevoke} loading={revoke.isPending}>
              {t('app_password.revoke_2')}
            </Button>
          </>
        }
      >
        <p className={settingsCardStyles.text}>
          {t('app_password.everything_using_disconnected_at_once', { name: revokeTarget?.name ?? '' })}
        </p>
        {actionError ? (
          <p className={styles.error} role="alert">
            {actionError}
          </p>
        ) : null}
      </SettingsDialog>
      <SettingsDialog
        open={!!wipeTarget}
        title={t('app_password.wipe_device')}
        onClose={() => setWipeTarget(null)}
        actions={
          <>
            <Button variant="text" onClick={() => setWipeTarget(null)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmWipe} loading={wipe.isPending}>
              {t('app_password.wipe_2')}
            </Button>
          </>
        }
      >
        <p className={settingsCardStyles.text}>
          {t('app_password.next_time_that_device_erase', { name: wipeTarget?.name ?? '' })}
        </p>
        {actionError ? (
          <p className={styles.error} role="alert">
            {actionError}
          </p>
        ) : null}
      </SettingsDialog>
    </div>
  )
}
