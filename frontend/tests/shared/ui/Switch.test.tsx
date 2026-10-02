import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '../../../src/test/test-utils'
import { StowSwitch } from '../../../src/shared/ui/Switch'

afterEach(cleanup)

describe('StowSwitch', () => {
  it('proposes a change without changing until the controlled value changes', () => {
    const onChange = vi.fn()
    const { rerender } = render(<StowSwitch checked={false} label="Enable feature" onChange={onChange} />)

    const control = screen.getByRole<HTMLInputElement>('switch', { name: 'Enable feature' })
    expect(control.checked).toBe(false)

    fireEvent.click(control)
    expect(onChange).toHaveBeenCalledWith(true)
    expect(control.checked).toBe(false)

    rerender(<StowSwitch checked label="Enable feature" onChange={onChange} />)
    expect(control.checked).toBe(true)
  })

  it('keeps a hidden label as the accessible name', () => {
    render(<StowSwitch label="Enable feature" hideLabel />)

    expect(screen.getByRole('switch', { name: 'Enable feature' })).toBeTruthy()
    expect(screen.queryByText('Enable feature')).toBeNull()
  })
})
