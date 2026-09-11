import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { ApiError } from '../../api/client'
import { oidcErrorMessage } from '../../api/oidc'
import { describeApiError } from '../../api/error-text'
import { formatDateNs } from '../../i18n'
import { useI18n } from '../../i18n/use-i18n'
import { oidcLinkStartMutation, oidcUnlinkMutation } from '../../query/account'
import { oidcConfigQuery, sessionQuery } from '../../query/session'
import { Button } from '../Button'
import { TextField } from '../TextField'
import { SettingsDialog } from './SettingsDialog'

export function OidcSection() {
  const { t } = useI18n()
  const session = useQuery(sessionQuery())
  const config = useQuery(oidcConfigQuery())
  const link = useMutation(oidcLinkStartMutation())
  const unlink = useMutation(oidcUnlinkMutation())
  const [dialog, setDialog] = useState<'connect' | 'disconnect' | null>(null)
  const [connectPassword, setConnectPassword] = useState('')
  const [disconnectPassword, setDisconnectPassword] = useState('')
  const [connectError, setConnectError] = useState<string | null>(null)
  const [disconnectError, setDisconnectError] = useState<string | null>(null)
  const linked = session.data?.oidc.linked ?? false
  const configured = config.data?.enabled ?? false
  const providerLabel = config.data?.display_name || t('oidc.identity_provider')
  const smbDedicated = session.data?.user.smb_credential === 'dedicated'
  const flowError = oidcErrorMessage(new URLSearchParams(window.location.search).get('oidc_error'))

  function describe(value: unknown, fallback: string): string {
    if (value instanceof ApiError) {
      if (value.code === 'auth.invalid_credentials') return t('common.incorrect_password')
      if (value.code === 'oidc.disabled') return t('oidc.single_sign_not_configured')
      if (value.code === 'oidc.provider_unavailable') return t('oidc.could_not_reach_identity_provider')
      if (value.code === 'oidc.not_linked') return t('oidc.account_has_no_connected_identity')
    }
    return describeApiError(value, fallback)
  }
  function openConnect(): void {
    setConnectPassword('')
    setConnectError(null)
    link.reset()
    setDialog('connect')
  }
  function confirmConnect(): void {
    link.mutate({ password: connectPassword, returnTo: window.location.pathname }, {
      onSuccess: (result) => { window.location.href = result.authorize_url },
      onError: (value) => setConnectError(describe(value, t('oidc.could_not_start_connection_try')))
    })
  }
  function openDisconnect(): void {
    setDisconnectPassword('')
    setDisconnectError(null)
    unlink.reset()
    setDialog('disconnect')
  }
  function confirmDisconnect(): void {
    unlink.mutate(disconnectPassword, {
      onSuccess: () => { setDialog(null); void session.refetch() },
      onError: (value) => setDisconnectError(describe(value, t('oidc.could_not_disconnect_try_again')))
    })
  }

  return (
    <div className="sc-oidc">
      {flowError ? <p className="sc-oidc__error" role="alert">{flowError}</p> : null}
      <div className="sc-oidc__status">
        <span className={linked ? 'sc-oidc__badge sc-oidc__badge--on' : 'sc-oidc__badge'}>{linked ? t('oidc.connected') : t('oidc.not_connected')}</span>
        {linked ? <Button variant="outlined" onClick={openDisconnect}>{t('oidc.disconnect')}</Button> : configured ? <Button onClick={openConnect}>{t('oidc.connect_provider', { provider: providerLabel })}</Button> : null}
      </div>
      {linked ? (
        <>
          <p className="sc-oidc__detail">{session.data?.oidc.subject_hint ? t('oidc.identity', { subject: session.data.oidc.subject_hint }) : null}{session.data?.oidc.linked_ns ? ` ${t('oidc.connected_on', { date: formatDateNs(session.data.oidc.linked_ns) })}` : null}</p>
          {!configured ? <p className="sc-oidc__detail">{t('oidc.single_sign_currently_switched_off')}</p> : null}
        </>
      ) : configured ? <p className="sc-oidc__detail">{t('oidc.connect_sign_instead_your_account', { provider: providerLabel })}</p> : null}
      <SettingsDialog open={dialog === 'connect'} title={t('oidc.connect_provider', { provider: providerLabel })} onClose={() => { if (!link.isPending) setDialog(null) }} actions={<><Button variant="text" onClick={() => setDialog(null)} disabled={link.isPending}>{t('common.cancel')}</Button><Button onClick={confirmConnect} disabled={!connectPassword} loading={link.isPending}>{t('common.continue')}</Button></>}>
        <p>{t('oidc.after_you_confirm_password_taken')}</p><p className="sc-oidc__warning">{t('oidc.connecting_closes_smb_access_account')}</p><TextField type="password" label={t('common.current_password')} value={connectPassword} error={connectError} autoComplete="current-password" onValueChange={setConnectPassword} />
      </SettingsDialog>
      <SettingsDialog open={dialog === 'disconnect'} title={t('oidc.disconnect_single_sign')} onClose={() => { if (!unlink.isPending) setDialog(null) }} actions={<><Button variant="text" onClick={() => setDialog(null)} disabled={unlink.isPending}>{t('common.cancel')}</Button><Button onClick={confirmDisconnect} disabled={!disconnectPassword} loading={unlink.isPending}>{t('oidc.disconnect')}</Button></>}>
        <p>{t('oidc.you_sign_your_account_password')}</p><p className="sc-oidc__warning">{t('oidc.every_session_opened_through_signed')}</p>{smbDedicated ? <p className="sc-oidc__detail">{t('smb.dedicated_will_be_replaced')}</p> : null}<TextField type="password" label={t('common.current_password')} value={disconnectPassword} error={disconnectError} autoComplete="current-password" onValueChange={setDisconnectPassword} />
      </SettingsDialog>
    </div>
  )
}
