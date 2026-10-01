import { useHashTab } from '../../../hooks/use-hash-tab'

const ADMIN_TABS = ['users', 'shares', 'storage', 'server', 'logs'] as const
export type AdminTab = (typeof ADMIN_TABS)[number]

export function useAdminTab(): { tab: AdminTab; selectTab: (next: AdminTab) => void } {
  const [tab, selectTab] = useHashTab(ADMIN_TABS, 'users')
  return { tab, selectTab }
}
