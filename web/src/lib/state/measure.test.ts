import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { selectionMeasure } from './measure.svelte'

// The module reaches the server through the api client, which this replaces so
// the tests describe the keying rather than a network.
const folderSize = vi.fn()
vi.mock('../api/client', () => ({
  api: { folderSize: (p: string) => folderSize(p) },
  ApiError: class ApiError extends Error {
    code: string
    constructor(code: string) {
      super(code)
      this.code = code
    }
  }
}))

beforeEach(() => {
  vi.useFakeTimers()
  folderSize.mockReset()
  // Every test starts from a selection of nothing, which is also what clears
  // the module's memory of the last one.
  selectionMeasure.retarget([], { bytes: 0, files: 0 })
})

afterEach(() => {
  vi.useRealTimers()
})

describe('selectionMeasure', () => {
  // Synchronously done, not measuring. Falling through to the timer here
  // would show a spinner for the settle delay and then report the number the
  // listing already had, which is a wait bought for nothing.
  it('reports a files-only selection without walking anything', () => {
    selectionMeasure.retarget([], { bytes: 2048, files: 2 })
    expect(selectionMeasure.state).toEqual({ kind: 'done', bytes: 2048, files: 2 })
    expect(folderSize).not.toHaveBeenCalled()
  })

  it('adds what the folders hold to the files already counted', async () => {
    folderSize.mockResolvedValue({ bytes: 1000, files: 5 })
    selectionMeasure.retarget(['home/Documents'], { bytes: 1024, files: 1 })
    await vi.runAllTimersAsync()
    expect(selectionMeasure.state).toEqual({ kind: 'done', bytes: 2024, files: 6 })
  })

  // The defect this keying exists for. Ctrl-clicking a file into a selection
  // leaves its folders untouched, so a key built from the folder paths alone
  // matched the previous one and the retarget was dropped: the total stayed on
  // screen without the file in it.
  it('notices a file added beside an unchanged set of folders', async () => {
    folderSize.mockResolvedValue({ bytes: 1000, files: 5 })
    selectionMeasure.retarget(['home/Documents'], { bytes: 0, files: 0 })
    await vi.runAllTimersAsync()
    expect(selectionMeasure.state).toEqual({ kind: 'done', bytes: 1000, files: 5 })

    selectionMeasure.retarget(['home/Documents'], { bytes: 1024, files: 1 })
    await vi.runAllTimersAsync()
    expect(selectionMeasure.state).toEqual({ kind: 'done', bytes: 2024, files: 6 })
  })

  it('notices a file removed from one', async () => {
    folderSize.mockResolvedValue({ bytes: 1000, files: 5 })
    selectionMeasure.retarget(['home/Documents'], { bytes: 1024, files: 1 })
    await vi.runAllTimersAsync()
    selectionMeasure.retarget(['home/Documents'], { bytes: 0, files: 0 })
    await vi.runAllTimersAsync()
    expect(selectionMeasure.state).toEqual({ kind: 'done', bytes: 1000, files: 5 })
  })

  // Arrowing through a listing changes the selection on every keypress. Only
  // where it stops is worth a walk.
  it('walks once for a selection that changed while it was settling', async () => {
    folderSize.mockResolvedValue({ bytes: 1, files: 1 })
    selectionMeasure.retarget(['a'], { bytes: 0, files: 0 })
    selectionMeasure.retarget(['b'], { bytes: 0, files: 0 })
    selectionMeasure.retarget(['c'], { bytes: 0, files: 0 })
    await vi.runAllTimersAsync()
    expect(folderSize).toHaveBeenCalledTimes(1)
    expect(folderSize).toHaveBeenCalledWith('c')
  })

  it('measures every folder in the selection', async () => {
    folderSize.mockResolvedValue({ bytes: 10, files: 1 })
    selectionMeasure.retarget(['a', 'b', 'c'], { bytes: 0, files: 0 })
    await vi.runAllTimersAsync()
    expect(folderSize).toHaveBeenCalledTimes(3)
    expect(selectionMeasure.state).toEqual({ kind: 'done', bytes: 30, files: 3 })
  })

  // A late answer for a selection nobody is looking at any more would show one
  // folder's size under another's name.
  it('drops an answer that arrives after the selection moved on', async () => {
    const gate = Promise.withResolvers<{ bytes: number; files: number }>()
    folderSize.mockImplementationOnce(() => gate.promise)
    selectionMeasure.retarget(['slow'], { bytes: 0, files: 0 })
    await vi.advanceTimersByTimeAsync(500)

    folderSize.mockResolvedValue({ bytes: 7, files: 7 })
    selectionMeasure.retarget(['fast'], { bytes: 0, files: 0 })
    await vi.runAllTimersAsync()

    gate.resolve({ bytes: 999, files: 999 })
    await vi.runAllTimersAsync()
    expect(selectionMeasure.state).toEqual({ kind: 'done', bytes: 7, files: 7 })
  })

  it('reports a refusal it cannot measure through', async () => {
    folderSize.mockRejectedValue(new Error('nope'))
    selectionMeasure.retarget(['x'], { bytes: 0, files: 0 })
    await vi.runAllTimersAsync()
    expect(selectionMeasure.state).toEqual({ kind: 'failed', reason: 'other' })
  })
})
