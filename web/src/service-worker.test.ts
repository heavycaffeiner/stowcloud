// web/src/service-worker.test.ts: unit coverage for the pure pieces of the
// worker: header building, Range parsing, and the one-shot download claim.
// The `fetch`/`install`/`activate` listeners themselves need a real
// `ServiceWorkerGlobalScope` (`self.clients`, `FetchEvent`) this jsdom
// environment does not provide, so they are exercised by the manual browser
// run recorded in the delivery notes instead of here.
import { describe, expect, it } from 'vitest'
import { contentDisposition, handleDownload, mediaSuccessResponseInit, parseRangeHeader, receiveWorkerMessage } from './service-worker'

describe('contentDisposition', () => {
  it('carries an ASCII fallback and the UTF-8 percent-encoded real name, in that order', () => {
    // \uBCF4\uACE0\uC11C (final).pdf is Korean; escaped so no source file
    // outside the i18n catalogues carries literal Korean text.
    const header = contentDisposition('\uBCF4\uACE0\uC11C (final).pdf')
    const fallbackAt = header.indexOf('filename=')
    const utf8At = header.indexOf("filename*=UTF-8''")
    expect(fallbackAt).toBeGreaterThan(-1)
    expect(utf8At).toBeGreaterThan(fallbackAt)
    expect(header).toContain('filename="___ (final).pdf"')
    expect(header).toContain(`filename*=UTF-8''${encodeURIComponent('\uBCF4\uACE0\uC11C (final).pdf')}`)
  })

  it('leaves a plain ASCII name untouched in the fallback', () => {
    const header = contentDisposition('report.pdf')
    expect(header).toContain('filename="report.pdf"')
  })

  it('replaces a double quote in the ASCII fallback so the header cannot be broken out of', () => {
    const header = contentDisposition('a "quoted" name.txt')
    expect(header).toContain(`filename="a 'quoted' name.txt"`)
  })
})

describe('parseRangeHeader', () => {
  it('returns null for no header at all', () => {
    expect(parseRangeHeader(null)).toBeNull()
  })

  it('parses a bounded range', () => {
    expect(parseRangeHeader('bytes=200-999')).toEqual({ start: 200, end: 999 })
  })

  it('parses an open-ended range, leaving end unresolved', () => {
    expect(parseRangeHeader('bytes=200-')).toEqual({ start: 200, end: undefined })
  })

  it('parses a suffix range (the last N bytes)', () => {
    expect(parseRangeHeader('bytes=-500')).toEqual({ suffixLength: 500 })
  })

  it('treats a malformed or non-byte range as no range at all rather than throwing', () => {
    expect(parseRangeHeader('bytes=0-99,200-299')).toBeNull()
    expect(parseRangeHeader('items=0-9')).toBeNull()
    expect(parseRangeHeader('bytes=')).toBeNull()
  })
})

