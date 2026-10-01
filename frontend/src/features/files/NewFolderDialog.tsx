import { useState } from 'react'
import { overlay } from 'overlay-kit'
import { Button } from '../../ui/Button'
import { TextField } from '../../ui/TextField'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../../hooks/use-i18n'

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
  const [name, setName] = useState(() => t('common.new_folder'))
  const submit = (): void => {
    const value = name.trim()
    if (value) onCreate(value)
  }
  return (
    <BrowseDialog
      open={open}
      title={t('common.new_folder')}
      onClose={onClose}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit}>{t('common.create')}</Button>
        </>
      }
    >
      <TextField
        value={name}
        label={t('new_folder.folder_name')}
        autoFocus
        onValueChange={setName}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault()
            submit()
          }
        }}
      />
    </BrowseDialog>
  )
}
