import { beforeEach, describe, expect, it, vi } from 'vitest'
import { dropUpload, ShareTooLargeError } from '../../../../lib/api/share'
import { createPublicShareQueue } from './public-share-queue'

vi.mock('../../../../lib/api/share', () => ({
  dropUpload: vi.fn(),
  ShareTooLargeError: class extends Error {}
}))

beforeEach(() => vi.mocked(dropUpload).mockReset())

describe('public share upload queue', () => {
  it('uploads in order, reports a failure, and retries without reuploading completed items', async () => {
    let finishFirst!: (name: string) => void
    vi.mocked(dropUpload)
      .mockImplementationOnce(() => new Promise<string>((resolve) => { finishFirst = resolve }))
      .mockRejectedValueOnce(new ShareTooLargeError())
      .mockResolvedValueOnce('second (1).txt')
    const states: { uploading: boolean; statuses: string[] }[] = []
    const queue = createPublicShareQueue({ token: 'first', setState: (state) => states.push({ uploading: state.uploading ?? false, statuses: state.queue?.map((item) => item.status) ?? [] }) })

    queue.add([new File(['1'], 'first.txt'), new File(['2'], 'second.txt')], null)
    await vi.waitFor(() => expect(dropUpload).toHaveBeenCalledTimes(1))
    expect(vi.mocked(dropUpload).mock.calls[0]?.[0]).toBe('first')
    finishFirst('first.txt')
    await vi.waitFor(() => expect(queue.items[1]?.status).toBe('error'))
    expect(queue.items[0]?.status).toBe('done')
    queue.retry(1)
    await vi.waitFor(() => expect(queue.items[1]?.status).toBe('done'))
    expect(queue.items[1]?.storedAs).toBe('second (1).txt')
    expect(vi.mocked(dropUpload)).toHaveBeenCalledTimes(3)
    expect(states.at(-1)).toEqual({ uploading: false, statuses: ['done', 'done'] })
  })

  it('does not publish an old token upload after switching shares', async () => {
    let finish!: (name: string) => void
    vi.mocked(dropUpload).mockImplementationOnce(() => new Promise<string>((resolve) => { finish = resolve }))
    let active = true
    const publish = vi.fn()
    const queue = createPublicShareQueue({ token: 'old', setState: publish, isActive: () => active })
    queue.add([new File(['x'], 'old.txt')], null)
    await vi.waitFor(() => expect(dropUpload).toHaveBeenCalledTimes(1))
    const callsBeforeSwitch = publish.mock.calls.length
    active = false
    finish('old.txt')
    await vi.waitFor(() => expect(queue.items[0]?.status).toBe('done'))
    expect(publish).toHaveBeenCalledTimes(callsBeforeSwitch)
  })
})
