<script lang="ts">
  // WebDAV connection guide. No settings to change here (there is nothing
  // server-side to toggle: the mount is always on when the feature is), just
  // the address and per-OS steps to connect an existing file manager to it.
  //
  // The base URL is derived from `window.location.origin` rather than a
  // server-reported value, because the value that matters is the address the
  // browser is actually using right now, proxies and port forwarding
  // included. This app is prerendered (adapter-static), so `window` does not
  // exist during that render; the read is deferred to an effect and starts
  // empty rather than guessing.
  import { t } from '../../i18n'
  import Button from '../Button.svelte'

  let origin = $state('')
  $effect(() => {
    origin = window.location.origin
  })

  const baseUrl = $derived(origin ? `${origin}/dav` : '')
  // GNOME Files and KDE Dolphin resolve this scheme themselves; everything
  // else takes the plain URL. Replacing only "http" keeps the "s" of
  // "https", so a TLS origin becomes "davs" and a plain one becomes "dav".
  const davUrl = $derived(baseUrl.replace(/^http/, 'dav'))
  const netUseCommand = $derived(baseUrl ? `net use Z: ${baseUrl} /user:USERNAME` : '')

  // The rclone remote a plain share needs. Written out as a config stanza
  // rather than as a sequence of `rclone config` prompts because the stanza
  // is what the user can paste, and because the crypt variant below only
  // differs from it by four settings, which is easier to see side by side.
  const rcloneRemote = $derived(
    baseUrl
      ? [
          '[stowcloud]',
          'type = webdav',
          `url = ${baseUrl}/SHARE`,
          'vendor = other',
          'user = ACCOUNT',
          'pass = OBSCURED_APP_PASSWORD'
        ].join('\n')
      : ''
  )

  // The crypt overlay for an end-to-end encrypted share.
  //
  // filename_encryption and directory_name_encryption are off, and suffix is
  // none, because that is what the server stores: content is encrypted and
  // names are not, so a name typed here has to reach the wire unchanged or
  // rclone would look for a file that is not there. password2 is the share's
  // own salt, shown on the share's encryption settings.
  const rcloneCrypt = $derived(
    [
      '[stowcloud-crypt]',
      'type = crypt',
      'remote = stowcloud:',
      'filename_encryption = off',
      'directory_name_encryption = false',
      'suffix = none',
      'password = OBSCURED_PASSPHRASE',
      'password2 = OBSCURED_SALT'
    ].join('\n')
  )

  const rcloneMount = 'rclone mount stowcloud: /mnt/stowcloud'
  const rcloneMountCrypt = 'rclone mount stowcloud-crypt: /mnt/stowcloud'

  /** This section's own live region, per `SmbSection`'s reasoning: a
   *  per-route Snackbar goes silent on navigation. */
  let announcement = $state('')

  async function copy(text: string, name: string): Promise<void> {
    if (!text) return
    try {
      await navigator.clipboard.writeText(text)
      announcement = t('webdav.copied_to_clipboard', { name })
    } catch {
      // clipboard API unavailable: the text is still selectable, user-select: all
    }
  }
</script>

