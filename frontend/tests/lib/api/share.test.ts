// Mock branch of the public share page's
// client. `VITE_API_MOCK` is forced here the same way setup.test.ts does
// (see that file's header comment): the const is read at import time, so
// ambient `.env` state must not decide whether these pass.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

let getShare: typeof import('../../../src/lib/api/share').getShare
let unlockShare: typeof import('../../../src/lib/api/share').unlockShare
let shareDownloadUrl: typeof import('../../../src/lib/api/share').shareDownloadUrl
let dropUpload: typeof import('../../../src/lib/api/share').dropUpload
let ShareNotFoundError: typeof import('../../../src/lib/api/share').ShareNotFoundError
let SharePasswordRequiredError: typeof import('../../../src/lib/api/share').SharePasswordRequiredError
let ShareTooLargeError: typeof import('../../../src/lib/api/share').ShareTooLargeError

describe('share.ts (mock)', () => {
  beforeEach(async () => {
    vi.stubEnv('VITE_API_MOCK', '1')
    vi.resetModules()
    ;({
      getShare,
      unlockShare,
      shareDownloadUrl,
      dropUpload,
      ShareNotFoundError,
      SharePasswordRequiredError,
      ShareTooLargeError
    } = await import('../../../src/lib/api/share'))
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

  it('a download is an address, and a subpath rides in the query', () => {
    expect(shareDownloadUrl('demo-token')).toContain('/s/demo-token/download')
    expect(shareDownloadUrl('demo-token', 'a/b.txt')).toContain('path=a%2Fb.txt')
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

describe('share.ts (http) unlockShare', () => {
  let ShareUnlockFailedError: typeof import('../../../src/lib/api/share').ShareUnlockFailedError

  beforeEach(async () => {
    vi.stubEnv('VITE_API_MOCK', '0')
    vi.resetModules()
    ;({ unlockShare, ShareNotFoundError, ShareUnlockFailedError } = await import('../../../src/lib/api/share'))
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
  })

  const refusal = (status: number, reasonKey: string): Response =>
    new Response(
      JSON.stringify({ error: { code: 'unprocessable', message: 'refused', detail: { reason_key: reasonKey } } }),
      {
        status
      }
    )

  it('resolves false only for the wrong-password refusal', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => refusal(422, 'fs.link_password'))
    )
    await expect(unlockShare('tok', 'wrong')).resolves.toBe(false)
  })

  it('resolves true when the server sets the ticket', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(null, { status: 204 }))
    )
    await expect(unlockShare('tok', 'right')).resolves.toBe(true)
  })

  it('throws for a refusal that is not the password', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => refusal(422, 'unprocessable'))
    )
    await expect(unlockShare('tok', 'x')).rejects.toBeInstanceOf(ShareUnlockFailedError)
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => refusal(429, 'auth.rate_limited'))
    )
    await expect(unlockShare('tok', 'x')).rejects.toBeInstanceOf(ShareUnlockFailedError)
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('not json', { status: 422 }))
    )
    await expect(unlockShare('tok', 'x')).rejects.toBeInstanceOf(ShareUnlockFailedError)
  })

  it('throws ShareNotFoundError for a dead link', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => refusal(404, 'fs.not_found'))
    )
    await expect(unlockShare('tok', 'x')).rejects.toBeInstanceOf(ShareNotFoundError)
  })
})
