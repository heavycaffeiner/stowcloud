import { useState } from 'react'
import { overlay } from 'overlay-kit'
import { useForm, useWatch } from 'react-hook-form'
import { describeApiError } from '../../../api/error-text'
import { t } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { useSession } from '../../auth/api'
import { useClearSmbPassword, useSetSmbPassword, useSmbSettings, useSmbConnections } from '../api'
import { Icon, StowBadge, StowButton, StowFormTextField, StowSwitch, StowTextField } from '@/shared/ui'
import { useCopyText } from '../../../hooks/use-copy-text'
import { askPassword } from './PasswordPrompt'
import { SettingsDialog } from './SettingsDialog'
import * as styles from './SmbSection.css'
import * as settingsCardStyles from './SettingsCard.css'
import { ApiError } from '../../../api/fetcher'

function smbErrorText(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.code === 'auth.invalid_credentials') return t('common.incorrect_password')
  if (error instanceof ApiError && error.code === 'auth.weak_password')
    return t('smb.password_too_short', { min: error.reasonNumber('min_length') ?? 10 })
  if (error instanceof ApiError && error.status === 404) return t('smb.no_separate_password_set')
  return describeApiError(error, fallback)
}

export function SmbSection() {
  const { t } = useI18n()
  const session = useSession()
  const settings = useSmbSettings()
  const clearPassword = useClearSmbPassword()
  const [announcement, setAnnouncement] = useState('')
  const user = session.data?.user
  const connections = useSmbConnections(session.data)
  const [serverAddress, setServerAddress] = useState<string | null>(null)
  const server = (serverAddress ?? connections.data?.server ?? window.location.hostname).trim()
  const credential = user?.smb_credential ?? 'none'
  const optOut = user?.smb_opt_out ?? false
  const enabled = user?.smb_enabled ?? false
  const totpEnabled = user?.totp_enabled ?? false
  const oidcLinked = session.data?.oidc.linked ?? false

  const stateLine =
    credential === 'account'
      ? t('smb.uses_account_password')
      : credential === 'dedicated'
        ? t('smb.uses_separate_password')
        : user?.smb_unavailable_reason === 'totp_blocked'
          ? t('smb.unavailable_totp_blocked')
          : optOut
            ? t('smb.unavailable_opted_out')
            : !enabled
              ? t('smb.unavailable_not_published')
              : totpEnabled || oidcLinked
                ? t('smb.unavailable_not_set')
                : t('smb.unavailable_awaiting_sign_in')
  const revertsToAccount = !totpEnabled && !oidcLinked && !optOut && enabled

  function toggle(nextOptOut: boolean, nextEnabled: boolean): void {
    const confirmingOptOut = nextOptOut && !optOut
    void askPassword({
      title: confirmingOptOut ? t('smb.confirm_opt_out_title') : t('smb.confirm_setting_title'),
      action: confirmingOptOut ? t('smb.confirm_opt_out') : t('common.save'),
      body: (
        <p className={settingsCardStyles.text}>
          {confirmingOptOut ? t('smb.confirm_opt_out_warning') : t('smb.confirm_setting_hint')}
        </p>
      ),
      run: (currentPassword) => settings.mutateAsync({ currentPassword, enabled: nextEnabled, optOut: nextOptOut }),
      describeError: (error) => smbErrorText(error, t('smb.could_not_save_setting_try'))
    })
  }
  async function setSeparate(): Promise<void> {
    // The server turns the account's own switches back on with the password.
    const togglesCleared = optOut || !enabled
    if (!(await askSmbPassword())) return
    setAnnouncement(togglesCleared ? `${t('smb.password_set')} ${t('smb.toggles_cleared')}` : t('smb.password_set'))
  }
  async function clearSeparate(): Promise<void> {
    const revertible = await askPassword({
      title: t('smb.remove_password_title'),
      action: t('common.remove_2'),
      body: (
        <p className={settingsCardStyles.text}>
          {revertsToAccount ? t('smb.remove_reverts_to_account') : t('smb.remove_ends_access')}
        </p>
      ),
      run: (currentPassword) => clearPassword.mutateAsync(currentPassword),
      describeError: (error) => smbErrorText(error, t('smb.could_not_save_password'))
    })
    if (revertible === null) return
    setAnnouncement(revertible ? t('smb.password_removed_reverted') : t('smb.password_removed_no_access'))
  }

  return (
    <div className={styles.root}>
      <p className={styles.note}>{t('smb.smb_reachable_only_from_local')}</p>
      <p className={styles.state}>
        {t('smb.authentication')}: {stateLine}
      </p>
      <StowTextField
        label={t('smb.server_address')}
        value={serverAddress ?? connections.data?.server ?? window.location.hostname}
        helper={t('smb.server_address_hint')}
        onValueChange={setServerAddress}
        autoComplete="off"
      />
      <div className={styles.actions}>
        <StowButton
          variant="text"
          icon={<Icon name="refresh" />}
          loading={connections.isFetching}
          onClick={() => void connections.refetch()}
        >
          {t('common.refresh')}
        </StowButton>
      </div>
      {connections.error ? (
        <p role="alert" className={settingsCardStyles.warning}>
          {describeApiError(connections.error, t('smb.could_not_load_connections'))}
        </p>
      ) : null}
      {connections.data?.folders.map((folder) => (
        <div key={`${folder.personal}:${folder.label}`} className={styles.folder}>
          <div className={styles.folderHeading}>
            <strong>{folder.label}</strong>
            {folder.personal ? <StowBadge>{t('smb.personal_folder')}</StowBadge> : null}
            <StowBadge tone={folder.available ? undefined : 'warning'}>
              {folder.available ? t('smb.folder_available') : folderReason(folder.reason)}
            </StowBadge>
          </div>
          {folder.personal ? <p className={styles.note}>{t('smb.home_private_hint')}</p> : null}
          {folder.share && server ? (
            <>
              <ConnectionAddress key={`windows:${server}`} label="Windows" address={`\\\\${server}\\${folder.share}`} />
              <ConnectionAddress
                key={`smb:${server}`}
                label="macOS / Linux"
                address={`smb://${server}/${encodeURIComponent(folder.share)}`}
              />
            </>
          ) : null}
        </div>
      ))}
      {connections.data?.folders.length === 0 ? <p className={styles.note}>{t('smb.no_folders')}</p> : null}
      <div>
        <StowSwitch
          checked={enabled}
          label={t('smb.allow_smb_access')}
          onChange={(checked) => toggle(optOut, checked)}
        />
      </div>
      <div>
        <StowSwitch
          checked={optOut}
          label={t('smb.do_not_store_smb_credentials')}
          onChange={(checked) => toggle(checked, checked ? false : enabled)}
        />
      </div>
      <div className={styles.actions}>
        <StowButton variant={credential === 'dedicated' ? 'outlined' : 'filled'} onClick={() => void setSeparate()}>
          {credential === 'dedicated' ? t('smb.change_separate_password') : t('smb.set_separate_password')}
        </StowButton>
        {credential === 'dedicated' ? (
          <StowButton variant="text" onClick={() => void clearSeparate()}>
            {t('smb.remove_separate_password')}
          </StowButton>
        ) : null}
      </div>
      <p className={styles.announce} aria-live="polite">
        {announcement}
      </p>
    </div>
  )
}

