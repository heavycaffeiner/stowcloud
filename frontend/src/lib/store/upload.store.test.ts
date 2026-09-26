import { beforeEach, describe, expect, it } from 'vitest'
import { uploads, type UploadItem } from './upload.store'

function item(over: Partial<UploadItem> = {}): UploadItem {
  return {
    id: 'u1',
    name: 'photo.png',
    dest: '/Files',
    total: 100,
    sent: 0,
    rate: 0,
    etaSec: Infinity,
    status: 'uploading',
    ...over
  }
}

beforeEach(() => {
  uploads.reset()
})

describe('the upload queue', () => {
  it('opens the tray for a newly queued file', () => {
    uploads.queue(item())
    expect(uploads.peek().open).toBe(true)
    expect(uploads.peek().items).toHaveLength(1)
  })

  it('patches one row and leaves the others alone', () => {
    uploads.queue(item({ id: 'u1' }))
    uploads.queue(item({ id: 'u2' }))
    uploads.patch('u1', { sent: 60 })
    expect(uploads.peek().items.map((i) => i.sent)).toEqual([60, 0])
  })

  it('ignores a patch for a row that is gone', () => {
    uploads.queue(item({ id: 'u1' }))
    uploads.patch('u2', { sent: 60 })
    expect(uploads.peek().items).toEqual([item({ id: 'u1' })])
  })

  it('keeps what is still running when finished rows are cleared', () => {
    uploads.queue(item({ id: 'u1', status: 'done' }))
    uploads.queue(item({ id: 'u2', status: 'canceled' }))
    uploads.queue(item({ id: 'u3', status: 'uploading' }))
    uploads.queue(item({ id: 'u4', status: 'error' }))
    uploads.clearFinished()
    // An error stays: it is the only record that the file did not arrive.
    expect(uploads.peek().items.map((i) => i.id)).toEqual(['u3', 'u4'])
  })

  it('dismisses only the row asked for', () => {
    uploads.queue(item({ id: 'u1' }))
    uploads.queue(item({ id: 'u2' }))
    uploads.dismiss('u1')
    expect(uploads.peek().items.map((i) => i.id)).toEqual(['u2'])
  })

  it('reports a row status so a late chunk cannot undo a pause', () => {
    uploads.queue(item({ id: 'u1' }))
    uploads.patch('u1', { status: 'paused' })
    expect(uploads.statusOf('u1')).toBe('paused')
    expect(uploads.statusOf('missing')).toBeUndefined()
  })
})
