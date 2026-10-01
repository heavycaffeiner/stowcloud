import { afterEach, describe, expect, it, vi } from 'vitest'
import { HttpTransport } from '../../../src/lib/upload/transport'
import type { Transport } from '../../../src/lib/upload/transport'

describe('HttpTransport.patchChunk with XMLHttpRequest', () => {
  const origXHR = globalThis.XMLHttpRequest

  afterEach(() => {
    globalThis.XMLHttpRequest = origXHR
    vi.restoreAllMocks()
  })

  it('delegates to xhr.upload.onprogress and reports incremental bytes', async () => {
    let uploadProgressHandler: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null = null
    let sendCalled = false

    class MockXHR {
      upload = {
        set onprogress(handler: typeof uploadProgressHandler) {
          uploadProgressHandler = handler
        }
      }
      status = 200
      open = vi.fn()
      setRequestHeader = vi.fn()
      withCredentials = false
      onload: (() => void) | null = null
      onerror: (() => void) | null = null
      onabort: (() => void) | null = null
      getResponseHeader(name: string) {
        if (name === 'Upload-Offset') return '500'
        return null
      }
      send(_body: unknown) {
        sendCalled = true
        if (uploadProgressHandler) {
          uploadProgressHandler({ lengthComputable: true, loaded: 250, total: 500 })
          uploadProgressHandler({ lengthComputable: true, loaded: 500, total: 500 })
        }
        this.onload?.()
      }
      abort = vi.fn()
    }

    // Mock global XHR for testing browser environment path
    globalThis.XMLHttpRequest = MockXHR as unknown as typeof XMLHttpRequest

    const progress: number[] = []
    const blob = new Blob([new Uint8Array(500)])
    const transport: Transport = new HttpTransport()

    const result = await transport.patchChunk('sess-1', 0, blob, undefined, (bytes) => {
      progress.push(bytes)
    })

    expect(sendCalled).toBe(true)
    expect(progress).toEqual([250, 500])
    expect(result.offset).toBe(500)
  })
})
