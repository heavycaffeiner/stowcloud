import { render } from '@testing-library/react'
import { Outlet, RouterProvider, createMemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, screen } from '../../src/test/test-utils'
import { RouteErrorBoundary } from '../../src/app/RouteErrorBoundary'

function Broken(): never {
  throw new Error('internal detail')
}

function renderAt(path: string) {
  const router = createMemoryRouter(
    [
      {
        path: '/',
        errorElement: <RouteErrorBoundary />,
        element: (
          <>
            <nav>Shell navigation</nav>
            <Outlet />
          </>
        ),
        children: [{ errorElement: <RouteErrorBoundary embedded />, children: [{ path: 'b/*', element: <Broken /> }] }]
      }
    ],
    { initialEntries: [path] }
  )
  return render(<RouterProvider router={router} />)
}

beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('RouteErrorBoundary', () => {
  it('replaces only the failed page inside the shell and hides the raw error', () => {
    renderAt('/b/')

    expect(screen.getByText('Shell navigation')).toBeTruthy()
    expect(screen.getByRole('heading', { name: 'Could not show this page' })).toBeTruthy()
    expect(screen.getByText('Retry')).toBeTruthy()
    expect(screen.queryByText('internal detail')).toBeNull()
    expect(screen.queryByText('Stowcloud')).toBeNull()
    expect(screen.queryByRole('main')).toBeNull()
  })

  it('names an unknown address without offering a retry', () => {
    renderAt('/nowhere')

    expect(screen.getByRole('main')).toBeTruthy()
    expect(screen.getByRole('heading', { name: 'This page does not exist' })).toBeTruthy()
    expect(screen.queryByText('Retry')).toBeNull()
    expect(screen.getByText('Go to files')).toBeTruthy()
  })
})
