// Measuring what a selection actually holds.
// Rewritten to delegate state management to Zustand measure slice.

import { createMeasureStore, type MeasureState } from '../store/slices/measure.slice'

export type { MeasureState }
class SelectionMeasure {
  #store = createMeasureStore()
  #state = $state<MeasureState>({ kind: 'idle' })

  constructor() {
    this.#store.subscribe((snap) => {
      this.#state = snap.state
    })
  }

  get state(): MeasureState {
    return this.#state
  }

  set state(v: MeasureState) {
    this.#state = v
  }
  get store() {
    return this.#store
  }


  retarget(paths: string[], base: { bytes: number; files: number }): void {
    this.#store.retarget(paths, base)
  }

  retry(paths: string[], base: { bytes: number; files: number }): void {
    this.#store.retry(paths, base)
  }
}

export const selectionMeasure = new SelectionMeasure()
