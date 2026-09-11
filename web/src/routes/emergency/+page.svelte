<script lang="ts">
  // The safe-mode settings editor. Top-level route, outside `(app)`: its shell
  // reads the session, the roots and the jobs, none of which exist in a
  // process whose engine did not come up.
  //
  // It edits the settings document as a document, one section at a time, and
  // does not draw the settings screen's controls. That is deliberate: those
  // controls are built from a field list the engine produces, and this screen
  // exists precisely for when there is no engine. What is true in every mode
  // is what is in the database, so that is what this shows.
  import { t } from '../../lib/i18n'
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
  import Button from '../../lib/ui/Button.svelte'
  import Dialog from '../../lib/ui/Dialog.svelte'
  import TextField from '../../lib/ui/TextField.svelte'

  type Step = 'loading' | 'setup' | 'credentials' | 'totp' | 'editing'
  type SectionOutcome = { section: string; ok: boolean; message: string }

  let step = $state<Step>('loading')
  let reason = $state('')
  let username = $state('')
  let password = $state('')
  let code = $state('')
  let busy = $state(false)
  let errorMsg = $state<string | null>(null)

  let sections = $state<string[]>([])
  let section = $state('network')
  let selectedSection = $state('network')
  let sectionLoading = $state(false)
  let document_ = $state('{}')
  let baselineDocument = $state('{}')
  const dirty = $derived(document_ !== baselineDocument)
  let listen = $state('')
  let appHosts = $state<string[]>([])
  let warnings = $state<EmergencyFinding[]>([])
  let warningSection = $state<string | null>(null)
  let restarting = $state<boolean | null>(null)
  let sectionOutcome = $state<SectionOutcome | null>(null)

  let settingsRequestId = 0
  let pendingSection = $state<string | null>(null)
  let sectionDialogOpen = $state(false)

  // The catalogue renders the sentence; the server sends the key and its
  // placeholders, so the keys cannot be seen at the call site.
  /* i18n */ 'settings.would_lock_you_out'
  /* i18n */ 'settings.proxy_range_is_everything'
  function findingText(f: EmergencyFinding): string {
    return t(f.reason_key, f.reason_params ?? {})
  }

  function messageFor(err: unknown): string {
    return describeApiError(err, t('emergency.something_went_wrong'))
  }

  function storedDocument(settings: EmergencySettings, name: string): string {
    return JSON.stringify(settings.stored?.[name] ?? {}, null, 2)
  }

  function applySettingsMetadata(settings: EmergencySettings): void {
    sections = settings.sections ?? []
    listen = settings.listen ?? ''
    appHosts = settings.app_hosts ?? []
  }

  function adoptSectionDocument(name: string, settings: EmergencySettings): void {
    const next = storedDocument(settings, name)
    section = name
    selectedSection = name
    document_ = next
    baselineDocument = next
    sectionOutcome = null
    warnings = []
    warningSection = null
  }

  $effect(() => {
    void (async () => {
      try {
        const door = await emergencyDoor()
        reason = door.reason
        step = door.setup_required ? 'setup' : 'credentials'
      } catch (err) {
        // The gate answers 404 to a source address it does not admit, which is
        // what somebody reaching this from outside the local network sees.
        errorMsg = messageFor(err)
        step = 'credentials'
      }
    })()
  })

  async function loadSettings(): Promise<void> {
    const requestId = ++settingsRequestId
    sectionLoading = true
    errorMsg = null
    try {
      const settings = await emergencySettings()
      if (requestId !== settingsRequestId) return
      applySettingsMetadata(settings)
      const initialSection = sections.includes(section) ? section : (sections[0] ?? 'network')
      step = 'editing'
      sectionLoading = false
      adoptSectionDocument(initialSection, settings)
    } catch (err) {
      if (requestId !== settingsRequestId) return
      sectionLoading = false
      errorMsg = messageFor(err)
    }
  }

  async function loadSection(name: string): Promise<void> {
    const requestId = ++settingsRequestId
    sectionLoading = true
    sectionOutcome = null
    try {
      const settings = await emergencySettings()
      if (requestId !== settingsRequestId) return
      if (dirty) {
        // A response that arrives after somebody starts typing is never
        // allowed to replace their text. Revert the selector to the active
        // section and require an explicit discard or save choice next time.
        selectedSection = section
        sectionLoading = false
        sectionOutcome = {
          section: name,
          ok: false,
          message: t('emergency.section_change_requires_choice', { section: name })
        }
        return
      }
      applySettingsMetadata(settings)
      sectionLoading = false
      adoptSectionDocument(name, settings)
    } catch (err) {
      if (requestId !== settingsRequestId) return
      sectionLoading = false
      selectedSection = section
      sectionOutcome = {
        section: name,
        ok: false,
        message: t('emergency.section_load_failed', { section: name, error: messageFor(err) })
      }
    }
  }

  function pickSection(next: string): void {
    if (next === section) {
      if (sectionLoading) {
        settingsRequestId++
        sectionLoading = false
        selectedSection = section
      }
      return
    }
    if (next === selectedSection) return
    selectedSection = next
    if (dirty) {
      pendingSection = next
      sectionDialogOpen = true
      return
    }
    void loadSection(next)
  }

  function stayOnSection(): void {
    pendingSection = null
    selectedSection = section
    sectionDialogOpen = false
  }

  function discardSectionAndLoad(): void {
    const next = pendingSection
    pendingSection = null
    sectionDialogOpen = false
    if (!next) return
    document_ = baselineDocument
    selectedSection = next
    void loadSection(next)
  }

  async function saveSectionAndLoad(): Promise<void> {
    if (busy) return
    const next = pendingSection
    if (!next) return
    if (!(await saveCurrentSection())) return
    pendingSection = null
    sectionDialogOpen = false
    selectedSection = next
    void loadSection(next)
  }

  async function signIn(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    errorMsg = null
    busy = true
    try {
      const res = await emergencyLogin(username, password, step === 'totp' ? code : undefined)
      if (res.status === 'totp_required') {
        // The password was right and the code is next, which is not a failure.
        step = 'totp'
        return
      }
      await loadSettings()
    } catch (err) {
      errorMsg = messageFor(err)
    } finally {
      busy = false
    }
  }

  async function saveCurrentSection(): Promise<boolean> {
    const targetSection = section
    let body: unknown
    try {
      body = JSON.parse(document_)
    } catch {
      sectionOutcome = {
        section: targetSection,
        ok: false,
        message: t('emergency.section_invalid_json', { section: targetSection })
      }
      return false
    }

    errorMsg = null
    sectionOutcome = null
    warnings = []
    warningSection = null
    busy = true
    try {
      const res = await emergencySave(targetSection, body)
      if (section !== targetSection) return false
      baselineDocument = document_
      warnings = res.warnings
      warningSection = targetSection
      sectionOutcome = {
        section: targetSection,
        ok: true,
        message: t('emergency.section_stored_takes_effect_on_restart', { section: targetSection })
      }
      return true
    } catch (err) {
      sectionOutcome = {
        section: targetSection,
        ok: false,
        message: t('emergency.section_save_failed', { section: targetSection, error: messageFor(err) })
      }
      return false
    } finally {
      busy = false
    }
  }

  function save(e: SubmitEvent): void {
    e.preventDefault()
    void saveCurrentSection()
  }

  async function restart(): Promise<void> {
    errorMsg = null
    busy = true
    try {
      const res = await emergencyRestart()
      restarting = res.restarting
    } catch (err) {
      errorMsg = messageFor(err)
    } finally {
      busy = false
    }
  }
