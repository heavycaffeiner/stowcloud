import { createStore, type StoreApi } from 'zustand/vanilla'
import { api, ApiError } from '../../api/client'

export type MeasureState =
  | { kind: 'idle' }
  | { kind: 'measuring' }
  | { kind: 'done'; bytes: number; files: number }
  | { kind: 'failed'; reason: 'denied' | 'other' }

export interface MeasureSnapshot {
  readonly state: MeasureState
  readonly key: string | null
}

const SETTLE_MS = 400

export function buildMeasureKey(paths: readonly string[], base: { bytes: number; files: number }): string | null {
  if (paths.length === 0 && base.files === 0) return null
  return `${base.bytes}:${base.files}\u0000${paths.join('\u0000')}`
}

export function aggregateSizes(
  parts: readonly { bytes: number; files: number }[],
  base: { bytes: number; files: number }
): { bytes: number; files: number } {
  return {
    bytes: parts.reduce((acc, p) => acc + p.bytes, base.bytes),
    files: parts.reduce((acc, p) => acc + p.files, base.files)
  }
}

export interface MeasureStore extends StoreApi<MeasureSnapshot> {
  retarget(paths: string[], base: { bytes: number; files: number }): void
  retry(paths: string[], base: { bytes: number; files: number }): void
  cancel(): void
}

export function createMeasureStore(): MeasureStore {
  let timer: number | null = null

  const store = createStore<MeasureSnapshot>(() => ({
    state: { kind: 'idle' },
    key: null
  }))

  function cancelTimer(): void {
    if (timer !== null) {
      window.clearTimeout(timer)
      timer = null
    }
  }

  async function runWalk(
    paths: string[],
    base: { bytes: number; files: number },
    targetKey: string
  ): Promise<void> {
    try {
      const parts = await Promise.all(paths.map((p) => api.folderSize(p)))
      if (store.getState().key !== targetKey) return
      const totals = aggregateSizes(parts, base)
      store.setState({
        key: targetKey,
        state: { kind: 'done', ...totals }
      })
    } catch (err) {
      if (store.getState().key !== targetKey) return
      const reason = err instanceof ApiError && (err.code === 'acl.denied' || err.code === 'fs.denied') ? 'denied' : 'other'
      store.setState({
        key: targetKey,
        state: { kind: 'failed', reason }
      })
    }
  }

  function retarget(paths: string[], base: { bytes: number; files: number }): void {
    const key = buildMeasureKey(paths, base)
    if (key === store.getState().key) return

    cancelTimer()

    if (key === null) {
      store.setState({ key: null, state: { kind: 'idle' } })
      return
    }

    if (paths.length === 0) {
      store.setState({ key, state: { kind: 'done', ...base } })
      return
    }

    store.setState({ key, state: { kind: 'measuring' } })
    timer = window.setTimeout(() => void runWalk(paths, base, key), SETTLE_MS)
  }

  function retry(paths: string[], base: { bytes: number; files: number }): void {
    store.setState((prev) => ({ ...prev, key: null }))
    retarget(paths, base)
  }

  return {
    ...store,
    retarget,
    retry,
    cancel(): void {
      cancelTimer()
      store.setState({ key: null, state: { kind: 'idle' } })
    }
  }
}
