import { describe, expect, it } from 'vitest'
import {
  createUploadStore,
  patchItemInList,
  uploadReducer,
  type UploadItem,
  type UploadSnapshot
} from './upload.slice'

const sampleItem: UploadItem = {
  id: 'up-1',
  name: 'file.txt',
  dest: '/dest',
  total: 1000,
  sent: 0,
  rate: 0,
  etaSec: 10,
  status: 'queued'
}

describe('uploadReducer and pure helpers', () => {
  it('patchItemInList returns updated list without mutating inputs', () => {
    const original = [sampleItem]
    const patched = patchItemInList(original, 'up-1', { sent: 500, status: 'uploading' })

    expect(patched[0].sent).toBe(500)
    expect(patched[0].status).toBe('uploading')
    expect(original[0].sent).toBe(0)
  })

  it('queuing item opens tray and appends to items', () => {
    const initial: UploadSnapshot = { items: [], open: false }
    const next = uploadReducer(initial, { type: 'QUEUE_ITEM', item: sampleItem })

    expect(next.open).toBe(true)
    expect(next.items.length).toBe(1)
    expect(next.items[0].id).toBe('up-1')
  })

  it('clearing finished filters done and canceled items', () => {
    const list: UploadItem[] = [
      { ...sampleItem, id: '1', status: 'done' },
      { ...sampleItem, id: '2', status: 'uploading' },
      { ...sampleItem, id: '3', status: 'canceled' },
      { ...sampleItem, id: '4', status: 'paused' }
    ]
    const state: UploadSnapshot = { items: list, open: true }
    const cleared = uploadReducer(state, { type: 'CLEAR_FINISHED' })

    expect(cleared.items.map((x) => x.id)).toEqual(['2', '4'])
  })

  it('dismissing item removes only matching id', () => {
    const list: UploadItem[] = [
      { ...sampleItem, id: '1' },
      { ...sampleItem, id: '2' }
    ]
    const state: UploadSnapshot = { items: list, open: true }
    const dismissed = uploadReducer(state, { type: 'DISMISS_ITEM', id: '1' })

    expect(dismissed.items.map((x) => x.id)).toEqual(['2'])
  })
})

describe('uploadStore', () => {
  it('dispatches actions and updates snapshot', () => {
    const store = createUploadStore()

    store.dispatch({ type: 'QUEUE_ITEM', item: sampleItem })
    expect(store.getState().items.length).toBe(1)
    expect(store.getState().open).toBe(true)

    store.dispatch({ type: 'SET_OPEN', open: false })
    expect(store.getState().open).toBe(false)
  })
})
