import { describe, expect, it } from 'vitest'
import { syncTabHash } from './tab-hash'

const VALID = ['account', 'security', 'connections', 'appearance']

/** Drives the sync the way a component does: `seen` and `tab` carry over, and
 *  the hash only moves when a real navigation moves it. */
function click(state: { seen: string; tab: string; hash: string }, to: string): void {
  state.tab = to
  const next = syncTabHash(state.hash, state.seen, state.tab, VALID)
  state.seen = next.seen
  state.tab = next.tab
}

describe('syncTabHash', () => {
  it('writes the tab to the URL when the hash lags behind', () => {
    expect(syncTabHash('account', 'account', 'security', VALID)).toEqual({
      seen: 'account',
      tab: 'security',
      write: 'security'
    })
  })

  it('adopts a hash that changed outside the component', () => {
    expect(syncTabHash('appearance', 'account', 'account', VALID)).toEqual({
      seen: 'appearance',
      tab: 'appearance',
      write: null
    })
  })

  it('writes nothing when the URL already agrees', () => {
    expect(syncTabHash('security', 'security', 'security', VALID)).toEqual({
      seen: 'security',
      tab: 'security',
      write: null
    })
  })

  it('keeps the tab and restores the URL when the hash is not a tab', () => {
    expect(syncTabHash('nonsense', 'account', 'security', VALID)).toEqual({
      seen: 'nonsense',
      tab: 'security',
      write: 'security'
    })
  })

  it('leaves the tab alone on a repeated read of a stale hash', () => {
    // `replaceState` never updates `page.url`, so after a deep link to
    // `#security` every later read still returns `security`. Consuming it once
    // and remembering it as seen is what stops the second click landing on it.
    const state = { seen: 'security', tab: 'security', hash: 'security' }
    click(state, 'appearance')
    expect(state.tab).toBe('appearance')
    click(state, 'account')
    expect(state.tab).toBe('account')
  })

  it('still adopts a hash the user navigates to after several tab clicks', () => {
    const state = { seen: 'security', tab: 'security', hash: 'security' }
    click(state, 'appearance')
    click(state, 'account')
    state.hash = 'connections'
    const next = syncTabHash(state.hash, state.seen, state.tab, VALID)
    expect(next.tab).toBe('connections')
  })
})
