// Administrator screens: accounts, groups, grants, shares, server settings,
// storage, the search index, the server log and the audit trail.
import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { useCallback } from 'react'
import { client, decimal, errorFrom, malformed, oneOf, settle, unwrap, unwrapEmpty } from '../../api/fetcher'
import type { components } from '../../api/generated/schema'
import {
  PAGE_SIZE,
  pureBucketNs,
  pureLocalToNs,
  pureToAuditQuery,
  pureToQuery,
  type LogFilters
} from './logic/log-view'
import { invalidateEncryptedShares } from '../../lib/crypto/encrypted-shares'
import { keys, type GrantScope } from '../../lib/query/keys'
import { jobFromWire } from '../jobs/api'

type Schemas = components['schemas']

export interface AdminUser {
  id: number
  name: string
  display_name: string
  is_admin: boolean
  disabled: boolean
  totp_enabled: boolean
  smb_enabled: boolean
  created_ns: string
  /** Bytes as a decimal string; null is unlimited. */
  quota_bytes: string | null
  /** The charged ledger, not a recount of the disk. */
  usage_bytes: string
}

/** The full subject, unlike the session's hint: an administrator comparing it
 *  against the provider needs the exact string. */
export interface AdminUserOidc {
  linked: boolean
  issuer: string | null
  subject: string | null
  linked_ns: string | null
  last_login_ns: string | null
}

export interface AdminGroup {
  id: number
  name: string
  members: number[]
}

export type ShareBackend = 'local' | 's3' | 'veracrypt'

export interface AdminShare {
  id: number
  name: string
  /** The host path; empty for every backend but `local`. */
  host: string
  /** Fixed at creation: grants and links reference data the backend holds. */
  backend: ShareBackend
  /** A redacted description of where the data lives. Never a credential. */
  source: string
  trash_enabled: boolean
  /** Why the share cannot be served right now. */
  broken_reason?: string
  /** Holds nothing, trash included: the one state in which encryption may be
   *  switched. Absent on a write answer. */
  empty?: boolean
}

/** On a patch an absent field keeps the stored value. The secret and the
 *  password are write-only. */
export type ShareS3Config = Schemas['ShareS3Request']
export type ShareVeracryptConfig = Schemas['ShareVeracryptRequest']

export interface CreateShareReq {
  name: string
  /** Absent reads as `local`. */
  backend?: ShareBackend
  host?: string
  s3?: ShareS3Config
  veracrypt?: ShareVeracryptConfig
}

export interface UpdateShareReq {
  name?: string
  host?: string
  trash_enabled?: boolean
  s3?: ShareS3Config
  veracrypt?: ShareVeracryptConfig
}

export type GrantPermName = 'read' | 'write' | 'create' | 'delete' | 'rename' | 'move' | 'share' | 'download'

export const ALL_GRANT_PERMS: readonly GrantPermName[] = [
  'read',
  'write',
  'create',
  'delete',
  'rename',
  'move',
  'share',
  'download'
]

export interface GrantPrincipal {
  kind: 'user' | 'group'
  id: number
}

/** A same-depth `deny` beats an `allow`. */
export interface AdminGrant {
  id: number
  principal: GrantPrincipal
  share: number
  /** Share-relative; empty is the share's root. */
  subpath: string
  allow: GrantPermName[]
  deny: GrantPermName[]
  inherit: boolean
  label: string | null
  created_ns: string
}

export interface CreateGrantReq {
  principal: GrantPrincipal
  share: number
  subpath: string
  allow: GrantPermName[]
  deny: GrantPermName[]
  inherit: boolean
  /** Empty takes the share's name when the subpath is empty too. */
  label?: string
}

/** The server replaces all four on every update; an empty label clears it. */
export interface GrantUpdate {
  allow: GrantPermName[]
  deny: GrantPermName[]
  inherit: boolean
  label: string
}

export interface AuditRow {
  rowid: number
  ts_ns: string
  /** Null for an event no account caused. */
  actor: number | null
  /** Null for an account since deleted, too. */
  actor_name: string | null
  event: string
  target: string | null
  ip: string | null
  ok: boolean
  detail: string | null
}

/** The audit route pages by row id and filters on nothing else this screen
 *  sends, so its rows do not follow the time window. */
