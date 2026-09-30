import { useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { DEFAULT_CONCURRENCY } from '../../../../lib/upload/chunk-planner'
import { loadStoredConcurrency, subscribeUploadPreferences } from '../../../../lib/upload/preferences'
import { setUploadConcurrency } from '../../../../lib/upload/queue'

export const settingsTabs = ['account', 'security', 'connections', 'appearance'] as const
export type SettingsTab = (typeof settingsTabs)[number]

const concurrencyPresets: readonly number[] = [1, 2, 4, 8]

function hashTab(): SettingsTab {
  const value = window.location.hash.slice(1)
  return (settingsTabs as readonly string[]).includes(value) ? (value as SettingsTab) : 'account'
}

export function useSettingsTabs(featureConnections: boolean) {
  const [tab, setTab] = useState(hashTab)
  const [concurrencySaveFailed, setConcurrencySaveFailed] = useState(false)
  const concurrency = useSyncExternalStore(subscribeUploadPreferences, loadStoredConcurrency, () => DEFAULT_CONCURRENCY)
  const concurrencyChoices = useMemo(
    () =>
      concurrencyPresets.includes(concurrency)
        ? concurrencyPresets
        : [...concurrencyPresets, concurrency].sort((a, b) => a - b),
    [concurrency]
  )

  useEffect(() => {
    const sync = () => setTab(hashTab())
    window.addEventListener('hashchange', sync)
    return () => window.removeEventListener('hashchange', sync)
  }, [])

  const visibleTabs = useMemo(
    () => (featureConnections ? settingsTabs : settingsTabs.filter((item) => item !== 'connections')),
    [featureConnections]
  )

  function selectTab(next: SettingsTab): void {
    window.history.replaceState(
      window.history.state,
      '',
      `${window.location.pathname}${window.location.search}#${next}`
    )
    setTab(next)
  }

  function onSetConcurrency(value: number): boolean {
    const saved = setUploadConcurrency(value)
    setConcurrencySaveFailed(!saved)
    return saved
  }

  return {
    tab,
    visibleTabs,
    selectTab,
    concurrency,
    concurrencyChoices,
    concurrencySaveFailed,
    onSetConcurrency
  }
}
