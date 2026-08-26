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
    type EmergencyFinding
  } from '../../lib/api/emergency'
  import { ApiError } from '../../lib/api/types'
  import Button from '../../lib/ui/Button.svelte'
  import TextField from '../../lib/ui/TextField.svelte'

  type Step = 'loading' | 'setup' | 'credentials' | 'totp' | 'editing'

  let step = $state<Step>('loading')
  let reason = $state('')
  let username = $state('')
  let password = $state('')
  let code = $state('')
  let busy = $state(false)
  let errorMsg = $state<string | null>(null)

  let sections = $state<string[]>([])
  let section = $state('network')
  let document_ = $state('{}')
  let listen = $state('')
  let appHosts = $state<string[]>([])
  let warnings = $state<EmergencyFinding[]>([])
  let saved = $state(false)
  let restarting = $state<boolean | null>(null)

  // The catalogue renders the sentence; the server sends the key and its
  // placeholders, so the keys cannot be seen at the call site.
  /* i18n */ 'settings.would_lock_you_out'
  /* i18n */ 'settings.proxy_range_is_everything'
  function findingText(f: EmergencyFinding): string {
    return t(f.reason_key, f.reason_params ?? {})
  }

  function messageFor(err: unknown): string {
    if (!(err instanceof ApiError)) return t('emergency.something_went_wrong')
    const key = err.detail?.reason_key
    if (typeof key === 'string') {
      const params = err.detail?.reason_params
      return t(key, typeof params === 'object' && params !== null ? (params as Record<string, string>) : {})
    }
    return err.message
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
    const s = await emergencySettings()
    sections = s.sections
    listen = s.listen
    appHosts = s.app_hosts
    document_ = JSON.stringify(s.stored[section] ?? {}, null, 2)
    step = 'editing'
  }

  function pickSection(next: string): void {
    section = next
    saved = false
    warnings = []
    void (async () => {
      try {
        const s = await emergencySettings()
        document_ = JSON.stringify(s.stored[next] ?? {}, null, 2)
      } catch (err) {
        errorMsg = messageFor(err)
      }
    })()
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

  async function save(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    errorMsg = null
    warnings = []
    saved = false
    let body: unknown
    try {
      body = JSON.parse(document_)
    } catch {
      // Refused here rather than sent: a malformed document would come back as
      // a generic parse failure with nothing pointing at which line.
      errorMsg = t('emergency.that_is_not_valid_json')
      return
    }
    busy = true
    try {
      const res = await emergencySave(section, body)
      warnings = res.warnings
      saved = true
    } catch (err) {
      errorMsg = messageFor(err)
    } finally {
      busy = false
    }
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
        <dd><code>{appHosts.join(', ') || t('emergency.none')}</code></dd>
      </dl>

      <form class="sc-emergency__form" onsubmit={save}>
        <label class="sc-emergency__label" for="sc-emergency-section">{t('emergency.section')}</label>
        <select
          id="sc-emergency-section"
          class="sc-emergency__select"
          value={section}
          onchange={(e) => pickSection((e.currentTarget as HTMLSelectElement).value)}
        >
          {#each sections as s (s)}
            <option value={s}>{s}</option>
          {/each}
        </select>

        <label class="sc-emergency__label" for="sc-emergency-doc">{t('emergency.stored_document')}</label>
        <textarea
          id="sc-emergency-doc"
          class="sc-emergency__doc"
          rows="14"
          spellcheck="false"
          bind:value={document_}
        ></textarea>
        <p class="sc-emergency__hint">{t('emergency.document_hint')}</p>

        <div class="sc-emergency__actions">
          <Button variant="filled" type="submit" loading={busy}>{t('common.save')}</Button>
          <Button variant="outlined" onclick={restart} loading={busy}>{t('emergency.restart_now')}</Button>
        </div>
      </form>

      {#if saved}
        <p class="sc-emergency__ok" role="status">{t('emergency.stored_takes_effect_on_restart')}</p>
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
