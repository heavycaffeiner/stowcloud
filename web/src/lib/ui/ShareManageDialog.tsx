import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import type { PermsReq, ShareLinkInfo } from '../api/client'
import { describeApiError } from '../api/error-text'
import { formatDateNs } from '../i18n'
import { useI18n } from '../i18n/use-i18n'
import { shareCreateMutation, shareDeleteMutation, shareLinksQuery, shareUpdateMutation } from '../query/shares'
import { Button } from './Button'
import { Switch } from './Switch'
import { Dialog } from './Dialog'
import { Icon } from './Icon'
import { IconButton } from './IconButton'
import { ProgressCircular } from './ProgressCircular'
import { Select, type SelectOption } from './Select'
import { TextField } from './TextField'
import './share-manage.css'

export interface ShareManageDialogProps {
  open: boolean
  path: string
  targetName: string
  targetIsDir: boolean
  onClose?: () => void
  onclose?: () => void
}

const PRESET_DAYS: Record<string, number> = { '1d': 1, '7d': 7, '30d': 30 }

function isoToExpiresNs(iso: string): string | undefined {
  const [year, month, day] = iso.split('-').map(Number)
  if (!year || !month || !day) return undefined
  return String(BigInt(new Date(year, month - 1, day, 23, 59, 59, 999).getTime()) * 1_000_000n)
}

function expiryToNs(choice: string, iso: string): string | undefined {
  if (choice === 'custom') return isoToExpiresNs(iso)
  if (choice === 'none' || choice === 'keep') return undefined
  return String(BigInt(Date.now() + PRESET_DAYS[choice] * 86_400_000) * 1_000_000n)
}

function expiryUnusable(choice: string, iso: string): boolean {
  if (choice !== 'custom') return false
  const ns = isoToExpiresNs(iso)
  return ns === undefined || BigInt(ns) <= BigInt(Date.now()) * 1_000_000n
}

function isDropLink(link: ShareLinkInfo): boolean {
  return Boolean(link.perms.create && !link.perms.read && !link.perms.download)
}

async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Fall through to the selectable textarea fallback.
  }
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.append(area)
  area.select()
  let copied = false
  try {
    copied = document.execCommand('copy')
  } catch {
    copied = false
  }
  area.remove()
  return copied
}

const newExpiryOptions = (t: (key: string) => string): SelectOption[] => [
  { value: 'none', text: t('share.none') },
  { value: '1d', text: t('share.1_day') },
  { value: '7d', text: t('share.7_days') },
  { value: '30d', text: t('share.30_days_default') },
  { value: 'custom', text: t('share.pick_a_date') }
]

const editExpiryOptions = (t: (key: string) => string): SelectOption[] => [
  { value: 'keep', text: t('share.leave_unchanged') },
  { value: 'none', text: t('share.none') },
  { value: '1d', text: t('share.1_day') },
  { value: '7d', text: t('share.7_days') },
  { value: '30d', text: t('share.30_days') },
  { value: 'custom', text: t('share.pick_a_date') }
]