export interface AuditQuery {
  before?: number
  limit?: number
}

/** `next` is present only when the page came back full. */
export interface AuditPage {
  rows: AuditRow[]
  next: number | null
}

export interface AdminLogRecord {
  ts_ns: string
  /** Left a plain string so a level this build does not know still shows. */
  level: string
  msg: string
  subsystem: string
  request_id: string
  attrs: Record<string, string>
}

/** In severity order, which is the order the filter renders them in. */
export const ALL_LOG_LEVELS = ['DEBUG', 'INFO', 'WARN', 'ERROR'] as const

/** Nanosecond bounds are decimal strings so a bound never rounds. An empty
 *  `levels` is every level. `cursor` is opaque. */
export interface AdminLogQuery {
  since?: string
  until?: string
  levels?: string[]
  text?: string
  subsystem?: string
  request_id?: string
  limit?: number
  cursor?: string
}

/** `cursor` is empty once the walk is exhausted. */
export interface AdminLogPage {
  records: AdminLogRecord[]
  cursor: string
  stored_bytes: string
  segments: number
}

export interface AdminLogsTimelineQuery {
  since?: string
  until?: string
  levels?: string[]
  text?: string
  subsystem?: string
  request_id?: string
  bucket_ns?: string
}

/** `server` is keyed by level and `audit` by `ok` or `failed`; a key with no
 *  events is absent rather than zero. */
export interface AdminLogsTimelineBucket {
  start_ns: string
  server: Record<string, number>
  audit: Record<string, number>
}

/** Buckets are oldest first with no gaps. `truncated` means the walk hit its
 *  deadline and the buckets cover a prefix of the window. */
export interface AdminLogsTimeline {
  bucket_ns: string
  buckets: AdminLogsTimelineBucket[]
  truncated: boolean
}

/** The bound is the server's, sent with the field, so the client never holds
 *  a copy that can disagree with it. */
export interface SettingRange {
  kind: string
  min?: number
  max?: number
  choices: string[]
}

export interface SettingsField {
  key: string
  value: unknown
  range: SettingRange
  source: string
  /** A property of the field, not a statement about this value. */
  restart_required: boolean
  /** A catalogue key for what leaving the field empty does, where empty is a
   *  setting rather than a gap. */
  empty_means_key?: string
}

/** Where this very request arrived from, judged against the trusted proxies.
 *  A forwarding header seen from an untrusted peer is the misconfiguration
 *  this surfaces. */
export type Hop = Schemas['HopView']

/** What the SMB agent did with the last render, on the machine smbd runs on.
 *  `key` is a catalogue key; `detail` is a tool's own diagnostic, shown as is. */
export interface SmbAgentReport {
  key: string
  ok: boolean
  shares: string[]
  interfaces: string
  hosts_allow: string
  smbd: string
  missing_paths: string[]
  missing_passdb: string[]
  detail?: string
}

export interface SettingsSnapshot {
  fields: SettingsField[]
  hop: Hop
  smb_agent?: SmbAgentReport
}

/** `reason` is a catalogue key and `args` its placeholders. A blocking
 *  finding refuses the save. */
export type SettingsFinding = Schemas['FindingView']

/** Stored, applied and restart-required are three separate facts. The counts
 *  are what a restart would interrupt, present only when one is required. */
export interface ApplyOutcome {
  stored: boolean
  applied: boolean
  restart_required: boolean
  active_uploads?: number
  active_jobs?: number
  findings: SettingsFinding[]
}

/** One settings group's save. The body's keys are the group's field names. */
export interface SettingsPatch {
  section: string
  req: Record<string, unknown>
}

export interface UploadSettingsReq {
  chunk_min: number
  chunk_default: number
  /** Omitted keeps the stored value. */
  cache_enabled?: boolean
}

export interface SystemHealth {
  status: 'ok' | 'degraded' | 'failing'
  reasons: string[]
}

export interface StorageReport {
  db_bytes: number
  shares: { label: string; free_bytes: number; total_bytes: number }[]
}

export interface IndexEstimate {
  files: number
  index_bytes: number
  /** Processor time; a real build runs only while the server is idle. */
  build_secs: number
  /** `measured` or `modelled`. */
  confidence: string
}

