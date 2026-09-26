import { beforeEach, describe, expect, it, vi } from 'vitest'
import { uploads, type UploadItem } from '../store/upload.store'
import { addFiles, handle } from './queue'

vi.mock('../crypto/encrypted-shares', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../crypto/encrypted-shares')>()
  return {
    ...actual,
    encryptionForLabel: vi.fn(async () => ({ salt: 'test-salt' })),
    shareLabelOf: vi.fn(() => 'Encrypted')
  }
})

vi.mock('../crypto/e2ee', () => ({
  encryptForUpload: vi.fn(async (file: File) => new Uint8Array(await file.arrayBuffer())),
  FileTooLargeError: class FileTooLargeError extends Error {},
  LockedSessionError: class LockedSessionError extends Error {}
}))

const workerMessages: unknown[] = []
class FakeWorker {
  addEventListener(): void {}
  postMessage(message: unknown): void {
    workerMessages.push(message)
  }
}
vi.stubGlobal('Worker', FakeWorker)

interface AddMessage {
  t: 'add'
  items: { id: string }[]
}

function isAddMessage(message: unknown): message is AddMessage {
  if (typeof message !== 'object' || message === null || !('t' in message) || message.t !== 'add') return false
  if (!('items' in message) || !Array.isArray(message.items)) return false
  return message.items.every(
    (item) => typeof item === 'object' && item !== null && 'id' in item && typeof item.id === 'string'
  )
}

function item(overrides: Partial<UploadItem> = {}): UploadItem {
  return {
    id: 'u1',
    name: 'photo.png',
    dest: '/Files',
    total: 100,
    sent: 40,
    rate: 1,
    etaSec: 60,
    status: 'uploading',
    ...overrides
  }
}

beforeEach(() => {
  uploads.reset()
  uploads.queue(item())
})

describe('encrypted upload preparation', () => {
  it('retains only one prepared ciphertext until the worker releases it', async () => {
    workerMessages.length = 0
    const completion = addFiles(
      [
        new File(['first'], 'first.txt', { lastModified: 1 }),
        new File(['second'], 'second.txt', { lastModified: 2 })
      ],
      '/Encrypted'
    )

    const addMessages = () => workerMessages.filter(isAddMessage)
    await vi.waitFor(() => expect(addMessages()).toHaveLength(1))
    const firstID = addMessages()[0].items[0].id

    handle({ t: 'released', id: firstID })
    await completion
    expect(addMessages()).toHaveLength(2)

    handle({ t: 'released', id: addMessages()[1].items[0].id })
  })
})

describe('upload cancellation state transitions', () => {
  it('keeps an uncertain cleanup as an error until cancellation is confirmed', () => {
    handle({ t: 'error', id: 'u1', code: 'upload.cleanup_pending', message: 'upload.cleanup_pending' })
    handle({ t: 'progress', id: 'u1', sent: 100, total: 100, rate: 0, etaSec: 0 })

    expect(uploads.peek().items[0]).toMatchObject({
      status: 'error',
      errorCode: 'upload.cleanup_pending',
      message: 'upload.cleanup_pending',
      sent: 100
    })

    handle({ t: 'canceled', id: 'u1' })

    expect(uploads.peek().items[0]).toMatchObject({ status: 'canceled' })
    expect(uploads.peek().items[0].errorCode).toBeUndefined()
  })

  it('does not let a late progress event undo a confirmed cancellation', () => {
    handle({ t: 'canceled', id: 'u1' })
    handle({ t: 'progress', id: 'u1', sent: 100, total: 100, rate: 0, etaSec: 0 })

    expect(uploads.peek().items[0]).toMatchObject({ status: 'canceled', sent: 100 })
  })
})