export function ShareManageDialog({ open, path, targetName, targetIsDir, onClose, onclose }: ShareManageDialogProps) {
  const { t } = useI18n()
  const closeParent = onClose ?? onclose ?? (() => undefined)
  const [dialogOpen, setDialogOpen] = useState(open)
  const [creatingOpen, setCreatingOpen] = useState(false)
  const [newKind, setNewKind] = useState('download')
  const [newRead, setNewRead] = useState(true)
  const [newDownload, setNewDownload] = useState(true)
  const [newPassword, setNewPassword] = useState('')
  const [newExpiry, setNewExpiry] = useState('30d')
  const [newExpiryDate, setNewExpiryDate] = useState('')
  const [newMaxDownloads, setNewMaxDownloads] = useState('')
  const [newLabel, setNewLabel] = useState('')
  const [createValidation, setCreateValidation] = useState<string | null>(null)
  const [justCreated, setJustCreated] = useState<ShareLinkInfo | null>(null)
  const [issuedAcknowledged, setIssuedAcknowledged] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editRead, setEditRead] = useState(true)
  const [editDownload, setEditDownload] = useState(true)
  const [editClearPassword, setEditClearPassword] = useState(false)
  const [editNewPassword, setEditNewPassword] = useState('')
  const [editExpiry, setEditExpiry] = useState('keep')
  const [editExpiryDate, setEditExpiryDate] = useState('')
  const [editMaxDownloads, setEditMaxDownloads] = useState('')
  const [editLabel, setEditLabel] = useState('')
  const [revokeTarget, setRevokeTarget] = useState<ShareLinkInfo | null>(null)
  const [copiedId, setCopiedId] = useState<number | null>(null)
  const [copyErrorId, setCopyErrorId] = useState<number | null>(null)

  const sharesQuery = useQuery(shareLinksQuery(path, open))
  const createMut = useMutation(shareCreateMutation())
  const updateMut = useMutation(shareUpdateMutation())
  const deleteMut = useMutation(shareDeleteMutation())
  const links = sharesQuery.data ?? []
  const newExpiryBad = expiryUnusable(newExpiry, newExpiryDate)
  const editExpiryBad = expiryUnusable(editExpiry, editExpiryDate)
  const createError = createValidation ?? (createMut.error ? describeApiError(createMut.error, t('share.could_not_create_share_link')) : null)
  const loadError = sharesQuery.error
    ? describeApiError(sharesQuery.error, t('share.could_not_load_share_links'))
    : updateMut.error
      ? describeApiError(updateMut.error, t('share.could_not_update_share_link'))
      : deleteMut.error
        ? describeApiError(deleteMut.error, t('share.could_not_revoke_share_link'))
        : null

  const newKindOptions = useMemo<SelectOption[]>(() => targetIsDir
    ? [{ value: 'download', text: t('share.kind_download') }, { value: 'drop', text: t('share.kind_drop') }]
    : [{ value: 'download', text: t('share.kind_download') }], [t, targetIsDir])
  const newExpiryChoices = useMemo(() => newExpiryOptions(t), [t])
  const editExpiryChoices = useMemo(() => editExpiryOptions(t), [t])

  useEffect(() => {
    setDialogOpen(open)
    if (open) {
      setJustCreated(null)
      setIssuedAcknowledged(false)
      setCopyErrorId(null)
      setCreatingOpen(false)
      setEditingId(null)
      setRevokeTarget(null)
    }
  }, [open])

  function resetCreateForm(): void {
    setNewKind('download')
    setNewRead(true)
    setNewDownload(true)
    setNewPassword('')
    setNewExpiry('30d')
    setNewExpiryDate('')
    setNewMaxDownloads('')
    setNewLabel('')
    setCreateValidation(null)
    createMut.reset()
  }

  function openCreate(): void {
    resetCreateForm()
    setCreatingOpen(true)
  }

  async function submitCreate(): Promise<void> {
    setCreateValidation(null)
    const isDrop = newKind === 'drop'
    const perms: PermsReq = isDrop ? { create: true } : { read: newRead, download: newDownload }
    const maxDownloads = !isDrop && newMaxDownloads.trim() ? Number(newMaxDownloads.trim()) : undefined
    if (maxDownloads !== undefined && (!Number.isInteger(maxDownloads) || maxDownloads <= 0)) {
      setCreateValidation(t('share.download_limit_must_integer_1'))
      return
    }
    try {
      const created = await createMut.mutateAsync({ path, perms, password: newPassword.trim() || undefined, expires_ns: expiryToNs(newExpiry, newExpiryDate), max_downloads: maxDownloads, label: newLabel.trim() || undefined })
      setJustCreated(created)
      setIssuedAcknowledged(false)
      setCopyErrorId(null)
      setCopiedId(null)
      setCreatingOpen(false)
    } catch {
      // The mutation error is rendered beside the form.
    }
  }

  async function copyLink(text: string, id: number): Promise<void> {
    setCopyErrorId(null)
    if (await copyText(text)) {
      setCopiedId(id)
      window.setTimeout(() => setCopiedId((current) => current === id ? null : current), 2000)
    } else {
      setCopiedId(null)
      setCopyErrorId(id)
    }
  }

  function acknowledgeIssued(): void {
    setIssuedAcknowledged(true)
    setJustCreated(null)
    setCopyErrorId(null)
  }

  function closeIssued(): void {
    if (justCreated && !issuedAcknowledged) {
      setDialogOpen(true)
      return
    }
    setDialogOpen(false)
    setJustCreated(null)
    closeParent()
  }

  function openEdit(link: ShareLinkInfo): void {
    setEditingId(link.id)
    setEditRead(link.perms.read)
    setEditDownload(link.perms.download)
    setEditClearPassword(false)
    setEditNewPassword('')
    setEditExpiry('keep')
    setEditExpiryDate('')
    setEditMaxDownloads(link.max_downloads === null ? '' : String(link.max_downloads))
    setEditLabel(link.label ?? '')
    updateMut.reset()
  }

  async function submitEdit(link: ShareLinkInfo): Promise<void> {
    const maxDownloads = editMaxDownloads.trim() ? Number(editMaxDownloads.trim()) : null
    if (maxDownloads !== null && (!Number.isInteger(maxDownloads) || maxDownloads <= 0)) return
    try {
      await updateMut.mutateAsync({
        id: link.id,
        patch: {
          perms: { read: editRead, download: editDownload, create: link.perms.create },
          password: editClearPassword ? null : editNewPassword.trim() || undefined,
          expires_ns: editExpiry === 'keep' ? undefined : (expiryToNs(editExpiry, editExpiryDate) ?? null),
          max_downloads: maxDownloads,
          label: editLabel.trim() || null
        }
      })
      setEditingId(null)
    } catch {
      // The mutation error is rendered in the shared error banner.
    }
  }

  async function confirmRevoke(): Promise<void> {
    if (!revokeTarget) return
    const id = revokeTarget.id
    setRevokeTarget(null)
    if (justCreated?.id === id) setJustCreated(null)
    try {
      await deleteMut.mutateAsync(id)
    } catch {
      // The mutation error is rendered in the shared error banner.
    }
  }

  return (
    <>
      <Dialog className="sc-share-dialog" open={dialogOpen} title={t('share.share_links', { name: targetName })} onClose={closeIssued} role="dialog" closedby="any" actions={<Button variant="text" onClick={closeIssued}>{t('common.close')}</Button>}>
        {justCreated ? (
          <div className="sc-share__issued">
            <p className="sc-share__issued-note">{t('share.link_shown_only_now_cannot')}</p>
            <div className="sc-share__url-row">
              <textarea className="sc-share__url" readOnly rows={3} aria-label={t('share.copy_link')} value={justCreated.url ?? ''} />
              <IconButton label={t('share.copy_link')} onClick={() => void copyLink(justCreated.url ?? '', -1)}><Icon name="copy" /></IconButton>
            </div>
            {copiedId === -1 ? <p className="sc-share__copy-feedback" role="status">{t('common.copied')}</p> : null}
            {copyErrorId === -1 ? <p className="sc-share__copy-feedback sc-share__copy-feedback--error" role="alert">{t('share.copy_failed')}</p> : null}
            <Button variant="text" onClick={acknowledgeIssued}>{t('share.acknowledge_link_saved')}</Button>
          </div>
        ) : null}
        {sharesQuery.isPending ? <div className="sc-share__loading"><ProgressCircular /></div> : loadError ? <p className="sc-share__error" role="alert">{loadError}</p> : (
          <>
            {links.length === 0 && !creatingOpen ? <p className="sc-share__empty">{t('share.no_share_links_item')}</p> : null}
            <ul className="sc-share__list">
              {links.map((link) => (
                <li className="sc-share__item" key={link.id}>
                  {editingId === link.id ? (
                    <div className="sc-share__edit-form">
                      <div className="sc-share__perm-row">
                        <Switch checked={editRead} label={t('share.read_view')} onChange={setEditRead} />
                        <Switch checked={editDownload} label={t('common.download')} onChange={setEditDownload} />
                      </div>
                      <Select label={t('share.expiry')} options={editExpiryChoices} value={editExpiry} onValueChange={setEditExpiry} />
                      {editExpiry === 'custom' ? <TextField type="date" label={t('share.expiry_date')} value={editExpiryDate} onValueChange={setEditExpiryDate} error={editExpiryDate && editExpiryBad ? t('share.expiry_date_must_be_in_the_future') : null} /> : null}
                      <TextField value={editMaxDownloads} label={t('share.download_limit_optional')} placeholder={t('share.no_limit')} onValueChange={setEditMaxDownloads} />
                      <TextField value={editLabel} label={t('share.label_optional')} onValueChange={setEditLabel} />
                      {link.has_password && !editClearPassword ? <Switch checked={editClearPassword} label={t('share.remove_password')} onChange={setEditClearPassword} /> : null}
                      {!editClearPassword ? <TextField value={editNewPassword} type="password" label={t('share.new_password_optional')} onValueChange={setEditNewPassword} /> : null}
                      <div className="sc-share__edit-actions"><Button variant="text" onClick={() => setEditingId(null)}>{t('common.cancel')}</Button><Button disabled={updateMut.isPending || editExpiryBad} onClick={() => void submitEdit(link)}>{t('common.save')}</Button></div>
                    </div>
                  ) : (
                    <div className="sc-share__item-row">
                      <Icon name={link.has_password ? 'lock' : 'link'} size={18} />
                      <div className="sc-share__item-main">
                        <span className="sc-share__item-label">{link.label || t('share.no_label')}</span>
                        <span className="sc-share__item-meta">{isDropLink(link) ? t('share.kind_drop') : `${link.perms.read ? t('common.read') : ''}${link.perms.read && link.perms.download ? ' - ' : ''}${link.perms.download ? t('common.download') : ''} - ${t('share.used_times', { count: link.max_downloads ? `${link.downloads}/${link.max_downloads}` : link.downloads })}`} {link.expires_ns ? `- ${t('share.expires', { date: formatDateNs(link.expires_ns) })}` : `- ${t('share.never_expires')}`}</span>
                        <span className="sc-share__item-meta">{t('share.created', { date: formatDateNs(link.created_ns) })}</span>
                      </div>
                      <div className="sc-share__item-actions">
                        {link.url ? <IconButton label={copiedId === link.id ? t('share.copied') : t('share.copy_link')} onClick={() => void copyLink(link.url ?? '', link.id)}><Icon name={copiedId === link.id ? 'check' : 'copy'} /></IconButton> : null}
                        <IconButton label={t('share.edit')} onClick={() => openEdit(link)}><Icon name="rename" /></IconButton>
                        <IconButton label={t('share.revoke')} onClick={() => setRevokeTarget(link)}><Icon name="close" /></IconButton>
                      </div>
                    </div>
                  )}
                </li>
              ))}
            </ul>
            {creatingOpen ? (
              <div className="sc-share__create-form">
                <h3>{t('share.create_new_link')}</h3>
                <Select label={t('share.kind_label')} options={newKindOptions} value={newKind} onValueChange={setNewKind} />
                {newKind === 'drop' ? <p className="sc-share__hint">{t('share.drop_hint')}</p> : <div className="sc-share__perm-row"><Switch checked={newRead} label={t('share.read_view')} onChange={setNewRead} /><Switch checked={newDownload} label={t('common.download')} onChange={setNewDownload} /></div>}
                <Select label={t('share.expiry')} options={newExpiryChoices} value={newExpiry} onValueChange={setNewExpiry} />
                {newExpiry === 'custom' ? <TextField type="date" label={t('share.expiry_date')} value={newExpiryDate} onValueChange={setNewExpiryDate} error={newExpiryDate && newExpiryBad ? t('share.expiry_date_must_be_in_the_future') : null} /> : null}
                <TextField value={newPassword} type="password" label={t('share.password_optional')} onValueChange={setNewPassword} />
                {newKind !== 'drop' ? <TextField value={newMaxDownloads} label={t('share.download_limit_optional')} placeholder={t('share.no_limit')} onValueChange={setNewMaxDownloads} /> : null}
                <TextField value={newLabel} label={t('share.label_optional')} placeholder={targetName} onValueChange={setNewLabel} />
                {createError ? <p className="sc-share__error" role="alert">{createError}</p> : null}
                <div className="sc-share__edit-actions"><Button variant="text" onClick={() => setCreatingOpen(false)}>{t('common.cancel')}</Button><Button disabled={createMut.isPending || newExpiryBad || (newKind !== 'drop' && !newRead && !newDownload)} onClick={() => void submitCreate()}>{t('common.create')}</Button></div>
              </div>
            ) : <Button variant="tonal" onClick={openCreate} icon={<Icon name="add" size={18} />}>{t('share.create_new_link')}</Button>}
          </>
        )}
      </Dialog>
      <Dialog className="sc-share-dialog" open={revokeTarget !== null} title={t('share.revoke_share_link')} onClose={() => setRevokeTarget(null)} actions={<><Button variant="text" onClick={() => setRevokeTarget(null)}>{t('common.cancel')}</Button><Button danger onClick={() => void confirmRevoke()}>{t('share.revoke')}</Button></>}>
        <p>{t('share.link_stops_working_cannot_undone')}</p>
      </Dialog>
    </>
  )
}
