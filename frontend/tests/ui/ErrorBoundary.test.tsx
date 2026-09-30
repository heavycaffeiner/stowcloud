import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '../../src/test/test-utils'
import { ErrorBoundary } from '../../src/ui/ErrorBoundary'

let broken = true

function Fragile() {
  if (broken) throw new Error('internal detail')
  return <p>Working</p>
}

beforeEach(() => {
  broken = true
  // React reports every caught error to the console.
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('ErrorBoundary', () => {
  it('keeps a crash to its region and renders the children again on retry', () => {
    render(
      <>
        <p>Neighbour</p>
        <ErrorBoundary>
          <Fragile />
        </ErrorBoundary>
      </>
    )

    expect(screen.getByText('Neighbour')).toBeTruthy()
    expect(screen.getByRole('alert').textContent).toContain('Could not show this part of the page')
    expect(screen.queryByText('internal detail')).toBeNull()

    broken = false
    fireEvent.click(screen.getByText('Retry'))
    expect(screen.getByText('Working')).toBeTruthy()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('clears a crash when the reset key changes', () => {
    const { rerender } = render(
      <ErrorBoundary resetKey="a">
        <Fragile />
      </ErrorBoundary>
    )
    expect(screen.getByRole('alert')).toBeTruthy()

    broken = false
    rerender(
      <ErrorBoundary resetKey="a">
        <Fragile />
      </ErrorBoundary>
    )
    expect(screen.getByRole('alert')).toBeTruthy()

    rerender(
      <ErrorBoundary resetKey="b">
        <Fragile />
      </ErrorBoundary>
    )
    expect(screen.getByText('Working')).toBeTruthy()
  })

  it('shows the caller fallback in place of the notice', () => {
    render(
      <ErrorBoundary fallback={<p>Own fallback</p>}>
        <Fragile />
      </ErrorBoundary>
    )
    expect(screen.getByText('Own fallback')).toBeTruthy()
    expect(screen.queryByText('Retry')).toBeNull()
  })
})
