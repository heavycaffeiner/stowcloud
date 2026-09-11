import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../i18n'
import { ApiError } from '../api/types'

// jsdom implements neither `showModal`/`close` on `<dialog>` (m3-svelte's
// Dialog wrapper calls them directly on open/close) nor the ripple layer's
// `matchMedia` read and `beginElement` call. Stubbed the same way
// `switch.test.ts` does: none of that chrome is what this file tests.
if (!HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function (this: HTMLDialogElement) {
    this.setAttribute('open', '')
  }
  HTMLDialogElement.prototype.close = function (this: HTMLDialogElement) {
    this.removeAttribute('open')
  }
}
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn().mockImplementation((query) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn()
  }))
})
const svgProto = window.SVGElement.prototype as SVGElement & { beginElement: () => void }
svgProto.beginElement = () => {}

interface Listing {
  path: string
  parent: string
  entries: { name: string; path: string; is_dir: boolean }[]
  truncated: boolean
}

// `vi.hoisted`: `vi.mock` factories run before the rest of this file's
// imports, so the mock function they close over has to be created the same
// way.
const { browseHostPath } = vi.hoisted(() => ({ browseHostPath: vi.fn() }))

vi.mock('../api/client', () => ({
  api: {
    browseHostPath: (path: string) => browseHostPath(path),
    browseSetupPath: (_token: string, path: string) => browseHostPath(path)
  }
}))

function listing(path: string, parent: string, entries: Listing['entries']): Listing {
  return { path, parent, entries, truncated: false }
}

/** Wires the mock transport to a fixed directory tree. A path with no entry
 *  here answers the way the real server does for one it cannot open: 404. */
function serveListings(byPath: Record<string, Listing>): void {
  browseHostPath.mockImplementation(async (path: string) => {
    const found = byPath[path]
    if (!found) throw new ApiError(404, { code: 'fs.not_found', message: 'no such path' })
    return found
  })
}

async function mountPicker(props: { mode: 'folder' | 'file'; start?: string; onpick?: (path: string) => void }) {
  // Dynamic, and it has to be: `PathPickerDialog.svelte` pulls in m3-svelte's
  // `Icon`/`Button`, which touch `matchMedia` at module scope. A static
  // import here would be hoisted above the polyfills at the top of this
  // file and throw before the first test, the same reasoning `switch.test.ts`
  // documents.
  const { render, fireEvent, cleanup, setup } = await import('@testing-library/svelte')
  await setup()
  const { default: Wrapper } = await import('./query-test-wrapper.svelte')
  const { default: PathPickerDialog } = await import('./PathPickerDialog.svelte')
  const onpick = props.onpick ?? vi.fn()
  const onclose = vi.fn()
  const utils = render(
    PathPickerDialog,
    { props: { open: true, mode: props.mode, start: props.start, onclose, onpick } },
    { wrapper: Wrapper }
  )
  return { ...utils, fireEvent, cleanup, onpick, onclose }
}

beforeEach(() => {
  setLocale('en')
  browseHostPath.mockReset()
})

afterEach(async () => {
  const { cleanup } = await import('@testing-library/svelte')
  cleanup()
})

describe('PathPickerDialog', () => {
  it('opens on the root listing when nothing is typed, and confirms the folder navigated into', async () => {
    serveListings({
      '': listing('', '', [{ name: '/home', path: '/home', is_dir: true }]),
      '/home': listing('/home', '/', [{ name: 'alice', path: '/home/alice', is_dir: true }])
    })
    const onpick = vi.fn()
    const { findByRole, getByRole, fireEvent } = await mountPicker({ mode: 'folder', onpick })

    expect((getByRole('button', { name: 'Parent folder' }) as HTMLButtonElement).disabled).toBe(true)

    await fireEvent.click(await findByRole('button', { name: 'Open /home' }))
    await findByRole('button', { name: 'Open alice' })
    expect((getByRole('button', { name: 'Parent folder' }) as HTMLButtonElement).disabled).toBe(false)

    await fireEvent.click(getByRole('button', { name: 'Choose' }))
    expect(onpick).toHaveBeenCalledWith('/home')
  })

  it('keeps Choose disabled until a file is selected, in file mode', async () => {
    serveListings({
      '/home/alice': listing('/home/alice', '/home', [
        { name: 'documents', path: '/home/alice/documents', is_dir: true },
        { name: 'vault.hc', path: '/home/alice/vault.hc', is_dir: false }
      ])
    })
    const onpick = vi.fn()
    const { findByRole, getByRole, fireEvent } = await mountPicker({
      mode: 'file',
      start: '/home/alice/vault.hc',
      onpick
    })

    // The start value is a file, so browsing opens its parent directory.
    const chooseBtn = await findByRole('button', { name: 'Choose' })
    expect((chooseBtn as HTMLButtonElement).disabled).toBe(true)

    // A folder is still an ordinary navigable row in file mode.
    expect(getByRole('button', { name: 'Open documents' })).toBeTruthy()

    await fireEvent.click(getByRole('button', { name: 'vault.hc' }))
    expect((chooseBtn as HTMLButtonElement).disabled).toBe(false)

    await fireEvent.click(chooseBtn)
    expect(onpick).toHaveBeenCalledWith('/home/alice/vault.hc')
  })

  it('shows a file as a non-interactive row in folder mode', async () => {
    serveListings({
      '/srv/shared': listing('/srv/shared', '/srv', [{ name: 'readme.txt', path: '/srv/shared/readme.txt', is_dir: false }])
    })
    const { findByText, queryByRole } = await mountPicker({ mode: 'folder', start: '/srv/shared' })

    await findByText('readme.txt')
    expect(queryByRole('button', { name: /readme/ })).toBeNull()
  })

  it('falls back to the root listing when the typed path cannot be read', async () => {
    serveListings({
      '': listing('', '', [{ name: '/home', path: '/home', is_dir: true }])
      // '/no/such/place' is deliberately absent: the mock answers 404 for it.
    })
    const { findByRole, queryByRole } = await mountPicker({ mode: 'folder', start: '/no/such/place' })

    await findByRole('button', { name: 'Open /home' })
    expect(queryByRole('alert')).toBeNull()
  })

  it('shows the server refusal once the user has actually navigated somewhere unreadable', async () => {
    browseHostPath.mockImplementation(async (path: string) => {
      if (path === '') return listing('', '', [{ name: '/mnt', path: '/mnt', is_dir: true }])
      throw new ApiError(403, { code: 'unprocessable', message: 'denied', detail: { reason_key: 'admin.fs_denied' } })
    })
    const { findByRole, findByText, fireEvent } = await mountPicker({ mode: 'folder' })

    await fireEvent.click(await findByRole('button', { name: 'Open /mnt' }))
    await findByText('This server cannot open that path. Only what its sandbox allows is listed.')
  })
})
