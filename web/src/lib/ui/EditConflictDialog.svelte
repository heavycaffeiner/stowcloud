<script lang="ts">
  // EditConflictDialog.svelte — `/edit` save conflict (`412 fs.precondition`,
  // `go/internal/apierr`'s precondition code). Distinct from
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
    /**
     * Whether the refusal came from an advisory change token.
     *
     * With one, the refusal does not prove the file changed: the token is
     * derived from metadata that can repeat. Saying it definitely changed
     * would be asserting something this client cannot know, so the copy says
     * it may have.
     */
    weak?: boolean
    onclose: () => void
    onreload: () => void
    onoverwrite: () => void
  }
  let { open, name, weak = false, onclose, onreload, onoverwrite }: Props = $props()
</script>

<Dialog
  {open}
  title={weak ? t('edit_conflict.file_may_have_changed') : t('edit_conflict.file_changed_elsewhere')}
  onclose={onclose}
>
  <p>
    {weak
      ? t('edit_conflict.may_have_been_modified_elsewhere', { name })
      : t('edit_conflict.was_modified_elsewhere_after_you', { name })}
  </p>
  <p>{t('edit_conflict.choose_whether_keep_what_you')}</p>
  {#if weak}
    <p>{t('edit_conflict.change_check_is_advisory')}</p>
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{t('common.cancel')}</Button>
    <Button variant="outlined" onclick={onreload}>{t('edit_conflict.reload_newer_version')}</Button>
    <Button variant="filled" onclick={onoverwrite}>{t('edit_conflict.overwrite_with_my_changes')}</Button>
  {/snippet}
</Dialog>
