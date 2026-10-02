import { useState } from 'react'
import { overlay } from 'overlay-kit'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { describeApiError } from '../../../api/error-text'
import { formatDateNs } from '../../../i18n'
import { useCopyText } from '../../../hooks/use-copy-text'
import { useI18n } from '../../../hooks/use-i18n'
import { useAppPasswords, useCreateAppPassword, useRevokeAppPassword } from '../api'
import { confirmAction, StowButton, StowFormTextField, StowSwitch, VirtualList } from '@/shared/ui'
import { SettingsDialog } from './SettingsDialog'
import * as styles from './AppPasswordsSection.css'
import * as settingsCardStyles from './SettingsCard.css'
import { ApiError } from '../../../api/fetcher'
import type { AppPasswordInfo } from '../api'

function isExpired(item: AppPasswordInfo): boolean {
  return item.expires_ns ? Number(item.expires_ns) / 1e6 <= Date.now() : false
}

export function AppPasswordsSection() {
  const { t } = useI18n()
  const list = useAppPasswords()
  const revoke = useRevokeAppPassword()
  // Keeps the row that opened a dialog mounted so focus can return to it.
  const [pinned, setPinned] = useState<number | null>(null)

  async function create(): Promise<void> {
    const token = await askNewAppPassword()
    if (token !== null) await showIssuedToken(token)
  }
  async function remove(item: AppPasswordInfo, wipe: boolean): Promise<void> {
    setPinned(item.id)
    await confirmAction({
      title: wipe ? t('app_password.wipe_device') : t('app_password.revoke_app_password'),
      action: wipe ? t('app_password.wipe_2') : t('app_password.revoke_2'),
      body: (
        <p className={settingsCardStyles.text}>
          {wipe
            ? t('app_password.next_time_that_device_erase', { name: item.name })
            : t('app_password.everything_using_disconnected_at_once', { name: item.name })}
        </p>
      ),
      // Someone else already removed it, which is what was asked for.
      run: () =>
        revoke.mutateAsync({ id: item.id, wipe }).catch((error: unknown) => {
          if (!(error instanceof ApiError && error.status === 404)) throw error
        }),
      describeError: (error) => describeApiError(error, t('common.could_not_save_change'))
    })
    setPinned(null)
  }

  return (
    <div className={styles.root}>
      {list.isPending ? (
        <p className={settingsCardStyles.text}>{t('common.loading')}</p>
      ) : list.isError ? (
        <p className={styles.error}>{t('common.could_not_load_list')}</p>
      ) : list.data.length === 0 ? (
        <p className={settingsCardStyles.text}>{t('app_password.no_app_passwords_issued_yet')}</p>
      ) : (
        <VirtualList
          className={styles.list}
          items={list.data}
          itemKey={(item) => item.id}
          pinnedKeys={pinned === null ? undefined : [pinned]}
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
                  <StowButton
                    variant="text"
                    aria-label={t('app_password.wipe', { name: item.name })}
                    onClick={() => void remove(item, true)}
                  >
                    {t('app_password.wipe_2')}
                  </StowButton>
                ) : null}
                <StowButton
                  variant="text"
                  aria-label={t('app_password.revoke', { name: item.name })}
                  onClick={() => void remove(item, false)}
                >
                  {t('app_password.revoke_2')}
                </StowButton>
              </div>
            </>
          )}
        />
      )}
      <div className={styles.actions}>
        <StowButton variant="outlined" onClick={() => void create()}>
          {t('app_password.new_app_password')}
        </StowButton>
      </div>
    </div>
  )
}

