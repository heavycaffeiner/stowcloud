import { useNavigate, useParams } from '@tanstack/react-router'
import { choiceParam } from '../../../lib/url-search'
import { adminTabs, type AdminTab } from '../tabs'

/** The tab named by the URL. Switching tabs replaces the history entry instead of pushing one. */
export function useAdminTab(): { tab: AdminTab; selectTab: (next: AdminTab) => void } {
  const navigate = useNavigate()
  const tab = choiceParam(adminTabs, useParams({ from: '/_app/admin/{-$tab}' }).tab) ?? 'users'
  const selectTab = (next: AdminTab): void => {
    if (next !== tab) void navigate({ to: '/admin/{-$tab}', params: { tab: next }, search: true, replace: true })
  }
  return { tab, selectTab }
}