/** `incomplete` means the index covers less than the corpus, so every search
 *  still walks. */
export interface IndexStatus {
  enabled: boolean
  entries: number
  incomplete: boolean
}

/** `path` comes from the server rather than being joined here, so a name
 *  holding a slash cannot become a path that does not exist. */
export type HostEntry = Schemas['HostEntryView']

/** An empty `path` lists the places the server can open at all. */
export interface HostListing {
  path: string
  parent: string
  entries: HostEntry[]
  truncated: boolean
}

export interface OidcEndpoints {
  redirect_uris: string[]
  post_logout_redirect_uris: string[]
}

const SHARE_BACKENDS: readonly ShareBackend[] = ['local', 's3', 'veracrypt']
const HEALTH_STATES: readonly SystemHealth['status'][] = ['ok', 'degraded', 'failing']

function userFromWire(w: Schemas['UserView']): AdminUser {
  return {
    id: decimal(w.id, 'user id'),
    name: w.login,
    display_name: w.display ?? '',
    is_admin: w.admin,
    disabled: w.disabled,
    totp_enabled: w.totp,
    smb_enabled: w.smb,
    created_ns: w.created_ns,
    quota_bytes: w.quota_bytes ?? null,
    usage_bytes: w.usage_bytes
  }
}

function userOidcFromWire(w: Schemas['OIDCLinkView']): AdminUserOidc {
  return {
    linked: w.linked,
    issuer: w.issuer ?? null,
    subject: w.subject ?? null,
    linked_ns: w.linked_ns ?? null,
    last_login_ns: w.last_login_ns ?? null
  }
}

function groupFromWire(w: Schemas['GroupView']): AdminGroup {
  return {
    id: decimal(w.id, 'group id'),
    name: w.name,
    members: (w.members ?? []).map((member) => decimal(member, 'group member id'))
  }
}

function shareFromWire(w: Schemas['ShareView']): AdminShare {
  return {
    id: decimal(w.id, 'share id'),
    name: w.name,
    host: w.host,
    backend: oneOf(w.backend, SHARE_BACKENDS, 'share backend'),
    source: w.source,
    trash_enabled: w.trash,
    broken_reason: w.broken,
    empty: w.empty
  }
}

/** An unknown permission is refused rather than dropped: an edit replaces the
 *  whole set, so a dropped bit would be deleted on the next save. */
function permsFromWire(names: string[] | null): GrantPermName[] {
  return (names ?? []).map((name) => oneOf(name, ALL_GRANT_PERMS, 'grant permission'))
}

function grantFromWire(w: Schemas['GrantView']): AdminGrant {
  return {
    id: decimal(w.id, 'grant id'),
    principal:
      w.group !== undefined
        ? { kind: 'group', id: decimal(w.group, 'grant group id') }
        : { kind: 'user', id: decimal(w.user, 'grant user id') },
    share: decimal(w.share, 'grant share id'),
    subpath: w.subpath ?? '',
    allow: permsFromWire(w.allow),
    deny: permsFromWire(w.deny),
    inherit: w.inherit,
    label: w.label ?? null,
    created_ns: w.created_ns
  }
}

function auditFromWire(w: Schemas['AuditPageView']): AuditPage {
  return {
    rows: (w.rows ?? []).map((row) => ({
      rowid: decimal(row.id, 'audit row id'),
      ts_ns: row.ts_ns,
      actor: row.actor !== undefined ? decimal(row.actor, 'audit actor id') : null,
      actor_name: row.actor_name ?? null,
      event: row.event,
      target: row.target ?? null,
      ip: row.ip ?? null,
      ok: row.ok,
      detail: row.detail ?? null
    })),
    next: w.next !== undefined ? decimal(w.next, 'audit next row id') : null
  }
}

function logPageFromWire(w: Schemas['LogPageView']): AdminLogPage {
  return {
    records: (w.records ?? []).map((r) => ({
      ts_ns: r.ts_ns,
      level: r.level,
      msg: r.msg,
      subsystem: r.subsystem,
      request_id: r.request_id,
      attrs: r.attrs ?? {}
    })),
    cursor: w.cursor,
    stored_bytes: w.stored_bytes,
    segments: w.segments
  }
}

