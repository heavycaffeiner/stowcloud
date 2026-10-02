import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/react'
import { render, screen } from '../../../../src/test/test-utils'
import { PathPickerDialog } from '../../../../src/features/file-browser/components/PathPickerDialog'

function listing(path: string, entries: Array<{ name: string; path: string; is_dir: boolean }>, parent = '') {
  return { path, parent, entries, truncated: false }
}

describe('PathPickerDialog', () => {
  afterEach(() => vi.restoreAllMocks())

  it('uses the setup browse endpoint and confirms a selected file', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify(listing('', [{ name: 'note.txt', path: '/note.txt', is_dir: false }])), {
        status: 200
      })
    )
    const onPick = vi.fn()
    render(<PathPickerDialog open mode="file" token="setup-token" onClose={vi.fn()} onPick={onPick} />)

    const file = await screen.findByRole('button', { name: 'note.txt' })
    file.dispatchEvent(new Event('click', { bubbles: true }))
    const choose = screen.getByText('Choose')
    await waitFor(() => expect(choose.hasAttribute('disabled')).toBe(false))
    choose.dispatchEvent(new Event('click', { bubbles: true }))
    expect(onPick).toHaveBeenCalledWith('/note.txt')
    const request = fetchMock.mock.calls[0][0] as Request
    expect(request.url).toContain('/api/v1/system/setup/browse')
    expect(request.method).toBe('POST')
    expect(await request.json()).toEqual({ token: 'setup-token', path: '' })
  })
  it('falls back to roots after an initial failure', async () => {
    vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: { message: 'not found' } }), { status: 404 }))
      .mockResolvedValueOnce(
        new Response(JSON.stringify(listing('', [{ name: 'root', path: '/root', is_dir: true }])), { status: 200 })
      )
    render(<PathPickerDialog open mode="folder" start="/typed" onClose={vi.fn()} onPick={vi.fn()} />)
    expect(await screen.findByRole('button', { name: 'Open root' })).toBeTruthy()
  })
})
