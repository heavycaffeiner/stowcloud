import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '../../../../src/test/test-utils'
import { EmergencyPage } from '../../../../src/features/emergency/routes/EmergencyPage'

describe('EmergencyPage', () => {
  afterEach(() => vi.restoreAllMocks())

  it('asks the server for the door state once', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async () => new Response(JSON.stringify({ reason: '', setup_required: false })))
    render(<EmergencyPage />)
    await waitFor(() => expect(document.querySelector('[autocomplete="username"]')).not.toBeNull())
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
