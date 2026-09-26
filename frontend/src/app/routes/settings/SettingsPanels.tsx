import { lazy, Suspense } from 'react'
import type { SessionInfo } from '../../../lib/api/types'
import type { Locale } from '../../../lib/i18n/state'
import type { ThemePref } from '../../../lib/store/ui.store'
import { SettingsCard } from '../../../features/settings/SettingsCard'
import { Button } from '../../../lib/ui/Button'
import { Icon } from '../../../lib/ui/Icon'
const PasswordSection = lazy(() => import('../../../features/settings/PasswordSection').then((m) => ({ default: m.PasswordSection })))
const TotpSection = lazy(() => import('../../../features/settings/TotpSection').then((m) => ({ default: m.TotpSection })))
const AppPasswordsSection = lazy(() => import('../../../features/settings/AppPasswordsSection').then((m) => ({ default: m.AppPasswordsSection })))
const SessionsSection = lazy(() => import('../../../features/settings/SessionsSection').then((m) => ({ default: m.SessionsSection })))
const SmbSection = lazy(() => import('../../../features/settings/SmbSection').then((m) => ({ default: m.SmbSection })))
const WebdavSection = lazy(() => import('../../../features/settings/WebdavSection').then((m) => ({ default: m.WebdavSection })))
const OidcSection = lazy(() => import('../../../features/settings/OidcSection').then((m) => ({ default: m.OidcSection })))

type AccountPanelProps = {
  session: SessionInfo | undefined
  signOutPending: boolean
  onSignOut: () => void
  t: (key: string) => string
}

export function AccountPanel({ session, signOutPending, onSignOut, t }: AccountPanelProps) {
  return (
    <div className="sc-settings-page-grid">
      <SettingsCard
        leading={<div className="sc-settings-avatar">{((session?.user.display_name || session?.user.name || 'U')[0] ?? 'U').toUpperCase()}</div>}
        title={t('settings.account')}
        description={<><p className="sc-settings-account-name">{session?.user.display_name || session?.user.name}</p>{session?.user.display_name && session.user.name && session.user.display_name !== session.user.name ? <p className="sc-settings-username">@{session.user.name}</p> : null}</>}
        trailing={<span className="sc-settings-badge">{session?.user.is_admin ? t('common.administrator') : t('common.user_2')}</span>}
      >
        <div className="sc-settings-row">
          <Button variant="outlined" icon={<Icon name="close" size={18} />} loading={signOutPending} onClick={onSignOut}>
            {t('common.sign_out')}
          </Button>
        </div>
      </SettingsCard>
    </div>
  )
}

type SecurityPanelProps = {
  oidcVisible: boolean
  t: (key: string) => string
}

export function SecurityPanel({ oidcVisible, t }: SecurityPanelProps) {
  return (
    <Suspense fallback={<p>{t('common.loading')}</p>}>
      <div className="sc-settings-page-grid">
        <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="lock" size={20} /></div>} title={t('common.password')} description={<p className="sc-settings-card-hint">{t('settings.at_least_10_characters_changing')}</p>}>
          <PasswordSection />
        </SettingsCard>
        <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="lock" size={20} /></div>} title={t('settings.two_factor_authentication')} description={<p className="sc-settings-card-hint">{t('settings.asks_6_digit_code_from')}</p>}>
          <TotpSection />
        </SettingsCard>
        {oidcVisible ? (
          <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="admin" size={20} /></div>} title={t('settings.single_sign_on')} description={<p className="sc-settings-card-hint">{t('settings.sign_your_organisations_identity_provider')}</p>}>
            <OidcSection />
          </SettingsCard>
        ) : null}
        <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="lock" size={20} /></div>} title={t('settings.app_passwords')} description={<p className="sc-settings-card-hint">{t('settings.use_one_where_your_account')}</p>}>
          <AppPasswordsSection />
        </SettingsCard>
        <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="recent" size={20} /></div>} title={t('settings.active_sessions')} description={<p className="sc-settings-card-hint">{t('settings.devices_currently_signed_account_sign')}</p>}>
          <SessionsSection />
        </SettingsCard>
      </div>
    </Suspense>
  )
}