</script>

<svelte:head><title>{t('emergency.emergency_settings')}</title></svelte:head>

<div class="sc-emergency">
  <div class="sc-emergency__card">
    <h1 class="sc-emergency__title">{t('emergency.emergency_settings')}</h1>
    <p class="sc-emergency__subtitle">{t('emergency.subtitle')}</p>

    {#if reason}
      <!-- Somebody was sent here rather than arriving deliberately. What
           failed is the first thing they need. -->
      <p class="sc-emergency__banner" role="alert">
        {t('emergency.the_server_is_degraded', { reason })}
      </p>
    {/if}

    {#if errorMsg}
      <p class="sc-emergency__error" role="alert">{errorMsg}</p>
    {/if}

    {#if step === 'loading'}
      <p class="sc-emergency__hint">{t('common.loading')}</p>
    {:else if step === 'setup'}
      <p class="sc-emergency__hint">{t('emergency.no_administrator_yet')}</p>
      <div class="sc-emergency__actions">
        <Button variant="filled" onclick={() => (window.location.href = '/setup')}>
          {t('emergency.go_to_setup')}
        </Button>
      </div>
    {:else if step === 'credentials' || step === 'totp'}
      <form class="sc-emergency__form" onsubmit={signIn}>
        {#if step === 'credentials'}
          <TextField label={t('login.username')} bind:value={username} autofocus autocomplete="username" />
          <TextField
            label={t('common.password')}
            type="password"
            bind:value={password}
            autocomplete="current-password"
          />
        {:else}
          <p class="sc-emergency__hint">{t('emergency.enter_your_code')}</p>
          <TextField label={t('login.verification_code')} bind:value={code} autofocus autocomplete="one-time-code" />
        {/if}
        <div class="sc-emergency__actions">
          <Button variant="filled" type="submit" loading={busy}>{t('login.sign')}</Button>
        </div>
      </form>
    {:else}
      <dl class="sc-emergency__facts">
        <dt>{t('server.bind_address')}</dt>
        <dd><code>{listen}</code></dd>
        <dt>{t('server.app_hosts_comma_separated')}</dt>
        <dd><code>{appHosts?.join(', ') || t('emergency.none')}</code></dd>
      </dl>

      <form class="sc-emergency__form" onsubmit={save}>
        <label class="sc-emergency__label" for="sc-emergency-section">{t('emergency.section')}</label>
        <select
          id="sc-emergency-section"
          class="sc-emergency__select"
          value={selectedSection}
          aria-busy={sectionLoading}
          onchange={(e) => pickSection((e.currentTarget as HTMLSelectElement).value)}
        >
          {#each sections as s (s)}
            <option value={s}>{s}</option>
          {/each}
        </select>

        {#if sectionLoading}
          <p class="sc-emergency__hint" role="status">
            {t('emergency.loading_section', { section: selectedSection })}
          </p>
        {/if}

        <label class="sc-emergency__label" for="sc-emergency-doc">{t('emergency.stored_document')}</label>
        <textarea
          id="sc-emergency-doc"
          class="sc-emergency__doc"
          rows="14"
          spellcheck="false"
          disabled={sectionLoading || busy}
          bind:value={document_}
        ></textarea>
        <p class="sc-emergency__hint">{t('emergency.document_hint')}</p>

        <div class="sc-emergency__actions">
          <Button variant="filled" type="submit" loading={busy} disabled={sectionLoading}>{t('common.save')}</Button>
          <Button variant="outlined" onclick={restart} loading={busy} disabled={sectionLoading}>{t('emergency.restart_now')}</Button>
        </div>
      </form>

      {#if sectionOutcome}
        <p class:sc-emergency__ok={sectionOutcome.ok} class:sc-emergency__error={!sectionOutcome.ok} role={sectionOutcome.ok ? 'status' : 'alert'}>
          {sectionOutcome.message}
        </p>
      {/if}
      {#if warningSection && warnings.length > 0}
        <p class="sc-emergency__warning" role="status">
          {t('emergency.warnings_for_section', { section: warningSection })}
        </p>
      {/if}
      {#each warnings as w, i (w.reason_key + i)}
        <p class="sc-emergency__warning" role="status">{findingText(w)}</p>
      {/each}
      {#if restarting === true}
        <p class="sc-emergency__ok" role="status">{t('emergency.restarting_now')}</p>
      {:else if restarting === false}
        <p class="sc-emergency__warning" role="status">{t('emergency.no_supervisor_to_restart')}</p>
      {/if}
    {/if}
  </div>
</div>
<Dialog open={sectionDialogOpen} title={t('emergency.unsaved_section')} onclose={stayOnSection}>
  <p>{t('emergency.unsaved_section_prompt', { section })}</p>
  {#snippet actions()}
    <Button variant="text" onclick={stayOnSection}>{t('editor.stay')}</Button>
    <Button variant="outlined" danger onclick={discardSectionAndLoad}>{t('emergency.discard_and_change')}</Button>
    <Button variant="filled" loading={busy} onclick={saveSectionAndLoad}>{t('emergency.save_and_change')}</Button>
  {/snippet}
</Dialog>

<style>
  .sc-emergency {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    min-height: 100dvh;
    padding: var(--sc-page-pad);
    background: var(--m3c-surface);
  }
  .sc-emergency__card {
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: min(640px, 100%);
    padding: 24px;
    border-radius: var(--m3-shape-large);
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.3);
  }
  .sc-emergency__title {
    margin: 0;
    @apply --m3-headline-small;
  }
  .sc-emergency__subtitle,
  .sc-emergency__hint {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-medium;
  }
  .sc-emergency__form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .sc-emergency__actions {
    display: flex;
    gap: 8px;
  }
  .sc-emergency__label {
    @apply --m3-label-large;
  }
  .sc-emergency__select,
  .sc-emergency__doc {
    padding: 8px 12px;
    border: 1px solid var(--m3c-outline);
    border-radius: var(--m3-shape-small);
    background: var(--m3c-surface);
    color: var(--m3c-on-surface);
    font: inherit;
  }
  .sc-emergency__doc {
    font-family: ui-monospace, monospace;
    resize: vertical;
  }
  .sc-emergency__facts {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 4px 16px;
    margin: 0;
    @apply --m3-body-medium;
  }
  .sc-emergency__facts dt {
    color: var(--m3c-on-surface-variant);
  }
  .sc-emergency__facts dd {
    margin: 0;
  }
  .sc-emergency__banner,
  .sc-emergency__warning {
    margin: 0;
    padding: 12px 16px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-surface-container-highest);
    color: var(--m3c-on-surface);
    @apply --m3-body-medium;
  }
  .sc-emergency__error {
    margin: 0;
    padding: 12px 16px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-medium;
  }
  .sc-emergency__ok {
    margin: 0;
    color: var(--m3c-primary);
    @apply --m3-body-medium;
  }
</style>
