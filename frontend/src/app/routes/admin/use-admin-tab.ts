import { useEffect, useRef } from 'react'
import { useRouteStore } from '../../use-route-store'

const ADMIN_TABS = ['users', 'shares', 'storage', 'server', 'logs'] as const
export type AdminTab = (typeof ADMIN_TABS)[number]

type AdminState = { tab: AdminTab }

function isAdminTab(value: string): value is AdminTab {
  return ADMIN_TABS.includes(value as AdminTab)
}

function currentTab(): AdminTab {
  const hash = window.location.hash.slice(1)
  return isAdminTab(hash) ? hash : 'users'
}

export function useAdminTab(): { tab: AdminTab; selectTab: (next: AdminTab) => void } {
  const [state, setState] = useRouteStore<AdminState>(() => ({ tab: currentTab() }))
  const seenHash = useRef(window.location.hash.slice(1))

  useEffect(() => {
    if (!isAdminTab(seenHash.current)) {
      window.history.replaceState(window.history.state, '', `${window.location.pathname}${window.location.search}#users`)
      seenHash.current = 'users'
      setState({ tab: 'users' })
    }

    const reconcileHash = (): void => {
      const hash = window.location.hash.slice(1)
      seenHash.current = hash
      if (isAdminTab(hash)) {
        setState({ tab: hash })
        return
      }
      window.history.replaceState(window.history.state, '', `${window.location.pathname}${window.location.search}#users`)
      seenHash.current = 'users'
      setState({ tab: 'users' })
    }

    window.addEventListener('hashchange', reconcileHash)
    return () => window.removeEventListener('hashchange', reconcileHash)
  }, [setState])

  const selectTab = (next: AdminTab): void => {
    if (next === state.tab) return
    setState({ tab: next })
    seenHash.current = next
    window.history.replaceState(window.history.state, '', `${window.location.pathname}${window.location.search}#${next}`)
  }

  return { tab: state.tab, selectTab }
}
