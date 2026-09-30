import { useEffect, useRef, useState } from 'react'

const ADMIN_TABS = ['users', 'shares', 'storage', 'server', 'logs'] as const
export type AdminTab = (typeof ADMIN_TABS)[number]

function isAdminTab(value: string): value is AdminTab {
  return ADMIN_TABS.includes(value as AdminTab)
}

function currentTab(): AdminTab {
  const hash = window.location.hash.slice(1)
  return isAdminTab(hash) ? hash : 'users'
}

export function useAdminTab(): { tab: AdminTab; selectTab: (next: AdminTab) => void } {
  const [tab, setTab] = useState(currentTab)
  const seenHash = useRef(window.location.hash.slice(1))

  useEffect(() => {
    if (!isAdminTab(seenHash.current)) {
      window.history.replaceState(
        window.history.state,
        '',
        `${window.location.pathname}${window.location.search}#users`
      )
      seenHash.current = 'users'
      setTab('users')
    }

    const reconcileHash = (): void => {
      const hash = window.location.hash.slice(1)
      seenHash.current = hash
      if (isAdminTab(hash)) {
        setTab(hash)
        return
      }
      window.history.replaceState(
        window.history.state,
        '',
        `${window.location.pathname}${window.location.search}#users`
      )
      seenHash.current = 'users'
      setTab('users')
    }

    window.addEventListener('hashchange', reconcileHash)
    return () => window.removeEventListener('hashchange', reconcileHash)
  }, [])

  const selectTab = (next: AdminTab): void => {
    if (next === tab) return
    setTab(next)
    seenHash.current = next
    window.history.replaceState(
      window.history.state,
      '',
      `${window.location.pathname}${window.location.search}#${next}`
    )
  }

  return { tab, selectTab }
}
