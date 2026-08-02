<script lang="ts">
  // Top-level route (sibling of `(app)` and `s/[token]`), not inside the
  // `(app)` group on purpose — DESIGN-FRONTEND.md §7's nav rail/upload tray
  // shell has no business existing before the user is authenticated.
  import { t } from '../../lib/i18n'
  import { goto } from '$app/navigation'
  import { page } from '$app/state'
  import { api, ApiError } from '../../lib/api/client'
  import { fetchOidcConfig, oidcErrorMessage, startOidcLogin } from '../../lib/api/oidc'
  import { completeLogin } from '../../lib/state/auth-bootstrap'
  import Button from '../../lib/ui/Button.svelte'
  import TextField from '../../lib/ui/TextField.svelte'

  // AGPL §13 (docs/DEPLOYMENT.md §14.1): a network service built from modified
  // source must offer that source to everyone who reaches it, not just to
  // whoever received the binary. This URL has to serve the tree the running
  // build came from — **if you modify Stowcloud and run it for anyone but
  // yourself, repoint this at your own source.** Left as-is, a modified
  // deployment's offer points at code it is not running, which is worse than
  // no offer at all.
  const SOURCE_URL = 'https://github.com/heavycaffeiner/stowcloud'

  // Login Flow v2's `returnTo` (`sc-compat-nc::login_flow::LoginFlowService::
  // login_redirect`): an unauthenticated visit to
  // `/index.php/login/v2/flow/<token>` — the URL a compat-protocol mobile app
  // opens in the system browser — bounces here with `?returnTo=` pointing
  // back at that same flow URL so the human lands on the consent screen
  // ("Grant access") after signing in, instead of the file browser.
  //
  // Before this existed, a device's very first login always failed: the app
  // opens the login URL, gets redirected here (fresh browser, no session
  // cookie yet), the human authenticates, and this page unconditionally sent
  // them to `/b/` — dropping `returnTo` on the floor. The consent screen was
  // never reached, `grant` was never called, and the app's poll loop sat on
  // 404 until Login Flow v2's ~20-minute expiry. Only a session that was
  // *already* authenticated when the flow URL was first opened (e.g. a
  // desktop browser already logged in) ever completed the handshake.
  //
  // `returnTo` is reflected off a query parameter a client can set to
  // anything, so it is validated before use: it must be a same-origin,
  // single-leading-slash path. Rejecting `//host/...` and `/\host/...`
  // matters because browsers treat both as protocol-relative URLs — i.e. an
  // open redirect to an attacker's host — and a bare scheme
  // (`https://evil`) never starts with `/` at all.
  function safeReturnTo(raw: string | null): string | null {
    if (!raw) return null
    if (!raw.startsWith('/')) return null
    if (raw.startsWith('//') || raw.startsWith('/\\')) return null
    return raw
  }

  const returnTo = $derived(safeReturnTo(page.url.searchParams.get('returnTo')))

  // The consent screen lives outside the SPA — it is rendered server-side by
  // `sc-compat-nc` — so this is a full navigation (`goto()` would try to
  // resolve it as a client-side route and 404 against the SvelteKit router).
  async function afterLogin(): Promise<void> {
    if (returnTo) {
      window.location.href = returnTo
      return
    }
    await completeLogin()
    await goto('/b/')
  }

  type Step = 'credentials' | 'totp'
  let step = $state<Step>('credentials')
  let username = $state('')
  let password = $state('')
  let code = $state('')
  let challenge = ''
  let submitting = $state(false)
  let errorMsg = $state<string | null>(null)

  // Single sign-on (`docs/proposals/stowcloud-0-oidc-login.md` §5-1).
  //
  // Fetched rather than assumed, and only the two things this screen is
  // allowed to know: whether a provider is configured, and what to write on
  // the button. Failure means no button, so a deployment without an IdP (or
  // one whose config route is unreachable) shows exactly the password form it
  // showed before any of this existed.
  let ssoName = $state<string | null>(null)
  fetchOidcConfig().then((cfg) => {
    if (cfg.enabled) ssoName = cfg.display_name
  })

  // §5-2 table B. The callback answers a person's browser with a redirect, not
  // JSON, so a failed sign-in arrives back here as `?oidc_error=<code>` and
  // this is where that code becomes a sentence. Shown as its own message
  // rather than through `errorMsg`, which belongs to the password form and is
  // cleared the moment somebody submits it.
  const ssoError = $derived(oidcErrorMessage(page.url.searchParams.get('oidc_error')))

  // A full navigation, not `fetch`: `/api/auth/oidc/start` answers a `302` to
  // the provider, and the person has to actually arrive at the provider's own
  // login page. `returnTo` rides along so a compat client's consent screen
  // is still where they land afterwards.
  function signInWithSso(): void {
    startOidcLogin(returnTo)
  }

  function messageFor(err: unknown): string {
    if (err instanceof ApiError) {
      switch (err.code) {
        case 'auth.invalid_credentials':
          return t('login.incorrect_username_or_password')
        case 'rate.limited':
          return t('login.too_many_sign_attempts_try')
        default:
          // Deliberately not surfacing err.message here — it may be a raw,
          // untranslated string from the server and this UI is Korean-only.
          return t('login.could_not_sign_try_again')
      }
    }
    return t('login.could_not_sign_check_your')
  }

  async function submitCredentials(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    if (submitting || !username.trim() || !password) return
    errorMsg = null
    submitting = true
    try {
      const result = await api.login(username.trim(), password)
      if (result.status === 'totp_required') {
        challenge = result.challenge
        step = 'totp'
      } else {
        await afterLogin()
      }
    } catch (err) {
      errorMsg = messageFor(err)
    } finally {
      submitting = false
    }
  }

  async function submitTotp(e: SubmitEvent): Promise<void> {
    e.preventDefault()
    if (submitting || code.trim().length === 0) return
    errorMsg = null
    submitting = true
    try {
      const result = await api.loginTotp(challenge, code.trim())
      if (result.status === 'ok') {
        await afterLogin()
      }
    } catch (err) {
      errorMsg = messageFor(err)
    } finally {
      submitting = false
    }
  }

  function backToCredentials(): void {
    step = 'credentials'
    code = ''
    errorMsg = null
  }
