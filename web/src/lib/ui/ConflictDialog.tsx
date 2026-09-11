import { Button } from './Button'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../i18n/use-i18n'
export function ConflictDialog({ open, name, onClose, onKeepBoth, onOverwrite, onSkip }: { open: boolean; name: string; onClose: () => void; onKeepBoth: () => void; onOverwrite: () => void; onSkip: () => void }) { const { t } = useI18n(); return <BrowseDialog open={open} title={t('conflict.name_already_exists')} onClose={onClose} actions={<><Button variant="text" onClick={onSkip}>{t('conflict.skip')}</Button><Button variant="outlined" onClick={onKeepBoth}>{t('conflict.keep_both')}</Button><Button onClick={onOverwrite}>{t('conflict.overwrite')}</Button></>}><p>{t('conflict.already_destination_folder_what_would', { name })}</p></BrowseDialog> }
