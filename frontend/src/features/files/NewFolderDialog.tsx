import { useEffect } from 'react'
import { Button } from '../../lib/ui/Button'
import { TextField } from '../../lib/ui/TextField'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../../hooks/use-i18n'
import { useComponentState } from '../../hooks/use-component-state'
export function NewFolderDialog({ open, onClose, onCreate }: { open: boolean; onClose: () => void; onCreate: (name: string) => void }) {
  const { t } = useI18n()
  const [form, setForm] = useComponentState({ name: '', submitted: false })
  useEffect(() => { if (open) setForm({ name: t('common.new_folder'), submitted: false }) }, [open, t, setForm])
  const submit = () => { if (form.submitted) return; const value = form.name.trim(); if (!value) return; setForm((state) => ({ ...state, submitted: true })); onCreate(value) }
  return <BrowseDialog open={open} title={t('common.new_folder')} onClose={onClose} actions={<><Button variant="text" onClick={onClose}>{t('common.cancel')}</Button><Button onClick={submit}>{t('common.create')}</Button></>}><TextField value={form.name} label={t('new_folder.folder_name')} autoFocus onValueChange={(name) => setForm((state) => ({ ...state, name }))} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); submit() } }} /></BrowseDialog>
}
