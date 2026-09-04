// web/src/lib/format/download.test.ts — the two-step download is the only
// path left to trigger a file download: mint a ticket by path, then hand its
// `url` to the browser's own navigation. No client code may compose a
// download URL from a path directly. `downloadPath` calls the client's
// `download()` with the path and navigates to the ticket's own `url`; the
// source-tree scan for a surviving hand-built download URL lives in
// `tools/no-download-url.test.ts` (outside `tsconfig.json`'s checked set,
// same as `tools/stylelint-four-px.test.ts`, since it needs `node:fs`).
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'

const download = vi.fn()
vi.mock('../api/client', () => ({
  api: { download: (p: string) => download(p) }
}))

import { downloadPath, triggerUrlDownload } from './download'

beforeEach(() => {
  download.mockReset()
})

afterEach(() => {
  document.body.innerHTML = ''
})

describe('downloadPath', () => {
  it('posts the path and navigates to the ticket url the server returned', async () => {
    const ticket = { token: 't', name: 'report.pdf', url: '/api/v1/files/download/fetch?token=t' }
    download.mockResolvedValue(ticket)

    let clicked: HTMLAnchorElement | null = null
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
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