function timelineFromWire(w: Schemas['LogsTimelineView']): AdminLogsTimeline {
  return {
    bucket_ns: w.bucket_ns,
    buckets: (w.buckets ?? []).map((b) => ({ start_ns: b.start_ns, server: b.server ?? {}, audit: b.audit ?? {} })),
    truncated: w.truncated
  }
}

function settingsFromWire(w: Schemas['SettingsView']): SettingsSnapshot {
  const agent = w.smb_agent
  return {
    fields: (w.fields ?? []).map((f) => ({
      key: f.key,
      value: f.value,
      range: { kind: f.range.kind, min: f.range.min, max: f.range.max, choices: f.range.choices ?? [] },
      source: f.source,
      restart_required: f.restart_required,
      empty_means_key: f.empty_means_key
    })),
    hop: w.hop,
    smb_agent: agent && {
      key: agent.key,
      ok: agent.ok,
      shares: agent.shares ?? [],
      interfaces: agent.interfaces,
      hosts_allow: agent.hosts_allow,
      smbd: agent.smbd,
      missing_paths: agent.missing_paths ?? [],
      missing_passdb: agent.missing_passdb ?? [],
      detail: agent.detail
    }
  }
}

function outcomeFromWire(w: Schemas['ApplyOutcomeView']): ApplyOutcome {
  return {
    stored: w.stored,
    applied: w.applied,
    restart_required: w.restart_required,
    active_uploads: w.active_uploads,
    active_jobs: w.active_jobs,
    findings: w.findings ?? []
  }
}

function listingFromWire(w: Schemas['HostListingView']): HostListing {
  return { path: w.path, parent: w.parent, entries: w.entries ?? [], truncated: w.truncated }
}

function numberParam(value: number | undefined): string | undefined {
  return value === undefined ? undefined : String(value)
}

/** Shared by the log list and the timeline. Levels cross as one
 *  comma-separated parameter, omitted when none is picked. */
function logFilterParams(query: AdminLogQuery | AdminLogsTimelineQuery) {
  return {
    since: query.since,
    until: query.until,
    level: query.levels?.length ? query.levels.join(',') : undefined,
    text: query.text,
    subsystem: query.subsystem,
    request_id: query.request_id
  }
}

async function listLogs(query: AdminLogQuery, signal: AbortSignal): Promise<AdminLogPage> {
  const params = { query: { ...logFilterParams(query), limit: numberParam(query.limit), cursor: query.cursor } }
  return logPageFromWire(await unwrap(client.GET('/api/v1/admin/logs', { params, signal })))
}

async function listAudit(query: AuditQuery, signal: AbortSignal): Promise<AuditPage> {
  const params = { query: { before: numberParam(query.before), limit: numberParam(query.limit) } }
  return auditFromWire(await unwrap(client.GET('/api/v1/admin/audit', { params, signal })))
}

async function logsTimeline(query: AdminLogsTimelineQuery, signal: AbortSignal): Promise<AdminLogsTimeline> {
  const params = { query: { ...logFilterParams(query), bucket_ns: query.bucket_ns } }
  return timelineFromWire(await unwrap(client.GET('/api/v1/admin/logs/timeline', { params, signal })))
}

/** A blocking finding answers 422 with the outcome as its body, which is
 *  returned as data so the screen can say which field refused. */
async function saveSettings({ section, req }: SettingsPatch): Promise<ApplyOutcome> {
  const { data, error, response } = await settle(
    client.PATCH('/api/v1/admin/settings/{section}', { params: { path: { section } }, body: req })
  )
  if (
    response.status === 422 &&
    error !== null &&
    typeof error === 'object' &&
    'findings' in error &&
    Array.isArray(error.findings)
  )
    return outcomeFromWire(error as Schemas['ApplyOutcomeView'])
  if (!response.ok) throw errorFrom(response, error)
  if (data === undefined) throw malformed(response.status, 'an empty body')
  return outcomeFromWire(data)
}

/** A 503 still answers with a status, so any readable body counts. Anything
 *  else rejects, and the restart wait reads a rejection as the outage: a
 *  proxy's HTML page while the process is down must not pass as health. */
