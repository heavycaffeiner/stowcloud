import { useSearch } from '@tanstack/react-router'
import { describeApiError } from '../../../api/error-text'
import { formatDateNs } from '../../../i18n'
import { useI18n } from '../../../hooks/use-i18n'
import { useOidcConfig, useSession } from '../../auth/api'
import { useOidcLinkStart, useOidcUnlink } from '../api'
import { StowBadge, StowButton } from '@/shared/ui'
import { askPassword } from './PasswordPrompt'
import * as settingsCardStyles from './SettingsCard.css'
import { ApiError } from '../../../api/fetcher'
import { oidcErrorMessage } from '../../auth/oidc-error'

export function OidcSection() {
  const { t } = useI18n()
  const session = useSession()
  const config = useOidcConfig()
  const link = useOidcLinkStart()
  const unlink = useOidcUnlink()
  const linked = session.data?.oidc.linked ?? false
  const configured = config.data?.enabled ?? false
  const providerLabel = config.data?.display_name || t('oidc.identity_provider')
  const smbDedicated = session.data?.user.smb_credential === 'dedicated'
  const flowError = oidcErrorMessage(
    useSearch({ from: '/_app/settings/{-$tab}', select: (search) => search.oidc_error })
  )

  function describe(value: unknown, fallback: string): string {
    if (value instanceof ApiError) {
      if (value.code === 'oidc.disabled') return t('oidc.single_sign_not_configured')
      if (value.code === 'oidc.provider_unavailable') return t('oidc.could_not_reach_identity_provider')
      if (value.code === 'oidc.not_linked') return t('oidc.account_has_no_connected_identity')
    }
    return describeApiError(value, fallback)
  }
  async function connect(): Promise<void> {
    const url = await askPassword({
      title: t('oidc.connect_provider', { provider: providerLabel }),
      action: t('common.continue'),
      body: (
        <>
          <p className={settingsCardStyles.text}>{t('oidc.after_you_confirm_password_taken')}</p>
          <p className={settingsCardStyles.warning}>{t('oidc.connecting_closes_smb_access_account')}</p>
        </>
      ),
      run: (password) => link.mutateAsync({ password, returnTo: window.location.pathname }),
      describeError: (error) => describe(error, t('oidc.could_not_start_connection_try'))
    })
    if (url !== null) window.location.href = url
  }
  function disconnect(): void {
    void askPassword({
      title: t('oidc.disconnect_single_sign'),
      action: t('oidc.disconnect'),
      body: (
        <>
          <p className={settingsCardStyles.text}>{t('oidc.you_sign_your_account_password')}</p>
          <p className={settingsCardStyles.warning}>{t('oidc.every_session_opened_through_signed')}</p>
          {smbDedicated ? <p className={settingsCardStyles.muted}>{t('smb.dedicated_will_be_replaced')}</p> : null}
        </>
      ),
      run: (password) => unlink.mutateAsync(password),
      describeError: (error) => describe(error, t('oidc.could_not_disconnect_try_again'))
    })
  }

  return (
    <div className={settingsCardStyles.section}>
      {flowError ? (
        <p className={settingsCardStyles.error} role="alert">
          {flowError}
        </p>
      ) : null}
      <div className={settingsCardStyles.cluster}>
        <StowBadge tone={linked ? 'accent' : 'neutral'}>
          {linked ? t('oidc.connected') : t('oidc.not_connected')}
        </StowBadge>
        {linked ? (
          <StowButton variant="outlined" onClick={disconnect}>
            {t('oidc.disconnect')}
          </StowButton>
        ) : configured ? (
          <StowButton onClick={() => void connect()}>
            {t('oidc.connect_provider', { provider: providerLabel })}
          </StowButton>
        ) : null}
      </div>
      {linked ? (
        <>
          <p className={settingsCardStyles.muted}>
            {session.data?.oidc.subject_hint ? t('oidc.identity', { subject: session.data.oidc.subject_hint }) : null}
            {session.data?.oidc.linked_ns
              ? ` ${t('oidc.connected_on', { date: formatDateNs(session.data.oidc.linked_ns) })}`
              : null}
          </p>
          {!configured ? (
            <p className={settingsCardStyles.muted}>{t('oidc.single_sign_currently_switched_off')}</p>
          ) : null}
        </>
      ) : configured ? (
        <p className={settingsCardStyles.muted}>
          {t('oidc.connect_sign_instead_your_account', { provider: providerLabel })}
        </p>
      ) : null}
    </div>
  )
}
