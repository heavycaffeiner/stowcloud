import { useState } from 'react'
import { useI18n } from '../../../hooks/use-i18n'
import { cx, StowButton } from '@/shared/ui'
import * as styles from './WebdavSection.css'
import * as settingsCardStyles from './SettingsCard.css'

export function WebdavSection() {
  const { t } = useI18n()

  const origin = window.location.origin
  const [announcement, setAnnouncement] = useState('')

  const baseUrl = origin ? `${origin}/dav` : ''
  const davUrl = baseUrl.replace(/^http/, 'dav')
  const netUseCommand = baseUrl ? `net use Z: ${baseUrl} /user:USERNAME` : ''
  const rcloneRemote = baseUrl
    ? [
        '[stowcloud]',
        'type = webdav',
        `url = ${baseUrl}/SHARE`,
        'vendor = other',
        'user = ACCOUNT',
        'pass = OBSCURED_APP_PASSWORD'
      ].join('\n')
    : ''
  const rcloneCrypt = [
    '[stowcloud-crypt]',
    'type = crypt',
    'remote = stowcloud:',
    'filename_encryption = off',
    'directory_name_encryption = false',
    'suffix = none',
    'password = OBSCURED_PASSPHRASE',
    'password2 = OBSCURED_SALT'
  ].join('\n')
  const rcloneMount = 'rclone mount stowcloud: /mnt/stowcloud'
  const rcloneMountCrypt = 'rclone mount stowcloud-crypt: /mnt/stowcloud'

  async function copy(text: string, name: string): Promise<void> {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      setAnnouncement(t('webdav.copied_to_clipboard', { name }))
    } catch {
      // The value remains selectable when clipboard access is unavailable.
    }
  }

  const token = (value: string, name: string, pre = false) => (
    <div className={styles.tokenRow}>
      {pre ? (
        <pre className={cx(styles.token, styles.tokenBlock)}>{value}</pre>
      ) : (
        <code
          className={styles.token}
          data-testid={name === t('webdav.server_address') ? 'webdav-base-url' : undefined}
        >
          {value}
        </code>
      )}
      <StowButton variant="text" aria-label={t('common.copy_named', { name })} onClick={() => void copy(value, name)}>
        {t('common.copy')}
      </StowButton>
    </div>
  )

  return (
    <div className={styles.root} data-testid="webdav-guide">
      <div>
        <span className={styles.label}>{t('webdav.server_address')}</span>
        {token(baseUrl, t('webdav.server_address'))}
      </div>
      <p className={styles.credentials}>{t('webdav.credentials_note')}</p>
      <section className={styles.os}>
        <h3 className={styles.heading}>{t('webdav.macos_heading')}</h3>
        <ol className={styles.steps}>
          <li>{t('webdav.macos_step_open_connect')}</li>
          <li>{t('webdav.macos_step_enter_url')}</li>
          <li>{t('webdav.macos_step_credentials')}</li>
        </ol>
      </section>
      <section className={styles.os}>
        <h3 className={styles.heading}>{t('webdav.windows_heading')}</h3>
        <ol className={styles.steps}>
          <li>{t('webdav.windows_step_open')}</li>
          <li>{t('webdav.windows_step_enter_url')}</li>
          <li>{t('webdav.windows_step_credentials')}</li>
        </ol>
        <p className={settingsCardStyles.text}>{t('webdav.windows_net_use_hint')}</p>
        {token(netUseCommand, t('webdav.net_use_command_label'))}
      </section>
      <section className={styles.os}>
        <h3 className={styles.heading}>{t('webdav.linux_heading')}</h3>
        <ol className={styles.steps}>
          <li>{t('webdav.linux_step_open')}</li>
          <li>{t('webdav.linux_step_enter_url')}</li>
        </ol>
        {token(davUrl, t('webdav.dav_url_label'))}
      </section>
      <section className={styles.os}>
        <h3 className={styles.heading}>{t('webdav.generic_heading')}</h3>
        <p className={settingsCardStyles.text}>{t('webdav.generic_hint')}</p>
      </section>
      <section className={styles.os}>
        <h3 className={styles.heading}>{t('webdav.rclone_heading')}</h3>
        <p className={settingsCardStyles.text}>{t('webdav.rclone_hint')}</p>
        {token(rcloneRemote, t('webdav.rclone_remote_label'), true)}
        <p className={settingsCardStyles.text}>{t('webdav.rclone_mount_hint')}</p>
        {token(rcloneMount, t('webdav.rclone_mount_label'))}
        <h4 className={styles.subheading}>{t('webdav.rclone_crypt_heading')}</h4>
        <p className={settingsCardStyles.text}>{t('webdav.rclone_crypt_hint')}</p>
        {token(rcloneCrypt, t('webdav.rclone_crypt_label'), true)}
        {token(rcloneMountCrypt, t('webdav.rclone_mount_crypt_label'))}
        <p className={settingsCardStyles.text}>{t('webdav.rclone_crypt_warning')}</p>
      </section>
      <p className={styles.nfcNote}>{t('webdav.nfc_note')}</p>
      <p className={styles.announce} aria-live="polite">
        {announcement}
      </p>
    </div>
  )
}
