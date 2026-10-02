import { useState } from 'react'
import { overlay } from 'overlay-kit'
import { describeApiError } from '../../../api/error-text'
import { formatDateNs } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { useAdminUserOidc, useUnlinkUserOidc } from '../api'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { ProgressCircular } from '../../../ui/ProgressCircular'
import * as styles from './UserOidcDialog.css'
import { ApiError } from '../../../api/fetcher'
import type { AdminUser } from '../api'

/** Shows a user's single sign-on connection. Settles once it has closed. */
export function showUserOidc(user: AdminUser): Promise<void> {
  return overlay.openAsync<void>(({ isOpen, close, unmount }) => (
    <UserOidcDialog user={user} open={isOpen} onClose={() => close()} onClosed={unmount} />
  ))
}

interface UserOidcDialogProps {
  user: AdminUser
  open: boolean
  onClose: () => void
  onClosed: () => void
}

function UserOidcDialog({ user, open, onClose, onClosed }: UserOidcDialogProps) {
  const { t } = useI18n()
  const query = useAdminUserOidc(user.id)
  const unlink = useUnlinkUserOidc()
  const [confirmUnlink, setConfirmUnlink] = useState(false)
  function describeError(error: unknown, fallback: string): string {
    if (error instanceof ApiError) {
      if (error.code === 'oidc.disabled') return t('oidc.single_sign_not_configured')
      if (error.code === 'oidc.invalid_subject') return t('oidc.enter_identifier')
      if (error.code === 'oidc.subject_already_linked') return t('oidc.identity_already_connected_another_account')
      if (error.code === 'oidc.not_linked') return t('oidc.account_has_no_connected_identity')
    }
    return describeApiError(error, fallback)
  }
  function submitUnlink(): void {
    unlink.mutate(user.id, { onSuccess: () => setConfirmUnlink(false) })
  }
  const link = query.data
  const loadError = query.error ? describeError(query.error, t('oidc.could_not_load_connection_status')) : null
  const actions =
    link?.linked && confirmUnlink ? (
      <>
        <Button variant="text" onClick={() => setConfirmUnlink(false)} disabled={unlink.isPending}>
          {t('common.cancel')}
        </Button>
        <Button danger variant="filled" loading={unlink.isPending} onClick={submitUnlink}>
          {t('oidc.disconnect_anyway')}
        </Button>
      </>
    ) : (
      <>
        <Button variant="text" onClick={onClose}>
          {t('common.close')}
        </Button>
        {link?.linked ? (
          <Button danger variant="outlined" onClick={() => setConfirmUnlink(true)}>
            {t('oidc.disconnect')}
          </Button>
        ) : null}
      </>
    )
  return (
    <Dialog
      open={open}
      title={t('oidc.single_sign_for', { name: user.display_name || user.name })}
      onClose={onClose}
      onClosed={onClosed}
      actions={actions}
    >
      {query.isPending ? (
        <ProgressCircular />
      ) : loadError ? (
        <p className={styles.error} role="alert">
          {loadError}
        </p>
      ) : link?.linked ? (
        <>
          <dl className={styles.facts}>
            <div>
              <dt className={styles.factLabel}>{t('oidc.provider')}</dt>
              <dd className={styles.factValue}>{link.issuer}</dd>
            </div>
            <div>
              <dt className={styles.factLabel}>{t('oidc.subject_sub')}</dt>
              <dd className={styles.factValue}>
                <code>{link.subject}</code>
              </dd>
            </div>
            <div>
              <dt className={styles.factLabel}>{t('oidc.linked_on')}</dt>
              <dd className={styles.factValue}>{link.linked_ns ? formatDateNs(link.linked_ns) : '-'}</dd>
            </div>
            <div>
              <dt className={styles.factLabel}>{t('oidc.last_sign')}</dt>
              <dd className={styles.factValue}>
                {link.last_login_ns ? formatDateNs(link.last_login_ns) : t('oidc.never')}
              </dd>
            </div>
          </dl>
          {confirmUnlink ? (
            <>
              <p className={styles.warning}>{t('oidc.disconnecting_here_cannot_restore_smb')}</p>
              {unlink.error ? (
                <p className={styles.error} role="alert">
                  {describeError(unlink.error, t('oidc.could_not_disconnect_try_again'))}
                </p>
              ) : null}
            </>
          ) : (
            <p className={styles.hint}>{t('oidc.disconnecting_signs_account_out_every')}</p>
          )}
        </>
      ) : (
        <>
          {unlink.isSuccess ? <p className={styles.warning}>{t('oidc.disconnected_smb_access_account_will')}</p> : null}
          <p className={styles.hint}>{t('oidc.no_identity_connected_user_must_link')}</p>
        </>
      )}
    </Dialog>
  )
}
