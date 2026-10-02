import { SwitchCase } from 'react-simplikit'
import { useI18n } from '../../../hooks/use-i18n'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { ErrorBoundary, StowPageTabs, StowTabbedPage, type IconName } from '@/shared/ui'
import { AccountPanel, AppearancePanel, ConnectionsPanel, SecurityPanel } from '../components/SettingsPanels'
import { useSettingsTabs } from '../hooks/use-settings-tabs'
import type { SettingsTab } from '../tabs'

const TAB_LABELS: Record<SettingsTab, string> = {
  account: /* i18n */ 'settings.account',
  security: /* i18n */ 'settings.security',
  connections: /* i18n */ 'settings.connections',
  appearance: /* i18n */ 'settings.browser_preferences'
}

const TAB_ICONS: Record<SettingsTab, IconName> = {
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
    <StowTabbedPage title={t('common.settings')}>
      <StowPageTabs
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
    </StowTabbedPage>
  )
}
