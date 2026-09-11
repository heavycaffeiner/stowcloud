import { afterEach, describe, expect, it, vi } from 'vitest'

// m3-svelte's DOM layer reads these browser APIs when it is loaded or
// interacted with. Keep the polyfills local to this test file; accessibility
// queries still inspect the real rendered labels and attributes.
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn().mockImplementation((query) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn()
  }))
})

class FakeResizeObserver {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
window.ResizeObserver = FakeResizeObserver as unknown as typeof ResizeObserver

// These imports stay dynamic because static imports run before the jsdom
// polyfills above, while m3-svelte reads those APIs during module loading.
const { cleanup, render } = await import('@testing-library/svelte')
const { default: TextField } = await import('./TextField.svelte')

afterEach(() => cleanup())

describe('TextField', () => {
  it('keeps generated input labels and error descriptions associated', async () => {
    const view = render(TextField, { label: 'Password' })
    const input = view.container.querySelector('input')

    expect(input).not.toBeNull()
    if (!(input instanceof HTMLInputElement)) throw new Error('TextField did not render an input')

    expect(Array.from(input.labels ?? [], (label) => label.textContent?.trim())).toContain('Password')

    await view.rerender({ label: 'Password', error: 'Password is required' })

    expect(input.getAttribute('aria-invalid')).toBe('true')
    const describedBy = input.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()

    const alert = view.getByRole('alert')
    expect(alert.textContent).toContain('Password is required')
    expect(alert.id).toBe(describedBy)
    expect(view.container.ownerDocument.getElementById(describedBy!)).toBe(alert)
  })
})
