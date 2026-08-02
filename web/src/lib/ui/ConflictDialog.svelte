<script lang="ts">
  // ConflictDialog — If-Match mismatch on move/copy/rename (DESIGN-API.md §5.1).
  import { t } from '../i18n'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    open: boolean
    name: string
    onclose: () => void
    onkeepboth: () => void
    onoverwrite: () => void
    onskip: () => void
  }
  let { open, name, onclose, onkeepboth, onoverwrite, onskip }: Props = $props()
</script>

<Dialog {open} title={t('conflict.name_already_exists')} onclose={onclose}>
  <p>{t('conflict.already_destination_folder_what_would', { name })}</p>
  {#snippet actions()}
    <Button variant="text" onclick={onskip}>{t('conflict.skip')}</Button>
    <Button variant="outlined" onclick={onkeepboth}>{t('conflict.keep_both')}</Button>
    <Button variant="filled" onclick={onoverwrite}>{t('conflict.overwrite')}</Button>
  {/snippet}
</Dialog>
