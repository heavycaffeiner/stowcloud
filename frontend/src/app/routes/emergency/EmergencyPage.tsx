import { useRef } from 'react'
import type { EmergencyFinding } from '../../../lib/api/emergency'
import { describeApiError } from '../../../lib/api/error-text'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { Button } from '../../../lib/ui/Button'
import { Select } from '../../../lib/ui/Select'
import { TextField } from '../../../lib/ui/TextField'
import { useDocumentTitle } from '../../use-document-title'
import { useEmergencyLifecycle } from './use-emergency-lifecycle'
import { useEmergencyFlows } from './use-emergency-flows'
import { useEmergencyState } from './use-emergency-state'
import '../../../styles/app/routes/emergency.css.ts'

/* i18n */ 'settings.would_lock_you_out'
/* i18n */ 'settings.proxy_range_is_everything'

export function EmergencyPage() {
  const { t } = useI18n()
  const { state, actions } = useEmergencyState()
  const { step, reason, username, password, code, busy, errorMessage, sections, section, selectedSection, sectionLoading, documentText, baselineDocument, listen, appHosts, warnings, warningSection, restarting, sectionOutcome, pendingSection, sectionDialogOpen } = state
  const sectionDialogRef = useRef<HTMLElement | null>(null)
  const dirty = documentText !== baselineDocument
  const flows = useEmergencyFlows(state, actions)
  useDocumentTitle(t('emergency.emergency_settings'))
  const messageFor = (error: unknown): string => describeApiError(error, t('emergency.something_went_wrong'))
  useEmergencyLifecycle({ sectionDialogRef, setState: actions.patch, dirty, messageFor })
  const findingText = (finding: EmergencyFinding): string => t(finding.reason_key, finding.reason_params ?? {})

  return (
    <main className="sc-emergency">
      <div className="sc-emergency-card">
        <h1 className="sc-emergency-title">{t('emergency.emergency_settings')}</h1>
        <p className="sc-emergency-subtitle">{t('emergency.subtitle')}</p>
        {reason ? <p className="sc-emergency-banner" role="alert">{t('emergency.the_server_is_degraded', { reason })}</p> : null}
        {errorMessage ? <p className="sc-emergency-error" role="alert">{errorMessage}</p> : null}
        {step === 'loading' ? <p className="sc-emergency-hint">{t('common.loading')}</p> : null}
        {step === 'setup' ? <><p className="sc-emergency-hint">{t('emergency.no_administrator_yet')}</p><div className="sc-emergency-actions"><Button onClick={() => { window.location.href = '/setup' }}>{t('emergency.go_to_setup')}</Button></div></> : null}
        {step === 'credentials' || step === 'totp' ? (
          <form className="sc-emergency-form" onSubmit={flows.signIn}>
            {step === 'credentials' ? <><TextField value={username} label={t('login.username')} autoFocus autoComplete="username" onValueChange={actions.setUsername} /><TextField value={password} label={t('common.password')} type="password" autoComplete="current-password" onValueChange={actions.setPassword} /></> : <><p className="sc-emergency-hint">{t('emergency.enter_your_code')}</p><TextField value={code} label={t('login.verification_code')} autoFocus autoComplete="one-time-code" onValueChange={actions.setCode} /></>}
            <div className="sc-emergency-actions"><Button type="submit" loading={busy} disabled={!username.trim() || !password || (step === 'totp' && !code.trim())}>{t('login.sign')}</Button></div>
          </form>
        ) : null}
        {step === 'editing' ? (
          <>
            <dl className="sc-emergency-facts"><dt>{t('server.bind_address')}</dt><dd><code>{listen}</code></dd><dt>{t('server.app_hosts_comma_separated')}</dt><dd><code>{appHosts.join(', ') || t('emergency.none')}</code></dd></dl>
            <form className="sc-emergency-form" onSubmit={(event) => { event.preventDefault(); void flows.saveCurrentSection() }}>
              <Select id="sc-emergency-section" label={t('emergency.section')} value={selectedSection} options={sections.map((name) => ({ value: name, text: name }))} disabled={sectionLoading || busy} onValueChange={flows.chooseSection} />
              {sectionLoading ? <p className="sc-emergency-hint" role="status">{t('emergency.loading_section', { section: selectedSection })}</p> : null}
              <label className="sc-emergency-label" htmlFor="sc-emergency-doc">{t('emergency.stored_document')}</label>
              <textarea id="sc-emergency-doc" className="sc-emergency-textarea" rows={14} spellCheck={false} disabled={sectionLoading || busy} value={documentText} onChange={(event) => actions.setDocumentText(event.target.value)} />
              <p className="sc-emergency-hint">{t('emergency.document_hint')}</p>
              <div className="sc-emergency-actions"><Button type="submit" loading={busy} disabled={sectionLoading}>{t('common.save')}</Button><Button variant="outlined" onClick={() => void flows.restart()} loading={busy} disabled={sectionLoading}>{t('emergency.restart_now')}</Button></div>
            </form>
            {sectionOutcome ? <p className={sectionOutcome.ok ? 'sc-emergency-ok' : 'sc-emergency-error'} role={sectionOutcome.ok ? 'status' : 'alert'}>{sectionOutcome.message}</p> : null}
            {warningSection && warnings.length > 0 ? <p className="sc-emergency-warning" role="status">{t('emergency.warnings_for_section', { section: warningSection })}</p> : null}
            {warnings.map((warning, index) => <p className="sc-emergency-warning" role="status" key={`${warning.reason_key}-${index}`}>{findingText(warning)}</p>)}
            {restarting === true ? <p className="sc-emergency-ok" role="status">{t('emergency.restarting_now')}</p> : null}
            {restarting === false ? <p className="sc-emergency-warning" role="status">{t('emergency.no_supervisor_to_restart')}</p> : null}
          </>
        ) : null}
      </div>
      <mdui-dialog ref={sectionDialogRef} open={sectionDialogOpen} headline={t('emergency.unsaved_section')} close-on-esc close-on-overlay-click>
        <p>{t('emergency.unsaved_section_prompt', { section })}</p>
        <mdui-button slot="action" variant="text" onClick={flows.stayOnSection}>{t('editor.stay')}</mdui-button>
        <mdui-button slot="action" variant="outlined" onClick={flows.discardAndChange}>{t('emergency.discard_and_change')}</mdui-button>
        <mdui-button slot="action" variant="filled" loading={busy} onClick={() => void flows.saveAndChange()}>{t('emergency.save_and_change')}</mdui-button>
      </mdui-dialog>
    </main>
  )
}