async function probeHealth(): Promise<SystemHealth> {
  const { data, error, response } = await settle(client.GET('/api/v1/system/health'))
  const body: unknown = data ?? error
  if (body === null || typeof body !== 'object' || !('status' in body) || typeof body.status !== 'string')
    throw malformed(response.status, 'a health answer that could not be read')
  const reasons = 'reasons' in body && Array.isArray(body.reasons) ? body.reasons : []
  return {
    status: oneOf(body.status, HEALTH_STATES, 'health status', 'failing'),
    reasons: reasons.filter((reason): reason is string => typeof reason === 'string')
  }
}

/** The setup token stands in for a session during first-run setup and is
 *  checked without being spent. */
async function browseHost(token: string | null, path: string): Promise<HostListing> {
  const listing = token
    ? await unwrap(client.POST('/api/v1/system/setup/browse', { body: { token, path } }))
    : await unwrap(client.GET('/api/v1/admin/fs', { params: { query: { path: path || undefined } } }))
  return listingFromWire(listing)
}

function id(value: number): { id: string } {
  return { id: String(value) }
}

/** A write that marks `stale` for refetch. `settled` refetches after a refusal
 *  too, for controls that render server state and must snap back. */
function useWrite<V, R>(mutationFn: (vars: V) => Promise<R>, stale: readonly QueryKey[], settled = false) {
  const queryClient = useQueryClient()
  const refresh = () => {
    for (const queryKey of stale) void queryClient.invalidateQueries({ queryKey })
  }
  return useMutation(settled ? { mutationFn, onSettled: refresh } : { mutationFn, onSuccess: refresh })
}

// A refused account write leaves the server holding the old value, so these
// refetch on failure too: the last-administrator refusal is that case.

export function useAdminUsers(enabled = true) {
  return useQuery({
    queryKey: keys.adminUsers(),
    queryFn: async () => ((await unwrap(client.GET('/api/v1/admin/users'))) ?? []).map(userFromWire),
    enabled
  })
}

export function useCreateUser() {
  return useWrite(
    async ({ name, password }: { name: string; password: string }) =>
      userFromWire(await unwrap(client.POST('/api/v1/admin/users', { body: { login: name, password } }))),
    [keys.adminUsers()],
    true
  )
}

function patchUser(userId: number, body: Schemas['AdminUpdateUserRequest']): Promise<AdminUser> {
  return unwrap(client.PATCH('/api/v1/admin/users/{id}', { params: { path: id(userId) }, body })).then(userFromWire)
}

export function useSetUserDisabled() {
  return useWrite(
    ({ id, disabled }: { id: number; disabled: boolean }) => patchUser(id, { disabled }),
    [keys.adminUsers()],
    true
  )
}

/** `null` removes the quota. The server reads a null quota as "leave it", so
 *  removal is its own flag. */
export function useSetUserQuota() {
  return useWrite(
    ({ id, quotaBytes }: { id: number; quotaBytes: number | null }) =>
      patchUser(id, quotaBytes === null ? { clear_quota: true } : { quota_bytes: quotaBytes }),
    [keys.adminUsers()],
    true
  )
}

/** Ends every session the old password opened. */
export function useSetUserPassword() {
  return useWrite(
    ({ id, password }: { id: number; password: string }) => patchUser(id, { password }),
    [keys.adminUsers()],
    true
  )
}

export function useDeleteUser() {
  return useWrite(
    (userId: number) => unwrapEmpty(client.DELETE('/api/v1/admin/users/{id}', { params: { path: id(userId) } })),
    [keys.adminUsers()],
    true
  )
}

export function useAdminUserOidc(userId: number | null) {
  return useQuery({
    queryKey: keys.adminUserOidc(userId ?? 0),
    queryFn: async () =>
      userOidcFromWire(
        await unwrap(client.GET('/api/v1/admin/users/{id}/oidc', { params: { path: id(userId ?? 0) } }))
      ),
    enabled: userId !== null
  })
}

export function useUnlinkUserOidc() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (userId: number) =>
      unwrapEmpty(client.DELETE('/api/v1/admin/users/{id}/oidc', { params: { path: id(userId) } })),
    onSuccess: (_void, userId) => {
      void queryClient.invalidateQueries({ queryKey: keys.adminUserOidc(userId) })
      void queryClient.invalidateQueries({ queryKey: keys.adminUsers() })
    }
  })
}

