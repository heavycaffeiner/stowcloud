<script lang="ts">
  import { t } from '../i18n'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'
  import TextField from './TextField.svelte'

  interface Props {
    open: boolean
    currentName: string
    onclose: () => void
    onrename: (name: string) => void
  }
  let { open, currentName, onclose, onrename }: Props = $props()
  // svelte-ignore state_referenced_locally -- intentional: this seeds the
  // editable field once; the $effect below re-seeds it every time the
  // dialog re-opens with a (possibly different) target.
  let name = $state(currentName)

  $effect(() => {
    if (open) name = currentName
  })

  // Same guard as the new-folder dialogue: the close is the parent's doing a
  // tick later, so a second Enter inside that window would send a second
  // rename, this time against a name that no longer exists.
  let submitted = $state(false)

  $effect(() => {
    if (open) submitted = false
  })

  function submit() {
    if (submitted) return
    submitted = true
    const trimmed = name.trim()
    if (trimmed && trimmed !== currentName) onrename(trimmed)
    else onclose()
  }
</script>

<Dialog {open} title={t('common.rename')} onclose={onclose}>
  <TextField
    bind:value={name}
    label={t('rename.new_name')}
    autofocus
    onkeydown={(e) => {
      // The framework wraps this dialogue's buttons in a
      // `<form method="dialog">`, and Enter in a field implicitly submits it.
      // That closes and reopens the dialog underneath us, which is why the
      // keyboard path left it on screen with the action already carried out
      // while clicking the button worked. Stopping the default keeps Enter to
      // exactly what it reads as: the same thing the button does.
      if (e.key !== 'Enter') return
      e.preventDefault()
      submit()
    }}
  />
  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submit}>{t('common.ok')}</Button>
  {/snippet}
</Dialog>