<div class="sc-webdav" data-testid="webdav-guide">
  <div>
    <span class="sc-webdav__label">{t('webdav.server_address')}</span>
    <div class="sc-webdav__token-row">
      <code class="sc-webdav__token" data-testid="webdav-base-url">{baseUrl}</code>
      <Button
        variant="text"
        ariaLabel={t('common.copy_named', { name: t('webdav.server_address') })}
        onclick={() => copy(baseUrl, t('webdav.server_address'))}
      >
        {t('common.copy')}
      </Button>
    </div>
  </div>

  <p class="sc-webdav__credentials">{t('webdav.credentials_note')}</p>

  <section class="sc-webdav__os">
    <h3>{t('webdav.macos_heading')}</h3>
    <ol>
      <li>{t('webdav.macos_step_open_connect')}</li>
      <li>{t('webdav.macos_step_enter_url')}</li>
      <li>{t('webdav.macos_step_credentials')}</li>
    </ol>
  </section>

  <section class="sc-webdav__os">
    <h3>{t('webdav.windows_heading')}</h3>
    <ol>
      <li>{t('webdav.windows_step_open')}</li>
      <li>{t('webdav.windows_step_enter_url')}</li>
      <li>{t('webdav.windows_step_credentials')}</li>
    </ol>
    <p>{t('webdav.windows_net_use_hint')}</p>
    <div class="sc-webdav__token-row">
      <code class="sc-webdav__token">{netUseCommand}</code>
      <Button
        variant="text"
        ariaLabel={t('common.copy_named', { name: t('webdav.net_use_command_label') })}
        onclick={() => copy(netUseCommand, t('webdav.net_use_command_label'))}
      >
        {t('common.copy')}
      </Button>
    </div>
  </section>

  <section class="sc-webdav__os">
    <h3>{t('webdav.linux_heading')}</h3>
    <ol>
      <li>{t('webdav.linux_step_open')}</li>
      <li>{t('webdav.linux_step_enter_url')}</li>
    </ol>
    <div class="sc-webdav__token-row">
      <code class="sc-webdav__token">{davUrl}</code>
      <Button
        variant="text"
        ariaLabel={t('common.copy_named', { name: t('webdav.dav_url_label') })}
        onclick={() => copy(davUrl, t('webdav.dav_url_label'))}
      >
        {t('common.copy')}
      </Button>
    </div>
  </section>

  <section class="sc-webdav__os">
    <h3>{t('webdav.generic_heading')}</h3>
    <p>{t('webdav.generic_hint')}</p>
  </section>

  <section class="sc-webdav__os">
    <h3>{t('webdav.rclone_heading')}</h3>
    <p>{t('webdav.rclone_hint')}</p>
    <div class="sc-webdav__token-row">
      <pre class="sc-webdav__token">{rcloneRemote}</pre>
      <Button
        variant="text"
        ariaLabel={t('common.copy_named', { name: t('webdav.rclone_remote_label') })}
        onclick={() => copy(rcloneRemote, t('webdav.rclone_remote_label'))}
      >
        {t('common.copy')}
      </Button>
    </div>
    <p>{t('webdav.rclone_mount_hint')}</p>
    <div class="sc-webdav__token-row">
      <code class="sc-webdav__token">{rcloneMount}</code>
      <Button
        variant="text"
        ariaLabel={t('common.copy_named', { name: t('webdav.rclone_mount_label') })}
        onclick={() => copy(rcloneMount, t('webdav.rclone_mount_label'))}
      >
        {t('common.copy')}
      </Button>
    </div>
    <h4>{t('webdav.rclone_crypt_heading')}</h4>
    <p>{t('webdav.rclone_crypt_hint')}</p>
    <div class="sc-webdav__token-row">
      <pre class="sc-webdav__token">{rcloneCrypt}</pre>
      <Button
        variant="text"
        ariaLabel={t('common.copy_named', { name: t('webdav.rclone_crypt_label') })}
        onclick={() => copy(rcloneCrypt, t('webdav.rclone_crypt_label'))}
      >
        {t('common.copy')}
      </Button>
    </div>
    <div class="sc-webdav__token-row">
      <code class="sc-webdav__token">{rcloneMountCrypt}</code>
      <Button
        variant="text"
        ariaLabel={t('common.copy_named', { name: t('webdav.rclone_mount_crypt_label') })}
        onclick={() => copy(rcloneMountCrypt, t('webdav.rclone_mount_crypt_label'))}
      >
        {t('common.copy')}
      </Button>
    </div>
    <p>{t('webdav.rclone_crypt_warning')}</p>
  </section>

  <p class="sc-webdav__nfc-note">{t('webdav.nfc_note')}</p>

  <p class="sc-webdav__announce" aria-live="polite">{announcement}</p>
</div>

<style>
  .sc-webdav {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .sc-webdav__nfc-note,
  .sc-webdav__announce {
    margin: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-webdav__label {
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-webdav__credentials {
    margin: 0;
    @apply --m3-body-medium;
  }
  .sc-webdav__token-row {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-block: 8px;
  }
  .sc-webdav__token {
    padding: 8px 12px;
    border-radius: var(--m3-shape-extra-small);
    background: var(--m3c-surface-container-highest);
    @apply --m3-body-medium;
    overflow-wrap: anywhere;
    user-select: all;
  }
  .sc-webdav__os h3 {
    margin: 0 0 8px;
    @apply --m3-title-medium;
  }
  /* The browser's default list indent is 40px on the start side and nothing
     on the end side. Stated symmetrically so the step numbers sit on the 4px
     grid the rest of the card is laid out on. */
  .sc-webdav__os ol {
    margin: 0;
    padding-inline: 24px;
  }
  .sc-webdav__os p {
    margin: 8px 0 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-webdav__os h4 {
    margin: 16px 0 8px;
    @apply --m3-title-small;
  }
  /* A config stanza is multi-line, so it needs the whitespace kept and the
     row's centre alignment turned into a top alignment beside its button. */
  .sc-webdav__token-row:has(pre) {
    align-items: flex-start;
  }
  pre.sc-webdav__token {
    margin: 0;
    white-space: pre-wrap;
    overflow-x: auto;
  }
</style>
