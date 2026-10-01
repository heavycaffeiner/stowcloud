import { overlay } from 'overlay-kit'
import { Button } from '../../ui/Button'
import { BrowseDialog } from './browse-dialog'
import { Icon } from '../../ui/Icon'
import { useI18n } from '../../hooks/use-i18n'
import * as styles from './DeleteDialog.css'

export interface DeleteConfirmation {
  count: number
  externalShare: boolean
  trashEnabled: boolean
}

/** Asks to confirm a delete. True when confirmed. */
export function confirmDelete(what: DeleteConfirmation): Promise<boolean> {
  return overlay.openAsync<boolean>(({ isOpen, close, unmount }) => (
    <DeleteDialog
      {...what}
      open={isOpen}
      onClose={() => close(false)}
      onClosed={unmount}
      onConfirm={() => close(true)}
    />
  ))
}

function DeleteDialog({
  open,
  count,
  externalShare,
  trashEnabled,
  onClose,
  onClosed,
  onConfirm
}: DeleteConfirmation & {
  open: boolean
  onClose: () => void
  onClosed: () => void
  onConfirm: () => void
}) {
  const { t, tp } = useI18n()
  return (
    <BrowseDialog
      open={open}
      title={t('delete.delete')}
      onClose={onClose}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button danger onClick={onConfirm}>
            {t('common.delete')}
          </Button>
        </>
      }
    >
      <p>
        {tp('delete.deletes_items', count)}{' '}
        {trashEnabled ? t('delete.they_moved_trash') : t('delete.folder_does_not_use_trash')}
      </p>
      {externalShare ? (
        <p className={styles.externalWarning}>
          <Icon name="warning" size={16} />
          <span>
            {t('common.shared_with_other_services')}: {t('delete.another_service_may_reading_folder')}
          </span>
        </p>
      ) : null}
    </BrowseDialog>
  )
}
