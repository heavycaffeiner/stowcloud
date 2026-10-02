// A plain-share download mints a ticket by path and hands its `url` to the
// browser's own navigation; no client code composes a download URL from a path.
// An encrypted path skips the ticket and goes through `downloadEncryptedFile`,
// checked here only at that routing boundary.
import { beforeEach, describe, expect, it, vi } from 'vitest'

const download = vi.fn()
const stat = vi.fn()
const shareEncryptionList = vi.fn()
vi.mock('../../../src/features/files/api', () => ({
  download: (p: string) => download(p),
  stat: (p: string) => stat(p)
}))
vi.mock('../../../src/features/shares/api', () => ({
  fetchShareEncryptions: () => shareEncryptionList()
}))

const downloadEncryptedFile = vi.fn()
vi.mock('../../../src/features/files/download-sw', () => ({
  downloadEncryptedFile: (entry: unknown) => downloadEncryptedFile(entry)
}))

import { invalidateEncryptedShares } from '../../../src/features/shares/encrypted-shares'
import { downloadPath, triggerUrlDownload } from '../../../src/features/files/download'

beforeEach(() => {
  download.mockReset()
  stat.mockReset()
  downloadEncryptedFile.mockReset()
  shareEncryptionList.mockReset()
  shareEncryptionList.mockResolvedValue([])
  invalidateEncryptedShares()
})

describe('downloadPath', () => {
  it('posts the path and navigates to the ticket url the server returned', async () => {
    const ticket = { token: 't', name: 'report.pdf', url: '/api/v1/files/download/fetch?token=t' }
    download.mockResolvedValue(ticket)

    let clicked: HTMLAnchorElement | null = null
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
      this: HTMLAnchorElement
    ) {
      clicked = this
    })

    await downloadPath('/home/report.pdf')

    // The path went to the mint call, not into any URL this module built.
    expect(download).toHaveBeenCalledWith('/home/report.pdf')
    expect(download).toHaveBeenCalledTimes(1)

    // The navigation carries exactly what the ticket named: the opaque `url`
    // and a `download` attribute, so the browser's own manager fetches it
    // rather than this tab reading the body.
    expect(clicked).not.toBeNull()
    expect(clicked!.href).toContain(ticket.url)
    expect(clicked!.download).toBe(ticket.name)

    clickSpy.mockRestore()
  })

  it('propagates a refusal rather than navigating anywhere', async () => {
    const refusal = new Error('folder')
    download.mockRejectedValue(refusal)
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click')

    await expect(downloadPath('/home/a-folder')).rejects.toBe(refusal)
    expect(clickSpy).not.toHaveBeenCalled()

    clickSpy.mockRestore()
  })

  it('routes an encrypted path through downloadEncryptedFile instead of minting a ticket', async () => {
    shareEncryptionList.mockResolvedValue([
      { share: 1, labels: ['home'], scheme: 'rclone-crypt-v1', salt: 's'.repeat(22), verifier: 'v', createdNs: 0 }
    ])
    const entry = { name: 'report.pdf', path: '/home/report.pdf', kind: 'file', size: 10, content: 'claim' }
    stat.mockResolvedValue(entry)

    await downloadPath('/home/report.pdf')

    expect(stat).toHaveBeenCalledWith('/home/report.pdf')
    expect(downloadEncryptedFile).toHaveBeenCalledWith(entry)
    expect(download).not.toHaveBeenCalled()
  })
})

describe('triggerUrlDownload', () => {
  it('builds an anchor with a download attribute rather than reading the body', () => {
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    triggerUrlDownload('/api/v1/files/download/fetch?token=abc', 'a.txt')
    expect(clickSpy).toHaveBeenCalledTimes(1)
    // No trace of the element is left behind for a second call to collide with.
    expect(document.body.querySelector('a')).toBeNull()
    clickSpy.mockRestore()
  })
})
