// How the sign-in and first-run calls read the server's answers.
import { QueryClientProvider } from '@tanstack/react-query'
import { renderHook } from '@testing-library/react'
import type { PropsWithChildren } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { startOidcLogin, useCreateInitialAdmin } from '../../../src/features/auth/api'
import { createTestQueryClient } from '../../../src/test/test-utils'

afterEach(() => {
  vi.unstubAllGlobals()
})

const json = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

describe('startOidcLogin', () => {
  /** `window.location.href` is a real navigation under jsdom, so the
   *  property is replaced with a plain writable one for the duration. */
  async function captureNavigation(run: () => Promise<void>): Promise<string> {
    const original = Object.getOwnPropertyDescriptor(window, 'location')
    let href = ''
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: {
        get href() {
          return href
        },
        set href(v: string) {
          href = v
        }
      }
    })
    try {
      await run()
    } finally {
      if (original) Object.defineProperty(window, 'location', original)
    }
    return href
  }

  it('navigates to the authorization URL the server returns', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json(200, { authorize_url: 'https://idp.example.test/authorize?state=abc' }))
    )
    await expect(captureNavigation(() => startOidcLogin())).resolves.toBe(
      'https://idp.example.test/authorize?state=abc'
    )
  })

  it('does not navigate to a URL that is not https', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json(200, { authorize_url: 'javascript:alert(1)' }))
    )
    await expect(captureNavigation(() => startOidcLogin())).resolves.toBe('')
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json(200, { authorize_url: 'http://idp.example.test/authorize' }))
    )
    await expect(captureNavigation(() => startOidcLogin())).resolves.toBe('')
  })

  it('percent-encodes returnTo in the start request', async () => {
    const fetchMock = vi.fn(async (_request: Request) => json(200, { authorize_url: 'https://idp.example.test/a' }))
    vi.stubGlobal('fetch', fetchMock)
    await captureNavigation(() => startOidcLogin('/b/Docs?sort=name&order=desc'))
    const request = fetchMock.mock.calls[0]?.[0]
    expect(request?.url).toMatch(/\/api\/v1\/auth\/oidc\/start\?return_to=%2Fb%2FDocs%3Fsort%3Dname%26order%3Ddesc$/)
  })
})

describe('useCreateInitialAdmin', () => {
  function renderCreate() {
    const queryClient = createTestQueryClient()
    const wrapper = ({ children }: PropsWithChildren) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    )
    return renderHook(() => useCreateInitialAdmin(), { wrapper }).result
  }

  const request = {
    token: 'SETUP-TOKEN',
    username: 'root',
    password: 'longenoughpw',
    app_hosts: ['localhost'],
    trusted_proxies: []
  }

  it('preserves field findings when the server rejects setup validation', async () => {
    const findings = [{ section: 'network', field: 'app_hosts', reason: 'host.invalid', blocking: true }]
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json(422, { findings }))
    )
    const result = renderCreate()
    await expect(result.current.mutateAsync(request)).rejects.toMatchObject({ name: 'SetupValidationError', findings })
  })

  it('reports a committed account separately when the optional first share failed', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => json(200, { warnings: [], share_failed: true }))
    )
    const result = renderCreate()
    await expect(
      result.current.mutateAsync({ ...request, first_share: { name: 'Documents', host: '/srv/documents' } })
    ).resolves.toEqual({ warnings: [], share_failed: true })
  })
})
