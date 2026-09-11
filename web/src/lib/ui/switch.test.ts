import { describe, expect, it, vi } from 'vitest'

// m3-svelte's Switch reads `matchMedia` and `ResizeObserver` at module scope,
// and jsdom has neither. Install them before loading the component.
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
// m3-svelte's ripple calls beginElement on its SMIL animation elements.
// jsdom implements neither, so the prototype is widened once, here, rather
// than at the call site: a well-known DOM object the lib types do not cover.
const svgProto = window.SVGElement.prototype as SVGElement & { beginElement: () => void }
svgProto.beginElement = () => {}
const { render, fireEvent } = await import('@testing-library/svelte')
const { default: Switch } = await import('./Switch.svelte')

function mount(checked: boolean, onchange: (v: boolean) => void) {
  const result = render(Switch, { checked, label: 'Allow SMB access', onchange })
  const input = result.container.querySelector('input[type="checkbox"]') as HTMLInputElement
  return { ...result, input, fireEvent }
}

// A switch renders server state and nothing else. A click proposes a change;
// only the answer moves the control. Both a dismissed confirm dialog and a
// refused request leave the caller's `checked` where it was, and the track has
// to follow that rather than the click.
describe('Switch', () => {
  it('proposes the opposite of what it is showing', async () => {
    let proposed: boolean | null = null
    const { input, fireEvent } = await mount(false, (v) => (proposed = v))

    await fireEvent.click(input)
    expect(proposed).toBe(true)
  })

  it('stays put when the caller does not accept the change', async () => {
    const { input, fireEvent } = await mount(false, () => {})

    await fireEvent.click(input)
    expect(input.checked).toBe(false)
  })

  it('moves when the caller accepts it', async () => {
    const { input, fireEvent, rerender } = await mount(false, () => {})

    await fireEvent.click(input)
    await rerender({ checked: true, label: 'Allow SMB access', onchange: () => {} })
    expect(input.checked).toBe(true)
  })

  it('follows the caller back when a write is undone', async () => {
    const { input, fireEvent, rerender } = await mount(true, () => {})
    expect(input.checked).toBe(true)

    await fireEvent.click(input)
    await rerender({ checked: false, label: 'Allow SMB access', onchange: () => {} })
    expect(input.checked).toBe(false)
  })
})