// Membership decides what a grant resolves to, so group writes refresh grants.
const GROUP_READS = [keys.adminGroups(), keys.adminGrantsPrefix()]

export function useAdminGroups() {
  return useQuery({
    queryKey: keys.adminGroups(),
    queryFn: async () => ((await unwrap(client.GET('/api/v1/admin/groups'))) ?? []).map(groupFromWire)
  })
}

export function useCreateGroup() {
  return useWrite(
    async (name: string) => groupFromWire(await unwrap(client.POST('/api/v1/admin/groups', { body: { name } }))),
    GROUP_READS
  )
}

export function useRenameGroup() {
  return useWrite(
    async ({ id: groupId, name }: { id: number; name: string }) =>
      groupFromWire(
        await unwrap(client.PATCH('/api/v1/admin/groups/{id}', { params: { path: id(groupId) }, body: { name } }))
      ),
    GROUP_READS
  )
}

export function useDeleteGroup() {
  return useWrite(
    (groupId: number) => unwrapEmpty(client.DELETE('/api/v1/admin/groups/{id}', { params: { path: id(groupId) } })),
    GROUP_READS
  )
}

export function useAddGroupMember() {
  return useWrite(
    ({ groupId, userId }: { groupId: number; userId: number }) =>
      unwrapEmpty(
        client.POST('/api/v1/admin/groups/{id}/members', {
          params: { path: id(groupId) },
          body: { user: String(userId) }
        })
      ),
    GROUP_READS
  )
}

export function useRemoveGroupMember() {
  return useWrite(
    ({ groupId, userId }: { groupId: number; userId: number }) =>
      unwrapEmpty(
        client.DELETE('/api/v1/admin/groups/{id}/members/{user}', {
          params: { path: { id: String(groupId), user: String(userId) } }
        })
      ),
    GROUP_READS
  )
}

// A share is a root: the session's roots and every listing change with it. A
// refused write refetches too, so no control keeps showing what was proposed.
const SHARE_READS = [keys.adminShares(), keys.session(), keys.paths()]

export function useAdminShares() {
  return useQuery({
    queryKey: keys.adminShares(),
    queryFn: async () => ((await unwrap(client.GET('/api/v1/admin/shares'))) ?? []).map(shareFromWire)
  })
}

export function useCreateShare() {
  return useWrite(
    async (req: CreateShareReq) => shareFromWire(await unwrap(client.POST('/api/v1/admin/shares', { body: req }))),
    SHARE_READS,
    true
  )
}

export function useUpdateShare() {
  return useWrite(
    async ({ id: shareId, patch }: { id: number; patch: UpdateShareReq }) =>
      shareFromWire(
        await unwrap(client.PATCH('/api/v1/admin/shares/{id}', { params: { path: id(shareId) }, body: patch }))
      ),
    SHARE_READS,
    true
  )
}

export function useDeleteShare() {
  return useWrite(
    (shareId: number) => unwrapEmpty(client.DELETE('/api/v1/admin/shares/{id}', { params: { path: id(shareId) } })),
    SHARE_READS,
    true
  )
}

/** Re-opens a share whose disk came back, without retyping its path. */
export function useRetryShare() {
  return useWrite(
    async (shareId: number) =>
      shareFromWire(await unwrap(client.POST('/api/v1/admin/shares/{id}/retry', { params: { path: id(shareId) } }))),
    SHARE_READS,
    true
  )
}

/** Allowed only while the share is empty. The server holds no key. */
export function useEnableShareEncryption() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id: shareId, ...body }: { id: number; scheme: string; salt: string; verifier: string }) =>
      unwrapEmpty(client.POST('/api/v1/encryption/{id}', { params: { path: id(shareId) }, body })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: keys.shareEncryptions() })
      invalidateEncryptedShares()
    }
  })
}

export function useDisableShareEncryption() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (shareId: number) =>
      unwrapEmpty(client.DELETE('/api/v1/encryption/{id}', { params: { path: id(shareId) } })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: keys.shareEncryptions() })
      invalidateEncryptedShares()
    }
  })
}

// A grant decides what the session's roots hold and what every listing shows.
const GRANT_READS = [keys.adminGrantsPrefix(), keys.session(), keys.paths()]

