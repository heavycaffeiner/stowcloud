import { useMutation, useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { localeStore } from '../../../lib/i18n/state'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { logoutMutation, oidcConfigQuery, sessionQuery } from '../../../lib/query/session'
import { ui } from '../../../lib/store/ui.store'
import { useStore } from '../../../lib/store/use-store'
import { Icon } from '../../../lib/ui/Icon'
import { useDocumentTitle } from '../../use-document-title'
import { AccountPanel, AppearancePanel, ConnectionsPanel, SecurityPanel } from './SettingsPanels'
import { useSettingsTabs, type SettingsTab } from './use-settings-tabs'
import '../../../styles/app/routes/settings.css.ts'

export function SettingsPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const session = useQuery(sessionQuery())
  const oidcConfig = useQuery(oidcConfigQuery())
  const logout = useMutation(logoutMutation())
  const theme = useStore(ui, (state) => state.theme)
  const locale = useStore(localeStore, (state) => state.locale)
  const featureConnections = !!session.data?.features.smb || !!session.data?.features.webdav
  const settings = useSettingsTabs(featureConnections)
  const oidcVisible = oidcConfig.data !== undefined && (oidcConfig.data.enabled || !!session.data?.oidc.linked || new URLSearchParams(window.location.search).has('oidc_error'))

  useDocumentTitle(t('settings.settings_stowcloud'))

  function tabLabel(value: SettingsTab): string {
    if (value === 'account') return t('settings.account')
    if (value === 'security') return t('settings.security')
    if (value === 'connections') return t('settings.connections')
    return t('settings.browser_preferences')
  }

  function tabIcon(value: SettingsTab): string {
    if (value === 'account') return 'admin'
    if (value === 'security') return 'lock'
    if (value === 'connections') return 'folder-tree'
    return 'settings'
  }

  async function signOut(): Promise<void> {
    try {
      const result = await logout.mutateAsync()
      if (result.end_session_url) {
        window.location.href = result.end_session_url
        return
      }
    } catch {
      // local navigation still clears the app session
    }
    void navigate('/login', { replace: true })
  }

  function restoreValue(group: HTMLElement, value: string | string[]): void {
    const target = group as HTMLElement & { value: string | string[] }
    queueMicrotask(() => { target.value = Array.isArray(value) ? value[0] ?? '' : value })
  }

  return (
    <section className="sc-settings-page">
      <header><h1>{t('common.settings')}</h1></header>
      <nav className="sc-settings-page-tabs" aria-label={t('common.settings')}>
        {settings.visibleTabs.map((item) => (
          <button key={item} type="button" className="sc-settings-page-tab" aria-current={item === settings.tab ? 'page' : undefined} onClick={() => settings.selectTab(item)}>
            <Icon name={tabIcon(item)} />
            {tabLabel(item)}
          </button>
        ))}
      </nav>

      {settings.tab === 'account' ? <AccountPanel session={session.data} signOutPending={logout.isPending} onSignOut={() => void signOut()} t={t} /> : null}
      {settings.tab === 'security' ? <SecurityPanel oidcVisible={oidcVisible} t={t} /> : null}
      {settings.tab === 'connections' ? <ConnectionsPanel session={session.data} t={t} /> : null}
      {settings.tab === 'appearance' ? (
        <AppearancePanel
          theme={theme}
          locale={locale}
          concurrency={settings.concurrency}
          concurrencyChoices={settings.concurrencyChoices}
          concurrencySaveFailed={settings.concurrencySaveFailed}
          t={t}
          onThemeChange={(value, group) => {
            if (value === 'system' || value === 'light' || value === 'dark') ui.setTheme(value)
            else restoreValue(group, theme)
          }}
          onLocaleChange={(value, group) => {
            if (value === 'ko' || value === 'en') localeStore.setLocale(value)
            else restoreValue(group, locale)
          }}
          onConcurrencyChange={(value, group) => {
            if (typeof value === 'string' && settings.concurrencyChoices.some((count) => String(count) === value) && settings.onSetConcurrency(Number(value))) return
            restoreValue(group, String(settings.concurrency))
          }}
        />
      ) : null}
    </section>
  )
}
