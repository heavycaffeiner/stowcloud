import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render } from '../../test/test-utils'
import { Switch } from './Switch'
describe('Switch', () => {
  it('proposes a change without changing until the controlled value changes', () => {
    const onChange = vi.fn()
    const { rerender } = render(<Switch checked={false} label="Enable feature" onChange={onChange} />)

    const control = document.querySelector('mdui-switch') as HTMLElement & { checked: boolean }
    expect(control.checked).toBe(false)

    control.checked = true
    fireEvent.change(control)
    expect(onChange).toHaveBeenCalledWith(true)
    expect(control.checked).toBe(false)

    rerender(<Switch checked={true} label="Enable feature" onChange={onChange} />)
    expect(control.checked).toBe(true)
  })
})
