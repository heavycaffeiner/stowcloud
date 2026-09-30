import { StrictMode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '../../src/test/test-utils'
import { Modal } from '../../src/ui/Modal'

afterEach(cleanup)

describe('Modal', () => {
  it('stays open when a close from a remounted effect lands after it shows again', () => {
    const onClose = vi.fn()
    render(
      <StrictMode>
        <Modal open label="Main menu" onClose={onClose}>
          <button type="button">Files</button>
        </Modal>
      </StrictMode>
    )
    const dialog = document.querySelector('dialog') as HTMLDialogElement

    fireEvent(dialog, new Event('close'))
    expect(onClose).not.toHaveBeenCalled()

    dialog.removeAttribute('open')
    fireEvent(dialog, new Event('close'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