</script>

<svelte:head><title>{t('login.sign_stowcloud')}</title></svelte:head>

<div class="sc-auth-page">
  <form class="sc-auth-card" onsubmit={step === 'credentials' ? submitCredentials : submitTotp}>
    <h1 class="sc-auth-card__title">Stowcloud</h1>

    {#if returnTo}
      <p class="sc-auth-card__subtitle">
        {t('login.sign_first_authorise_app')}
      </p>
    {/if}

    {#if step === 'credentials'}
      <p class="sc-auth-card__subtitle">{t('login.sign_your_account')}</p>
      <TextField label={t('login.username')} bind:value={username} autofocus autocomplete="username" />
      <TextField label={t('common.password')} type="password" bind:value={password} autocomplete="current-password" />
    {:else}
      <p class="sc-auth-card__subtitle">{t('login.enter_your_two_factor_code')}</p>
      <TextField label={t('login.verification_code')} placeholder={t('login.6_digits')} bind:value={code} autofocus autocomplete="one-time-code" />
    {/if}

    {#if ssoError && step === 'credentials'}
      <p class="sc-auth-card__error" role="alert">{ssoError}</p>
    {/if}

    {#if errorMsg}
      <p class="sc-auth-card__error" role="alert">{errorMsg}</p>
    {/if}

    <div class="sc-auth-card__actions">
      {#if step === 'totp'}
        <Button variant="text" type="button" onclick={backToCredentials}>{t('login.back')}</Button>
      {/if}
      <Button variant="filled" type="submit" loading={submitting}>
        {step === 'credentials' ? t('login.sign') : t('common.ok')}
      </Button>
    </div>

    {#if step === 'credentials' && ssoName !== null}
      <!-- Below the password form, not above it: the account password is the
           path that always works, and it is the recovery path when the IdP is
           down (§4.3.5 keeps `oidc.local_password_login` on `allow` by default
           for exactly that reason). `type="button"` because this sits inside
           the form and must not submit it. -->
      <div class="sc-auth-card__divider"><span>{t('login.or')}</span></div>
      <Button variant="tonal" type="button" onclick={signInWithSso}>
        {ssoName ? t('login.sign_with', { provider: ssoName }) : t('login.sign_with_single_sign')}
      </Button>
    {/if}

    {#if step === 'credentials'}
      <a class="sc-auth-card__setup-link sc-focus-ring" href="/setup">{t('login.first_time_here_create_administrator')}</a>
    {/if}

    <!-- Outside every `{#if}`: this is the one page every user of the service
         reaches, and the offer has to be there on both steps to count as
         prominent. The licence identifier is an SPDX string, not prose, so it
         is not translated. -->
    <p class="sc-auth-card__licence">
      AGPL-3.0-or-later ·
      <a class="sc-focus-ring" href={SOURCE_URL} target="_blank" rel="noreferrer">{t('login.source')}</a>
    </p>
  </form>
</div>

<style>
  .sc-auth-page {
    display: flex;
    align-items: center;
    justify-content: center;
    /* See (app)/+layout.svelte's `.sc-app-shell` comment: `100dvh` tracks the
       real visible viewport on mobile instead of the address-bar-collapsed
       maximum `100vh` measures. `min-height` here (not `height`) means the
       consequence of skipping this would only be a slightly-too-tall page,
       not a stranded nav bar, but the card should still center against the
       viewport actually on screen. */
    min-height: 100vh;
    min-height: 100dvh;
    padding: var(--sc-page-pad);
    background: var(--m3c-surface);
  }
  .sc-auth-card {
    display: flex;
    flex-direction: column;
    gap: 16px;
    width: min(400px, 100%);
    /* 24px, not the page's own margin: this is a card's internal padding
       (MD3 gives cards 24dp), and on a phone the page margin plus a 32px card
       inset used to leave the login form 262px of a 390px screen. */
    padding: 24px;
    border-radius: var(--m3-shape-large);
    background: var(--m3c-surface-container-high);
    color: var(--m3c-on-surface);
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.3);
  }
  .sc-auth-card__title {
    margin: 0;
    @apply --m3-headline-small;
    text-align: center;
  }
  .sc-auth-card__subtitle {
    margin: 0 0 8px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-medium;
    text-align: center;
  }
  .sc-auth-card__error {
    margin: 0;
    padding: 12px 16px;
    border-radius: var(--m3-shape-small);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-medium;
  }
  .sc-auth-card__actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 8px;
  }
  /* A labelled rule between the two ways in. The rules are drawn with a
     flexing pseudo-element on each side rather than a border on the label,
     so the word stays centred however wide its translation is. */
  .sc-auth-card__divider {
    display: flex;
    align-items: center;
    gap: 12px;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-auth-card__divider::before,
  .sc-auth-card__divider::after {
    flex: 1 1 auto;
    height: 1px;
    background: var(--m3c-outline-variant);
    content: '';
  }
  .sc-auth-card__setup-link {
    margin-top: 16px;
    padding-block: 4px;
    color: var(--m3c-primary);
    @apply --m3-body-small;
    text-align: center;
    text-decoration: none;
  }
  .sc-auth-card__setup-link:hover {
    text-decoration: underline;
  }
  .sc-auth-card__licence {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
    text-align: center;
  }
  .sc-auth-card__licence a {
    color: inherit;
  }
</style>
