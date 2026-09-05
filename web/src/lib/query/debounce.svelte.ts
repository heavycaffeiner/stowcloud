// A settling window in front of a reactive value.
//
// Used where a value feeds a query key and changes on every keystroke: without
// it, "refused" is six filter changes and six sets of requests.
export interface Debounced<T> {
  readonly current: T
}

export function debounced<T>(read: () => T, ms: number): Debounced<T> {
  let settled = $state.raw(read())
  $effect(() => {
    const next = read()
    const timer = window.setTimeout(() => {
      settled = next
    }, ms)
    return () => window.clearTimeout(timer)
  })
  return {
    get current(): T {
      return settled
    }
  }
}
