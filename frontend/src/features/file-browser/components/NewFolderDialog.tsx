import { overlay } from 'overlay-kit'
import { useForm } from 'react-hook-form'
import { StowButton, StowFormTextField } from '@/shared/ui'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../../../hooks/use-i18n'

/** Asks for the name of a new folder. Null when cancelled. */
export function askNewFolderName(): Promise<string | null> {
  return overlay.openAsync<string | null>(({ isOpen, close, unmount }) => (
    <NewFolderDialog open={isOpen} onClose={() => close(null)} onClosed={unmount} onCreate={close} />
  ))
}

function NewFolderDialog({
  open,
  onClose,
  onClosed,
  onCreate
}: {
  open: boolean
  onClose: () => void
  onClosed: () => void
  onCreate: (name: string) => void
}) {
  const { t } = useI18n()
  const { control, handleSubmit } = useForm({ defaultValues: { name: t('common.new_folder') } })
  const submit = handleSubmit(({ name }) => {
    const value = name.trim()
    if (value) onCreate(value)
  })
  return (
    <BrowseDialog
      open={open}
      title={t('common.new_folder')}
      onClose={onClose}
      onClosed={onClosed}
      actions={
        <>
          <StowButton variant="text" onClick={onClose}>
            {t('common.cancel')}
          </StowButton>
          <StowButton onClick={() => void submit()}>{t('common.create')}</StowButton>
        </>
      }
    >
      <form onSubmit={(event) => void submit(event)}>
        <StowFormTextField control={control} name="name" label={t('new_folder.folder_name')} autoFocus />
      </form>
    </BrowseDialog>
  )
}
