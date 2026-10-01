import { useHashTab } from '../../../hooks/use-hash-tab'
import { useSession } from '../../auth/api'

export const settingsTabs = ['account', 'security', 'connections', 'appearance'] as const
export type SettingsTab = (typeof settingsTabs)[number]

/** The selected tab and the tabs this server offers: connections only when SMB or WebDAV is on. */
export function useSettingsTabs() {
  const session = useSession()
  const [tab, selectTab] = useHashTab(settingsTabs, 'account')
  const connections = !!session.data?.features.smb || !!session.data?.features.webdav
  const visibleTabs = connections ? settingsTabs : settingsTabs.filter((item) => item !== 'connections')
  return { tab, visibleTabs, selectTab }
}
