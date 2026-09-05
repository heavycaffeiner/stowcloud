// Every query key in the app, in one place.
//
// Keys are hierarchical so a write invalidates a prefix instead of naming each
// query it touched: `keys.dir(path)` covers that directory's listing, its
// `stat` and its measured size, and `keys.admin()` covers every admin screen.
import type { AdminLogQuery, AdminLogsTimelineQuery, AuditQuery, Order, SortKey } from '../api/client'

export interface Sort {
  readonly key: SortKey
  readonly order: Order
}

/** Which grants a grant list is scoped to; `{}` means every grant. */
export interface GrantScope {
  readonly userId?: number
  readonly groupId?: number
  readonly share?: number
}

export const keys = {
  // ── session and account ──
  session: () => ['session'] as const,
  setupRequired: () => ['setup-required'] as const,
  oidcConfig: () => ['oidc-config'] as const,
  appPasswords: () => ['app-passwords'] as const,
  activeSessions: () => ['active-sessions'] as const,
  recoveryCodes: () => ['recovery-codes'] as const,

  // ── files ──
  //
  // One namespace for everything read about a path, whether it is a folder or
  // a file. A change reported for a folder invalidates that prefix and, by
  // predicate, every path under it: the split into "directory reads" and "file
  // reads" only made it possible for a file's own `stat` to survive a change
  // to the folder holding it.
  path: (path: string) => ['path', path] as const,
  pathList: (path: string, sort: Sort) => ['path', path, 'list', sort.key, sort.order] as const,
  pathStat: (path: string) => ['path', path, 'stat'] as const,
  pathSize: (path: string) => ['path', path, 'size'] as const,
  pathContent: (path: string) => ['path', path, 'content'] as const,
  pathArchive: (path: string) => ['path', path, 'archive'] as const,
  recent: () => ['recent'] as const,
  trash: () => ['trash'] as const,
  shareLinks: (path?: string) => ['share-links', path ?? null] as const,

  // ── jobs ──
  jobs: () => ['jobs'] as const,
  job: (id: string) => ['jobs', id] as const,

  // ── admin ──
  admin: () => ['admin'] as const,
  adminUsers: () => ['admin', 'users'] as const,
  adminUserOidc: (id: number) => ['admin', 'users', id, 'oidc'] as const,
  adminGroups: () => ['admin', 'groups'] as const,
  adminShares: () => ['admin', 'shares'] as const,
  adminGrants: (scope: GrantScope = {}) => ['admin', 'grants', scope] as const,
  adminSettings: () => ['admin', 'settings'] as const,
  adminStorage: () => ['admin', 'storage'] as const,
  adminIndexEstimate: () => ['admin', 'index-estimate'] as const,
  adminLogs: (query: AdminLogQuery) => ['admin', 'logs', query] as const,
  adminAudit: (query: AuditQuery) => ['admin', 'audit', query] as const,
  adminTimeline: (query: AdminLogsTimelineQuery) => ['admin', 'timeline', query] as const,
  systemHealth: () => ['system-health'] as const
} as const
