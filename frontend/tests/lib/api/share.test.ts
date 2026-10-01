// The public share page's client: how it reads the server's answers.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShareNotFoundError, ShareUnlockFailedError, shareDownloadUrl, unlockShare } from '../../../src/lib/api/share'

describe('shareDownloadUrl', () => {
  it('a download is an address, and a subpath rides in the query', () => {
    expect(shareDownloadUrl('demo-token')).toContain('/s/demo-token/download')
    expect(shareDownloadUrl('demo-token', 'a/b.txt')).toContain('path=a%2Fb.txt')
  })
})

describe('unlockShare', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
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
