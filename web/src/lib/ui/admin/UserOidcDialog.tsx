import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { ApiError, type AdminUser } from '../../api/client'
import { describeApiError } from '../../api/error-text'
import { formatDateNs, t } from '../../i18n'
import { adminUnlinkOidcMutation, adminUserOidcQuery } from '../../query/admin'
import { Button } from '../Button'
import { Dialog } from '../Dialog'
import { ProgressCircular } from '../ProgressCircular'
import './admin.css'

interface UserOidcDialogProps { user: AdminUser | null; onClose: () => void }

export function UserOidcDialog({ user, onClose }: UserOidcDialogProps) {
  const query = useQuery(adminUserOidcQuery(user?.id ?? null))
  const unlink = useMutation(adminUnlinkOidcMutation())
  const [confirmUnlink, setConfirmUnlink] = useState(false)
  const [openFor, setOpenFor] = useState<number | null>(null)
  useEffect(() => {
    const id = user?.id ?? null
    if (id === openFor) return
    setOpenFor(id)
    setConfirmUnlink(false)
    unlink.reset()
  }, [openFor, unlink, user?.id])
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
  const actions = link?.linked && confirmUnlink ? <><Button variant="text" onClick={() => setConfirmUnlink(false)} disabled={unlink.isPending}>{t('common.cancel')}</Button><Button danger variant="filled" loading={unlink.isPending} onClick={submitUnlink}>{t('oidc.disconnect_anyway')}</Button></> : <><Button variant="text" onClick={onClose}>{t('common.close')}</Button>{link?.linked ? <Button danger variant="outlined" onClick={() => setConfirmUnlink(true)}>{t('oidc.disconnect')}</Button> : null}</>
  return (
    <Dialog open={!!user} title={user ? t('oidc.single_sign_for', { name: user.display_name || user.name }) : t('settings.single_sign_on')} onClose={onClose} actions={actions}>
      {query.isPending ? <ProgressCircular /> : loadError ? <p className="sc-user-oidc__error" role="alert">{loadError}</p> : link?.linked ? <><dl className="sc-user-oidc__facts"><div><dt>{t('oidc.provider')}</dt><dd>{link.issuer}</dd></div><div><dt>{t('oidc.subject_sub')}</dt><dd><code>{link.subject}</code></dd></div><div><dt>{t('oidc.linked_on')}</dt><dd>{link.linked_ns ? formatDateNs(link.linked_ns) : '-'}</dd></div><div><dt>{t('oidc.last_sign')}</dt><dd>{link.last_login_ns ? formatDateNs(link.last_login_ns) : t('oidc.never')}</dd></div></dl>{confirmUnlink ? <><p className="sc-user-oidc__warning">{t('oidc.disconnecting_here_cannot_restore_smb')}</p>{unlink.error ? <p className="sc-user-oidc__error" role="alert">{describeError(unlink.error, t('oidc.could_not_disconnect_try_again'))}</p> : null}</> : <p className="sc-user-oidc__hint">{t('oidc.disconnecting_signs_account_out_every')}</p>}</> : <>{unlink.isSuccess ? <p className="sc-user-oidc__warning">{t('oidc.disconnected_smb_access_account_will')}</p> : null}<p className="sc-user-oidc__hint">{t('oidc.no_identity_connected_user_must_link')}</p></>}
    </Dialog>
  )
}
