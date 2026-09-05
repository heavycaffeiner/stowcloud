// Administrator screens: users, groups, grants, shares, server settings,
// storage and the search index.
import { mutationOptions, queryOptions } from '@tanstack/svelte-query'
import { api } from '../api/client'
import type {
  ApplyOutcome,
  ArchiveSettingsReq,
  CreateGrantReq,
  CreateGroupReq,
  CreateShareReq,
  DbSettingsReq,
  HomesSettingsReq,
  NetworkSettingsReq,
  OidcSettingsReq,
  RateSettingsReq,
  SearchSettingsReq,
  SmbSettingsReq,
  ThumbnailSettingsReq,
  UpdateGrantReq,
  UpdateGroupReq,
  UpdateShareReq,
  UploadSettingsReq,
  WatchSettingsReq
} from '../api/types'
import { queryClient } from './client'
import { keys, type GrantScope } from './keys'

export function adminUsersQuery() {
  return queryOptions({ queryKey: keys.adminUsers(), queryFn: () => api.adminListUsers() })
}

export function adminUserOidcQuery(id: number | null) {
  return queryOptions({
    queryKey: keys.adminUserOidc(id ?? 0),
    queryFn: () => api.adminGetUserOidc(id as number),
    enabled: id !== null
  })
}

export function adminGroupsQuery() {
  return queryOptions({ queryKey: keys.adminGroups(), queryFn: () => api.adminListGroups() })
}

export function adminSharesQuery() {
  return queryOptions({ queryKey: keys.adminShares(), queryFn: () => api.adminListShares() })
}

export function adminGrantsQuery(scope: GrantScope = {}) {
  return queryOptions({ queryKey: keys.adminGrants(scope), queryFn: () => api.adminListGrants(scope) })
}

export function adminSettingsQuery() {
  return queryOptions({ queryKey: keys.adminSettings(), queryFn: () => api.adminGetServerSettings() })
}

export function adminStorageQuery() {
  return queryOptions({ queryKey: keys.adminStorage(), queryFn: () => api.adminStorage() })
}

export function adminIndexEstimateQuery() {
  return queryOptions({ queryKey: keys.adminIndexEstimate(), queryFn: () => api.adminIndexEstimate() })
}

/** Polled only while a restart is expected, which is why the interval is the
 *  caller's to set. A 503 is a valid answer here, not a failure. */
export function systemHealthQuery(pollMs: number | false) {
  return queryOptions({
    queryKey: keys.systemHealth(),
    queryFn: () => api.systemHealth(),
    refetchInterval: pollMs,
    staleTime: 0,
    retry: false
  })
}

function invalidate(key: readonly unknown[]): void {
  void queryClient.invalidateQueries({ queryKey: key })
}

// ── users ──

export function adminUserMutation() {
  return mutationOptions({
    mutationFn: (action: AdminUserAction) => applyUserAction(action),
    onSuccess: () => invalidate(keys.adminUsers())
  })
}

export type AdminUserAction =
  | { kind: 'create'; name: string; password: string }
  | { kind: 'disable'; id: number; disabled: boolean }
  | { kind: 'quota'; id: number; quotaBytes: number | null }
  | { kind: 'password'; id: number; password: string }
  | { kind: 'delete'; id: number }

function applyUserAction(action: AdminUserAction): Promise<unknown> {
  switch (action.kind) {
    case 'create':
      return api.adminCreateUser(action.name, action.password)
    case 'disable':
      return api.adminSetUserDisabled(action.id, action.disabled)
    case 'quota':
      return api.adminSetUserQuota(action.id, action.quotaBytes)
    case 'password':
      return api.adminSetUserPassword(action.id, action.password)
    case 'delete':
      return api.adminDeleteUser(action.id)
  }
}

export function adminUnlinkOidcMutation() {
  return mutationOptions({
    mutationFn: (id: number) => api.adminUnlinkUserOidc(id),
    onSuccess: (_result, id) => {
      invalidate(keys.adminUserOidc(id))
      invalidate(keys.adminUsers())
    }
  })
}

// ── groups ──

export type AdminGroupAction =
  | { kind: 'create'; req: CreateGroupReq }
  | { kind: 'rename'; id: number; patch: UpdateGroupReq }
  | { kind: 'delete'; id: number }
  | { kind: 'add-member'; id: number; userId: number }
  | { kind: 'remove-member'; id: number; userId: number }

function applyGroupAction(action: AdminGroupAction): Promise<unknown> {
  switch (action.kind) {
    case 'create':
      return api.adminCreateGroup(action.req)
    case 'rename':
      return api.adminRenameGroup(action.id, action.patch)
    case 'delete':
      return api.adminDeleteGroup(action.id)
    case 'add-member':
      return api.adminAddGroupMember(action.id, action.userId)
    case 'remove-member':
      return api.adminRemoveGroupMember(action.id, action.userId)
  }
}

