<script lang="ts">
  // ConfirmDialog.svelte — generic MD3 confirmation dialog, the `danger`
  // sibling of `DeleteDialog.svelte` (which is specifically "move to trash"
  // copy). Used where a client action is irreversible and needs to say so
  // plainly rather than reuse delete's softer wording: trash purge
  // ('s `.sctrash` entries are `unlink`ed for real,
  // `crates/sc-core/src/trash.rs::trash_purge`) and revoking a share link
  // (the token is gone the instant this resolves — nothing recreates the
  // exact same link).
  import { t } from '../i18n'
  import Button from './Button.svelte'
  import Dialog from './Dialog.svelte'

  interface Props {
    open: boolean
    title: string
    message: string
    confirmLabel?: string
    cancelLabel?: string
    /** MD3 error-role button coloring — the visual signal that this is the
     *  destructive path, not the routine one. */
    danger?: boolean
    onclose: () => void
    onconfirm: () => void
  }
  // The two labels have no default *value*: a default evaluated once at
  // construction would freeze the locale it was built in. They fall back in
  // the template instead, where `t` re-runs.
  let { open, title, message, confirmLabel, cancelLabel, danger = true, onclose, onconfirm }: Props = $props()
</script>

<Dialog {open} {title} {onclose}>
  <p>{message}</p>
  {#snippet actions()}
    <Button variant="text" onclick={onclose}>{cancelLabel ?? t('common.cancel')}</Button>
    <Button variant="filled" {danger} onclick={onconfirm}>{confirmLabel ?? t('common.ok')}</Button>
  {/snippet}
</Dialog>
