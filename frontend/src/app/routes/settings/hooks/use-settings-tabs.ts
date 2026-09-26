import { useEffect, useMemo, useSyncExternalStore } from 'react'
import { useRouteStore } from '../../../hooks/use-route-store'
import { DEFAULT_CONCURRENCY, loadStoredConcurrency, subscribeUploadPreferences } from '../../../../lib/upload/chunk-planner'
import { setUploadConcurrency } from '../../../../lib/upload/queue'

export const settingsTabs = ['account', 'security', 'connections', 'appearance'] as const
export type SettingsTab = (typeof settingsTabs)[number]

const concurrencyPresets: readonly number[] = [1, 2, 4, 8]

type SettingsState = { tab: SettingsTab; concurrencySaveFailed: boolean }

function hashTab(): SettingsTab {
  const value = window.location.hash.slice(1)
  return (settingsTabs as readonly string[]).includes(value) ? value as SettingsTab : 'account'
}

export function useSettingsTabs(featureConnections: boolean) {
  const [state, setState] = useRouteStore<SettingsState>(() => ({ tab: hashTab(), concurrencySaveFailed: false }))
  const concurrency = useSyncExternalStore(subscribeUploadPreferences, loadStoredConcurrency, () => DEFAULT_CONCURRENCY)
  const concurrencyChoices = useMemo(() => concurrencyPresets.includes(concurrency)
    ? concurrencyPresets
    : [...concurrencyPresets, concurrency].sort((a, b) => a - b), [concurrency])

  useEffect(() => {
    const sync = () => setState({ tab: hashTab() })
    window.addEventListener('hashchange', sync)
    return () => window.removeEventListener('hashchange', sync)
  }, [setState])

  const visibleTabs = useMemo(() => featureConnections ? settingsTabs : settingsTabs.filter((item) => item !== 'connections'), [featureConnections])

  function selectTab(tab: SettingsTab): void {
    window.history.replaceState(window.history.state, '', `${window.location.pathname}${window.location.search}#${tab}`)
    setState({ tab })
  }

  function onSetConcurrency(value: number): boolean {
    const saved = setUploadConcurrency(value)
    setState({ concurrencySaveFailed: !saved })
    return saved
  }

  return {
    tab: state.tab,
    visibleTabs,
    selectTab,
    concurrency,
    concurrencyChoices,
    concurrencySaveFailed: state.concurrencySaveFailed,
    onSetConcurrency
  }
}