export function adminGroupMutation() {
  return mutationOptions({
    mutationFn: (action: AdminGroupAction) => applyGroupAction(action),
    onSuccess: () => {
      invalidate(keys.adminGroups())
      // Group membership decides what a grant resolves to.
      invalidate(keys.adminGrants())
    }
  })
}

// ── shares ──

export type AdminShareAction =
  | { kind: 'create'; req: CreateShareReq }
  | { kind: 'update'; id: number; patch: UpdateShareReq }
  | { kind: 'delete'; id: number }
  | { kind: 'retry'; id: number }

function applyShareAction(action: AdminShareAction): Promise<unknown> {
  switch (action.kind) {
    case 'create':
      return api.adminCreateShare(action.req)
    case 'update':
      return api.adminUpdateShare(action.id, action.patch)
    case 'delete':
      return api.adminDeleteShare(action.id)
    case 'retry':
      return api.adminRetryShare(action.id)
  }
}

export function adminShareMutation() {
  return mutationOptions({
    mutationFn: (action: AdminShareAction) => applyShareAction(action),
    onSuccess: () => {
      invalidate(keys.adminShares())
      // A share is a root: the roots the session reports and every listing
      // built on them change with it.
      invalidate(keys.session())
      invalidate(['dir'])
    }
  })
}

// ── grants ──

export type AdminGrantAction =
  | { kind: 'create'; req: CreateGrantReq }
  | { kind: 'update'; id: number; patch: UpdateGrantReq }
  | { kind: 'delete'; id: number }

function applyGrantAction(action: AdminGrantAction): Promise<unknown> {
  switch (action.kind) {
    case 'create':
      return api.adminCreateGrant(action.req)
    case 'update':
      return api.adminUpdateGrant(action.id, action.patch)
    case 'delete':
      return api.adminDeleteGrant(action.id)
  }
}

export function adminGrantMutation() {
  return mutationOptions({
    mutationFn: (action: AdminGrantAction) => applyGrantAction(action),
    onSuccess: () => {
      invalidate(keys.adminGrants())
      invalidate(keys.session())
      invalidate(['dir'])
    }
  })
}

// ── server settings ──

/**
 * One mutation for all eleven settings groups.
 *
 * They share a route shape, a response shape and one invalidation, so they
 * share a mutation; the section tag is what picks the call. A blocking
 * validation failure comes back as a normal `ApplyOutcome` carrying findings
 * rather than as a rejection, so callers read `stored`/`applied`, not only the
 * absence of an error.
 */
export type SettingsPatch =
  | { section: 'smb'; req: SmbSettingsReq }
  | { section: 'search'; req: SearchSettingsReq }
  | { section: 'archive'; req: ArchiveSettingsReq }
  | { section: 'rate'; req: RateSettingsReq }
  | { section: 'network'; req: NetworkSettingsReq }
  | { section: 'db'; req: DbSettingsReq }
  | { section: 'homes'; req: HomesSettingsReq }
  | { section: 'watch'; req: WatchSettingsReq }
  | { section: 'oidc'; req: OidcSettingsReq }
  | { section: 'thumbnail'; req: ThumbnailSettingsReq }

function writeSettings(patch: SettingsPatch): Promise<ApplyOutcome> {
  switch (patch.section) {
    case 'smb':
      return api.adminSetSmbSettings(patch.req)
    case 'search':
      return api.adminSetSearchSettings(patch.req)
    case 'archive':
      return api.adminSetArchiveSettings(patch.req)
    case 'rate':
      return api.adminSetRateSettings(patch.req)
    case 'network':
      return api.adminSetNetworkSettings(patch.req)
    case 'db':
      return api.adminSetDbSettings(patch.req)
    case 'homes':
      return api.adminSetHomesSettings(patch.req)
    case 'watch':
      return api.adminSetWatchSettings(patch.req)
    case 'oidc':
      return api.adminSetOidcSettings(patch.req)
    case 'thumbnail':
      return api.adminSetThumbnailSettings(patch.req)
  }
}

export function adminSettingsMutation() {
  return mutationOptions({
    mutationFn: (patch: SettingsPatch) => writeSettings(patch),
    onSuccess: () => invalidate(keys.adminSettings())
  })
}

export function adminUploadSettingsMutation() {
  return mutationOptions({
    mutationFn: (req: UploadSettingsReq) => api.adminSetUploadSettings(req),
    onSuccess: () => {
      invalidate(keys.adminSettings())
      // The upload planner reads its floor and default from the session.
      invalidate(keys.session())
    }
  })
}

export function adminIndexSettingsMutation() {
  return mutationOptions({
    mutationFn: (nameEnabled: boolean) => api.adminSetIndexSettings(nameEnabled),
    onSuccess: () => invalidate(keys.adminSettings())
  })
}

export function adminBuildIndexMutation() {
  return mutationOptions({
    mutationFn: () => api.adminBuildIndex(),
    onSuccess: () => {
      invalidate(keys.jobs())
      invalidate(keys.adminIndexEstimate())
    }
  })
}

export function adminRestartMutation() {
  return mutationOptions({ mutationFn: () => api.adminSystemRestart() })
}
