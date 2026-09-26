import { useEffect, type RefObject } from 'react'
import type { NavigateFunction } from 'react-router-dom'
import { emergencyDoor } from '../../../../lib/api/emergency'

type StateSetter = (patch: { sectionDialogOpen?: boolean; reason?: string; step?: 'loading' | 'setup' | 'credentials' | 'totp' | 'editing'; errorMessage?: string }) => void

export function useEmergencyLifecycle({
  sectionDialogRef,
  setState,
  dirty,
  messageFor
}: {
  sectionDialogRef: RefObject<HTMLElement | null>
  setState: StateSetter
  dirty: boolean
  messageFor: (error: unknown) => string
}): void {
  useEffect(() => {
    const element = sectionDialogRef.current
    if (!element) return
    const close = () => setState({ sectionDialogOpen: false })
    element.addEventListener('close', close)
    return () => element.removeEventListener('close', close)
  }, [sectionDialogRef, setState])

  useEffect(() => {
    let cancelled = false
    void emergencyDoor().then((door) => {
      if (cancelled) return
      setState({ reason: door.reason, step: door.setup_required ? 'setup' : 'credentials' })
    }).catch((error: unknown) => {
      if (cancelled) return
      setState({ errorMessage: messageFor(error), step: 'credentials' })
    })
    return () => { cancelled = true }
  }, [messageFor, setState])

  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!dirty) return
      event.preventDefault()
    }
    window.addEventListener('beforeunload', beforeUnload)
    return () => window.removeEventListener('beforeunload', beforeUnload)
  }, [dirty])
}