/** `{}` lists every grant. */
export function useAdminGrants(scope: GrantScope = {}) {
  return useQuery({
    queryKey: keys.adminGrants(scope),
    queryFn: async () => {
      const query = {
        user: numberParam(scope.userId),
        group: numberParam(scope.groupId),
        share: numberParam(scope.share)
      }
      return ((await unwrap(client.GET('/api/v1/admin/grants', { params: { query } }))) ?? []).map(grantFromWire)
    }
  })
}

export function useCreateGrant() {
  return useWrite(async ({ principal, share, label, ...rest }: CreateGrantReq) => {
    const body = {
      ...rest,
      user: principal.kind === 'user' ? String(principal.id) : undefined,
      group: principal.kind === 'group' ? String(principal.id) : undefined,
      share: String(share),
      label: label ?? ''
    }
    return grantFromWire(await unwrap(client.POST('/api/v1/admin/grants', { body })))
  }, GRANT_READS)
}

export function useUpdateGrant() {
  return useWrite(
    async ({ id: grantId, update }: { id: number; update: GrantUpdate }) =>
      grantFromWire(
        await unwrap(client.PATCH('/api/v1/admin/grants/{id}', { params: { path: id(grantId) }, body: update }))
      ),
    GRANT_READS
  )
}

export function useDeleteGrant() {
  return useWrite(
    (grantId: number) => unwrapEmpty(client.DELETE('/api/v1/admin/grants/{id}', { params: { path: id(grantId) } })),
    GRANT_READS
  )
}

export function useAdminSettings() {
  return useQuery({
    queryKey: keys.adminSettings(),
    queryFn: async () => settingsFromWire(await unwrap(client.GET('/api/v1/admin/settings')))
  })
}

/** Refetches the settings, for a change the server made on its own, such as
 *  a restart taking the stored values live. */
export function useRefreshAdminSettings(): () => void {
  const queryClient = useQueryClient()
  return useCallback(() => void queryClient.invalidateQueries({ queryKey: keys.adminSettings() }), [queryClient])
}

/** The endpoint strings follow `app_hosts`, which another card edits, so any
 *  settings save refreshes them. They change on nothing else. */
export function useOidcEndpoints() {
  return useQuery({
    queryKey: keys.adminOidcEndpoints(),
    queryFn: async (): Promise<OidcEndpoints> => {
      const w = await unwrap(client.GET('/api/v1/admin/oidc/endpoints'))
      return { redirect_uris: w.redirect_uris ?? [], post_logout_redirect_uris: w.post_logout_redirect_uris ?? [] }
    },
    staleTime: Infinity
  })
}

/** Resolves even when a blocking finding refused the save; callers read
 *  `stored` and `applied`. A refusal refetches so the switches snap back. */
export function useSaveSettings() {
  return useWrite(saveSettings, [keys.adminSettings(), keys.adminOidcEndpoints()], true)
}

/** The upload planner reads its chunk bounds from the session. */
export function useSaveUploadSettings() {
  return useWrite(
    (req: UploadSettingsReq) => unwrap(client.PATCH('/api/v1/admin/settings/upload', { body: req })),
    [keys.adminSettings(), keys.session()]
  )
}

export function useAdminStorage() {
  return useQuery({
    queryKey: keys.adminStorage(),
    queryFn: async (): Promise<StorageReport> => {
      const w = await unwrap(client.GET('/api/v1/admin/storage'))
      return {
        db_bytes: decimal(w.db_bytes, 'database size'),
        shares: (w.shares ?? []).map((s) => ({
          label: s.label,
          free_bytes: decimal(s.free_bytes ?? '0', 'free storage bytes'),
          total_bytes: decimal(s.total_bytes ?? '0', 'total storage bytes')
        }))
      }
    }
  })
}

/** Measures the corpus, so it runs only when asked. */
export function useIndexEstimate(enabled: boolean) {
  return useQuery({
    queryKey: keys.adminIndexEstimate(),
    queryFn: async (): Promise<IndexEstimate> => {
      const w = await unwrap(client.GET('/api/v1/admin/index/estimate'))
      return {
        files: decimal(w.files, 'index file count'),
        index_bytes: decimal(w.index_bytes, 'index size'),
        build_secs: w.build_secs,
        confidence: w.confidence
      }
    },
    enabled
  })
}

