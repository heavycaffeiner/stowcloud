import { useMutation, useQuery } from '@tanstack/react-query'
import { lazy, Suspense, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { currentLocale } from '../../lib/i18n'
import { localeStore } from '../../lib/i18n/state'
import { useI18n } from '../../lib/i18n/use-i18n'
import { logoutMutation, oidcConfigQuery, sessionQuery } from '../../lib/query/session'
import { ui } from '../../lib/store/ui.store'
import { useStore } from '../../lib/store/use-store'
import { loadStoredConcurrency } from '../../lib/upload/chunk-planner'
import { setUploadConcurrency } from '../../lib/upload/queue'
import { Button } from '../../lib/ui/Button'
import { Icon } from '../../lib/ui/Icon'
import { useDocumentTitle } from '../use-document-title'
import './settings.css'

const PasswordSection = lazy(() => import('../../lib/ui/settings/PasswordSection').then((m) => ({ default: m.PasswordSection })))
const TotpSection = lazy(() => import('../../lib/ui/settings/TotpSection').then((m) => ({ default: m.TotpSection })))
const AppPasswordsSection = lazy(() => import('../../lib/ui/settings/AppPasswordsSection').then((m) => ({ default: m.AppPasswordsSection })))
const SessionsSection = lazy(() => import('../../lib/ui/settings/SessionsSection').then((m) => ({ default: m.SessionsSection })))
const SmbSection = lazy(() => import('../../lib/ui/settings/SmbSection').then((m) => ({ default: m.SmbSection })))
const WebdavSection = lazy(() => import('../../lib/ui/settings/WebdavSection').then((m) => ({ default: m.WebdavSection })))
const OidcSection = lazy(() => import('../../lib/ui/settings/OidcSection').then((m) => ({ default: m.OidcSection })))

const tabs = ['account', 'security', 'connections', 'appearance'] as const
type Tab = (typeof tabs)[number]
function hashTab(): Tab {
  const value = window.location.hash.slice(1)
  return (tabs as readonly string[]).includes(value) ? value as Tab : 'account'
}

export function SettingsPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const session = useQuery(sessionQuery())
  const oidcConfig = useQuery(oidcConfigQuery())
  const logout = useMutation(logoutMutation())
  const theme = useStore(ui, (state) => state.theme)
  useStore(localeStore, (state) => state.locale)
  const [tab, setTab] = useState<Tab>(hashTab)
  const [concurrency, setConcurrency] = useState(loadStoredConcurrency())

  useDocumentTitle(t('settings.settings_stowcloud'))
  useEffect(() => {
    const sync = () => setTab(hashTab())
    window.addEventListener('hashchange', sync)
    return () => window.removeEventListener('hashchange', sync)
  }, [])

  const featureConnections = !!session.data?.features.smb || !!session.data?.features.webdav
  const visibleTabs = useMemo(() => featureConnections ? tabs : tabs.filter((item) => item !== 'connections'), [featureConnections])
  const oidcVisible = oidcConfig.data !== undefined && (oidcConfig.data.enabled || !!session.data?.oidc.linked || new URLSearchParams(window.location.search).has('oidc_error'))

  function tabLabel(value: Tab): string {
    if (value === 'account') return t('settings.account')
    if (value === 'security') return t('settings.security')
    if (value === 'connections') return t('settings.connections')
    return t('settings.browser_preferences')
  }
  function tabIcon(value: Tab): string {
    if (value === 'account') return 'admin'
    if (value === 'security') return 'lock'
    if (value === 'connections') return 'folder-tree'
    return 'settings'
  }
  function selectTab(value: Tab): void {
    window.history.replaceState(window.history.state, '', `${window.location.pathname}${window.location.search}#${value}`)
    setTab(value)
  }
  function onSetConcurrency(value: number): void {
    setConcurrency(value)
    setUploadConcurrency(value)
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

  return (
    <section className="sc-settings-page">
      <header><h1>{t('common.settings')}</h1></header>
      <nav className="sc-settings-page__tabs" aria-label={t('common.settings')}>
        {visibleTabs.map((item) => (
          <button key={item} type="button" className="sc-settings-page__tab" aria-current={item === tab ? 'page' : undefined} onClick={() => selectTab(item)}>
            <Icon name={tabIcon(item)} />
            {tabLabel(item)}
          </button>
        ))}
      </nav>

      {tab === 'account' ? (
        <div className="sc-settings-page__grid">
          <article className="sc-settings-card">
            <div className="sc-settings-card__head">
              <div className="sc-settings__avatar">
                {((session.data?.user.display_name || session.data?.user.name || 'U')[0] ?? 'U').toUpperCase()}
              </div>
              <div className="sc-settings-card__meta">
                <h2>{t('settings.account')}</h2>
                <p className="sc-settings__account-name">{session.data?.user.display_name || session.data?.user.name}</p>
                {session.data?.user.display_name && session.data?.user.name && session.data?.user.display_name !== session.data?.user.name ? (
                  <p className="sc-settings__username">@{session.data?.user.name}</p>
                ) : null}
              </div>
              <span className="sc-settings-badge">
                {session.data?.user.is_admin ? t('common.administrator') : t('common.user_2')}
              </span>
            </div>
            <div className="sc-settings-row">
              <Button variant="outlined" icon={<Icon name="close" size={18} />} loading={logout.isPending} onClick={() => void signOut()}>
                {t('common.sign_out')}
              </Button>
            </div>
          </article>
        </div>
      ) : null}

      {tab === 'security' ? (
        <Suspense fallback={<p>{t('common.loading')}</p>}>
          <div className="sc-settings-page__grid">
            <article className="sc-settings-card">
              <div className="sc-settings-card__head">
                <div className="sc-settings__card-icon"><Icon name="lock" size={20} /></div>
                <div className="sc-settings-card__meta">
                  <h2>{t('common.password')}</h2>
                  <p className="sc-settings-card__hint">{t('settings.at_least_10_characters_changing')}</p>
                </div>
              </div>
              <PasswordSection />
            </article>
            <article className="sc-settings-card">
              <div className="sc-settings-card__head">
                <div className="sc-settings__card-icon"><Icon name="lock" size={20} /></div>
                <div className="sc-settings-card__meta">
                  <h2>{t('settings.two_factor_authentication')}</h2>
                  <p className="sc-settings-card__hint">{t('settings.asks_6_digit_code_from')}</p>
                </div>
              </div>
              <TotpSection />
            </article>
            {oidcVisible ? (
              <article className="sc-settings-card">
                <div className="sc-settings-card__head">
                  <div className="sc-settings__card-icon"><Icon name="admin" size={20} /></div>
                  <div className="sc-settings-card__meta">
                    <h2>{t('settings.single_sign_on')}</h2>
                    <p className="sc-settings-card__hint">{t('settings.sign_your_organisations_identity_provider')}</p>
                  </div>
                </div>
                <OidcSection />
              </article>
            ) : null}
            <article className="sc-settings-card">
              <div className="sc-settings-card__head">
                <div className="sc-settings__card-icon"><Icon name="lock" size={20} /></div>
                <div className="sc-settings-card__meta">
                  <h2>{t('settings.app_passwords')}</h2>
                  <p className="sc-settings-card__hint">{t('settings.use_one_where_your_account')}</p>
                </div>
              </div>
              <AppPasswordsSection />
            </article>
            <article className="sc-settings-card">
              <div className="sc-settings-card__head">
                <div className="sc-settings__card-icon"><Icon name="recent" size={20} /></div>
                <div className="sc-settings-card__meta">
                  <h2>{t('settings.active_sessions')}</h2>
                  <p className="sc-settings-card__hint">{t('settings.devices_currently_signed_account_sign')}</p>
                </div>
              </div>
              <SessionsSection />
            </article>
          </div>
        </Suspense>
      ) : null}

      {tab === 'connections' ? (
        <Suspense fallback={<p>{t('common.loading')}</p>}>
          <div className="sc-settings-page__grid">
            {session.data?.features.smb ? (
              <article className="sc-settings-card">
                <div className="sc-settings-card__head">
                  <div className="sc-settings__card-icon"><Icon name="folder-tree" size={20} /></div>
                  <div className="sc-settings-card__meta">
                    <h2>{t('admin.server_smb')}</h2>
                    <p className="sc-settings-card__hint">{t('settings.mount_as_network_drive_file')}</p>
                  </div>
                </div>
                <SmbSection />
              </article>
            ) : null}
            {session.data?.features.webdav ? (
              <article className="sc-settings-card">
                <div className="sc-settings-card__head">
                  <div className="sc-settings__card-icon"><Icon name="link" size={20} /></div>
                  <div className="sc-settings-card__meta">
                    <h2>{t('settings.connections')}</h2>
                    <p className="sc-settings-card__hint">{t('webdav.connect_from_your_os_file_manager')}</p>
                  </div>
                </div>
                <WebdavSection />
              </article>
            ) : null}
          </div>
        </Suspense>
      ) : null}

      {tab === 'appearance' ? (
        <div className="sc-settings-page__grid">
          <article className="sc-settings-card">
            <div className="sc-settings-card__head">
              <div className="sc-settings__card-icon"><Icon name="settings" size={20} /></div>
              <div className="sc-settings-card__meta">
                <h2>{t('settings.theme')}</h2>
                <p className="sc-settings-card__hint">{t('settings.choosing_system_follows_your_device')}</p>
              </div>
            </div>
            <div className="sc-settings-row">
              <mdui-segmented-button-group value={theme} onChange={(event: any) => ui.setTheme(event.target.value)}>
                <mdui-segmented-button value="system">{t('common.system')}</mdui-segmented-button>
                <mdui-segmented-button value="light">{t('settings.light')}</mdui-segmented-button>
                <mdui-segmented-button value="dark">{t('settings.dark')}</mdui-segmented-button>
              </mdui-segmented-button-group>
            </div>
          </article>
          <article className="sc-settings-card">
            <div className="sc-settings-card__head">
              <div className="sc-settings__card-icon"><Icon name="info" size={20} /></div>
              <div className="sc-settings-card__meta">
                <h2>{t('settings.language')}</h2>
                <p className="sc-settings-card__hint">{t('settings.language_choice_stays_this_browser')}</p>
              </div>
            </div>
            <div className="sc-settings-row">
              <mdui-segmented-button-group value={currentLocale()} onChange={(event: any) => localeStore.setLocale(event.target.value)}>
                <mdui-segmented-button value="ko">한국어</mdui-segmented-button>
                <mdui-segmented-button value="en">English</mdui-segmented-button>
              </mdui-segmented-button-group>
            </div>
          </article>
          <article className="sc-settings-card">
            <div className="sc-settings-card__head">
              <div className="sc-settings__card-icon"><Icon name="upload" size={20} /></div>
              <div className="sc-settings-card__meta">
                <h2>{t('settings.upload_concurrency')}</h2>
                <p className="sc-settings-card__hint">{t('settings.upload_concurrency_hint')}</p>
              </div>
            </div>
            <div className="sc-settings-row">
              <mdui-segmented-button-group value={String(concurrency)} onChange={(event: any) => onSetConcurrency(Number(event.target.value) || 1)}>
                {[1, 2, 4, 8].map((count) => (
                  <mdui-segmented-button key={count} value={String(count)}>
                    {count}
                  </mdui-segmented-button>
                ))}
              </mdui-segmented-button-group>
            </div>
          </article>
        </div>
      ) : null}
    </section>
  )
}
