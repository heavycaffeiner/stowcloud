import { Button } from './Button'
import { BrowseDialog } from './browse-dialog'
import { Icon } from './Icon'
import { useI18n } from '../i18n/use-i18n'

export function DeleteDialog({
  open,
  count,
  externalShare = false,
  trashEnabled = false,
  onClose,
  onConfirm
}: {
  open: boolean
  count: number
  externalShare?: boolean
  trashEnabled?: boolean
  onClose: () => void
  onConfirm: () => void
}) {
  const { t, tp } = useI18n()
  return (
    <BrowseDialog
      open={open}
      title={t('delete.delete')}
      onClose={onClose}
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
        <p className="sc-delete-dialog__external-warning">
          <Icon name="warning" size={16} />
          <span>
            {t('common.shared_with_other_services')}: {t('delete.another_service_may_reading_folder')}
          </span>
        </p>
      ) : null}
    </BrowseDialog>
  )
}
