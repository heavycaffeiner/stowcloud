import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { overlay } from 'overlay-kit'
import { emergencyRestart, emergencySave, emergencySettings, type EmergencySettings } from '../api'
import { describeApiError } from '../../../api/error-text'
import { useBeforeUnload } from '../../../hooks/use-before-unload'
import { useI18n } from '../../../hooks/use-i18n'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { Select } from '../../../ui/Select'
import * as styles from '../routes/EmergencyPage.css'

/* i18n */ ;('settings.would_lock_you_out')
/* i18n */ ;('settings.proxy_range_is_everything')

type SectionChoice = 'stay' | 'discard' | 'save'
type SectionOutcome = { section: string; ok: boolean; message: string }

/** The section in the editor and the document it was opened or last saved with. */
interface OpenSection {
  settings: EmergencySettings
  name: string
  baseline: string
}

function openSection(settings: EmergencySettings, name: string): OpenSection {
  return { settings, name, baseline: JSON.stringify(settings.stored[name] ?? {}, null, 2) }
}

function askSectionChange(section: string): Promise<SectionChoice> {
  return overlay.openAsync<SectionChoice>(({ isOpen, close, unmount }) => (
    <SectionChangeDialog open={isOpen} section={section} onChoose={close} onClosed={unmount} />
  ))
}

interface SectionChangeDialogProps {
  open: boolean
  section: string
  onChoose: (choice: SectionChoice) => void
  onClosed: () => void
}

function SectionChangeDialog({ open, section, onChoose, onClosed }: SectionChangeDialogProps) {
  const { t } = useI18n()
  return (
    <Dialog
      open={open}
      title={t('emergency.unsaved_section')}
      onClose={() => onChoose('stay')}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" onClick={() => onChoose('stay')}>
            {t('editor.stay')}
          </Button>
          <Button variant="outlined" onClick={() => onChoose('discard')}>
            {t('emergency.discard_and_change')}
          </Button>
          <Button onClick={() => onChoose('save')}>{t('emergency.save_and_change')}</Button>
        </>
      }
    >
      <p>{t('emergency.unsaved_section_prompt', { section })}</p>
    </Dialog>
  )
}

/** Edits the stored settings one section at a time, as raw JSON. */
export function EmergencyEditor({ initial }: { initial: EmergencySettings }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(() =>
    openSection(initial, initial.sections.includes('network') ? 'network' : (initial.sections[0] ?? 'network'))
  )
  const [draft, setDraft] = useState(open.baseline)
  // The section the picker shows while a change to it waits on an answer or a load.
  const [target, setTarget] = useState<string | null>(null)
  const [outcome, setOutcome] = useState<SectionOutcome | null>(null)
  const load = useMutation({ mutationFn: emergencySettings })
  const save = useMutation({
    mutationFn: ({ section, body }: { section: string; body: unknown }) => emergencySave(section, body)
  })
  const restart = useMutation({ mutationFn: emergencyRestart })
  const dirty = draft !== open.baseline
  const locked = load.isPending || save.isPending || restart.isPending
  const describe = (error: unknown): string => describeApiError(error, t('emergency.something_went_wrong'))
  useBeforeUnload(dirty)

  async function saveSection(): Promise<boolean> {
    const section = open.name
    let body: unknown
    try {
      body = JSON.parse(draft)
    } catch {
      setOutcome({ section, ok: false, message: t('emergency.section_invalid_json', { section }) })
      return false
    }
    setOutcome(null)
    try {
      await save.mutateAsync({ section, body })
      setOpen((current) => ({ ...current, baseline: draft }))
      setOutcome({ section, ok: true, message: t('emergency.section_stored_takes_effect_on_restart', { section }) })
      return true
    } catch (error) {
      setOutcome({
        section,
        ok: false,
        message: t('emergency.section_save_failed', { section, error: describe(error) })
      })
      return false
    }
  }

  // A failed load keeps the draft, even one the user chose to discard.
  async function changeSection(name: string): Promise<void> {
    if (name === open.name || locked) return
    setTarget(name)
    if (dirty) {
      const choice = await askSectionChange(open.name)
      if (choice === 'stay' || (choice === 'save' && !(await saveSection()))) {
        setTarget(null)
        return
      }
    }
    setOutcome(null)
    try {
      const next = openSection(await load.mutateAsync(), name)
      setOpen(next)
      setDraft(next.baseline)
      save.reset()
    } catch (error) {
      setOutcome({
        section: name,
        ok: false,
        message: t('emergency.section_load_failed', { section: name, error: describe(error) })
      })
    }
    setTarget(null)
  }

  const warnings = save.data?.warnings ?? []
  return (
    <>
      {restart.error ? (
        <p className={styles.error} role="alert">
          {describe(restart.error)}
        </p>
      ) : null}
      <dl className={styles.facts}>
        <dt className={styles.factLabel}>{t('server.bind_address')}</dt>
        <dd className={styles.factValue}>
          <code>{open.settings.listen}</code>
        </dd>
        <dt className={styles.factLabel}>{t('server.app_hosts_comma_separated')}</dt>
        <dd className={styles.factValue}>
          <code>{open.settings.app_hosts.join(', ') || t('emergency.none')}</code>
        </dd>
      </dl>
      <form
        className={styles.form}
        onSubmit={(event) => {
          event.preventDefault()
          void saveSection()
        }}
      >
        <Select
          id="sc-emergency-section"
          label={t('emergency.section')}
          value={target ?? open.name}
          options={open.settings.sections.map((name) => ({ value: name, text: name }))}
          disabled={locked}
          onValueChange={(name) => void changeSection(name)}
        />
        {load.isPending && target ? (
          <p className={styles.hint} role="status">
            {t('emergency.loading_section', { section: target })}
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
          disabled={locked}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
        <p className={styles.hint}>{t('emergency.document_hint')}</p>
        <div className={styles.actions}>
          <Button className={styles.action} type="submit" loading={save.isPending} disabled={locked}>
            {t('common.save')}
          </Button>
          <Button
            className={styles.action}
            variant="outlined"
            onClick={() => restart.mutate()}
            loading={restart.isPending}
            disabled={locked}
          >
            {t('emergency.restart_now')}
          </Button>
        </div>
      </form>
      {outcome ? (
        <p className={outcome.ok ? styles.ok : styles.error} role={outcome.ok ? 'status' : 'alert'}>
          {outcome.message}
        </p>
      ) : null}
      {warnings.length > 0 ? (
        <p className={styles.warning} role="status">
          {t('emergency.warnings_for_section', { section: open.name })}
        </p>
      ) : null}
      {warnings.map((warning, index) => (
        <p className={styles.warning} role="status" key={`${warning.reason_key}-${index}`}>
          {t(warning.reason_key, warning.reason_params ?? {})}
        </p>
      ))}
      {restart.data?.restarting === true ? (
        <p className={styles.ok} role="status">
          {t('emergency.restarting_now')}
        </p>
      ) : null}
      {restart.data?.restarting === false ? (
        <p className={styles.warning} role="status">
          {t('emergency.no_supervisor_to_restart')}
        </p>
      ) : null}
    </>
  )
}
