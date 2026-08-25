// web/src/lib/state/job-tray.test.ts — the re-attach path a browser refresh
// depends on. `attachOpenJobs()` is the only thing standing between "the tab
// reloaded" and "the user has no idea a move was ever running", so what it
// carries over from `GET /api/jobs` is worth pinning: not just that the job
// reappears, but that an interrupted one reappears *with the paths it never
// finished*, which is what makes it actionable rather than a dead entry.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import type { JobStatus } from '../api/types'
import { JobTrayState } from './job-tray.svelte'

function job(partial: Partial<JobStatus>): JobStatus {
  return {
    id: 'J-1',
    kind: 'copy',
    state: 'interrupted',
    done: 1,
    total: 4,
    current: null,
    errors: [],
    results: [],
    attempting: [],
    pending: [],
    ...partial
  }
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('JobTrayState.attachOpenJobs', () => {
  it('re-attaches an interrupted job with the items it never finished', async () => {
    vi.spyOn(api, 'jobList').mockResolvedValue({
      jobs: [job({ attempting: ['/b'], pending: ['/c', '/d'] })]
    })
    const tray = new JobTrayState()
    await tray.attachOpenJobs()

    expect(tray.items).toHaveLength(1)
    expect(tray.items[0]).toMatchObject({
      id: 'J-1',
      kind: 'copy',
      status: 'interrupted',
      done: 1,
      total: 4,
      attempting: ['/b'],
      pending: ['/c', '/d']
    })
    expect(tray.open).toBe(true)
    expect(tray.stale).toBe(false)
  })

  it('raises `stale` instead of clearing the tray when the server is unreachable', async () => {
    vi.spyOn(api, 'jobList').mockRejectedValue(new Error('offline'))
    const tray = new JobTrayState()
    await tray.attachOpenJobs()

    // An empty tray reads as "nothing is running", which is not a claim this
    // state can back up while it cannot see the server.
    expect(tray.stale).toBe(true)
    expect(tray.items).toHaveLength(0)
  })
})
