import { useMemo, useState, useSyncExternalStore } from 'react'
import { useHashTab } from '../../../hooks/use-hash-tab'
import { DEFAULT_CONCURRENCY } from '../../../lib/upload/chunk-planner'
import { loadStoredConcurrency, subscribeUploadPreferences } from '../../../lib/upload/preferences'
import { setUploadConcurrency } from '../../../lib/upload/queue'

export const settingsTabs = ['account', 'security', 'connections', 'appearance'] as const
export type SettingsTab = (typeof settingsTabs)[number]

const concurrencyPresets: readonly number[] = [1, 2, 4, 8]

export function useSettingsTabs(featureConnections: boolean) {
  const [tab, selectTab] = useHashTab(settingsTabs, 'account')
  const [concurrencySaveFailed, setConcurrencySaveFailed] = useState(false)
  const concurrency = useSyncExternalStore(subscribeUploadPreferences, loadStoredConcurrency, () => DEFAULT_CONCURRENCY)
  const concurrencyChoices = useMemo(
    () =>
      concurrencyPresets.includes(concurrency)
        ? concurrencyPresets
        : [...concurrencyPresets, concurrency].sort((a, b) => a - b),
    [concurrency]
  )

  const visibleTabs = useMemo(
    () => (featureConnections ? settingsTabs : settingsTabs.filter((item) => item !== 'connections')),
    [featureConnections]
  )

  function onSetConcurrency(value: number): void {
    setConcurrencySaveFailed(!setUploadConcurrency(value))
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
