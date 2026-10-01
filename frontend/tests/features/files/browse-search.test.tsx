import { cleanup, render, screen } from '@testing-library/react'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  useParams
} from '@tanstack/react-router'
import { afterEach, describe, expect, it } from 'vitest'
import { splatOf, splatPath, validateBrowseSearch } from '../../../src/features/files/browse-search'
import { parseSearch, stringifySearch } from '../../../src/lib/url-search'

function Folder() {
  return <output>{splatPath(useParams({ strict: false, select: (params) => params._splat }))}</output>
}

/** Builds the link for `path`, opens it in a fresh router and reads the path back. */
async function roundTrip(path: string): Promise<{ href: string; path: string }> {
  const rootRoute = createRootRoute()
  const browseRoute = createRoute({ getParentRoute: () => rootRoute, path: 'b/$', component: Folder })
  const routeTree = rootRoute.addChildren([browseRoute])
  const href = createRouter({ routeTree }).buildLocation({ to: '/b/$', params: splatOf(path) }).href
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [href] }) })
  render(<RouterProvider router={router} />)
  return { href, path: (await screen.findByRole('status')).textContent ?? '' }
}

afterEach(cleanup)

describe('folder links', () => {
  it.each([
    '/docs',
    '/docs/a b',
    '/docs/report #3.txt',
    '/docs/what?.txt',
    '/docs/100% done',
    '/docs/back\\slash',
    '/docs/a+b&c=d',
    '/docs/한글 폴더',
    '/'
  ])('bring %s back unchanged', async (path) => {
    const result = await roundTrip(path)
    expect(result.path).toBe(path)
    expect(result.href).not.toMatch(/[#?]/)
  })
})

describe('validateBrowseSearch', () => {
  it('keeps known filters and drops anything else', () => {
    expect(validateBrowseSearch({ type: 'images', date: '7days' })).toEqual({ type: 'images', date: '7days' })
    expect(validateBrowseSearch({ type: 'executables', date: 7, preview: ['a'], focus: {} })).toEqual({})
  })

  it('treats an empty name as no name', () => {
    expect(validateBrowseSearch({ preview: '', focus: '' })).toEqual({})
  })

  it('reads a file name that looks like a number as text', () => {
    expect(validateBrowseSearch(parseSearch('?preview=007&focus=true'))).toEqual({ preview: '007', focus: 'true' })
  })
})

describe('query strings', () => {
  it('leave out unset values', () => {
    expect(stringifySearch({ type: 'images', date: undefined, preview: 'a b&c.txt' })).toBe(
      '?type=images&preview=a+b%26c.txt'
    )
    expect(stringifySearch({ type: undefined })).toBe('')
  })

  it('read back what they wrote', () => {
    const search = { preview: 'x=1&y #2?.txt', q: '100%' }
    expect(parseSearch(stringifySearch(search))).toEqual(search)
  })
})
