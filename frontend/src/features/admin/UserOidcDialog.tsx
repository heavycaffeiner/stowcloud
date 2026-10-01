import { useState } from 'react'
import { describeApiError } from '../../lib/api/error-text'
import { formatDateNs } from '../../lib/i18n'
import { useI18n } from '../../hooks/use-i18n'
import { useAdminUserOidc, useUnlinkUserOidc } from './api'
import { Button } from '../../ui/Button'
import { Dialog } from '../../ui/Dialog'
import { ProgressCircular } from '../../ui/ProgressCircular'
import * as styles from './UserOidcDialog.css'
import { ApiError } from '../../api/fetcher'
import type { AdminUser } from './api'

interface UserOidcDialogProps {
  user: AdminUser | null
  onClose: () => void
}

export function UserOidcDialog({ user, onClose }: UserOidcDialogProps) {
  const { t } = useI18n()
  const query = useAdminUserOidc(user?.id ?? null)
  const unlink = useUnlinkUserOidc()
  type OidcDialogState = { confirmUnlink: boolean; openFor: number | null }
  const [state, setState] = useState<OidcDialogState>({ confirmUnlink: false, openFor: user?.id ?? null })
  const { confirmUnlink } = state
  const setConfirmUnlink = (value: boolean): void => setState((current) => ({ ...current, confirmUnlink: value }))
  const openFor = user?.id ?? null
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
    if (user) unlink.mutate(user.id, { onSuccess: () => setConfirmUnlink(false) })
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
      open={!!user}
      title={user ? t('oidc.single_sign_for', { name: user.display_name || user.name }) : t('settings.single_sign_on')}
      onClose={onClose}
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
