import { useEffect, useMemo } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import type { PermsReq, ShareLinkInfo } from '../../../lib/api/client'
import { describeApiError } from '../../../lib/api/error-text'
import { shareCreateMutation, shareDeleteMutation, shareLinksQuery, shareUpdateMutation } from '../../../lib/query/shares'
import type { SelectOption } from '../../../lib/ui/Select'
import { useShareManageState } from './share-manage-state'

const PRESET_DAYS: Record<string, number> = { '1d': 1, '7d': 7, '30d': 30 }

export function isoToExpiresNs(iso: string): string | undefined {
  const [year, month, day] = iso.split('-').map(Number)
  if (!year || !month || !day) return undefined
  return String(BigInt(new Date(year, month - 1, day, 23, 59, 59, 999).getTime()) * 1_000_000n)
}

export function expiryToNs(choice: string, iso: string): string | undefined {
  if (choice === 'custom') return isoToExpiresNs(iso)
  if (choice === 'none' || choice === 'keep') return undefined
  return String(BigInt(Date.now() + PRESET_DAYS[choice] * 86_400_000) * 1_000_000n)
}

export function expiryUnusable(choice: string, iso: string): boolean {
  if (choice !== 'custom') return false
  const ns = isoToExpiresNs(iso)
  return ns === undefined || BigInt(ns) <= BigInt(Date.now()) * 1_000_000n
}

export function isDropLink(link: ShareLinkInfo): boolean {
  return Boolean(link.perms.create && !link.perms.read && !link.perms.download)
}

export async function copyText(text: string): Promise<boolean> {
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

export const newExpiryOptions = (t: (key: string) => string): SelectOption[] => [
  { value: 'none', text: t('share.none') },
  { value: '1d', text: t('share.1_day') },
  { value: '7d', text: t('share.7_days') },
  { value: '30d', text: t('share.30_days_default') },
  { value: 'custom', text: t('share.pick_a_date') }
]

export const editExpiryOptions = (t: (key: string) => string): SelectOption[] => [
  { value: 'keep', text: t('share.leave_unchanged') },
  { value: 'none', text: t('share.none') },
  { value: '1d', text: t('share.1_day') },
  { value: '7d', text: t('share.7_days') },
  { value: '30d', text: t('share.30_days') },
  { value: 'custom', text: t('share.pick_a_date') }
]


export function useShareManageController(
  open: boolean,
  path: string,
  targetIsDir: boolean,
  t: (key: string, values?: Record<string, string | number>) => string,
  closeParent: () => void
) {
  const [state, patch] = useShareManageState()
  const { newKind, newRead, newDownload, newPassword, newExpiry, newExpiryDate, newMaxDownloads, newLabel, createValidation, justCreated, issuedAcknowledged, editRead, editDownload, editClearPassword, editNewPassword, editExpiry, editExpiryDate, editMaxDownloads, editLabel, revokeTarget } = state
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
    patch({ dialogOpen: open })
    if (open) patch({ justCreated: null, issuedAcknowledged: false, copyErrorId: null, creatingOpen: false, editingId: null, revokeTarget: null })
  }, [open, patch])

  function resetCreateForm(): void {
    patch({ newKind: 'download', newRead: true, newDownload: true, newPassword: '', newExpiry: '30d', newExpiryDate: '', newMaxDownloads: '', newLabel: '', createValidation: null })
    createMut.reset()
  }

  function openCreate(): void {
    resetCreateForm()
    patch({ creatingOpen: true })
  }

  async function submitCreate(): Promise<void> {
    patch({ createValidation: null })
    const isDrop = newKind === 'drop'
    const perms: PermsReq = isDrop ? { create: true } : { read: newRead, download: newDownload }
    const maxDownloads = !isDrop && newMaxDownloads.trim() ? Number(newMaxDownloads.trim()) : undefined
    if (maxDownloads !== undefined && (!Number.isInteger(maxDownloads) || maxDownloads <= 0)) {
      patch({ createValidation: t('share.download_limit_must_integer_1') })
      return
    }
    try {
      const created = await createMut.mutateAsync({ path, perms, password: newPassword.trim() || undefined, expires_ns: expiryToNs(newExpiry, newExpiryDate), max_downloads: maxDownloads, label: newLabel.trim() || undefined })
      patch({ justCreated: created, issuedAcknowledged: false, copyErrorId: null, copiedId: null, creatingOpen: false })
    } catch {
      // The mutation error is rendered beside the form.
    }
  }

  async function copyLink(text: string, id: number): Promise<void> {
    patch({ copyErrorId: null })
    if (await copyText(text)) {
      patch({ copiedId: id })
      window.setTimeout(() => patch((current) => ({ copiedId: current.copiedId === id ? null : current.copiedId })), 2000)
    } else {
      patch({ copiedId: null, copyErrorId: id })
    }
  }

  function acknowledgeIssued(): void {
    patch({ issuedAcknowledged: true, justCreated: null, copyErrorId: null })
  }

  function closeIssued(): void {
    if (justCreated && !issuedAcknowledged) {
      patch({ dialogOpen: true })
      return
    }
    patch({ dialogOpen: false, justCreated: null })
    closeParent()
  }

  function openEdit(link: ShareLinkInfo): void {
    patch({ editingId: link.id, editRead: link.perms.read, editDownload: link.perms.download, editClearPassword: false, editNewPassword: '', editExpiry: 'keep', editExpiryDate: '', editMaxDownloads: link.max_downloads === null ? '' : String(link.max_downloads), editLabel: link.label ?? '' })
    updateMut.reset()
  }

  async function submitEdit(link: ShareLinkInfo): Promise<void> {
    const maxDownloads = editMaxDownloads.trim() ? Number(editMaxDownloads.trim()) : null
    if (maxDownloads !== null && (!Number.isInteger(maxDownloads) || maxDownloads <= 0)) return
    try {
      await updateMut.mutateAsync({ id: link.id, patch: { perms: { read: editRead, download: editDownload, create: link.perms.create }, password: editClearPassword ? null : editNewPassword.trim() || undefined, expires_ns: editExpiry === 'keep' ? undefined : (expiryToNs(editExpiry, editExpiryDate) ?? null), max_downloads: maxDownloads, label: editLabel.trim() || null } })
      patch({ editingId: null })
    } catch {
      // The mutation error is rendered in the shared error banner.
    }
  }

  async function confirmRevoke(): Promise<void> {
    if (!revokeTarget) return
    const id = revokeTarget.id
    patch({ revokeTarget: null })
    if (justCreated?.id === id) patch({ justCreated: null })
    try {
      await deleteMut.mutateAsync(id)
    } catch {
      // The mutation error is rendered in the shared error banner.
    }
  }

  return { state, links, sharesQuery, createMut, updateMut, deleteMut, newKindOptions, newExpiryChoices, editExpiryChoices, newExpiryBad, editExpiryBad, createError, loadError, patch, resetCreateForm, openCreate, submitCreate, copyLink, acknowledgeIssued, closeIssued, openEdit, submitEdit, confirmRevoke }
}