describe('mediaSuccessResponseInit', () => {
  const stream = new ReadableStream<Uint8Array>()

  it('answers 200 with the full Content-Length when no Range header was sent', () => {
    const reply = { ok: true as const, start: 0, end: 999, totalSize: 1000, contentType: 'video/mp4', stream }
    const init = mediaSuccessResponseInit(reply, false)
    expect(init).toEqual({
      status: 200,
      headers: {
        'Content-Type': 'video/mp4',
        'Accept-Ranges': 'bytes',
        'X-Content-Type-Options': 'nosniff',
        'Content-Length': '1000'
      }
    })
  })

  it('answers 206 with Content-Range when a Range header was sent, even covering the whole file', () => {
    const reply = { ok: true as const, start: 0, end: 999, totalSize: 1000, contentType: 'video/mp4', stream }
    const init = mediaSuccessResponseInit(reply, true)
    expect(init).toEqual({
      status: 206,
      headers: {
        'Content-Type': 'video/mp4',
        'Accept-Ranges': 'bytes',
        'X-Content-Type-Options': 'nosniff',
        'Content-Length': '1000',
        'Content-Range': 'bytes 0-999/1000'
      }
    })
  })

  it('answers a sub-range with the trimmed Content-Length and the real Content-Range', () => {
    const reply = { ok: true as const, start: 200, end: 699, totalSize: 1000, contentType: 'image/png', stream }
    const init = mediaSuccessResponseInit(reply, true)
    expect(init).toEqual({
      status: 206,
      headers: {
        'Content-Type': 'image/png',
        'Accept-Ranges': 'bytes',
        'X-Content-Type-Options': 'nosniff',
        'Content-Length': '500',
        'Content-Range': 'bytes 200-699/1000'
      }
    })
  })

  it('ignores Range for a zero-length file and always answers 200 with Content-Length 0', () => {
    const reply = { ok: true as const, start: 0, end: -1, totalSize: 0, contentType: 'text/plain', stream }
    expect(mediaSuccessResponseInit(reply, true).status).toBe(200)
    expect(mediaSuccessResponseInit(reply, true).headers).toMatchObject({ 'Content-Length': '0' })
  })

  it('serves a non-raster content type, image/svg+xml above all, as application/octet-stream instead', () => {
    const reply = { ok: true as const, start: 0, end: 99, totalSize: 100, contentType: 'image/svg+xml', stream }
    const init = mediaSuccessResponseInit(reply, false)
    expect(init).toMatchObject({ headers: { 'Content-Type': 'application/octet-stream' } })
  })
})

describe('the one-shot download claim', () => {
  function streamOf(bytes: number[]): ReadableStream<Uint8Array> {
    return new ReadableStream({
      start(controller) {
        controller.enqueue(new Uint8Array(bytes))
        controller.close()
      }
    })
  }

  it('answers the transferred stream with the right headers, once', async () => {
    receiveWorkerMessage({ kind: 'sc-download', id: 'abc', filename: 'a.zip', size: 3, stream: streamOf([1, 2, 3]) }, 'client-a')

    const res = handleDownload('abc', 'client-a')
    expect(res.status).toBe(200)
    expect(res.headers.get('Content-Type')).toBe('application/octet-stream')
    expect(res.headers.get('Content-Length')).toBe('3')
    expect(res.headers.get('Content-Disposition')).toContain('filename="a.zip"')
    expect(res.headers.get('X-Content-Type-Options')).toBe('nosniff')

    const body = await res.arrayBuffer()
    expect(Array.from(new Uint8Array(body))).toEqual([1, 2, 3])
  })

  it('answers 410 for an id that was never sent', () => {
    const res = handleDownload('never-registered', 'client-a')
    expect(res.status).toBe(410)
  })

  it('forgets the id the moment it is claimed, so a second fetch 410s', () => {
    receiveWorkerMessage({ kind: 'sc-download', id: 'once', filename: 'a.txt', stream: streamOf([9]) }, 'client-a')
    expect(handleDownload('once', 'client-a').status).toBe(200)
    expect(handleDownload('once', 'client-a').status).toBe(410)
  })

  it('omits Content-Length when no size was given, since a zip stream has none to report', () => {
    receiveWorkerMessage({ kind: 'sc-download', id: 'no-size', filename: 'archive.zip', stream: streamOf([1]) }, 'client-a')
    const res = handleDownload('no-size', 'client-a')
    expect(res.headers.has('Content-Length')).toBe(false)
  })

  it('ignores a message that is not a download-start message', () => {
    receiveWorkerMessage({ kind: 'something-else' }, 'client-a')
    receiveWorkerMessage(null, 'client-a')
    receiveWorkerMessage('a string', 'client-a')
    expect(handleDownload('something-else', 'client-a').status).toBe(410)
  })

  it('refuses a claim from a client other than the one that posted the download, without consuming it', () => {
    receiveWorkerMessage({ kind: 'sc-download', id: 'owned', filename: 'a.txt', stream: streamOf([1]) }, 'client-a')
    const stolen = handleDownload('owned', 'client-b')
    expect(stolen.status).toBe(403)
    const real = handleDownload('owned', 'client-a')
    expect(real.status).toBe(200)
  })
})
