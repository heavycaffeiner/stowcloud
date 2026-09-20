import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render } from '../../test/test-utils'
import { Dialog } from './Dialog'
import { Select } from './Select'

describe('Select', () => {
  it('does not dismiss an ancestor dialog when its dropdown closes', () => {
    const onClose = vi.fn()
    render(
      <Dialog open title="Folder access" onClose={onClose}>
        <Select
          label="Share"
          value="home"
          options={[
            { value: 'home', text: 'Home' },
            { value: 'share', text: 'Share' }
          ]}
        />
      </Dialog>
    )

    const select = document.querySelector('mdui-select')
    expect(select).not.toBeNull()

    fireEvent(select!, new CustomEvent('close', { bubbles: true, composed: true }))

    expect(onClose).not.toHaveBeenCalled()
    expect((document.querySelector('mdui-dialog') as HTMLElement & { open: boolean }).open).toBe(true)
  })
})
