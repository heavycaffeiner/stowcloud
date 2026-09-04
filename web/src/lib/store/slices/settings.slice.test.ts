import { describe, expect, it } from 'vitest'
import { isErr, isOk } from '../core/fp'
import {
  createDialogStore,
  createModalStore,
  modalReducer,
  type ModalSnapshot,
  validatePasswordChange
} from './settings.slice'

describe('modalReducer and createModalStore', () => {
  interface DialogPayload {
    name: string
    readOnly: boolean
  }

  const initialPayload: DialogPayload = { name: '', readOnly: false }

  it('handles modal lifecycle transitions', () => {
    const initial: ModalSnapshot<DialogPayload> = {
      isOpen: false,
      status: 'idle',
      data: initialPayload,
      error: null
    }

    const opened = modalReducer(initial, { type: 'OPEN', data: { name: 'test', readOnly: true } })
    expect(opened.isOpen).toBe(true)
    expect(opened.data.name).toBe('test')
    expect(opened.data.readOnly).toBe(true)

    const submitting = modalReducer(opened, { type: 'SUBMIT' })
    expect(submitting.status).toBe('submitting')

    const failed = modalReducer(submitting, { type: 'FAIL', error: 'Network error' })
    expect(failed.status).toBe('error')
    expect(failed.error).toBe('Network error')

    const succeeded = modalReducer(failed, { type: 'SUCCEED' })
    expect(succeeded.isOpen).toBe(false)
    expect(succeeded.status).toBe('success')
  })

  it('manages modal store actions', () => {
    const store = createModalStore<DialogPayload>(initialPayload)
    expect(store.getState().isOpen).toBe(false)

    store.open({ name: 'my-token', readOnly: false })
    expect(store.getState().isOpen).toBe(true)
    expect(store.getState().data.name).toBe('my-token')

    store.setData({ readOnly: true })
    expect(store.getState().data.readOnly).toBe(true)

    store.close()
    expect(store.getState().isOpen).toBe(false)
  })
  it('manages simple dialog store lifecycle', () => {
    const dialog = createDialogStore()
    expect(dialog.getState().isOpen).toBe(false)

    dialog.open()
    expect(dialog.getState().isOpen).toBe(true)
    expect(dialog.getState().status).toBe('idle')

    dialog.submit()
    expect(dialog.getState().status).toBe('submitting')

    dialog.fail('bad request')
    expect(dialog.getState().status).toBe('error')
    expect(dialog.getState().error).toBe('bad request')

    dialog.succeed()
    expect(dialog.getState().isOpen).toBe(false)
    expect(dialog.getState().status).toBe('success')
  })
})

describe('validatePasswordChange', () => {
  it('rejects short passwords with minimum parameter', () => {
    const res = validatePasswordChange('curr', 'short', 'short', 10)
    expect(isErr(res)).toBe(true)
    if (isErr(res) && res.error.type === 'too_short') {
      expect(res.error.type).toBe('too_short')
      expect(res.error.min).toBe(10)
    }
  })

  it('rejects non-matching passwords', () => {
    const res = validatePasswordChange('curr', 'longenoughpass', 'differentpass', 10)
    expect(isErr(res)).toBe(true)
    if (isErr(res)) {
      expect(res.error.type).toBe('mismatch')
    }
  })

  it('accepts matching and valid passwords', () => {
    const res = validatePasswordChange('curr', 'longenoughpass', 'longenoughpass', 10)
    expect(isOk(res)).toBe(true)
    if (isOk(res)) {
      expect(res.value.next).toBe('longenoughpass')
    }
  })
})
