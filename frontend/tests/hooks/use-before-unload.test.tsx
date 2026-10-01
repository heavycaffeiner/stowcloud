import { renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { useBeforeUnload } from '../../src/hooks/use-before-unload'

function unload(): boolean {
  const event = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(event)
  return event.defaultPrevented
}

describe('useBeforeUnload', () => {
  it('asks the browser to confirm leaving only while active', () => {
    const { rerender, unmount } = renderHook(({ active }) => useBeforeUnload(active), {
      initialProps: { active: true }
    })
    expect(unload()).toBe(true)

    rerender({ active: false })
    expect(unload()).toBe(false)

    rerender({ active: true })
    unmount()
    expect(unload()).toBe(false)
  })
})
