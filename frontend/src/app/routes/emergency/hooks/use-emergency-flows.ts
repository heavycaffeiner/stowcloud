import { useRef } from 'react'
import { emergencyLogin, emergencyRestart, emergencySave, emergencySettings } from '../../../../lib/api/emergency'
import { describeApiError } from '../../../../lib/api/error-text'
import { useI18n } from '../../../../hooks/use-i18n'
import type { EmergencyActions, EmergencyState } from './use-emergency-state'

export function useEmergencyFlows(state: EmergencyState, actions: EmergencyActions) {
  const { t } = useI18n()
  const requestId = useRef(0)
  const messageFor = (error: unknown): string => describeApiError(error, t('emergency.something_went_wrong'))
  const loadSettings = async (): Promise<void> => {
    const id = ++requestId.current
    actions.patch({ sectionLoading: true, errorMessage: null })
    try {
      const settings = await emergencySettings()
      if (requestId.current !== id) return
      actions.applyMetadata(settings)
      const initial = settings.sections.includes(state.section) ? state.section : (settings.sections[0] ?? 'network')
      actions.patch({ step: 'editing', sectionLoading: false })
      actions.adoptSection(initial, settings)
    } catch (error) {
      if (requestId.current !== id) return
      actions.patch({ sectionLoading: false, errorMessage: messageFor(error) })
    }
  }
  const loadSection = async (name: string): Promise<void> => {
    const id = ++requestId.current
    actions.patch({ sectionLoading: true, sectionOutcome: null })
    try {
      const settings = await emergencySettings()
      if (requestId.current !== id) return
      if (state.documentText !== state.baselineDocument) {
        actions.patch({ selectedSection: state.section, sectionLoading: false, sectionOutcome: { section: name, ok: false, message: t('emergency.section_change_requires_choice', { section: name }) } })
        return
      }
      actions.applyMetadata(settings)
      actions.patch({ sectionLoading: false })
      actions.adoptSection(name, settings)
    } catch (error) {
      if (requestId.current !== id) return
      actions.patch({ sectionLoading: false, selectedSection: state.section, sectionOutcome: { section: name, ok: false, message: t('emergency.section_load_failed', { section: name, error: messageFor(error) }) } })
    }
  }
  const chooseSection = (next: string): void => {
    if (next === state.section) {
      if (state.sectionLoading) {
        requestId.current++
        actions.patch({ sectionLoading: false, selectedSection: state.section })
      }
      return
    }
    if (next === state.selectedSection) return
    actions.patch({ selectedSection: next })
    if (state.documentText !== state.baselineDocument) {
      actions.patch({ pendingSection: next, sectionDialogOpen: true })
      return
    }
    void loadSection(next)
  }
  const stayOnSection = (): void => actions.patch({ pendingSection: null, selectedSection: state.section, sectionDialogOpen: false })
  const discardAndChange = (): void => {
    const next = state.pendingSection
    actions.patch({ pendingSection: null, sectionDialogOpen: false, documentText: state.baselineDocument })
    if (!next) return
    actions.patch({ selectedSection: next })
    void loadSection(next)
  }
  const saveCurrentSection = async (): Promise<boolean> => {
    const targetSection = state.section
    let body: unknown
    try { body = JSON.parse(state.documentText) } catch {
      actions.patch({ sectionOutcome: { section: targetSection, ok: false, message: t('emergency.section_invalid_json', { section: targetSection }) } })
      return false
    }
    actions.patch({ errorMessage: null, sectionOutcome: null, warnings: [], warningSection: null, busy: true })
    try {
      const result = await emergencySave(targetSection, body)
      if (state.section !== targetSection) return false
      actions.patch({ baselineDocument: state.documentText, warnings: result.warnings, warningSection: targetSection, sectionOutcome: { section: targetSection, ok: true, message: t('emergency.section_stored_takes_effect_on_restart', { section: targetSection }) } })
      return true
    } catch (error) {
      actions.patch({ sectionOutcome: { section: targetSection, ok: false, message: t('emergency.section_save_failed', { section: targetSection, error: messageFor(error) }) } })
      return false
    } finally { actions.patch({ busy: false }) }
  }
  const saveAndChange = async (): Promise<void> => {
    if (state.busy || !state.pendingSection) return
    const next = state.pendingSection
    if (!(await saveCurrentSection())) return
    actions.patch({ pendingSection: null, sectionDialogOpen: false, selectedSection: next })
    void loadSection(next)
  }
  const signIn = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    if (state.busy || !state.username.trim() || !state.password || (state.step === 'totp' && !state.code.trim())) return
    actions.patch({ errorMessage: null, busy: true })
    try {
      const result = await emergencyLogin(state.username.trim(), state.password, state.step === 'totp' ? state.code : undefined)
      if (result.status === 'totp_required') { actions.setStep('totp'); return }
      await loadSettings()
    } catch (error) { actions.patch({ errorMessage: messageFor(error) }) }
    finally { actions.patch({ busy: false }) }
  }
  const restart = async (): Promise<void> => {
    actions.patch({ errorMessage: null, busy: true })
    try { actions.patch({ restarting: (await emergencyRestart()).restarting }) }
    catch (error) { actions.patch({ errorMessage: messageFor(error) }) }
    finally { actions.patch({ busy: false }) }
  }
  return { loadSettings, chooseSection, stayOnSection, discardAndChange, saveCurrentSection, saveAndChange, signIn, restart }
}
