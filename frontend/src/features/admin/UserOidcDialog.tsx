import { useComponentState } from '../../lib/store/use-component-state'
import { useMutation, useQuery } from '@tanstack/react-query'
import { ApiError, type AdminUser } from '../../lib/api/client'
import { describeApiError } from '../../lib/api/error-text'
import { formatDateNs, t } from '../../lib/i18n'
import { adminUnlinkOidcMutation, adminUserOidcQuery } from '../../lib/query/admin'
import { Button } from '../../lib/ui/Button'
import { Dialog } from '../../lib/ui/Dialog'
import { ProgressCircular } from '../../lib/ui/ProgressCircular'
import './admin.css'

interface UserOidcDialogProps { user: AdminUser | null; onClose: () => void }

export function UserOidcDialog({ user, onClose }: UserOidcDialogProps) {
  const query = useQuery(adminUserOidcQuery(user?.id ?? null))
  const unlink = useMutation(adminUnlinkOidcMutation())
  type OidcDialogState = { confirmUnlink: boolean; openFor: number | null }
  const [state, setState] = useComponentState<OidcDialogState>({ confirmUnlink: false, openFor: user?.id ?? null })
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
  const actions = link?.linked && confirmUnlink ? <><Button variant="text" onClick={() => setConfirmUnlink(false)} disabled={unlink.isPending}>{t('common.cancel')}</Button><Button danger variant="filled" loading={unlink.isPending} onClick={submitUnlink}>{t('oidc.disconnect_anyway')}</Button></> : <><Button variant="text" onClick={onClose}>{t('common.close')}</Button>{link?.linked ? <Button danger variant="outlined" onClick={() => setConfirmUnlink(true)}>{t('oidc.disconnect')}</Button> : null}</>
  return (
    <Dialog open={!!user} title={user ? t('oidc.single_sign_for', { name: user.display_name || user.name }) : t('settings.single_sign_on')} onClose={onClose} actions={actions}>
      {query.isPending ? <ProgressCircular /> : loadError ? <p className="sc-user-oidc__error" role="alert">{loadError}</p> : link?.linked ? <><dl className="sc-user-oidc__facts"><div><dt>{t('oidc.provider')}</dt><dd>{link.issuer}</dd></div><div><dt>{t('oidc.subject_sub')}</dt><dd><code>{link.subject}</code></dd></div><div><dt>{t('oidc.linked_on')}</dt><dd>{link.linked_ns ? formatDateNs(link.linked_ns) : '-'}</dd></div><div><dt>{t('oidc.last_sign')}</dt><dd>{link.last_login_ns ? formatDateNs(link.last_login_ns) : t('oidc.never')}</dd></div></dl>{confirmUnlink ? <><p className="sc-user-oidc__warning">{t('oidc.disconnecting_here_cannot_restore_smb')}</p>{unlink.error ? <p className="sc-user-oidc__error" role="alert">{describeError(unlink.error, t('oidc.could_not_disconnect_try_again'))}</p> : null}</> : <p className="sc-user-oidc__hint">{t('oidc.disconnecting_signs_account_out_every')}</p>}</> : <>{unlink.isSuccess ? <p className="sc-user-oidc__warning">{t('oidc.disconnected_smb_access_account_will')}</p> : null}<p className="sc-user-oidc__hint">{t('oidc.no_identity_connected_user_must_link')}</p></>}
    </Dialog>
  )
}
