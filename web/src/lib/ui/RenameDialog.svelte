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

  function submit() {
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
      if (e.key === 'Enter') submit()
    }}
  />
  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submit}>{t('common.ok')}</Button>
  {/snippet}
</Dialog>
