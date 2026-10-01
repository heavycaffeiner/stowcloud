import { useState } from 'react'
import { describeApiError } from '../../lib/api/error-text'
import { formatDateNs } from '../../lib/i18n'
import { useI18n } from '../../hooks/use-i18n'
import { useOidcConfig, useSession } from '../auth/api'
import { useOidcLinkStart, useOidcUnlink } from './api'
import { Button } from '../../ui/Button'
import { TextField } from '../../ui/TextField'
import { SettingsDialog } from './SettingsDialog'
import * as styles from './OidcSection.css'
import * as settingsCardStyles from './SettingsCard.css'
import { cx } from '../../ui/cx'
import { ApiError } from '../../api/fetcher'
import { oidcErrorMessage } from '../auth/oidc-error'

export function OidcSection() {
  const { t } = useI18n()
  const session = useSession()
  const config = useOidcConfig()
  const link = useOidcLinkStart()
  const unlink = useOidcUnlink()
  type OidcState = {
    dialog: 'connect' | 'disconnect' | null
    connectPassword: string
    disconnectPassword: string
    connectError: string | null
    disconnectError: string | null
  }
  const [state, setState] = useState<OidcState>({
    dialog: null,
    connectPassword: '',
    disconnectPassword: '',
    connectError: null,
    disconnectError: null
  })
  const { dialog, connectPassword, disconnectPassword, connectError, disconnectError } = state
  const patchState = (patch: Partial<OidcState>): void => setState((current) => ({ ...current, ...patch }))
  const setDialog = (value: OidcState['dialog']): void => patchState({ dialog: value })
  const setConnectPassword = (value: string): void => patchState({ connectPassword: value })
  const setDisconnectPassword = (value: string): void => patchState({ disconnectPassword: value })
  const setConnectError = (value: string | null): void => patchState({ connectError: value })
  const setDisconnectError = (value: string | null): void => patchState({ disconnectError: value })
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
    link.mutate(
      { password: connectPassword, returnTo: window.location.pathname },
      {
        onSuccess: (url) => {
          window.location.href = url
        },
        onError: (value) => setConnectError(describe(value, t('oidc.could_not_start_connection_try')))
      }
    )
  }
  function openDisconnect(): void {
    setDisconnectPassword('')
    setDisconnectError(null)
    unlink.reset()
    setDialog('disconnect')
  }
  function confirmDisconnect(): void {
    unlink.mutate(disconnectPassword, {
      onSuccess: () => {
        setDialog(null)
        void session.refetch()
      },
      onError: (value) => setDisconnectError(describe(value, t('oidc.could_not_disconnect_try_again')))
    })
  }

  return (
    <div className={styles.root}>
      {flowError ? (
        <p className={styles.error} role="alert">
          {flowError}
        </p>
      ) : null}
      <div className={styles.status}>
        <span className={cx(styles.badge, linked && styles.badgeOn)}>
          {linked ? t('oidc.connected') : t('oidc.not_connected')}
        </span>
        {linked ? (
          <Button variant="outlined" onClick={openDisconnect}>
            {t('oidc.disconnect')}
          </Button>
        ) : configured ? (
          <Button onClick={openConnect}>{t('oidc.connect_provider', { provider: providerLabel })}</Button>
        ) : null}
      </div>
      {linked ? (
        <>
          <p className={styles.detail}>
            {session.data?.oidc.subject_hint ? t('oidc.identity', { subject: session.data.oidc.subject_hint }) : null}
            {session.data?.oidc.linked_ns
              ? ` ${t('oidc.connected_on', { date: formatDateNs(session.data.oidc.linked_ns) })}`
              : null}
          </p>
          {!configured ? <p className={styles.detail}>{t('oidc.single_sign_currently_switched_off')}</p> : null}
        </>
      ) : configured ? (
        <p className={styles.detail}>{t('oidc.connect_sign_instead_your_account', { provider: providerLabel })}</p>
      ) : null}
      <SettingsDialog
        open={dialog === 'connect'}
        title={t('oidc.connect_provider', { provider: providerLabel })}
        onClose={() => {
          if (!link.isPending) {
            setConnectPassword('')
            setConnectError(null)
            setDialog(null)
          }
        }}
        actions={
          <>
            <Button
              variant="text"
              onClick={() => {
                setConnectPassword('')
                setConnectError(null)
                setDialog(null)
              }}
              disabled={link.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmConnect} disabled={!connectPassword} loading={link.isPending}>
              {t('common.continue')}
            </Button>
          </>
        }
      >
        <p className={settingsCardStyles.text}>{t('oidc.after_you_confirm_password_taken')}</p>
        <p className={styles.warning}>{t('oidc.connecting_closes_smb_access_account')}</p>
        <TextField
          type="password"
          label={t('common.current_password')}
          value={connectPassword}
          error={connectError}
          autoComplete="current-password"
          onValueChange={setConnectPassword}
        />
      </SettingsDialog>
      <SettingsDialog
        open={dialog === 'disconnect'}
        title={t('oidc.disconnect_single_sign')}
        onClose={() => {
          if (!unlink.isPending) {
            setDisconnectPassword('')
            setDisconnectError(null)
            setDialog(null)
          }
        }}
        actions={
          <>
            <Button
              variant="text"
              onClick={() => {
                setDisconnectPassword('')
                setDisconnectError(null)
                setDialog(null)
              }}
              disabled={unlink.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button onClick={confirmDisconnect} disabled={!disconnectPassword} loading={unlink.isPending}>
              {t('oidc.disconnect')}
            </Button>
          </>
        }
      >
        <p className={settingsCardStyles.text}>{t('oidc.you_sign_your_account_password')}</p>
        <p className={styles.warning}>{t('oidc.every_session_opened_through_signed')}</p>
        {smbDedicated ? <p className={styles.detail}>{t('smb.dedicated_will_be_replaced')}</p> : null}
        <TextField
          type="password"
          label={t('common.current_password')}
          value={disconnectPassword}
          error={disconnectError}
          autoComplete="current-password"
          onValueChange={setDisconnectPassword}
        />
      </SettingsDialog>
    </div>
  )
}
