<script lang="ts">
  import { t } from '../i18n'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'
  import TextField from './TextField.svelte'

  interface Props {
    open: boolean
    onclose: () => void
    oncreate: (name: string) => void
  }
  let { open, onclose, oncreate }: Props = $props()
  // Re-seeded per open rather than once at construction: the seed is itself a
  // translated string, so a locale change between opens has to reach it.
  let name = $state(t('common.new_folder'))

  $effect(() => {
    if (open) name = t('common.new_folder')
  })

  // Guarded against a second submit. Enter on the field creates the folder,
  // and the dialog closing is the parent's doing a tick later: pressing it
  // again in that window sent a second mkdir, which is how one keystroke too
  // many became two folders.
  let submitted = $state(false)

  $effect(() => {
    if (open) submitted = false
  })

  function submit() {
    if (submitted) return
    const trimmed = name.trim()
    if (!trimmed) return
    submitted = true
    oncreate(trimmed)
  }
</script>

<Dialog {open} title={t('common.new_folder')} onclose={onclose}>
  <TextField
    bind:value={name}
    label={t('new_folder.folder_name')}
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
    <Button variant="filled" onclick={submit}>{t('common.create')}</Button>
  {/snippet}
</Dialog>
