import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import { formatBytes, bytesToMb, BYTES_PER_MB } from '../../format/bytes'
import { scorePasswordStrength } from '../../format/password-strength'
import { t } from '../../i18n'
import { ApiError, type AdminUser } from '../../api/client'
import { describeApiError } from '../../api/error-text'
import { adminUserMutation, adminUsersQuery } from '../../query/admin'
import { Button } from '../Button'
import { TextField } from '../TextField'
import { VirtualList } from '../VirtualList'
import { GrantManagementSection } from './GrantManagementSection'
import { UserOidcDialog } from './UserOidcDialog'
import { Icon } from '../Icon'
import './admin.css'

const MIN_PASSWORD_LEN = 10

export function UserManagementSection() {
  const usersQuery = useQuery(adminUsersQuery())
  const users = usersQuery.data ?? []
  const activeAdminCount = useMemo(() => users.filter((user) => user.is_admin && !user.disabled).length, [users])
  const lastActiveAdmin = (user: AdminUser) => user.is_admin && !user.disabled && activeAdminCount <= 1
  const [createOpen, setCreateOpen] = useState(false)
  const [newName, setNewName] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [createValidation, setCreateValidation] = useState<string | null>(null)
  const create = useMutation(adminUserMutation())
  const [deleteTarget, setDeleteTarget] = useState<AdminUser | null>(null)
  const remove = useMutation(adminUserMutation())
  const [quotaTarget, setQuotaTarget] = useState<AdminUser | null>(null)
  const [quotaInput, setQuotaInput] = useState('')
  const [quotaValidation, setQuotaValidation] = useState<string | null>(null)
  const quota = useMutation(adminUserMutation())
  const [passwordTarget, setPasswordTarget] = useState<AdminUser | null>(null)
  const [passwordInput, setPasswordInput] = useState('')
  const [passwordConfirm, setPasswordConfirm] = useState('')
  const [passwordValidation, setPasswordValidation] = useState<string | null>(null)
  const password = useMutation(adminUserMutation())
  const toggle = useMutation(adminUserMutation())
  const [grantsTarget, setGrantsTarget] = useState<AdminUser | null>(null)
  const [oidcTarget, setOidcTarget] = useState<AdminUser | null>(null)
  const togglingId = toggle.isPending && toggle.variables?.kind === 'disable' ? toggle.variables.id : null
  const deleteDialogRef = useRef<HTMLElement | null>(null)
  const grantsDialogRef = useRef<HTMLElement | null>(null)
  const quotaDialogRef = useRef<HTMLElement | null>(null)
  const passwordDialogRef = useRef<HTMLElement | null>(null)
  const createDialogRef = useRef<HTMLElement | null>(null)
  useEffect(() => {
    const element = createDialogRef.current
    if (!element) return
    const close = () => { if (!create.isPending) setCreateOpen(false) }
    element.addEventListener('close', close)
    return () => element.removeEventListener('close', close)
  }, [create.isPending])
  useEffect(() => {
    const element = deleteDialogRef.current
    if (!element) return
    const close = () => { if (!remove.isPending) setDeleteTarget(null) }
    element.addEventListener('close', close)
    return () => element.removeEventListener('close', close)
  }, [remove.isPending])
  useEffect(() => {
    const element = grantsDialogRef.current
    if (!element) return
    const close = () => setGrantsTarget(null)
    element.addEventListener('close', close)
    return () => element.removeEventListener('close', close)
  }, [])
  useEffect(() => {
    const element = quotaDialogRef.current
    if (!element) return
    const close = () => { if (!quota.isPending) setQuotaTarget(null) }
    element.addEventListener('close', close)
    return () => element.removeEventListener('close', close)
  }, [quota.isPending])
  useEffect(() => {
    const element = passwordDialogRef.current
    if (!element) return
    const close = () => { if (!password.isPending) setPasswordTarget(null) }
    element.addEventListener('close', close)
    return () => element.removeEventListener('close', close)
  }, [password.isPending])

  const createError = createValidation ?? (create.error ? createErrorText(create.error) : null)
  const deleteError = remove.error ? userDeleteError(remove.error) : null
  const toggleError = toggle.error ? userToggleError(toggle.error) : null
  const quotaError = quotaValidation ?? (quota.error ? describeApiError(quota.error, t('common.could_not_save')) : null)
  const passwordError = passwordValidation ?? (password.error ? describeApiError(password.error, t('password.could_not_change_password_try')) : null)

  function submitCreate(): void {
    setCreateValidation(null)
    if (newPassword.length < MIN_PASSWORD_LEN) { setCreateValidation(t('user.password_must_at_least_characters', { min: MIN_PASSWORD_LEN })); return }
    if (!newName.trim()) return
    create.mutate({ kind: 'create', name: newName.trim(), password: newPassword }, { onSuccess: (created) => { setCreateOpen(false); setGrantsTarget(created as AdminUser) } })
  }
  function submitDelete(): void {
    if (!deleteTarget) return
    remove.mutate({ kind: 'delete', id: deleteTarget.id }, { onSuccess: () => setDeleteTarget(null) })
  }
  function submitQuota(): void {
    if (!quotaTarget) return
    setQuotaValidation(null)
    const value = quotaInput.trim()
    if (!value) { quota.mutate({ kind: 'quota', id: quotaTarget.id, quotaBytes: null }, { onSuccess: () => setQuotaTarget(null) }); return }
    const mb = Number(value)
    if (!Number.isFinite(mb) || mb <= 0) { setQuotaValidation(t('user.enter_number_greater_than_0')); return }
    quota.mutate({ kind: 'quota', id: quotaTarget.id, quotaBytes: Math.round(mb * BYTES_PER_MB) }, { onSuccess: () => setQuotaTarget(null) })
  }
  function submitPassword(): void {
    if (!passwordTarget) return
    setPasswordValidation(null)
    if (passwordInput.length < MIN_PASSWORD_LEN) { setPasswordValidation(t('user.password_must_at_least_characters', { min: MIN_PASSWORD_LEN })); return }
    if (passwordInput !== passwordConfirm) { setPasswordValidation(t('password.new_passwords_do_not_match')); return }
    password.mutate({ kind: 'password', id: passwordTarget.id, password: passwordInput }, { onSuccess: () => setPasswordTarget(null) })
  }
  return (
    <section className="sc-admin-section sc-user-mgmt">
      <div className="sc-admin-section__header"><p className="sc-admin-section__hint">{t('user.create_accounts_suspend_or_re')}</p><Button icon={<Icon name="add" />} onClick={() => { create.reset(); setCreateValidation(null); setNewName(''); setNewPassword(''); setCreateOpen(true) }}>{t('user.add_user')}</Button></div>
      {toggleError ? <p className="sc-admin-section__error" role="alert">{toggleError}</p> : null}
      {usersQuery.isPending ? <mdui-circular-progress /> : usersQuery.error ? <p className="sc-admin-section__error" role="alert">{describeApiError(usersQuery.error, t('user.could_not_load_user_list'))}</p> : (
        <VirtualList
          className="sc-admin-list"
          items={users}
          itemKey={(user) => user.id}
          estimateSize={80}
          pinnedKeys={[deleteTarget?.id, quotaTarget?.id, passwordTarget?.id, grantsTarget?.id, oidcTarget?.id, togglingId].filter((id): id is number => id != null)}
          renderItem={(user) => {
            const locked = lastActiveAdmin(user)
            return <div className="sc-admin-row"><div className="sc-admin-row__body"><div className="sc-admin-row__title"><span className="sc-admin-row__name">{user.display_name || user.name}</span>{user.is_admin ? <span className="sc-admin-chip">{t('common.administrator')}</span> : null}{user.disabled ? <span className="sc-admin-chip sc-admin-chip--muted">{t('user.inactive')}</span> : null}</div><div className="sc-admin-row__supporting">{user.name}</div></div><mdui-switch checked={!user.disabled} disabled={locked} title={locked ? t('user.last_active_administrator_cannot_deactivated') : undefined} aria-label={t('user.enable_account', { name: user.name })} onChange={() => toggle.mutate({ kind: 'disable', id: user.id, disabled: !user.disabled })} /><button className="sc-admin-chip sc-admin-chip--muted" type="button" onClick={() => { quota.reset(); setQuotaValidation(null); setQuotaInput(user.quota_bytes ? String(bytesToMb(Number(BigInt(user.quota_bytes)))) : ''); setQuotaTarget(user) }}>{quotaLabel(user)}</button><div className="sc-admin-row__actions"><Button variant="text" square ariaLabel={t('common.manage_folders_visible', { name: user.name })} onClick={() => setGrantsTarget(user)}><Icon name="account_tree" /></Button><Button variant="text" square ariaLabel={t('oidc.manage_single_sign_connection', { name: user.name })} onClick={() => setOidcTarget(user)}><Icon name="link" /></Button><Button variant="text" square ariaLabel={t('password.change_password')} onClick={() => { password.reset(); setPasswordValidation(null); setPasswordInput(''); setPasswordConfirm(''); setPasswordTarget(user) }}><Icon name="lock" /></Button><Button variant="text" square ariaLabel={t('common.delete_2', { name: user.name })} disabled={locked || togglingId === user.id} onClick={() => { remove.reset(); setDeleteTarget(user) }}><Icon name="delete" /></Button></div></div>
          }}
        />
      )}
      <mdui-dialog ref={createDialogRef} open={createOpen} headline={t('user.add_user')} close-on-esc close-on-overlay-click><form className="sc-admin-form" onSubmit={(event) => { event.preventDefault(); submitCreate() }}><TextField label={t('user.username')} value={newName} autoComplete="off" autoFocus onValueChange={setNewName} /><TextField type="password" label={t('common.password')} value={newPassword} autoComplete="new-password" onValueChange={setNewPassword} />{newPassword ? <div><mdui-linear-progress value={scorePasswordStrength(newPassword).ratio} aria-label={t('common.password_strength', { level: scorePasswordStrength(newPassword).label })} /><span className="sc-admin-section__field-hint">{scorePasswordStrength(newPassword).label}</span></div> : null}<p className="sc-admin-section__field-hint">{t('user.at_least_characters_turning_smb', { min: MIN_PASSWORD_LEN })}</p>{createError ? <p className="sc-admin-section__error" role="alert">{createError}</p> : null}</form><mdui-button slot="action" variant="text" disabled={create.isPending} onClick={() => setCreateOpen(false)}>{t('common.cancel')}</mdui-button><mdui-button slot="action" variant="filled" loading={create.isPending} onClick={submitCreate}>{t('common.add')}</mdui-button></mdui-dialog>
      <mdui-dialog ref={deleteDialogRef} open={!!deleteTarget} headline={t('user.delete_user')} close-on-esc close-on-overlay-click><p>{t('user.permanently_deletes_account_including_its', { name: deleteTarget?.name ?? '' })}</p>{deleteError ? <p className="sc-admin-section__error" role="alert">{deleteError}</p> : null}<mdui-button slot="action" variant="text" disabled={remove.isPending} onClick={() => setDeleteTarget(null)}>{t('common.cancel')}</mdui-button><span slot="action" className="sc-danger"><mdui-button variant="filled" loading={remove.isPending} onClick={submitDelete}>{t('common.delete')}</mdui-button></span></mdui-dialog>
      <mdui-dialog ref={grantsDialogRef} open={!!grantsTarget} headline={grantsTarget ? t('user.folders_visible', { name: grantsTarget.display_name || grantsTarget.name }) : t('common.folder_permissions')} close-on-esc close-on-overlay-click><>{grantsTarget ? <GrantManagementSection principal={{ kind: 'user', id: grantsTarget.id }} label={grantsTarget.display_name || grantsTarget.name} /> : null}</><mdui-button slot="action" variant="text" onClick={() => setGrantsTarget(null)}>{t('common.close')}</mdui-button></mdui-dialog>
      <UserOidcDialog user={oidcTarget} onClose={() => setOidcTarget(null)} />
      <mdui-dialog ref={quotaDialogRef} open={!!quotaTarget} headline={quotaTarget ? t('user.storage_quota', { name: quotaTarget.display_name || quotaTarget.name }) : t('user.storage_quota_2')} close-on-esc close-on-overlay-click><form className="sc-admin-form" onSubmit={(event) => { event.preventDefault(); submitQuota() }}><TextField label={t('user.storage_quota_mb')} placeholder={t('user.empty_means_unlimited')} value={quotaInput} autoFocus onValueChange={setQuotaInput} /><p className="sc-admin-section__field-hint">{quotaTarget ? t('user.currently_using', { used: formatBytes(Number(BigInt(quotaTarget.usage_bytes))) }) : ''}{t('user.empty_means_unlimited_uploads_copies')}</p>{quotaError ? <p className="sc-admin-section__error" role="alert">{quotaError}</p> : null}</form><mdui-button slot="action" variant="text" disabled={quota.isPending} onClick={() => setQuotaTarget(null)}>{t('common.cancel')}</mdui-button><mdui-button slot="action" variant="filled" loading={quota.isPending} onClick={submitQuota}>{t('common.save')}</mdui-button></mdui-dialog>
      <mdui-dialog ref={passwordDialogRef} open={!!passwordTarget} headline={t('password.change_password')} close-on-esc close-on-overlay-click><form className="sc-admin-form" onSubmit={(event) => { event.preventDefault(); submitPassword() }}><TextField type="password" label={t('password.new_password')} value={passwordInput} autoComplete="new-password" autoFocus onValueChange={setPasswordInput} /><TextField type="password" label={t('password.confirm_new_password')} value={passwordConfirm} autoComplete="new-password" onValueChange={setPasswordConfirm} />{passwordInput ? <div><mdui-linear-progress value={scorePasswordStrength(passwordInput).ratio} aria-label={t('password.new_password_strength', { level: scorePasswordStrength(passwordInput).label })} /><span className="sc-admin-section__field-hint">{scorePasswordStrength(passwordInput).label}</span></div> : null}<p className="sc-admin-section__field-hint">{t('password.must_at_least_characters', { min: MIN_PASSWORD_LEN })}</p>{passwordError ? <p className="sc-admin-section__error" role="alert">{passwordError}</p> : null}</form><mdui-button slot="action" variant="text" disabled={password.isPending} onClick={() => setPasswordTarget(null)}>{t('common.cancel')}</mdui-button><mdui-button slot="action" variant="filled" loading={password.isPending} disabled={!passwordInput || passwordInput !== passwordConfirm} onClick={submitPassword}>{t('common.save')}</mdui-button></mdui-dialog>
    </section>
  )
}

function quotaLabel(user: AdminUser): string {
  const used = formatBytes(Number(BigInt(user.usage_bytes)))
  return user.quota_bytes ? `${used} / ${formatBytes(Number(BigInt(user.quota_bytes)))}` : t('user.used', { used })
}
function createErrorText(error: unknown): string {
  if (error instanceof ApiError && error.code === 'fs.conflict') return t('common.name_already_taken')
  if (error instanceof ApiError && error.code === 'auth.weak_password') return t('user.password_must_at_least_characters', { min: error.reasonNumber('min_length') ?? MIN_PASSWORD_LEN })
  return describeApiError(error, t('user.could_not_create_user'))
}
function userToggleError(error: unknown): string { return error instanceof ApiError && error.code === 'admin.last_admin' ? t('user.last_administrator_cannot_deactivated') : describeApiError(error, t('common.could_not_save_change')) }
function userDeleteError(error: unknown): string { return error instanceof ApiError && error.code === 'admin.last_admin' ? t('user.last_administrator_cannot_deleted') : describeApiError(error, t('common.could_not_delete')) }