/** Not polled: the screen tracking a build refreshes it when the build ends. */
export function useIndexStatus() {
  return useQuery({
    queryKey: keys.adminIndexStatus(),
    queryFn: async (): Promise<IndexStatus> => {
      const w = await unwrap(client.GET('/api/v1/admin/index/status'))
      return { enabled: w.enabled, entries: decimal(w.entries, 'index entry count'), incomplete: w.incomplete }
    }
  })
}

export function useRefreshIndexStatus(): () => void {
  const queryClient = useQueryClient()
  return useCallback(() => void queryClient.invalidateQueries({ queryKey: keys.adminIndexStatus() }), [queryClient])
}

/** The switch lives in the search group under the name the server reads. */
export function useSetNameIndex() {
  return useWrite(
    (enabled: boolean) =>
      unwrap(
        client.PATCH('/api/v1/admin/settings/{section}', {
          params: { path: { section: 'search' } },
          body: { name_index_enabled: enabled }
        })
      ),
    [keys.adminSettings(), keys.adminIndexStatus()],
    true
  )
}

/** A build walks every share, so it always runs as a job. */
export function useBuildIndex() {
  return useWrite(
    async () => jobFromWire(await unwrap(client.POST('/api/v1/admin/index/build'))),
    [keys.jobs(), keys.adminIndexEstimate()]
  )
}

/** Polled only while a restart is expected, so the interval is the caller's. */
export function useSystemHealth(pollMs: number | false) {
  return useQuery({
    queryKey: keys.systemHealth(),
    queryFn: probeHealth,
    refetchInterval: pollMs,
    staleTime: 0,
    retry: false
  })
}

/** Answers before the process goes down, so an answer means the restart is
 *  underway. */
export function useRestartServer() {
  return useMutation({ mutationFn: () => unwrap(client.POST('/api/v1/admin/system/restart')) })
}

/** With a setup token the listing is the first-run one, before any account
 *  exists. */
export function useHostListing(token: string | null, path: string, enabled: boolean) {
  return useQuery({
    queryKey: keys.hostListing(token, path),
    queryFn: () => browseHost(token, path),
    enabled,
    retry: false,
    placeholderData: (previous) => previous
  })
}

/** Keyed on the filters, so a filter change cancels the old request and a late
 *  answer lands under a key nothing observes. */
export function useAdminLogs(filters: LogFilters, enabled: boolean) {
  return useInfiniteQuery({
    queryKey: keys.adminLogs(pureToQuery(filters)),
    queryFn: ({ pageParam, signal }) => listLogs(pureToQuery(filters, pageParam ?? undefined), signal),
    initialPageParam: null as string | null,
    // The cursor is always present, so the end of the walk is a short page.
    getNextPageParam: (last) => (last.records.length < PAGE_SIZE ? null : last.cursor || null),
    enabled,
    staleTime: 10_000
  })
}

export function useAdminAudit(enabled: boolean) {
  return useInfiniteQuery({
    queryKey: keys.adminAudit(pureToAuditQuery()),
    queryFn: ({ pageParam, signal }) => listAudit(pureToAuditQuery(pageParam), signal),
    initialPageParam: null as number | null,
    getNextPageParam: (last) => last.next,
    enabled,
    staleTime: 10_000
  })
}

/** The bucket width follows the window, so the plot always has about as many
 *  bars as it is sized for. */
function timelineQuery(filters: LogFilters): AdminLogsTimelineQuery {
  const since = pureLocalToNs(filters.since)
  const until = pureLocalToNs(filters.until)
  return {
    since,
    until,
    levels: [...filters.levels].sort(),
    text: filters.text.trim() || undefined,
    subsystem: filters.subsystem.trim() || undefined,
    request_id: filters.requestId.trim() || undefined,
    bucket_ns: pureBucketNs(since, until)
  }
}

/** Not retried: the list still renders when the timeline fails. */
export function useAdminTimeline(filters: LogFilters, enabled: boolean) {
  const query = timelineQuery(filters)
  return useQuery({
    queryKey: keys.adminTimeline(query),
    queryFn: ({ signal }) => logsTimeline(query, signal),
    enabled,
    staleTime: 10_000,
    retry: false
  })
}