type ConnectionsPanelProps = {
  session: SessionInfo | undefined
  t: (key: string) => string
}

export function ConnectionsPanel({ session, t }: ConnectionsPanelProps) {
  return (
    <Suspense fallback={<p>{t('common.loading')}</p>}>
      <div className="sc-settings-page-grid">
        {session?.features.smb ? (
          <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="folder-tree" size={20} /></div>} title={t('admin.server_smb')} description={<p className="sc-settings-card-hint">{t('settings.mount_as_network_drive_file')}</p>}>
            <SmbSection />
          </SettingsCard>
        ) : null}
        {session?.features.webdav ? (
          <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="link" size={20} /></div>} title={t('settings.connections')} description={<p className="sc-settings-card-hint">{t('webdav.connect_from_your_os_file_manager')}</p>}>
            <WebdavSection />
          </SettingsCard>
        ) : null}
      </div>
    </Suspense>
  )
}

type AppearancePanelProps = {
  theme: ThemePref
  locale: Locale
  concurrency: number
  concurrencyChoices: readonly number[]
  concurrencySaveFailed: boolean
  onThemeChange: (value: string | string[], group: HTMLElement) => void
  onLocaleChange: (value: string | string[], group: HTMLElement) => void
  onConcurrencyChange: (value: string | string[], group: HTMLElement) => void
  t: (key: string) => string
}

export function AppearancePanel({ theme, locale, concurrency, concurrencyChoices, concurrencySaveFailed, onThemeChange, onLocaleChange, onConcurrencyChange, t }: AppearancePanelProps) {
  return (
    <div className="sc-settings-page-grid">
      <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="settings" size={20} /></div>} title={t('settings.theme')} description={<p className="sc-settings-card-hint">{t('settings.choosing_system_follows_your_device')}</p>}>
        <div className="sc-settings-row sc-settings-row-segmented">
          <mdui-segmented-button-group selects="single" aria-label={t('settings.theme')} value={theme} onChange={(event) => {
            const group = event.currentTarget as HTMLElement & { value: string | string[] }
            onThemeChange(group.value, group)
          }}>
            <mdui-segmented-button value="system">{t('common.system')}</mdui-segmented-button>
            <mdui-segmented-button value="light">{t('settings.light')}</mdui-segmented-button>
            <mdui-segmented-button value="dark">{t('settings.dark')}</mdui-segmented-button>
          </mdui-segmented-button-group>
        </div>
      </SettingsCard>
      <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="info" size={20} /></div>} title={t('settings.language')} description={<p className="sc-settings-card-hint">{t('settings.language_choice_stays_this_browser')}</p>}>
        <div className="sc-settings-row sc-settings-row-segmented">
          <mdui-segmented-button-group selects="single" aria-label={t('settings.language')} value={locale} onChange={(event) => {
            const group = event.currentTarget as HTMLElement & { value: string | string[] }
            onLocaleChange(group.value, group)
          }}>
            <mdui-segmented-button value="ko">한국어</mdui-segmented-button>
            <mdui-segmented-button value="en">English</mdui-segmented-button>
          </mdui-segmented-button-group>
        </div>
      </SettingsCard>
      <SettingsCard leading={<div className="sc-settings-card-icon"><Icon name="upload" size={20} /></div>} title={t('settings.upload_concurrency')} description={<p className="sc-settings-card-hint">{t('settings.upload_concurrency_hint')}</p>}>
        <div className="sc-settings-row sc-settings-row-segmented">
          <mdui-segmented-button-group selects="single" aria-label={t('settings.upload_concurrency')} value={String(concurrency)} onChange={(event) => {
            const group = event.currentTarget as HTMLElement & { value: string | string[] }
            onConcurrencyChange(group.value, group)
          }}>
            {concurrencyChoices.map((count) => <mdui-segmented-button key={count} value={String(count)}>{count}</mdui-segmented-button>)}
          </mdui-segmented-button-group>
        </div>
        {concurrencySaveFailed ? <p className="sc-settings-error" role="alert">{t('common.could_not_save_settings')}</p> : null}
      </SettingsCard>
    </div>
  )
}
