import { useEffect, useState } from 'react'
import { Button } from './Button'
import { TextField } from './TextField'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../i18n/use-i18n'
export function NewFolderDialog({ open, onClose, onCreate }: { open: boolean; onClose: () => void; onCreate: (name: string) => void }) {
  const { t } = useI18n(); const [name, setName] = useState(''); const [submitted, setSubmitted] = useState(false)
  useEffect(() => { if (open) { setName(t('common.new_folder')); setSubmitted(false) } }, [open, t])
  const submit = () => { if (submitted) return; const value = name.trim(); if (!value) return; setSubmitted(true); onCreate(value) }
  return <BrowseDialog open={open} title={t('common.new_folder')} onClose={onClose} actions={<><Button variant="text" onClick={onClose}>{t('common.cancel')}</Button><Button onClick={submit}>{t('common.create')}</Button></>}><TextField value={name} label={t('new_folder.folder_name')} autoFocus onValueChange={setName} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); submit() } }} /></BrowseDialog>
}
