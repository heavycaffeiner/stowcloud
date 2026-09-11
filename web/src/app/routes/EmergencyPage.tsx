import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  emergencyDoor,
  emergencyLogin,
  emergencyRestart,
  emergencySave,
  emergencySettings,
  type EmergencyFinding,
  type EmergencySettings
} from '../../lib/api/emergency'
import { describeApiError } from '../../lib/api/error-text'
import { useI18n } from '../../lib/i18n/use-i18n'
import { Button } from '../../lib/ui/Button'
import { TextField } from '../../lib/ui/TextField'
import { useDocumentTitle } from '../use-document-title'
import './emergency.css'

/* i18n */ 'settings.would_lock_you_out'
/* i18n */ 'settings.proxy_range_is_everything'

type Step = 'loading' | 'setup' | 'credentials' | 'totp' | 'editing'
type SectionOutcome = { section: string; ok: boolean; message: string }

function storedDocument(settings: EmergencySettings, name: string): string {
  return JSON.stringify(settings.stored[name] ?? {}, null, 2)
}

export function EmergencyPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [step, setStep] = useState<Step>('loading')
  const [reason, setReason] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [errorMessage, setErrorMessage] = useState<string | null>(null)
  const [sections, setSections] = useState<string[]>([])
  const [section, setSection] = useState('network')
  const [selectedSection, setSelectedSection] = useState('network')
  const [sectionLoading, setSectionLoading] = useState(false)
  const [documentText, setDocumentText] = useState('{}')
  const [baselineDocument, setBaselineDocument] = useState('{}')
  const [listen, setListen] = useState('')
  const [appHosts, setAppHosts] = useState<string[]>([])
  const [warnings, setWarnings] = useState<EmergencyFinding[]>([])
  const [warningSection, setWarningSection] = useState<string | null>(null)
  const [restarting, setRestarting] = useState<boolean | null>(null)
  const [sectionOutcome, setSectionOutcome] = useState<SectionOutcome | null>(null)
  const [pendingSection, setPendingSection] = useState<string | null>(null)
  const [sectionDialogOpen, setSectionDialogOpen] = useState(false)
  const sectionDialogRef = useRef<HTMLElement | null>(null)
  useEffect(() => {
    const element = sectionDialogRef.current
    if (!element) return
    const close = () => setSectionDialogOpen(false)
    element.addEventListener('close', close)
    return () => element.removeEventListener('close', close)
  }, [])
  const requestId = useRef(0)
  const doorRequestId = useRef(0)
  const dirty = documentText !== baselineDocument

  useDocumentTitle(t('emergency.emergency_settings'))

  const messageFor = (error: unknown): string => describeApiError(error, t('emergency.something_went_wrong'))
  const findingText = (finding: EmergencyFinding): string => t(finding.reason_key, finding.reason_params ?? {})

  useEffect(() => {
    const id = ++doorRequestId.current
    void emergencyDoor().then((door) => {
      if (doorRequestId.current !== id) return
      setReason(door.reason)
      setStep(door.setup_required ? 'setup' : 'credentials')
    }).catch((error: unknown) => {
      if (doorRequestId.current !== id) return
      setErrorMessage(messageFor(error))
      setStep('credentials')
    })
    return () => { doorRequestId.current++ }
  }, [])

  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!dirty) return
      event.preventDefault()
    }
    window.addEventListener('beforeunload', beforeUnload)
    return () => window.removeEventListener('beforeunload', beforeUnload)
  }, [dirty])

  const applyMetadata = (settings: EmergencySettings): void => {
    setSections(settings.sections)
    setListen(settings.listen)
    setAppHosts(settings.app_hosts)
  }

  const adoptSection = (name: string, settings: EmergencySettings): void => {
    const next = storedDocument(settings, name)
    setSection(name)
    setSelectedSection(name)
    setDocumentText(next)
    setBaselineDocument(next)
    setSectionOutcome(null)
    setWarnings([])
    setWarningSection(null)
  }

  const loadSettings = async (): Promise<void> => {
    const id = ++requestId.current
    setSectionLoading(true)
    setErrorMessage(null)
    try {
      const settings = await emergencySettings()
      if (requestId.current !== id) return
      applyMetadata(settings)
      const initial = settings.sections.includes(section) ? section : (settings.sections[0] ?? 'network')
      setStep('editing')
      setSectionLoading(false)
      adoptSection(initial, settings)
    } catch (error) {
      if (requestId.current !== id) return
      setSectionLoading(false)
      setErrorMessage(messageFor(error))
    }
  }

  const loadSection = async (name: string): Promise<void> => {
    const id = ++requestId.current
    setSectionLoading(true)
    setSectionOutcome(null)
    try {
      const settings = await emergencySettings()
      if (requestId.current !== id) return
      if (dirty) {
        setSelectedSection(section)
        setSectionLoading(false)
        setSectionOutcome({ section: name, ok: false, message: t('emergency.section_change_requires_choice', { section: name }) })
        return
      }
      applyMetadata(settings)
      setSectionLoading(false)
      adoptSection(name, settings)
    } catch (error) {
      if (requestId.current !== id) return
      setSectionLoading(false)
      setSelectedSection(section)
      setSectionOutcome({ section: name, ok: false, message: t('emergency.section_load_failed', { section: name, error: messageFor(error) }) })
    }
  }

  const chooseSection = (next: string): void => {
    if (next === section) {
      if (sectionLoading) {
        requestId.current++
        setSectionLoading(false)
        setSelectedSection(section)
      }
      return
    }
    if (next === selectedSection) return
    setSelectedSection(next)
    if (dirty) {
      setPendingSection(next)
      setSectionDialogOpen(true)
      return
    }
    void loadSection(next)
  }

  const stayOnSection = (): void => {
    setPendingSection(null)
    setSelectedSection(section)
    setSectionDialogOpen(false)
  }

  const discardAndChange = (): void => {
    const next = pendingSection
    setPendingSection(null)
    setSectionDialogOpen(false)
    if (!next) return
    setDocumentText(baselineDocument)
    setSelectedSection(next)
    void loadSection(next)
  }

  const saveCurrentSection = async (): Promise<boolean> => {
    const targetSection = section
    let body: unknown
    try {
      body = JSON.parse(documentText)
    } catch {
      setSectionOutcome({ section: targetSection, ok: false, message: t('emergency.section_invalid_json', { section: targetSection }) })
      return false
    }
    setErrorMessage(null)
    setSectionOutcome(null)
    setWarnings([])
    setWarningSection(null)
    setBusy(true)
    try {
      const result = await emergencySave(targetSection, body)
      if (section !== targetSection) return false
      setBaselineDocument(documentText)
      setWarnings(result.warnings)
      setWarningSection(targetSection)
      setSectionOutcome({ section: targetSection, ok: true, message: t('emergency.section_stored_takes_effect_on_restart', { section: targetSection }) })
      return true
    } catch (error) {
      setSectionOutcome({ section: targetSection, ok: false, message: t('emergency.section_save_failed', { section: targetSection, error: messageFor(error) }) })
      return false
    } finally {
      setBusy(false)
    }
  }

  const saveAndChange = async (): Promise<void> => {
    if (busy || !pendingSection) return
    const next = pendingSection
    if (!(await saveCurrentSection())) return
    setPendingSection(null)
    setSectionDialogOpen(false)
    setSelectedSection(next)
    void loadSection(next)
  }

  const signIn = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    if (busy || !username.trim() || !password || (step === 'totp' && !code.trim())) return
    setErrorMessage(null)
    setBusy(true)
    try {
      const result = await emergencyLogin(username.trim(), password, step === 'totp' ? code : undefined)
      if (result.status === 'totp_required') {
        setStep('totp')
        return
      }
      await loadSettings()
    } catch (error) {
      setErrorMessage(messageFor(error))
    } finally {
      setBusy(false)
    }
  }

  const restart = async (): Promise<void> => {
    setErrorMessage(null)
    setBusy(true)
    try {
      const result = await emergencyRestart()
      setRestarting(result.restarting)
    } catch (error) {
      setErrorMessage(messageFor(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="sc-emergency">
      <div className="sc-emergency__card">
        <h1 className="sc-emergency__title">{t('emergency.emergency_settings')}</h1>
        <p className="sc-emergency__subtitle">{t('emergency.subtitle')}</p>
        {reason ? <p className="sc-emergency__banner" role="alert">{t('emergency.the_server_is_degraded', { reason })}</p> : null}
        {errorMessage ? <p className="sc-emergency__error" role="alert">{errorMessage}</p> : null}

        {step === 'loading' ? <p className="sc-emergency__hint">{t('common.loading')}</p> : null}
        {step === 'setup' ? (
          <>
            <p className="sc-emergency__hint">{t('emergency.no_administrator_yet')}</p>
            <div className="sc-emergency__actions"><Button onClick={() => { window.location.href = '/setup' }}>{t('emergency.go_to_setup')}</Button></div>
          </>
        ) : null}
        {step === 'credentials' || step === 'totp' ? (
          <form className="sc-emergency__form" onSubmit={signIn}>
            {step === 'credentials' ? (
              <>
                <TextField value={username} label={t('login.username')} autoFocus autoComplete="username" onValueChange={setUsername} />
                <TextField value={password} label={t('common.password')} type="password" autoComplete="current-password" onValueChange={setPassword} />
              </>
            ) : (
              <>
                <p className="sc-emergency__hint">{t('emergency.enter_your_code')}</p>
                <TextField value={code} label={t('login.verification_code')} autoFocus autoComplete="one-time-code" onValueChange={setCode} />
              </>
            )}
            <div className="sc-emergency__actions"><Button type="submit" loading={busy} disabled={!username.trim() || !password || (step === 'totp' && !code.trim())}>{t('login.sign')}</Button></div>
          </form>
        ) : null}
        {step === 'editing' ? (
          <>
            <dl className="sc-emergency__facts">
              <dt>{t('server.bind_address')}</dt><dd><code>{listen}</code></dd>
              <dt>{t('server.app_hosts_comma_separated')}</dt><dd><code>{appHosts.join(', ') || t('emergency.none')}</code></dd>
            </dl>
            <form className="sc-emergency__form" onSubmit={(event) => { event.preventDefault(); void saveCurrentSection() }}>
              <label className="sc-emergency__label" htmlFor="sc-emergency-section">{t('emergency.section')}</label>
              <select id="sc-emergency-section" className="sc-emergency__select" value={selectedSection} aria-busy={sectionLoading} onChange={(event) => chooseSection(event.target.value)}>
                {sections.map((name) => <option value={name} key={name}>{name}</option>)}
              </select>
              {sectionLoading ? <p className="sc-emergency__hint" role="status">{t('emergency.loading_section', { section: selectedSection })}</p> : null}
              <label className="sc-emergency__label" htmlFor="sc-emergency-doc">{t('emergency.stored_document')}</label>
              <textarea id="sc-emergency-doc" className="sc-emergency__doc" rows={14} spellCheck={false} disabled={sectionLoading || busy} value={documentText} onChange={(event) => setDocumentText(event.target.value)} />
              <p className="sc-emergency__hint">{t('emergency.document_hint')}</p>
              <div className="sc-emergency__actions">
                <Button type="submit" loading={busy} disabled={sectionLoading}>{t('common.save')}</Button>
                <Button variant="outlined" onClick={() => void restart()} loading={busy} disabled={sectionLoading}>{t('emergency.restart_now')}</Button>
              </div>
            </form>
            {sectionOutcome ? <p className={sectionOutcome.ok ? 'sc-emergency__ok' : 'sc-emergency__error'} role={sectionOutcome.ok ? 'status' : 'alert'}>{sectionOutcome.message}</p> : null}
            {warningSection && warnings.length > 0 ? <p className="sc-emergency__warning" role="status">{t('emergency.warnings_for_section', { section: warningSection })}</p> : null}
            {warnings.map((warning, index) => <p className="sc-emergency__warning" role="status" key={`${warning.reason_key}-${index}`}>{findingText(warning)}</p>)}
            {restarting === true ? <p className="sc-emergency__ok" role="status">{t('emergency.restarting_now')}</p> : null}
            {restarting === false ? <p className="sc-emergency__warning" role="status">{t('emergency.no_supervisor_to_restart')}</p> : null}
          </>
        ) : null}
      </div>
      <mdui-dialog ref={sectionDialogRef} open={sectionDialogOpen} headline={t('emergency.unsaved_section')} close-on-esc close-on-overlay-click>
        <p>{t('emergency.unsaved_section_prompt', { section: section })}</p>
        <mdui-button slot="action" variant="text" onClick={stayOnSection}>{t('editor.stay')}</mdui-button>
        <mdui-button slot="action" variant="outlined" onClick={discardAndChange}>{t('emergency.discard_and_change')}</mdui-button>
        <mdui-button slot="action" variant="filled" loading={busy} onClick={() => void saveAndChange()}>{t('emergency.save_and_change')}</mdui-button>
      </mdui-dialog>
    </main>
  )
}
