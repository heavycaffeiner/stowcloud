import { useEffect, useState } from 'react'
import { Button } from './Button'
import { TextField } from './TextField'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../i18n/use-i18n'
export function RenameDialog({ open, currentName, onClose, onRename }: { open: boolean; currentName: string; onClose: () => void; onRename: (name: string) => void }) {
  const { t } = useI18n(); const [name, setName] = useState(''); const [submitted, setSubmitted] = useState(false)
  useEffect(() => { if (open) { setName(currentName); setSubmitted(false) } }, [open, currentName])
  const submit = () => { if (submitted) return; setSubmitted(true); const value = name.trim(); if (value && value !== currentName) onRename(value); else onClose() }
  return <BrowseDialog open={open} title={t('common.rename')} onClose={onClose} actions={<><Button variant="text" onClick={onClose}>{t('common.cancel')}</Button><Button onClick={submit}>{t('common.ok')}</Button></>}><TextField value={name} label={t('rename.new_name')} autoFocus onValueChange={setName} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); submit() } }} /></BrowseDialog>
}
