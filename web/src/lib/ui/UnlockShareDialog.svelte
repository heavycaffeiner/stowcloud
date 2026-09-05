<script lang="ts">
  // The passphrase prompt an
  // encrypted-share download surfaces on `LockedSessionError`: the share's
  // key lives only in `e2ee.ts`'s module scope for this tab's lifetime, so a
  // fresh tab (or one that never touched this share before) has to ask for
  // the passphrase before it can decrypt anything. `unlock()` itself checks
  // the passphrase against the share's stored verifier before holding the
  // key, so a wrong passphrase here never gets as far as encrypting or
  // decrypting anything under it.
  import { t } from '../i18n'
  import { unlock, WrongPassphraseError } from '../crypto/e2ee'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'
  import TextField from './TextField.svelte'

  interface Props {
    open: boolean
    salt: string
    verifier: string
    onunlock: () => void
    onclose: () => void
  }

  let { open, salt, verifier, onunlock, onclose }: Props = $props()

  let passphrase = $state('')
  let unlocking = $state(false)
  let error = $state<string | null>(null)

  // Cleared on every open, not just the first: a dialog reused for a second
  // locked share (or a second attempt at the same one) must not still show
  // the previous attempt's typed passphrase or error.
  $effect(() => {
    if (open) {
      passphrase = ''
      error = null
    }
  })

  async function submit(): Promise<void> {
    if (!passphrase || unlocking) return
    unlocking = true
    error = null
    try {
      await unlock(passphrase, salt, verifier)
      onunlock()
    } catch (err) {
      error = err instanceof WrongPassphraseError ? t('common.incorrect_password') : t('encryption.could_not_unlock')
    } finally {
      unlocking = false
    }
  }
</script>

<Dialog {open} title={t('encryption.unlock_title')} {onclose}>
  <p>{t('encryption.unlock_hint')}</p>
  <form onsubmit={(e) => (e.preventDefault(), submit())}>
    <TextField type="password" label={t('encryption.passphrase')} bind:value={passphrase} {error} autocomplete="current-password" autofocus />
  </form>
  {#snippet actions()}
    <Button variant="text" onclick={onclose} disabled={unlocking}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submit} loading={unlocking} disabled={!passphrase}>{t('encryption.unlock')}</Button>
  {/snippet}
</Dialog>
