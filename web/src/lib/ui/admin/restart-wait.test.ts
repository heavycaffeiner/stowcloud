import { describe, expect, it } from 'vitest'
import { nextRestartWaitStep, type RestartPollSnapshot } from './restart-wait'

const base: RestartPollSnapshot = {
  isSuccess: false,
  succeededAt: 0,
  isError: false,
  erroredAt: 0,
  waitStartedAt: 1_000,
  now: 1_000,
  deadline: 46_000
}

describe('nextRestartWaitStep', () => {
  // This is the bug: the server keeps answering under its old image for a
  // grace window after it accepts the restart, and the dialog's first poll
  // fires immediately, inside that window. A healthy answer here is the old
  // process, not the new one, and must not be read as a finished restart.
  it('does not confirm on a healthy poll that never saw an outage', () => {
    const step = nextRestartWaitStep(false, {
      ...base,
      isSuccess: true,
      succeededAt: 1_002 // a couple of milliseconds after asking, the old image answering
    })
    expect(step.outcome).toBe('waiting')
    expect(step.sawOutage).toBe(false)
  })

  it('records the outage without concluding anything on its own', () => {
    const step = nextRestartWaitStep(false, {
      ...base,
      isError: true,
      erroredAt: 1_250
    })
    expect(step.outcome).toBe('waiting')
    expect(step.sawOutage).toBe(true)
  })

  it('confirms once a success follows a recorded outage', () => {
    const step = nextRestartWaitStep(true, {
      ...base,
      isSuccess: true,
      succeededAt: 1_400
    })
    expect(step.outcome).toBe('confirmed')
  })

  it('confirms in the same tick an outage and its recovery are both visible', () => {
    const step = nextRestartWaitStep(false, {
      ...base,
      isError: true,
      erroredAt: 1_250,
      isSuccess: true,
      succeededAt: 1_400
    })
    expect(step.outcome).toBe('confirmed')
  })

  it('ignores a success left over from before this wait began', () => {
    const step = nextRestartWaitStep(true, {
      ...base,
      isSuccess: true,
      succeededAt: 500 // the mount-time query, run before the restart was asked for
    })
    expect(step.outcome).toBe('waiting')
  })

  it('ignores an error left over from before this wait began', () => {
    const step = nextRestartWaitStep(false, {
      ...base,
      isError: true,
      erroredAt: 500
    })
    expect(step.sawOutage).toBe(false)
  })

  it('times out once the deadline passes without ever confirming', () => {
    const step = nextRestartWaitStep(false, {
      ...base,
      now: 46_000
    })
    expect(step.outcome).toBe('timed-out')
  })

  it('still requires an outage even past the deadline, rather than confirming a bare success', () => {
    const step = nextRestartWaitStep(false, {
      ...base,
      isSuccess: true,
      succeededAt: 45_999,
      now: 46_000
    })
    expect(step.outcome).toBe('timed-out')
  })
})
