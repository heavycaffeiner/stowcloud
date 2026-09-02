// web/src/lib/api/client.ts — the ONE flip switch between the mock backend
// and the real server. `VITE_API_MOCK=1` lives in `.env.development`,
// which `npm run dev` applies and `npm run build` does not. Nothing else in
// the app imports mock.ts or http.ts directly.
import { httpApi } from './http'
import { mockApi } from './mock'

export const isMock = import.meta.env.VITE_API_MOCK === '1'

// A production build is what gets baked into the server binary by
// the embedded bundle. A mock one there is not a degraded product, it is a
// counterfeit: it accepts invented credentials and lists files that do not
// exist, and the real account is rejected by a server that never heard of it.
// It shipped exactly once, which is why this is a hard failure at module load
// rather than a warning nobody reads in a build log.
if (import.meta.env.PROD && isMock) {
  throw new Error(
    'VITE_API_MOCK=1 in a production build. The mock backend must never be embedded ' +
      'in the server binary — move the flag to web/.env.development.'
  )
}

export const api = isMock ? mockApi : httpApi

export type {
  SMBOutcome,
  SmbApplyReport,
  Entry,
  Perms,
  SessionInfo,
  SessionOidc,
  OidcConfig,
  BatchResult,
  BatchItemResult,
  MoveReq,
  MovePreflight,
  OnConflict,
  Order,
  SortKey,
  AuthUser,
  LoginResult,
  UserInfo,
  AppPasswordInfo,
  ActiveSession,
  StorageReport,
  StorageShareUsage,
  IndexEstimate,
  IndexSettings,
  JobStatus,
  JobState,
  JobKindWire,
  JobListResponse,
  AdminUser,
  AdminUserOidc,
  AdminOidcUnlinkResult,
  ReadFileResponse,
  TrashEntry,
  ShareLinkInfo,
  ShareLinkCreateReq,
  ShareLinkPatchReq,
  PermsReq,
  AdminShare,
  CreateShareReq,
  UpdateShareReq,
  AdminGrant,
  GrantPermName,
  GrantPrincipal,
  CreateGrantReq,
  UpdateGrantReq,
  AdminGroup,
  CreateGroupReq,
  UpdateGroupReq,
  AuditRow,
  AuditQuery,
  AuditPage,
  UploadSettingsReq,
  UploadSettingsResp,
  SettingsSource,
  SettingsField,
  SettingsSnapshot,
  SystemHealth,
  SystemRestartResult,
  ApplyOutcome,
  SmbSettingsReq,
  SearchSettingsReq,
  ArchiveSettingsReq,
  NetworkSettingsReq,
  OidcSettingsReq,
  DbSettingsReq,
  HomesSettingsReq,
  WatchSettingsReq
} from './types'
export { ALL_GRANT_PERMS } from './types'
export { ApiError } from './types'
export type { ListOpts, SearchHit } from './mock'
