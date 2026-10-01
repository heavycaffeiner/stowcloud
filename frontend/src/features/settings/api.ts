// The signed-in account's own settings: password, second factor, app
// passwords, sessions, SMB, the OIDC link, the root order, and signing out.
// Most writes change something the session reports, so the session is what
// they invalidate. Every password proof travels as `current`.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { client, decimal, malformed, unwrap, unwrapEmpty } from '../../api/fetcher'
import type { components } from '../../api/generated/schema'
import { lock } from '../../lib/crypto/e2ee'
import { invalidateEncryptedShares } from '../../lib/crypto/encrypted-shares'
import { keys } from '../../lib/query/keys'
import { resetUploadQueue } from '../uploads/queue'
import { isProviderUrl, logout } from '../auth/api'

type Schemas = components['schemas']

export interface AppPasswordInfo {
  id: number
  name: string
  created_ns: string
  last_used_ns: string | null
  expires_ns: string | null
  /** True when the scope holds no permission that changes anything. */
  read_only: boolean
}

/** One live session. `id_hash` names it for revocation and cannot resume it. */
export interface ActiveSession {
  id_hash: string
  created_ns: string
  last_seen_ns: string
  absolute_expiry_ns: string
  ip_first: string | null
  ua_first: string | null
  ua_display: string | null
  current: boolean
}

/** `shares` are root labels as the session lists them. No scope at all means
 *  unrestricted. */
export interface AppPasswordScope {
  readonly readOnly?: boolean
  readonly shares?: string[]
}

const WRITING_PERMS = ['write', 'create', 'delete', 'rename', 'move', 'share']

function appPasswordFromWire(w: Schemas['AppPasswordView']): AppPasswordInfo {
  return {
    id: decimal(w.id, 'app password id'),
    name: w.name,
    created_ns: w.created_ns,
    // Absent means never used and never expires; zero would be a real instant.
    last_used_ns: w.last_used_ns ?? null,
    expires_ns: w.expires_ns ?? null,
    read_only: (w.perms ?? []).every((p) => !WRITING_PERMS.includes(p))
  }
}

function sessionRowFromWire(w: Schemas['SessionView']): ActiveSession {
  return {
    id_hash: w.handle,
    created_ns: w.created_ns,
    last_seen_ns: w.last_seen_ns,
    absolute_expiry_ns: w.absolute_ns,
    ip_first: w.ip ?? null,
    ua_first: w.ua ?? null,
    ua_display: w.ua_display ?? null,
    current: w.current
  }
}

async function listAppPasswords(): Promise<AppPasswordInfo[]> {
  return ((await unwrap(client.GET('/api/v1/account/app-passwords'))) ?? []).map(appPasswordFromWire)
}

async function listSessions(): Promise<ActiveSession[]> {
  return ((await unwrap(client.GET('/api/v1/account/sessions'))) ?? []).map(sessionRowFromWire)
}

async function recoveryCodesRemaining(): Promise<number> {
  return (await unwrap(client.GET('/api/v1/account/totp/recovery-codes'))).remaining
}

async function createAppPassword(name: string, current: string, scope?: AppPasswordScope): Promise<string> {
  // A scope naming nothing is refused by the server, so an empty one is sent
  // as no scope at all.
  const perms = scope?.readOnly ? ['read', 'download'] : undefined
  const shares = scope?.shares?.length ? scope.shares : undefined
  const body = { name, current, scope: perms || shares ? { perms, shares } : undefined }
  return (await unwrap(client.POST('/api/v1/account/app-passwords', { body }))).token
}

/** A wipe is not a revoke: the credential keeps working until the lost
 *  device has erased its copies and reported back. */
async function revokeAppPassword(id: number, wipe: boolean): Promise<void> {
  const params = { path: { id: String(id) } }
  await unwrapEmpty(
    wipe
      ? client.POST('/api/v1/account/app-passwords/{id}/wipe', { params })
      : client.DELETE('/api/v1/account/app-passwords/{id}', { params })
  )
}

function useInvalidate(): (key: readonly unknown[]) => void {
  const queryClient = useQueryClient()
  return (key) => void queryClient.invalidateQueries({ queryKey: key })
}

export function useAppPasswords() {
  return useQuery({ queryKey: keys.appPasswords(), queryFn: listAppPasswords })
}

export function useActiveSessions() {
  return useQuery({ queryKey: keys.activeSessions(), queryFn: listSessions })
}

export function useRecoveryCodesRemaining(enabled: boolean) {
  return useQuery({ queryKey: keys.recoveryCodes(), queryFn: recoveryCodesRemaining, enabled })
}

export function useChangePassword() {
  return useMutation({
    mutationFn: ({ current, next }: { current: string; next: string }) =>
      unwrapEmpty(client.POST('/api/v1/account/password', { body: { current, new: next } }))
  })
}

