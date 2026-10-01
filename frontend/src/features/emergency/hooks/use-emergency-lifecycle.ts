import { useEffect, useEffectEvent } from 'react'
import { useBeforeUnload } from '../../../hooks/use-before-unload'
import { emergencyDoor } from '../api'

type StateSetter = (patch: {
  reason?: string
  step?: 'loading' | 'setup' | 'credentials' | 'totp' | 'editing'
  errorMessage?: string
}) => void

export function useEmergencyLifecycle({
  setState,
  dirty,
  messageFor
}: {
  setState: StateSetter
  dirty: boolean
  messageFor: (error: unknown) => string
}): void {
  // The caller rebuilds messageFor on every render; reading it through an effect event keeps the door fetch to one per mount.
  const describe = useEffectEvent(messageFor)
  useEffect(() => {
    let cancelled = false
    void emergencyDoor()
      .then((door) => {
        if (cancelled) return
        setState({ reason: door.reason, step: door.setup_required ? 'setup' : 'credentials' })
      })
      .catch((error: unknown) => {
        if (cancelled) return
        setState({ errorMessage: describe(error), step: 'credentials' })
      })
    return () => {
      cancelled = true
    }
  }, [setState])

  useBeforeUnload(dirty)
}
