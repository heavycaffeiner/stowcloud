import { useNavigate, useParams } from '@tanstack/react-router'
import { choiceParam } from '../../../lib/url-search'
import { useSession } from '../../auth/api'
import { settingsTabs, type SettingsTab } from '../tabs'

/** The tab named by the URL and the tabs this server offers: connections only when SMB or WebDAV is on.
 *  Switching tabs replaces the history entry instead of pushing one. */
export function useSettingsTabs() {
  const session = useSession()
  const navigate = useNavigate()
  const tab = choiceParam(settingsTabs, useParams({ from: '/_app/settings/{-$tab}' }).tab) ?? 'account'
  const selectTab = (next: SettingsTab): void => {
    if (next !== tab) void navigate({ to: '/settings/{-$tab}', params: { tab: next }, search: true, replace: true })
  }
  const connections = !!session.data?.features.smb || !!session.data?.features.webdav
  const visibleTabs = connections ? settingsTabs : settingsTabs.filter((item) => item !== 'connections')
  return { tab, visibleTabs, selectTab }
}
