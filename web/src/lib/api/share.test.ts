// web/src/lib/api/share.test.ts — mock branch of the public share page's
// client. `VITE_API_MOCK` is forced here the same way setup.test.ts does
// (see that file's header comment): the const is read at import time, so
// ambient `.env` state must not decide whether these pass.
import { beforeEach, describe, expect, it, vi } from 'vitest'

let getShare: typeof import('./share').getShare
let unlockShare: typeof import('./share').unlockShare
let requestShareDownload: typeof import('./share').requestShareDownload
let dropUpload: typeof import('./share').dropUpload
let ShareNotFoundError: typeof import('./share').ShareNotFoundError
let SharePasswordRequiredError: typeof import('./share').SharePasswordRequiredError
let ShareTooLargeError: typeof import('./share').ShareTooLargeError

describe('share.ts (mock)', () => {
  beforeEach(async () => {
    vi.stubEnv('VITE_API_MOCK', '1')
    vi.resetModules()
    ;({
      getShare,
      unlockShare,
      requestShareDownload,
      dropUpload,
      ShareNotFoundError,
      SharePasswordRequiredError,
      ShareTooLargeError
    } = await import('./share'))
  })

  it('returns a folder share with entries', async () => {
    const info = await getShare('demo-token')
    expect(info.isDir).toBe(true)
    expect(info.isDrop).toBe(false)
    expect(info.entries?.length).toBeGreaterThan(0)
  })

  it('throws ShareNotFoundError for an expired token', async () => {
    await expect(getShare('expired')).rejects.toBeInstanceOf(ShareNotFoundError)
  })

  it('throws SharePasswordRequiredError for a locked token', async () => {
    await expect(getShare('locked')).rejects.toBeInstanceOf(SharePasswordRequiredError)
  })

  it('unlockShare rejects the wrong password and accepts the right one', async () => {
    await expect(unlockShare('locked', 'wrong')).resolves.toBe(false)
    await expect(unlockShare('locked', 'hunter2')).resolves.toBe(true)
  })

  it('requestShareDownload resolves to a url', async () => {
    const url = await requestShareDownload('demo-token')
    expect(typeof url).toBe('string')
    expect(url.length).toBeGreaterThan(0)
  })

  it('a drop link lists nothing and carries its own upload ceiling', async () => {
    const info = await getShare('drop')
    expect(info.isDrop).toBe(true)
    expect(info.entries).toBeNull()
    expect(info.canDownload).toBe(false)
    expect(info.maxUploadBytes).toBeGreaterThan(0)
  })

  it('a download link is told no upload ceiling', async () => {
    const info = await getShare('demo-token')
    expect(info.maxUploadBytes).toBeNull()
  })

  it('dropUpload renames a colliding name instead of overwriting', async () => {
    const first = await dropUpload('drop', new File(['a'], 'report.pdf'))
    expect(first).toBe('report.pdf')
    const second = await dropUpload('drop', new File(['b'], 'report.pdf'))
    expect(second).toBe('report (1).pdf')
    const third = await dropUpload('drop', new File(['c'], 'report.pdf'))
    expect(third).toBe('report (2).pdf')
  })

  it('dropUpload refuses a file over the ceiling', async () => {
    const { maxUploadBytes } = await getShare('drop')
    const tooBig = new File([new Uint8Array((maxUploadBytes ?? 0) + 1)], 'huge.bin')
    await expect(dropUpload('drop', tooBig)).rejects.toBeInstanceOf(ShareTooLargeError)
  })
})
