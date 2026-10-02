import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '../../../src/test/test-utils'
import { StowSelect } from '../../../src/shared/ui/Select'

const options = [
  { value: 'home', label: 'Home' },
  { value: 'share', label: 'Share' }
]

afterEach(cleanup)

describe('StowSelect', () => {
  it('reports the chosen value while retaining the controlled one', () => {
    const onChange = vi.fn()
    render(<StowSelect label="Share" value="home" options={options} onChange={onChange} />)

    const select = screen.getByRole<HTMLSelectElement>('combobox', { name: 'Share' })
    expect(select.value).toBe('home')

    fireEvent.change(select, { target: { value: 'share' } })
    expect(onChange).toHaveBeenCalledWith('share')
    expect(select.value).toBe('home')
  })
})
