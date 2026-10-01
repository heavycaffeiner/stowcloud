import { SwitchCase } from 'react-simplikit'
import { useI18n } from '../../../hooks/use-i18n'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { PageTabs } from '../../../ui/PageTabs'
import { AccountPanel, AppearancePanel, ConnectionsPanel, SecurityPanel } from './SettingsPanels'
import { useSettingsTabs } from '../hooks/use-settings-tabs'
import type { SettingsTab } from '../tabs'
import { ErrorBoundary } from '../../../ui/ErrorBoundary'
import * as pageTabsStyles from '../../../ui/PageTabs.css'

const TAB_LABELS: Record<SettingsTab, string> = {
  account: /* i18n */ 'settings.account',
  security: /* i18n */ 'settings.security',
  connections: /* i18n */ 'settings.connections',
  appearance: /* i18n */ 'settings.browser_preferences'
}

const TAB_ICONS: Record<SettingsTab, string> = {
  account: 'admin',
  security: 'lock',
  connections: 'folder-tree',
  appearance: 'settings'
}

export function SettingsPage() {
  const { t } = useI18n()
  const { tab, visibleTabs, selectTab } = useSettingsTabs()
  useDocumentTitle(t('settings.settings_stowcloud'))

  return (
    <section className={pageTabsStyles.page}>
      <header className={pageTabsStyles.header}>
        <h1 className={pageTabsStyles.title}>{t('common.settings')}</h1>
      </header>
      <PageTabs
        label={t('common.settings')}
        items={visibleTabs.map((value) => ({ value, label: t(TAB_LABELS[value]), icon: TAB_ICONS[value] }))}
        active={tab}
        onSelect={selectTab}
      />
      <ErrorBoundary resetKey={tab}>
        <SwitchCase
          value={tab}
          caseBy={{
            account: () => <AccountPanel />,
            security: () => <SecurityPanel />,
            connections: () => <ConnectionsPanel />,
            appearance: () => <AppearancePanel />
          }}
        />
      </ErrorBoundary>
    </section>
  )
}
