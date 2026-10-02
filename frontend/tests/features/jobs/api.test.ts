// A job as the server describes it, read into the shape the tray renders.
import { describe, expect, it } from 'vitest'
import { jobFromWire } from '../../../src/features/jobs/api'

const wire = {
  id: 'J-3',
  kind: 'delete',
  state: 'running',
  progress: '1',
  total: '5',
  results: null,
  attempting: null
}

describe('jobFromWire', () => {
  it('reads the decimal counts as numbers', () => {
    expect(jobFromWire(wire)).toMatchObject({ id: 'J-3', kind: 'delete', state: 'running', done: 1, total: 5 })
  })

  it('reads the server failure state as error, so the poll stops', () => {
    expect(jobFromWire({ ...wire, state: 'failed' }).state).toBe('error')
  })

  it('names a failed item by the error code the rest of the app renders', () => {
    const job = jobFromWire({
      ...wire,
      state: 'done',
      results: [
        { index: '0', path: '/a', ok: true },
        { index: '1', path: '/b', ok: false, reason: 'denied', text: 'no' }
      ]
    })
    expect(job.results).toEqual([
      { path: '/a', ok: true },
      { path: '/b', ok: false, error: { code: 'fs.denied', message: 'no' } }
    ])
  })
})
