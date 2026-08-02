<script lang="ts">
  import { t } from '../i18n'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../icons'

  interface Props {
    open: boolean
    count: number
    /** item 133: target lives under a root marked `shared_externally` --
     *  another service (Jellyfin, SMB) may be reading it right now, so the
     *  routine "moved to trash" copy alone understates what deleting here
     *  can disrupt. */
    externalShare?: boolean
    /** Whether this share's trash is on. Off is the default now, so promising
     *  "moved to trash" unconditionally told the user a delete was undoable
     *  when it was a plain unlink. */
    trashEnabled?: boolean
    onclose: () => void
    onconfirm: () => void
  }
  let { open, count, externalShare = false, trashEnabled = false, onclose, onconfirm }: Props = $props()
</script>

<Dialog {open} title={t('delete.delete')} onclose={onclose}>
  <p>
    {t('delete.deletes_items', { count })}
    {#if trashEnabled}{t('delete.they_moved_trash')}{:else}{t('delete.folder_does_not_use_trash')}{/if}
  </p>
  {#if externalShare}
    <p class="sc-delete-dialog__external-warning">
      <Icon icon={icons.warning} size={16} />
      {t('common.shared_with_other_services')} — {t('delete.another_service_may_reading_folder')}
    </p>
  {/if}
  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{t('common.cancel')}</Button>
    <Button variant="filled" onclick={onconfirm}>{t('common.delete')}</Button>
  {/snippet}
</Dialog>

<style>
  .sc-delete-dialog__external-warning {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--m3c-tertiary);
  }
</style>
