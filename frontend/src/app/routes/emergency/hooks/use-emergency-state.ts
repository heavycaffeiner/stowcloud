import { useMemo } from 'react'
import { useRouteStore } from '../../../hooks/use-route-store'
import type { EmergencyFinding, EmergencySettings } from '../../../../lib/api/emergency'

export type EmergencyStep = 'loading' | 'setup' | 'credentials' | 'totp' | 'editing'
export type SectionOutcome = { section: string; ok: boolean; message: string }
export type EmergencyState = {
  step: EmergencyStep
  reason: string
  username: string
  password: string
  code: string
  busy: boolean
  errorMessage: string | null
  sections: string[]
  section: string
  selectedSection: string
  sectionLoading: boolean
  documentText: string
  baselineDocument: string
  listen: string
  appHosts: string[]
  warnings: EmergencyFinding[]
  warningSection: string | null
  restarting: boolean | null
  sectionOutcome: SectionOutcome | null
  pendingSection: string | null
  sectionDialogOpen: boolean
}

export type EmergencyActions = {
  patch: (patch: Partial<EmergencyState> | ((state: EmergencyState) => Partial<EmergencyState>)) => void
  setStep: (step: EmergencyStep) => void
  setUsername: (username: string) => void
  setPassword: (password: string) => void
  setCode: (code: string) => void
  setDocumentText: (documentText: string) => void
  setSelectedSection: (selectedSection: string) => void
  applyMetadata: (settings: EmergencySettings) => void
  adoptSection: (name: string, settings: EmergencySettings) => void
}

const initialState: EmergencyState = {
  step: 'loading', reason: '', username: '', password: '', code: '', busy: false,
  errorMessage: null, sections: [], section: 'network', selectedSection: 'network',
  sectionLoading: false, documentText: '{}', baselineDocument: '{}', listen: '', appHosts: [],
  warnings: [], warningSection: null, restarting: null, sectionOutcome: null,
  pendingSection: null, sectionDialogOpen: false
}

function storedDocument(settings: EmergencySettings, name: string): string {
  return JSON.stringify(settings.stored[name] ?? {}, null, 2)
}

export function useEmergencyState(): { state: EmergencyState; actions: EmergencyActions } {
  const [state, patch] = useRouteStore<EmergencyState>(initialState)
  const actions = useMemo<EmergencyActions>(() => ({
    patch,
    setStep: (step) => patch({ step }),
    setUsername: (username) => patch({ username }),
    setPassword: (password) => patch({ password }),
    setCode: (code) => patch({ code }),
    setDocumentText: (documentText) => patch({ documentText }),
    setSelectedSection: (selectedSection) => patch({ selectedSection }),
    applyMetadata: (settings) => patch({ sections: settings.sections, listen: settings.listen, appHosts: settings.app_hosts }),
    adoptSection: (name, settings) => {
      const documentText = storedDocument(settings, name)
      patch({ section: name, selectedSection: name, documentText, baselineDocument: documentText, sectionOutcome: null, warnings: [], warningSection: null })
    }
  }), [patch])
  return { state, actions }
}
