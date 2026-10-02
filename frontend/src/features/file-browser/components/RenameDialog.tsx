import { overlay } from 'overlay-kit'
import { useForm } from 'react-hook-form'
import { StowButton, StowFormTextField } from '@/shared/ui'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../../../hooks/use-i18n'

/** Asks for a new name. Null when cancelled or left unchanged. */
export function askNewName(currentName: string): Promise<string | null> {
  return overlay.openAsync<string | null>(({ isOpen, close, unmount }) => (
    <RenameDialog
      open={isOpen}
      currentName={currentName}
      onClose={() => close(null)}
      onClosed={unmount}
      onRename={close}
    />
  ))
}

function RenameDialog({
  open,
  currentName,
  onClose,
  onClosed,
  onRename
}: {
  open: boolean
  currentName: string
  onClose: () => void
  onClosed: () => void
  onRename: (name: string) => void
}) {
  const { t } = useI18n()
  const { control, handleSubmit } = useForm({ defaultValues: { name: currentName } })
  const submit = handleSubmit(({ name }) => {
    const value = name.trim()
    if (value && value !== currentName) onRename(value)
    else onClose()
  })
  return (
    <BrowseDialog
      open={open}
      title={t('common.rename')}
      onClose={onClose}
      onClosed={onClosed}
      actions={
        <>
          <StowButton variant="text" onClick={onClose}>
            {t('common.cancel')}
          </StowButton>
          <StowButton onClick={() => void submit()}>{t('common.ok')}</StowButton>
        </>
      }
    >
      <form onSubmit={(event) => void submit(event)}>
        <StowFormTextField control={control} name="name" label={t('rename.new_name')} autoFocus />
      </form>
    </BrowseDialog>
  )
}
