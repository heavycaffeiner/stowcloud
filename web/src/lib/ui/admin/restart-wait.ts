// The restart dialog's own decision: has the wait actually observed a
// restart, or only a poll that happened to answer.
//
// A healthy poll is not proof by itself. The server answers the confirm
// request and keeps serving under its old image for a grace window before it
// tears down (`restartGrace` in `go/engine/lifecycle/systemrestart.go`), and
// the dialog's own first poll fires immediately after that response, well
// inside that window on any ordinary connection. Accepting that poll as
// proof reports the restart finished before the process has even started
// replacing itself. Real proof is the sequence a restart actually produces:
// unreachable, then answering again. `sawOutage` carries the first half
// forward across ticks, since a single snapshot cannot represent something
// observed on an earlier tick and not on this one.

export interface RestartPollSnapshot {
  isSuccess: boolean
  succeededAt: number
  isError: boolean
  erroredAt: number
  waitStartedAt: number
  now: number
  deadline: number
}

export type RestartWaitOutcome = 'waiting' | 'confirmed' | 'timed-out'

export interface RestartWaitStep {
  outcome: RestartWaitOutcome
  sawOutage: boolean
}

/** Folds one poll tick into the wait's running state.
 *
 * `sawOutage` is the caller's own running total from the previous tick;
 * `confirmed` is reachable only once an outage has been recorded, on this
 * tick or an earlier one. Both timestamps are compared against
 * `waitStartedAt` so a query result left over from before the restart was
 * ever asked for (either the mount-time health check, or a stale error from
 * an earlier attempt) cannot count for or against this wait.
 */
export function nextRestartWaitStep(sawOutage: boolean, snapshot: RestartPollSnapshot): RestartWaitStep {
  const outageThisTick = snapshot.isError && snapshot.erroredAt >= snapshot.waitStartedAt
  const outageSoFar = sawOutage || outageThisTick

  if (outageSoFar && snapshot.isSuccess && snapshot.succeededAt >= snapshot.waitStartedAt) {
    return { outcome: 'confirmed', sawOutage: outageSoFar }
  }
  if (snapshot.now >= snapshot.deadline) {
    return { outcome: 'timed-out', sawOutage: outageSoFar }
  }
  return { outcome: 'waiting', sawOutage: outageSoFar }
}
