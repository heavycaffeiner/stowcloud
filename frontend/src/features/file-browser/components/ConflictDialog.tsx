import { overlay } from 'overlay-kit'
import { StowButton } from '@/shared/ui'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../../../hooks/use-i18n'
import type { OnConflict } from '../api'

/** Asks what to do about a name that is already taken. Null when dismissed. */
export function askConflictPolicy(name: string): Promise<OnConflict | null> {
  return overlay.openAsync<OnConflict | null>(({ isOpen, close, unmount }) => (
    <ConflictDialog open={isOpen} name={name} onClose={() => close(null)} onClosed={unmount} onChoose={close} />
  ))
}

function ConflictDialog({
  open,
  name,
  onClose,
  onClosed,
  onChoose
}: {
  open: boolean
  name: string
  onClose: () => void
  onClosed: () => void
  onChoose: (policy: OnConflict) => void
}) {
  const { t } = useI18n()
  return (
    <BrowseDialog
      open={open}
      title={t('conflict.name_already_exists')}
      onClose={onClose}
      onClosed={onClosed}
      actions={
        <>
          <StowButton variant="text" onClick={() => onChoose('skip')}>
            {t('conflict.skip')}
          </StowButton>
          <StowButton variant="outlined" onClick={() => onChoose('rename')}>
            {t('conflict.keep_both')}
          </StowButton>
          <StowButton onClick={() => onChoose('overwrite')}>{t('conflict.overwrite')}</StowButton>
        </>
      }
    >
      <p>{t('conflict.already_destination_folder_what_would', { name })}</p>
    </BrowseDialog>
  )
}
