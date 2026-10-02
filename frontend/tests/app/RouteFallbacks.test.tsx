import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider
} from '@tanstack/react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '../../src/test/test-utils'
import { RouteProblem } from '../../src/app/RouteFallbacks'

function Broken(): never {
  throw new Error('internal detail')
}

function renderAt(path: string) {
  const rootRoute = createRootRoute({
    component: () => (
      <>
        <nav>Shell navigation</nav>
        <Outlet />
      </>
    )
  })
  const brokenRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: 'b/$',
    errorComponent: () => <RouteProblem embedded />,
    component: Broken
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([brokenRoute]),
    history: createMemoryHistory({ initialEntries: [path] }),
    defaultNotFoundComponent: () => <RouteProblem notFound />
  })
  return render(<RouterProvider router={router} />)
}

beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
  vi.spyOn(console, 'warn').mockImplementation(() => undefined)
  // jsdom has no scrolling, and the router restores scroll on every navigation.
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('RouteProblem', () => {
  it('replaces only the failed page inside the shell and hides the raw error', async () => {
    renderAt('/b/')

    expect(await screen.findByRole('heading', { name: 'Could not show this page' })).toBeTruthy()
    expect(screen.getByText('Shell navigation')).toBeTruthy()
    expect(screen.getByText('Retry')).toBeTruthy()
    expect(screen.queryByText('internal detail')).toBeNull()
    expect(screen.queryByRole('main')).toBeNull()
  })

  it('names an unknown address without offering a retry', async () => {
    renderAt('/nowhere')

    expect(await screen.findByRole('heading', { name: 'This page does not exist' })).toBeTruthy()
    expect(screen.getByRole('main')).toBeTruthy()
    expect(screen.queryByText('Retry')).toBeNull()
    expect(screen.getByText('Go to files')).toBeTruthy()
  })
})
