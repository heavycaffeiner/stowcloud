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

  function submit() {
    const trimmed = name.trim()
    if (trimmed) oncreate(trimmed)
  }
</script>

<Dialog {open} title={t('common.new_folder')} onclose={onclose}>
  <TextField
    bind:value={name}
    label={t('new_folder.folder_name')}
    autofocus
    onkeydown={(e) => {
      if (e.key === 'Enter') submit()
    }}
  />
  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={submit}>{t('common.create')}</Button>
  {/snippet}
</Dialog>