/** Mints a secret without enrolling it. */
export function useTotpSetup() {
  return useMutation({
    mutationFn: (current: string) => unwrap(client.POST('/api/v1/account/totp/setup', { body: { current } }))
  })
}

/** Resolves to the recovery codes, which the server never shows again. */
export function useTotpEnroll() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: async ({ password, secret, code }: { password: string; secret: string; code: string }) =>
      (await unwrap(client.POST('/api/v1/account/totp/enroll', { body: { current: password, secret, code } })))
        .recovery_codes ?? [],
    onSuccess: () => {
      invalidate(keys.session())
      invalidate(keys.recoveryCodes())
    }
  })
}

export function useTotpDisable() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (current: string) => unwrapEmpty(client.POST('/api/v1/account/totp/disable', { body: { current } })),
    onSuccess: () => invalidate(keys.session())
  })
}

/** Replaces every recovery code; the old ones stop working at once. */
export function useReissueRecoveryCodes() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: async (current: string) =>
      (await unwrap(client.POST('/api/v1/account/totp/recovery-codes', { body: { current } }))).recovery_codes ?? [],
    onSuccess: () => invalidate(keys.recoveryCodes())
  })
}

/** Resolves to the token, which exists only in this answer. */
export function useCreateAppPassword() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: ({
      name,
      currentPassword,
      scope
    }: {
      name: string
      currentPassword: string
      scope?: AppPasswordScope
    }) => createAppPassword(name, currentPassword, scope),
    onSuccess: () => invalidate(keys.appPasswords())
  })
}

export function useRevokeAppPassword() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: ({ id, wipe }: { id: number; wipe: boolean }) => revokeAppPassword(id, wipe),
    onSuccess: () => invalidate(keys.appPasswords())
  })
}

export function useRevokeSession() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (idHash: string) =>
      unwrapEmpty(client.DELETE('/api/v1/account/sessions/{id}', { params: { path: { id: idHash } } })),
    onSuccess: () => invalidate(keys.activeSessions())
  })
}

export function useSmbSettings() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: async ({
      currentPassword,
      optOut,
      enabled
    }: {
      currentPassword: string
      optOut: boolean
      enabled: boolean
    }) => {
      await unwrap(client.POST('/api/v1/account/smb', { body: { current: currentPassword, opt_out: optOut, enabled } }))
    },
    onSuccess: () => invalidate(keys.session())
  })
}

/** Setting a separate SMB password also turns the account's own SMB switches
 *  back on, since a credential nobody publishes is not what was asked for. */
export function useSetSmbPassword() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: async ({ currentPassword, smbPassword }: { currentPassword: string; smbPassword: string }) => {
      await unwrap(
        client.POST('/api/v1/account/smb/password', { body: { current: currentPassword, new: smbPassword } })
      )
    },
    onSuccess: () => invalidate(keys.session())
  })
}

/** Resolves true when the account password serves SMB again, false when
 *  clearing the separate one left the account with no SMB access. */
export function useClearSmbPassword() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: async (currentPassword: string) =>
      (await unwrap(client.DELETE('/api/v1/account/smb/password', { body: { current: currentPassword } }))).revertible,
    onSuccess: () => invalidate(keys.session())
  })
}

/** Resolves to the provider URL the caller sends the browser to. */
export function useOidcLinkStart() {
  return useMutation({
    mutationFn: async ({ password, returnTo }: { password: string; returnTo?: string }) => {
      const body = { current: password, return_to: returnTo }
      const url = (await unwrap(client.POST('/api/v1/account/oidc-link/start', { body }))).authorize_url
      if (!isProviderUrl(url)) throw malformed(200, 'a provider URL that is not HTTPS')
      return url
    }
  })
}

/** The password re-derives the SMB credential the link had removed. */
export function useOidcUnlink() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (current: string) => unwrapEmpty(client.DELETE('/api/v1/account/oidc-link', { body: { current } })),
    onSuccess: () => invalidate(keys.session())
  })
}

/** The session echoes the saved order in `roots`, so it is what refreshes. */
export function useSetRootOrder() {
  const invalidate = useInvalidate()
  return useMutation({
    mutationFn: (order: string[]) => unwrapEmpty(client.POST('/api/v1/account/roots/order', { body: { order } })),
    onSuccess: () => invalidate(keys.session())
  })
}

/** Resolves to the provider's end-session URL when there is one. */
export function useLogout() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: logout,
    onSettled: () => {
      // The upload worker stops first so no queued command goes out under
      // the next account's credentials.
      resetUploadQueue()
      lock()
      queryClient.clear()
      invalidateEncryptedShares()
    }
  })
}
