import { useState, useSyncExternalStore } from 'react'
import { DEFAULT_CONCURRENCY } from '../../../lib/upload/chunk-planner'
import { loadStoredConcurrency, subscribeUploadPreferences } from '../../upload/preferences'
import { setUploadConcurrency } from '../../upload/queue'

const PRESETS: readonly number[] = [1, 2, 4, 8]

/** This browser's upload concurrency, with the presets plus whatever value was stored outside them. */
export function useUploadConcurrency() {
  const [saveFailed, setSaveFailed] = useState(false)
  const concurrency = useSyncExternalStore(subscribeUploadPreferences, loadStoredConcurrency, () => DEFAULT_CONCURRENCY)
  const choices = PRESETS.includes(concurrency) ? PRESETS : [...PRESETS, concurrency].sort((a, b) => a - b)
  const choose = (value: number): void => {
    if (choices.includes(value)) setSaveFailed(!setUploadConcurrency(value))
  }
  return { concurrency, choices, choose, saveFailed }
}
