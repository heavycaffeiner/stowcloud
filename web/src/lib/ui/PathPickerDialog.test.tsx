import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/react'
import { render, screen } from '../../test/test-utils'
import { PathPickerDialog } from './PathPickerDialog'

function listing(path: string, entries: Array<{ name: string; path: string; is_dir: boolean }>, parent = '') {
  return { path, parent, entries, truncated: false }
}

describe('PathPickerDialog', () => {
  afterEach(() => vi.restoreAllMocks())

  it('uses the setup browse endpoint and confirms a selected file', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify(listing('', [{ name: 'note.txt', path: '/note.txt', is_dir: false }])), { status: 200 }),
    )
    const onpick = vi.fn()
    render(<PathPickerDialog open mode="file" token="setup-token" onclose={vi.fn()} onpick={onpick} />)

    const file = await screen.findByRole('button', { name: 'note.txt' })
    file.dispatchEvent(new Event('click', { bubbles: true }))
    await waitFor(() => expect(document.querySelector('mdui-button[variant="filled"]')?.hasAttribute('disabled')).toBe(false))
    const choose = document.querySelector('mdui-button[variant="filled"]')!
    expect(choose.hasAttribute('disabled')).toBe(false)
    choose.dispatchEvent(new Event('click', { bubbles: true }))
    expect(onpick).toHaveBeenCalledWith('/note.txt')
    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/system/setup/browse'),
      expect.objectContaining({ method: 'POST' }),
    )
  })
  it('falls back to roots after an initial failure', async () => {
    vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: { message: 'not found' } }), { status: 404 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(listing('', [{ name: 'root', path: '/root', is_dir: true }])), { status: 200 }))
    render(<PathPickerDialog open mode="folder" start="/typed" onclose={vi.fn()} onpick={vi.fn()} />)
    expect(await screen.findByRole('button', { name: 'Open root' })).toBeTruthy()
  })
})
