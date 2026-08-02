<script lang="ts">
  // EditConflictDialog.svelte — `/edit` save conflict (`412 fs.precondition`,
  // `AppError::precondition` in `crates/sc-http/src/error.rs`). Distinct from
  // ConflictDialog.svelte, which is the move/copy/rename "name already
  // exists" prompt (Fail/Rename/Overwrite/Skip) — this is "someone else's
  // write landed between your last load and your save", so the only
  // sensible choices are keep mine (overwrite their change) or take theirs
  // (reload, discard mine).
  import { t } from '../i18n'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    open: boolean
    name: string
    onclose: () => void
    onreload: () => void
    onoverwrite: () => void
  }
  let { open, name, onclose, onreload, onoverwrite }: Props = $props()
</script>

<Dialog {open} title={t('edit_conflict.file_changed_elsewhere')} onclose={onclose}>
  <p>
    {t('edit_conflict.was_modified_elsewhere_after_you', { name })}
  </p>
  <p>{t('edit_conflict.choose_whether_keep_what_you')}</p>
  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{t('common.cancel')}</Button>
    <Button variant="outlined" onclick={onreload}>{t('edit_conflict.reload_newer_version')}</Button>
    <Button variant="filled" onclick={onoverwrite}>{t('edit_conflict.overwrite_with_my_changes')}</Button>
  {/snippet}
</Dialog>
