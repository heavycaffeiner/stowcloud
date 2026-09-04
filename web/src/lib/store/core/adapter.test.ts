import { describe, expect, it, vi } from 'vitest'
import { createStore } from 'zustand/vanilla'
import { toReadableStore } from './adapter'
import {
  err,
  flatMapResult,
  type Result,
  mapDelete,
  mapMerge,
  mapResult,
  mapSet,
  matchResult,
  ok,
  pipe,
  setAdd,
  setDelete,
  setDifference,
  setIntersect,
  setToggle,
  unwrapOr
} from './fp'

describe('toReadableStore Svelte store adapter', () => {
  interface CounterState {
    count: number
    name: string
  }

  const createCounterStore = (initial = 0) =>
    createStore<CounterState>(() => ({
      count: initial,
      name: 'item'
    }))

  it('synchronously emits current state upon subscription', () => {
    const store = createCounterStore(42)
    const readable = toReadableStore(store)

    const values: CounterState[] = []
    readable.subscribe((val) => {
      values.push(val)
    })

    expect(values).toHaveLength(1)
    expect(values[0]).toEqual({ count: 42, name: 'item' })
  })

  it('notifies subscribers on subsequent mutations', () => {
    const store = createCounterStore(0)
    const readable = toReadableStore(store)

    const values: number[] = []
    const unsub = readable.subscribe((val) => {
      values.push(val.count)
    })

    store.setState({ count: 1 })
    store.setState({ count: 2 })
    unsub()
    store.setState({ count: 3 })

    expect(values).toEqual([0, 1, 2])
  })

  it('applies selector projection cleanly', () => {
    const store = createCounterStore(10)
    const countStore = toReadableStore(store, (s) => s.count)

    const received: number[] = []
    countStore.subscribe((c) => {
      received.push(c)
    })

    store.setState({ count: 20 })
    expect(received).toEqual([10, 20])
  })
})

describe('functional programming utilities', () => {
  it('handles Result ok and err branches', () => {
    const good: Result<number, Error> = ok(10)
    const bad: Result<number, Error> = err(new Error('fail'))

    expect(mapResult(good, (x) => x * 2)).toEqual(ok(20))
    expect(mapResult(bad, (x) => x * 2)).toEqual(bad)

    expect(flatMapResult(good, (x) => ok(x + 5))).toEqual(ok(15))
    expect(unwrapOr(good, 0)).toBe(10)
    expect(unwrapOr(bad, 99)).toBe(99)

    const matchedGood = matchResult(good, {
      ok: (v) => `val:${v}`,
      err: (e) => `err:${e.message}`
    })
    expect(matchedGood).toBe('val:10')
  })

  it('pipes functions in order', () => {
    const result = pipe(
      5,
      (x) => x * 2,
      (x) => x + 3,
      (x) => `total:${x}`
    )
    expect(result).toBe('total:13')
  })

  it('performs pure set transformations', () => {
    const empty = new Set<string>()
    const withA = setAdd(empty, 'a')
    const withAB = setAdd(withA, 'b')

    expect(Array.from(withAB)).toEqual(['a', 'b'])
    expect(empty.size).toBe(0)

    const withoutA = setDelete(withAB, 'a')
    expect(Array.from(withoutA)).toEqual(['b'])

    const toggled = setToggle(withoutA, 'b')
    expect(toggled.size).toBe(0)

    const set1 = new Set(['x', 'y'])
    const set2 = new Set(['y', 'z'])
    expect(Array.from(setIntersect(set1, set2))).toEqual(['y'])
    expect(Array.from(setDifference(set1, set2))).toEqual(['x'])
  })

  it('performs pure map transformations', () => {
    const initial = new Map([['k1', 1]])
    const added = mapSet(initial, 'k2', 2)

    expect(added.get('k2')).toBe(2)
    expect(initial.has('k2')).toBe(false)

    const deleted = mapDelete(added, 'k1')
    expect(deleted.has('k1')).toBe(false)
    expect(added.has('k1')).toBe(true)

    const merged = mapMerge(initial, [['k3', 3], ['k1', 10]])
    expect(merged.get('k1')).toBe(10)
    expect(merged.get('k3')).toBe(3)
  })
})
