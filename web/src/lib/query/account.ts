// The signed-in account's own settings: password, second factor, app
// passwords, sessions, SMB and the OIDC link.
//
// Almost every write here changes something `GET /api/auth/session` reports
// (`totp_enabled`, `smb_opt_out`, the OIDC hint), so the session is the query
// they invalidate.
import { mutationOptions, queryOptions } from '@tanstack/svelte-query'
import { api } from '../api/client'
import { queryClient } from './client'
import { keys } from './keys'

export function appPasswordsQuery() {
  return queryOptions({ queryKey: keys.appPasswords(), queryFn: () => api.listAppPasswords() })
}

export function activeSessionsQuery() {
  return queryOptions({ queryKey: keys.activeSessions(), queryFn: () => api.listSessions() })
}

export function recoveryCodesQuery(enabled: boolean) {
  return queryOptions({ queryKey: keys.recoveryCodes(), queryFn: () => api.recoveryCodesRemaining(), enabled })
}

function invalidateSession(): void {
  void queryClient.invalidateQueries({ queryKey: keys.session() })
}

export function changePasswordMutation() {
  return mutationOptions({
    mutationFn: ({ current, next }: { current: string; next: string }) => api.changePassword(current, next)
  })
}

export function totpSetupMutation() {
  return mutationOptions({ mutationFn: (currentPassword: string) => api.totpSetup(currentPassword) })
}

export function totpEnrollMutation() {
  return mutationOptions({
    mutationFn: ({ password, secret, code }: { password: string; secret: string; code: string }) =>
      api.totpEnroll(password, secret, code),
    onSuccess: () => {
      invalidateSession()
      void queryClient.invalidateQueries({ queryKey: keys.recoveryCodes() })
    }
  })
}

export function totpDisableMutation() {
  return mutationOptions({ mutationFn: (password: string) => api.totpDisable(password), onSuccess: invalidateSession })
}

export function reissueRecoveryCodesMutation() {
  return mutationOptions({
    mutationFn: (password: string) => api.reissueRecoveryCodes(password),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.recoveryCodes() })
  })
}

export interface AppPasswordScope {
  readonly readOnly?: boolean
  readonly shares?: string[]
}

export function createAppPasswordMutation() {
  return mutationOptions({
    mutationFn: ({ name, currentPassword, scope }: { name: string; currentPassword: string; scope?: AppPasswordScope }) =>
      scope ? api.createScopedAppPassword(name, currentPassword, scope) : api.createAppPassword(name, currentPassword),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.appPasswords() })
  })
}

export function revokeAppPasswordMutation() {
  return mutationOptions({
    mutationFn: ({ id, wipe }: { id: number; wipe: boolean }) => (wipe ? api.wipeAppPassword(id) : api.revokeAppPassword(id)),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.appPasswords() })
  })
}

export function revokeSessionMutation() {
  return mutationOptions({
    mutationFn: (idHash: string) => api.revokeSession(idHash),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.activeSessions() })
  })
}

export function smbSettingsMutation() {
  return mutationOptions({
    mutationFn: ({ currentPassword, optOut, enabled }: { currentPassword: string; optOut: boolean; enabled: boolean }) =>
      api.updateSmbSettings(currentPassword, optOut, enabled),
    onSuccess: invalidateSession
  })
}

/**
 * Setting and clearing the SMB password are one control with two answers: a
 * set can report that it cleared the two self-service toggles, and a clear can
 * report that the account password now governs SMB again. The screen reads
 * whichever field it was told about, so both are optional here.
 */
export interface SmbPasswordResult {
  readonly smb_toggles_cleared?: boolean
  readonly reverted_to_account_password?: boolean
}

export function smbPasswordMutation() {
  return mutationOptions<SmbPasswordResult, Error, { currentPassword: string; smbPassword: string | null }>({
    mutationFn: ({ currentPassword, smbPassword }) =>
      smbPassword === null ? api.clearSmbPassword(currentPassword) : api.setSmbPassword(currentPassword, smbPassword),
    onSuccess: invalidateSession
  })
}

export function oidcLinkStartMutation() {
  return mutationOptions({
    mutationFn: ({ password, returnTo }: { password: string; returnTo?: string }) => api.oidcLinkStart(password, returnTo)
  })
}

export function oidcUnlinkMutation() {
  return mutationOptions({ mutationFn: (password: string) => api.oidcUnlink(password), onSuccess: invalidateSession })
}
