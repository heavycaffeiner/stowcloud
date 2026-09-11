import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '../../test/test-utils'
import { TextField } from './TextField'

describe('TextField', () => {
  it('reports user input while retaining the controlled value', () => {
    const onValueChange = vi.fn()
    render(<TextField value="before" label="Name" onValueChange={onValueChange} />)

    const field = document.querySelector('mdui-text-field') as unknown as HTMLElement & { value: string }
    expect(field.value).toBe('before')

    field.value = 'after'
    fireEvent.input(field)
    expect(onValueChange).toHaveBeenCalledWith('after')
  })

  it('renders an accessible error message for invalid input', () => {
    render(<TextField value="" label="Name" error="Name is required" onValueChange={vi.fn()} />)

    expect(screen.getByRole('alert').textContent).toBe('Name is required')
  })

})
