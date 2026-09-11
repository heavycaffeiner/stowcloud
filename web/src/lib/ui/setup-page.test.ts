import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLocale } from '../i18n'

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

const { createInitialAdmin, login, session, adminCreateShare, goto } = vi.hoisted(() => ({
  createInitialAdmin: vi.fn(),
  login: vi.fn(),
  session: vi.fn(),
  adminCreateShare: vi.fn(),
  goto: vi.fn()
}))

vi.mock('../api/setup', () => ({
  createInitialAdmin,
  SetupValidationError: class SetupValidationError extends Error {}
}))
vi.mock('../api/client', () => ({
  api: { session, adminCreateShare },
  isMock: false,
  ApiError: class ApiError extends Error {}
}))
vi.mock('../query/session', () => ({
  loginMutation: () => ({ mutationFn: login })
}))
vi.mock('$app/navigation', () => ({ goto }))

async function mountSetup() {
  // Dynamic imports keep @testing-library/svelte and the route below from
  // evaluating m3-svelte before the jsdom polyfills at the top of this file.
  const { cleanup, fireEvent, render, setup, waitFor } = await import('@testing-library/svelte')
  await setup()
  const { default: Wrapper } = await import('./query-test-wrapper.svelte')
  const { default: SetupPage } = await import('../../routes/setup/+page.svelte')
  const utils = render(SetupPage, {}, { wrapper: Wrapper })
  return { ...utils, cleanup, fireEvent, waitFor }
}

afterEach(async () => {
  const { cleanup } = await import('@testing-library/svelte')
  cleanup()
})

beforeEach(() => {
  setLocale('en')
  createInitialAdmin.mockReset()
  login.mockReset()
  session.mockReset()
  adminCreateShare.mockReset()
  goto.mockReset()
})

describe('setup first-share retry', () => {
  it('authenticates once while independently retrying failed share creation', async () => {
    createInitialAdmin.mockResolvedValue({ warnings: [], share_failed: true })
    login.mockResolvedValue({ required: undefined })
    session.mockResolvedValue({})
    adminCreateShare.mockRejectedValueOnce(new Error('first share failed')).mockResolvedValueOnce({})

    const { fireEvent, findByRole, findByText, getByLabelText, getByRole, container, waitFor } = await mountSetup()
    await fireEvent.input(getByLabelText('Setup token'), { target: { value: 'setup-token' } })
    await fireEvent.input(getByLabelText('Administrator username'), { target: { value: 'root' } })
    await fireEvent.input(getByLabelText('Password'), { target: { value: 'longenoughpw' } })
    await fireEvent.input(getByLabelText('Confirm password'), { target: { value: 'longenoughpw' } })
    await fireEvent.input(getByLabelText('Name'), { target: { value: 'Documents' } })
    await fireEvent.input(getByLabelText('Server path'), { target: { value: '/srv/documents' } })
    await fireEvent.submit(container.querySelector('form')!)

    const retryButton = await findByRole('button', { name: 'Retry adding the shared folder' })
    expect((getByRole('button', { name: 'Browse folders…' }) as HTMLButtonElement).disabled).toBe(true)

    await fireEvent.click(retryButton)
    await findByText('The shared folder could not be added. Check the folder path and try again.')
    expect(login).toHaveBeenCalledTimes(1)
    expect(session).toHaveBeenCalledTimes(1)
    expect(adminCreateShare).toHaveBeenCalledTimes(1)
    expect((getByRole('button', { name: 'Browse folders…' }) as HTMLButtonElement).disabled).toBe(false)

    await fireEvent.click(getByRole('button', { name: 'Retry adding the shared folder' }))
    await waitFor(() => expect(goto).toHaveBeenCalledWith('/b/'))
    expect(login).toHaveBeenCalledTimes(1)
    expect(session).toHaveBeenCalledTimes(1)
    expect(adminCreateShare).toHaveBeenCalledTimes(2)
  })
})