function folderReason(reason: string | undefined): string {
  const labels: Record<string, string> = {
    server_disabled: t('smb.folder_server_disabled'),
    server_unavailable: t('smb.folder_server_unavailable'),
    account_unavailable: t('smb.folder_account_unavailable'),
    credential_not_applied: t('smb.folder_credential_not_applied'),
    folder_unavailable: t('smb.folder_unavailable'),
    home_not_ready: t('smb.folder_home_not_ready'),
    subpath_permissions: t('smb.folder_subpath_permissions'),
    not_supported: t('smb.folder_not_supported'),
    not_applied: t('smb.folder_not_applied'),
    path_not_mounted: t('smb.folder_path_not_mounted')
  }
  return labels[reason ?? ''] ?? t('smb.folder_unavailable')
}

function ConnectionAddress({ label, address }: { label: string; address: string }) {
  const { t } = useI18n()
  const copy = useCopyText()
  return (
    <div className={styles.address}>
      <span>{label}</span>
      <code className={styles.addressValue}>{address}</code>
      <StowButton variant="text" icon={<Icon name="copy" size={18} />} onClick={() => copy.mutate(address)}>
        {copy.isSuccess ? t('common.copied') : t('common.copy')}
      </StowButton>
      {copy.error ? <span role="alert">{t('common.could_not_copy')}</span> : null}
    </div>
  )
}

/** Resolves true once a separate SMB password is stored, false when cancelled. */
function askSmbPassword(): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <SmbPasswordDialog open={isOpen} onDone={close} onClosed={unmount} />
  ))
}

interface SmbPasswordDialogProps {
  open: boolean
  onDone: (saved: boolean) => void
  onClosed: () => void
}

function SmbPasswordDialog({ open, onDone, onClosed }: SmbPasswordDialogProps) {
  const { t } = useI18n()
  const save = useSetSmbPassword()
  const { control, handleSubmit } = useForm({ defaultValues: { currentPassword: '', smbPassword: '' } })
  const [currentPassword, smbPassword] = useWatch({ control, name: ['currentPassword', 'smbPassword'] })
  const submit = handleSubmit((values) => {
    if (values.currentPassword && values.smbPassword && !save.isPending)
      save.mutate(values, { onSuccess: () => onDone(true) })
  })
  const cancel = (): void => {
    if (!save.isPending) onDone(false)
  }
  return (
    <SettingsDialog
      open={open}
      title={t('smb.set_password_title')}
      onClose={cancel}
      onClosed={onClosed}
      onSubmit={(event) => void submit(event)}
      actions={
        <>
          <StowButton variant="text" disabled={save.isPending} onClick={cancel}>
            {t('common.cancel')}
          </StowButton>
          <StowButton
            disabled={!currentPassword || !smbPassword}
            loading={save.isPending}
            onClick={() => void submit()}
          >
            {t('common.save')}
          </StowButton>
        </>
      }
    >
      <p className={settingsCardStyles.text}>{t('smb.set_password_hint')}</p>
      <StowFormTextField
        control={control}
        name="currentPassword"
        type="password"
        label={t('common.current_password')}
        autoComplete="current-password"
      />
      <StowFormTextField
        control={control}
        name="smbPassword"
        type="password"
        label={t('smb.new_smb_password')}
        autoComplete="new-password"
        error={save.error ? smbErrorText(save.error, t('smb.could_not_save_password')) : null}
      />
    </SettingsDialog>
  )
}
