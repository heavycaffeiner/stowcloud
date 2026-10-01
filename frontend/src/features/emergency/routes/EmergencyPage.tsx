import type { EmergencyFinding } from '../api'
import { describeApiError } from '../../../api/error-text'
import { useI18n } from '../../../hooks/use-i18n'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { Select } from '../../../ui/Select'
import { TextField } from '../../../ui/TextField'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import { useEmergencyLifecycle } from '../hooks/use-emergency-lifecycle'
import { useEmergencyFlows } from '../hooks/use-emergency-flows'
import { useEmergencyState } from '../hooks/use-emergency-state'
import * as styles from './EmergencyPage.css'

/* i18n */ ;('settings.would_lock_you_out')
/* i18n */ ;('settings.proxy_range_is_everything')

export function EmergencyPage() {
  const { t } = useI18n()
  const { state, actions } = useEmergencyState()
  const {
    step,
    reason,
    username,
    password,
    code,
    busy,
    errorMessage,
    sections,
    section,
    selectedSection,
    sectionLoading,
    documentText,
    baselineDocument,
    listen,
    appHosts,
    warnings,
    warningSection,
    restarting,
    sectionOutcome,
    sectionDialogOpen
  } = state
  const dirty = documentText !== baselineDocument
  const flows = useEmergencyFlows(state, actions)
  useDocumentTitle(t('emergency.emergency_settings'))
  const messageFor = (error: unknown): string => describeApiError(error, t('emergency.something_went_wrong'))
  useEmergencyLifecycle({ setState: actions.patch, dirty, messageFor })
  const findingText = (finding: EmergencyFinding): string => t(finding.reason_key, finding.reason_params ?? {})

  return (
    <main className={styles.root}>
      <div className={styles.card}>
        <h1 className={styles.title}>{t('emergency.emergency_settings')}</h1>
        <p className={styles.subtitle}>{t('emergency.subtitle')}</p>
        {reason ? (
          <p className={styles.banner} role="alert">
            {t('emergency.the_server_is_degraded', { reason })}
          </p>
        ) : null}
        {errorMessage ? (
          <p className={styles.error} role="alert">
            {errorMessage}
          </p>
        ) : null}
        {step === 'loading' ? <p className={styles.hint}>{t('common.loading')}</p> : null}
        {step === 'setup' ? (
          <>
            <p className={styles.hint}>{t('emergency.no_administrator_yet')}</p>
            <div className={styles.actions}>
              <Button
                className={styles.action}
                onClick={() => {
                  window.location.href = '/setup'
                }}
              >
                {t('emergency.go_to_setup')}
              </Button>
            </div>
          </>
        ) : null}
        {step === 'credentials' || step === 'totp' ? (
          <form className={styles.form} onSubmit={flows.signIn}>
            {step === 'credentials' ? (
              <>
                <TextField
                  value={username}
                  label={t('login.username')}
                  autoFocus
                  autoComplete="username"
                  onValueChange={actions.setUsername}
                />
                <TextField
                  value={password}
                  label={t('common.password')}
                  type="password"
                  autoComplete="current-password"
                  onValueChange={actions.setPassword}
                />
              </>
            ) : (
              <>
                <p className={styles.hint}>{t('emergency.enter_your_code')}</p>
                <TextField
                  value={code}
                  label={t('login.verification_code')}
                  autoFocus
                  autoComplete="one-time-code"
                  onValueChange={actions.setCode}
                />
              </>
            )}
            <div className={styles.actions}>
              <Button
                className={styles.action}
                type="submit"
                loading={busy}
                disabled={!username.trim() || !password || (step === 'totp' && !code.trim())}
              >
                {t('login.sign')}
              </Button>
            </div>
          </form>
        ) : null}
        {step === 'editing' ? (
          <>
            <dl className={styles.facts}>
              <dt className={styles.factLabel}>{t('server.bind_address')}</dt>
              <dd className={styles.factValue}>
                <code>{listen}</code>
              </dd>
              <dt className={styles.factLabel}>{t('server.app_hosts_comma_separated')}</dt>
              <dd className={styles.factValue}>
                <code>{appHosts.join(', ') || t('emergency.none')}</code>
              </dd>
            </dl>
            <form
              className={styles.form}
              onSubmit={(event) => {
                event.preventDefault()
                void flows.saveCurrentSection()
              }}
            >
              <Select
                id="sc-emergency-section"
                label={t('emergency.section')}
                value={selectedSection}
                options={sections.map((name) => ({ value: name, text: name }))}
                disabled={sectionLoading || busy}
                onValueChange={flows.chooseSection}
              />
              {sectionLoading ? (
                <p className={styles.hint} role="status">
                  {t('emergency.loading_section', { section: selectedSection })}
                </p>
              ) : null}
              <label className={styles.label} htmlFor="sc-emergency-doc">
                {t('emergency.stored_document')}
              </label>
              <textarea
                id="sc-emergency-doc"
                className={styles.textarea}
                rows={14}
                spellCheck={false}
                disabled={sectionLoading || busy}
                value={documentText}
                onChange={(event) => actions.setDocumentText(event.target.value)}
              />
              <p className={styles.hint}>{t('emergency.document_hint')}</p>
              <div className={styles.actions}>
                <Button className={styles.action} type="submit" loading={busy} disabled={sectionLoading}>
                  {t('common.save')}
                </Button>
                <Button
                  className={styles.action}
                  variant="outlined"
                  onClick={() => void flows.restart()}
                  loading={busy}
                  disabled={sectionLoading}
                >
                  {t('emergency.restart_now')}
                </Button>
              </div>
            </form>
            {sectionOutcome ? (
              <p className={sectionOutcome.ok ? styles.ok : styles.error} role={sectionOutcome.ok ? 'status' : 'alert'}>
                {sectionOutcome.message}
              </p>
            ) : null}
            {warningSection && warnings.length > 0 ? (
              <p className={styles.warning} role="status">
                {t('emergency.warnings_for_section', { section: warningSection })}
              </p>
            ) : null}
            {warnings.map((warning, index) => (
              <p className={styles.warning} role="status" key={`${warning.reason_key}-${index}`}>
                {findingText(warning)}
              </p>
            ))}
            {restarting === true ? (
              <p className={styles.ok} role="status">
                {t('emergency.restarting_now')}
              </p>
            ) : null}
            {restarting === false ? (
              <p className={styles.warning} role="status">
                {t('emergency.no_supervisor_to_restart')}
              </p>
            ) : null}
          </>
        ) : null}
      </div>
      <Dialog
        open={sectionDialogOpen}
        title={t('emergency.unsaved_section')}
        onClose={flows.stayOnSection}
        actions={
          <>
            <Button variant="text" onClick={flows.stayOnSection}>
              {t('editor.stay')}
            </Button>
            <Button variant="outlined" onClick={flows.discardAndChange}>
              {t('emergency.discard_and_change')}
            </Button>
            <Button loading={busy} onClick={() => void flows.saveAndChange()}>
              {t('emergency.save_and_change')}
            </Button>
          </>
        }
      >
        <p>{t('emergency.unsaved_section_prompt', { section })}</p>
      </Dialog>
    </main>
  )
}
