import { useEffect } from 'react'
import { Button } from '../../lib/ui/Button'
import { TextField } from '../../lib/ui/TextField'
import { BrowseDialog } from './browse-dialog'
import { useI18n } from '../../lib/i18n/use-i18n'
import { useComponentState } from '../../lib/store/use-component-state'
export function RenameDialog({ open, currentName, onClose, onRename }: { open: boolean; currentName: string; onClose: () => void; onRename: (name: string) => void }) {
  const { t } = useI18n()
  const [form, setForm] = useComponentState({ name: '', submitted: false })
  useEffect(() => { if (open) setForm({ name: currentName, submitted: false }) }, [open, currentName, setForm])
  const submit = () => { if (form.submitted) return; setForm((state) => ({ ...state, submitted: true })); const value = form.name.trim(); if (value && value !== currentName) onRename(value); else onClose() }
  return <BrowseDialog open={open} title={t('common.rename')} onClose={onClose} actions={<><Button variant="text" onClick={onClose}>{t('common.cancel')}</Button><Button onClick={submit}>{t('common.ok')}</Button></>}><TextField value={form.name} label={t('rename.new_name')} autoFocus onValueChange={(name) => setForm((state) => ({ ...state, name }))} onKeyDown={(event) => { if (event.key === 'Enter') { event.preventDefault(); submit() } }} /></BrowseDialog>
}
