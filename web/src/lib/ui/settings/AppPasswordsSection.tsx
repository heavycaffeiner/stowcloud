import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { ApiError, type AppPasswordInfo } from '../../api/client'
import { describeApiError } from '../../api/error-text'
import { formatDateNs } from '../../i18n'
import { useI18n } from '../../i18n/use-i18n'
import { appPasswordsQuery, createAppPasswordMutation, revokeAppPasswordMutation } from '../../query/account'
import { Button } from '../Button'
import { TextField } from '../TextField'
import { SettingsDialog } from './SettingsDialog'

export function AppPasswordsSection() {
  const { t } = useI18n()
  const list = useQuery(appPasswordsQuery())
  const create = useMutation(createAppPasswordMutation())
  const revoke = useMutation(revokeAppPasswordMutation())
  const wipe = useMutation(revokeAppPasswordMutation())
  const [createOpen, setCreateOpen] = useState(false)
  const [newName, setNewName] = useState('')
  const [newCurrent, setNewCurrent] = useState('')
  const [newReadOnly, setNewReadOnly] = useState(false)
  const [issuedToken, setIssuedToken] = useState<string | null>(null)
  const [issuedAcknowledged, setIssuedAcknowledged] = useState(false)
  const [tokenCopyState, setTokenCopyState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const [revokeTarget, setRevokeTarget] = useState<AppPasswordInfo | null>(null)
  const [wipeTarget, setWipeTarget] = useState<AppPasswordInfo | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  function isExpired(item: AppPasswordInfo): boolean {
    return item.expires_ns ? Number(item.expires_ns) / 1e6 <= Date.now() : false
  }
  function openCreate(): void {
    setNewName(''); setNewCurrent(''); setNewReadOnly(false); create.reset(); setActionError(null); setCreateOpen(true)
  }
  function confirmCreate(): void {
    if (!newName.trim() || !newCurrent) return
    create.mutate({ name: newName.trim(), currentPassword: newCurrent, scope: newReadOnly ? { readOnly: true } : undefined }, {
      onSuccess: (result) => { setCreateOpen(false); setIssuedToken(result.token); setIssuedAcknowledged(false); setTokenCopyState('idle'); void list.refetch() },
      onError: (error) => setActionError(describeApiError(error, t('app_password.could_not_create_app_password')))
    })
  }
  function closeIssued(): void {
    if (issuedToken && !issuedAcknowledged) return
    setIssuedToken(null)
  }
  function acknowledgeIssued(): void {
    setIssuedAcknowledged(true)
    setIssuedToken(null)
  }
  async function copyToken(): Promise<void> {
    if (!issuedToken) return
    try { await navigator.clipboard.writeText(issuedToken); setTokenCopyState('copied') } catch { setTokenCopyState('failed') }
  }
  function confirmRevoke(): void {
    if (!revokeTarget) return
    setActionError(null)
    revoke.mutate({ id: revokeTarget.id, wipe: false }, { onSuccess: () => setRevokeTarget(null), onError: (error) => { if (error instanceof ApiError && error.status === 404) { setRevokeTarget(null); void list.refetch() } else setActionError(describeApiError(error, t('common.could_not_save_change'))) } })
  }
  function confirmWipe(): void {
    if (!wipeTarget) return
    setActionError(null)
    wipe.mutate({ id: wipeTarget.id, wipe: true }, { onSuccess: () => setWipeTarget(null), onError: (error) => { if (error instanceof ApiError && error.status === 404) { setWipeTarget(null); void list.refetch() } else setActionError(describeApiError(error, t('common.could_not_save_change'))) } })
  }

  return (
    <div className="sc-app-passwords">
      {list.isPending ? <p>{t('common.loading')}</p> : list.isError ? <p className="sc-app-passwords__error">{t('common.could_not_load_list')}</p> : (list.data ?? []).length === 0 ? <p className="sc-app-passwords__empty">{t('app_password.no_app_passwords_issued_yet')}</p> : (
        <ul className="sc-app-passwords__list">
          {(list.data ?? []).map((item) => <li key={item.id}><div><strong className="sc-app-passwords__name">{item.name}</strong>{item.read_only ? <span className="sc-settings-badge">{t('common.read_only')}</span> : null}<p>{t('app_password.issued', { date: formatDateNs(item.created_ns) })} - {item.last_used_ns ? t('app_password.last_used', { date: formatDateNs(item.last_used_ns) }) : t('app_password.never_used')}{isExpired(item) ? ` - ${t('app_password.expired')}` : item.expires_ns ? ` - ${t('app_password.expires', { date: formatDateNs(item.expires_ns) })}` : ''}</p></div><div className="sc-settings-card__buttons">{!isExpired(item) ? <Button variant="text" ariaLabel={t('app_password.wipe', { name: item.name })} onClick={() => { setActionError(null); setWipeTarget(item) }}>{t('app_password.wipe_2')}</Button> : null}<Button variant="text" ariaLabel={t('app_password.revoke', { name: item.name })} onClick={() => { setActionError(null); setRevokeTarget(item) }}>{t('app_password.revoke_2')}</Button></div></li>)}
        </ul>
      )}
      <div className="sc-app-passwords__actions"><Button variant="outlined" onClick={openCreate}>{t('app_password.new_app_password')}</Button></div>
      <SettingsDialog open={createOpen} title={t('app_password.new_app_password')} onClose={() => setCreateOpen(false)} actions={<><Button variant="text" onClick={() => setCreateOpen(false)}>{t('common.cancel')}</Button><Button onClick={confirmCreate} disabled={!newName.trim() || !newCurrent} loading={create.isPending}>{t('common.create')}</Button></>}>
        {actionError ? <p className="sc-app-passwords__error" role="alert">{actionError}</p> : null}<p>{t('app_password.use_one_where_your_account')}</p><TextField label={t('common.name')} placeholder={t('app_password.e_g_rclone_backup')} value={newName} onValueChange={setNewName} /><TextField type="password" label={t('common.current_password')} autoComplete="current-password" value={newCurrent} onValueChange={setNewCurrent} /><label><input type="checkbox" checked={newReadOnly} onChange={(event) => setNewReadOnly(event.currentTarget.checked)} /> {t('app_password.read_only_download_only_no')}</label><p>{t('app_password.read_only_recommended_anywhere_only')}</p>
      </SettingsDialog>
      <SettingsDialog open={!!issuedToken} title={t('app_password.app_password_issued')} onClose={closeIssued} dismissible={false} actions={<Button onClick={acknowledgeIssued}>{t('app_password.acknowledge_saved')}</Button>}>
        <p>{t('app_password.once_you_close_cannot_shown')}</p><div className="sc-token-row"><input readOnly value={issuedToken ?? ''} aria-label={t('app_password.app_password_issued')} /><Button variant="text" onClick={() => void copyToken()}>{tokenCopyState === 'copied' ? t('common.copied') : t('common.copy')}</Button></div>{tokenCopyState === 'failed' ? <p className="sc-app-passwords__copy-feedback" role="alert">{t('app_password.copy_failed')}</p> : null}
      </SettingsDialog>
      <SettingsDialog open={!!revokeTarget} title={t('app_password.revoke_app_password')} onClose={() => setRevokeTarget(null)} actions={<><Button variant="text" onClick={() => setRevokeTarget(null)}>{t('common.cancel')}</Button><Button onClick={confirmRevoke} loading={revoke.isPending}>{t('app_password.revoke_2')}</Button></>}>
        <p>{t('app_password.everything_using_disconnected_at_once', { name: revokeTarget?.name ?? '' })}</p>{actionError ? <p className="sc-app-passwords__error" role="alert">{actionError}</p> : null}
      </SettingsDialog>
      <SettingsDialog open={!!wipeTarget} title={t('app_password.wipe_device')} onClose={() => setWipeTarget(null)} actions={<><Button variant="text" onClick={() => setWipeTarget(null)}>{t('common.cancel')}</Button><Button onClick={confirmWipe} loading={wipe.isPending}>{t('app_password.wipe_2')}</Button></>}>
        <p>{t('app_password.next_time_that_device_erase', { name: wipeTarget?.name ?? '' })}</p>{actionError ? <p className="sc-app-passwords__error" role="alert">{actionError}</p> : null}
      </SettingsDialog>
    </div>
  )
}
