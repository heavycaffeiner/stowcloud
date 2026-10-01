import { useState } from 'react'
import { overlay } from 'overlay-kit'
import { Button } from '../../ui/Button'
import { TextField } from '../../ui/TextField'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../../hooks/use-i18n'

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
  const [name, setName] = useState(currentName)
  const submit = (): void => {
    const value = name.trim()
    if (value && value !== currentName) onRename(value)
    else onClose()
  }
  return (
    <BrowseDialog
      open={open}
      title={t('common.rename')}
      onClose={onClose}
      onClosed={onClosed}
      actions={
        <>
          <Button variant="text" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit}>{t('common.ok')}</Button>
        </>
      }
    >
      <TextField
        value={name}
        label={t('rename.new_name')}
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