/** Resolves to the new token, or null when cancelled. */
function askNewAppPassword(): Promise<string | null> {
  return overlay.openAsync<string | null>(({ isOpen, close, unmount }) => (
    <NewAppPasswordDialog open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

interface NewAppPasswordDialogProps {
  open: boolean
  onDone: (token: string | null) => void
  onClosed: () => void
}

function NewAppPasswordDialog({ open, onDone, onClosed }: NewAppPasswordDialogProps) {
  const { t } = useI18n()
  const create = useCreateAppPassword()
  const { control, handleSubmit } = useForm({ defaultValues: { name: '', currentPassword: '', readOnly: false } })
  const [name, currentPassword] = useWatch({ control, name: ['name', 'currentPassword'] })
  const submit = handleSubmit((values) => {
    if (!values.name.trim() || !values.currentPassword || create.isPending) return
    create.mutate(
      {
        name: values.name.trim(),
        currentPassword: values.currentPassword,
        scope: values.readOnly ? { readOnly: true } : undefined
      },
      { onSuccess: onDone }
    )
  })
  const cancel = (): void => {
    if (!create.isPending) onDone(null)
  }
  return (
    <SettingsDialog
      open={open}
      title={t('app_password.new_app_password')}
      onClose={cancel}
      onClosed={onClosed}
      onSubmit={(event) => void submit(event)}
      actions={
        <>
          <StowButton variant="text" disabled={create.isPending} onClick={cancel}>
            {t('common.cancel')}
          </StowButton>
          <StowButton
            disabled={!name.trim() || !currentPassword}
            loading={create.isPending}
            onClick={() => void submit()}
          >
            {t('common.create')}
          </StowButton>
        </>
      }
    >
      {create.error ? (
        <p className={styles.error} role="alert">
          {create.error instanceof ApiError && create.error.code === 'auth.invalid_credentials'
            ? t('common.incorrect_password')
            : describeApiError(create.error, t('app_password.could_not_create_app_password'))}
        </p>
      ) : null}
      <p className={settingsCardStyles.text}>{t('app_password.use_one_where_your_account')}</p>
      <StowFormTextField
        control={control}
        name="name"
        label={t('common.name')}
        placeholder={t('app_password.e_g_rclone_backup')}
      />
      <StowFormTextField
        control={control}
        name="currentPassword"
        type="password"
        label={t('common.current_password')}
        autoComplete="current-password"
      />
      <Controller
        control={control}
        name="readOnly"
        render={({ field }) => (
          <StowSwitch
            checked={field.value}
            label={t('app_password.read_only_download_only_no')}
            onChange={field.onChange}
          />
        )}
      />
      <p className={settingsCardStyles.text}>{t('app_password.read_only_recommended_anywhere_only')}</p>
    </SettingsDialog>
  )
}

/** Shows a token once, until the user says it is saved. */
function showIssuedToken(token: string): Promise<void> {
  return overlay.openAsync<void>(({ isOpen, close, unmount }) => (
    <IssuedTokenDialog open={isOpen} token={token} onDone={() => close()} onClosed={unmount} />
  ))
}

interface IssuedTokenDialogProps {
  open: boolean
  token: string
  onDone: () => void
  onClosed: () => void
}

function IssuedTokenDialog({ open, token, onDone, onClosed }: IssuedTokenDialogProps) {
  const { t } = useI18n()
  const copy = useCopyText()
  return (
    <SettingsDialog
      open={open}
      title={t('app_password.app_password_issued')}
      onClosed={onClosed}
      dismissible={false}
      actions={<StowButton onClick={onDone}>{t('app_password.acknowledge_saved')}</StowButton>}
    >
      <p className={settingsCardStyles.text}>{t('app_password.once_you_close_cannot_shown')}</p>
      <div className={styles.tokenRow}>
        <input
          className={styles.tokenInput}
          readOnly
          value={token}
          aria-label={t('app_password.app_password_issued')}
        />
        <StowButton variant="text" onClick={() => copy.mutate(token)}>
          {copy.isSuccess ? t('common.copied') : t('common.copy')}
        </StowButton>
      </div>
      {copy.isError ? (
        <p className={styles.copyFeedback} role="alert">
          {t('app_password.copy_failed')}
        </p>
      ) : null}
    </SettingsDialog>
  )
}
