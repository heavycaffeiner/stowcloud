import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '../../../src/test/test-utils'
import { StowTextField } from '../../../src/shared/ui/TextField'

afterEach(cleanup)

describe('StowTextField', () => {
  it('reports user input while retaining the controlled value', () => {
    const onValueChange = vi.fn()
    render(<StowTextField value="before" label="Name" onValueChange={onValueChange} />)

    const field = screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' })
    expect(field.value).toBe('before')

    fireEvent.change(field, { target: { value: 'after' } })
    expect(onValueChange).toHaveBeenCalledWith('after')
    expect(field.value).toBe('before')
  })

  it('renders an accessible error message in place of the helper', () => {
    render(<StowTextField value="" label="Name" helper="Shown to others" error="Name is required" />)

    expect(screen.getByRole('alert').textContent).toBe('Name is required')
    expect(screen.queryByText('Shown to others')).toBeNull()
    expect(screen.getByRole('textbox', { name: 'Name' }).getAttribute('aria-invalid')).toBe('true')
  })
})
