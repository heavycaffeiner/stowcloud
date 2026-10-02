// Public share links: the owner's management of them, and the anonymous
// visitor's side under `/s/{token}`. The visitor's calls carry no session and
// import nothing from the signed-in app.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { client, decimal, errorFrom, malformed, oneOf, send, serverRoot, unwrap, unwrapEmpty } from '../../api/fetcher'
import type { components } from '../../api/generated/schema'
import { keys } from '../../api/query-keys'
import { permNamesOf, permsFromNames, type Perms } from '../files/perms'

type Schemas = components['schemas']

/** A link request grants only the bits it names; the rest default to false. */
export type PermsReq = Partial<Perms>

/** One link as its owner sees it. `token` and `url` exist only on the answer
 *  to the request that created it: the server keeps no plaintext token. */
export interface ShareLinkInfo {
  id: number
  path: string
  perms: Perms
  expires_ns: string | null
  max_downloads: number | null
  downloads: number
  label: string | null
  has_password: boolean
  created_ns: string
  token?: string
  url?: string
}

/** A link as the administrative overview lists it. `owner_name` is empty for
 *  an account since deleted. */
export interface OwnedShareLinkInfo extends ShareLinkInfo {
  owner: number
  owner_name: string
}

export interface ShareLinkCreateReq {
  path: string
  perms?: PermsReq
  password?: string
  /** Nanoseconds as a string. */
  expires_ns?: string
  max_downloads?: number
  label?: string
}

/** An absent field is left alone and `null` clears it. `label` clears with an
 *  empty string: the server reads a null label as "leave it". */
export interface ShareLinkPatchReq {
  perms?: PermsReq
  password?: string | null
  expires_ns?: string | null
  max_downloads?: number | null
  label?: string
}

/** Where a link's public page lives. The token is the whole address, so it is
 *  built here rather than sent. */
function shareLinkURL(token: string): string {
  return `${serverRoot || location.origin}/s/${encodeURIComponent(token)}`
}

function linkFromWire(w: Schemas['LinkView'], token?: string): ShareLinkInfo {
  return {
    id: decimal(w.id, 'share link id'),
    path: w.path,
    perms: permsFromNames(w.perms),
    // Absent means never. Zero would be an instant in 1970 and read as expired.
    expires_ns: w.expires_ns ?? null,
    max_downloads: w.max_downloads === undefined ? null : decimal(w.max_downloads, 'share link download limit'),
    downloads: decimal(w.downloads, 'share link downloads'),
    label: w.label ?? null,
    has_password: w.has_password,
    created_ns: w.created_ns,
    token,
    url: token ? shareLinkURL(token) : undefined
  }
}

function ownedLinkFromWire(w: Schemas['OwnedLinkView']): OwnedShareLinkInfo {
  return { ...linkFromWire(w), owner: decimal(w.owner, 'owned link owner id'), owner_name: w.owner_name ?? '' }
}

async function listLinks(path?: string): Promise<ShareLinkInfo[]> {
  const rows = await unwrap(client.GET('/api/v1/links', { params: { query: { path } } }))
  return (rows ?? []).map((row) => linkFromWire(row))
}

/** Every link on the deployment, whoever owns it. Admin only. */
async function listAllLinks(): Promise<OwnedShareLinkInfo[]> {
  const rows = await unwrap(client.GET('/api/v1/admin/links'))
  return (rows ?? []).map(ownedLinkFromWire)
}

async function createLink(req: ShareLinkCreateReq): Promise<ShareLinkInfo> {
  // A drop link is created without read, which is what lets somebody put a
  // file in without seeing what is already there.
  const body = { ...req, perms: req.perms ? permNamesOf(req.perms) : undefined }
  const out = await unwrap(client.POST('/api/v1/links', { body }))
  return linkFromWire(out.link, out.token)
}

async function updateLink(id: number, patch: ShareLinkPatchReq): Promise<ShareLinkInfo> {
  const body = { ...patch, perms: patch.perms ? permNamesOf(patch.perms) : undefined }
  return linkFromWire(await unwrap(client.PATCH('/api/v1/links/{id}', { params: { path: { id: String(id) } }, body })))
}

async function deleteLink(id: number): Promise<void> {
  await unwrapEmpty(client.DELETE('/api/v1/links/{id}', { params: { path: { id: String(id) } } }))
}

export function useShareLinks(path: string | undefined, enabled = true) {
  return useQuery({ queryKey: keys.shareLinks(path), queryFn: () => listLinks(path), enabled })
}

export function useAllShareLinks(enabled = true) {
  return useQuery({ queryKey: keys.adminLinks(), queryFn: listAllLinks, enabled })
}

/** Both listings: a link appears in the owner's own and in the overview, and
 *  revoking from one screen must not leave the other showing it. */
function useInvalidateLinks(): () => void {
  const queryClient = useQueryClient()
  return () => {
    void queryClient.invalidateQueries({ queryKey: ['share-links'] })
    void queryClient.invalidateQueries({ queryKey: keys.adminLinks() })
  }
}

export function useCreateShareLink() {
  const invalidate = useInvalidateLinks()
  return useMutation({ mutationFn: createLink, onSuccess: invalidate })
}

export function useUpdateShareLink() {
  const invalidate = useInvalidateLinks()
  return useMutation({
    mutationFn: ({ id, patch }: { id: number; patch: ShareLinkPatchReq }) => updateLink(id, patch),
    onSuccess: invalidate
  })
}

export function useDeleteShareLink() {
  const invalidate = useInvalidateLinks()
  return useMutation({ mutationFn: deleteLink, onSuccess: invalidate })
}

export interface ShareEntry {
  name: string
  kind: 'file' | 'dir'
  size: number
}

export interface ShareInfo {
  /** The shared file or folder's own name. */
  name: string
  isDir: boolean
  size: number
  label: string | null
  /** A file-drop link: uploads only, never lists or serves its contents. */
  isDrop: boolean
  /** The largest single upload the server accepts; null except on a drop link. */
  maxUploadBytes: number | null
  canDownload: boolean
  /** Null for a file, a drop link, or a folder still locked by its password. */
  entries: ShareEntry[] | null
  /** The subpath the server resolved, empty at the link's root, so the page
   *  can tell its request was honoured rather than widened. */
  path: string
}

/** A subpath the server refused, so the page clears back to the link's root. */
export class SharePathGoneError extends Error {}

export class ShareNotFoundError extends Error {}

/** A drop upload refused for size, after the operator lowered the limit
 *  since the page loaded. */
export class ShareTooLargeError extends Error {}

/** The link exists but needs `unlockShare` first. */
export class SharePasswordRequiredError extends Error {}

/** An unlock refused for a reason other than the password, which the page
 *  must not report as a wrong one. */
export class ShareUnlockFailedError extends Error {}

const ENTRY_KINDS: readonly ShareEntry['kind'][] = ['file', 'dir']

/** `/s/{token}` is both the page a visitor opens and this read; the Accept
 *  header tells the two apart. */
async function getShare(token: string, path: string): Promise<ShareInfo> {
  const { data, response } = await client.GET('/s/{token}', {
    params: { path: { token }, query: { path: path || undefined } },
    headers: { Accept: 'application/json' }
  })
  if (response.status === 404 && path !== '') throw new SharePathGoneError(path)
  if (response.status === 404 || response.status === 410) throw new ShareNotFoundError(token)
  if (!response.ok || data === undefined) throw new Error(`share lookup failed: ${response.status}`)
  // A locked link answers with nothing but `protected`.
  if (data.protected && data.name === undefined) throw new SharePasswordRequiredError(token)
  return {
    name: data.name ?? '',
    isDir: data.is_dir ?? false,
    size: data.size ?? 0,
    label: data.label ?? null,
    isDrop: data.drop ?? false,
    maxUploadBytes: data.max_upload_bytes ?? null,
    canDownload: data.can_download ?? false,
    entries: data.entries
      ? data.entries.map((e) => ({ name: e.name, kind: oneOf(e.kind, ENTRY_KINDS, 'share entry kind'), size: e.size }))
      : null,
    path: data.path ?? ''
  }
}

export function usePublicShare(token: string, path: string) {
  return useQuery({
    queryKey: ['share', token, path],
    queryFn: () => getShare(token, path),
    retry: false,
    enabled: token.length > 0
  })
}

/** On success the server sets a cookie scoped to the link, so the next read
 *  sees through the lock. Resolves false for a wrong password and throws for
 *  any other refusal. */
export async function unlockShare(token: string, password: string): Promise<boolean> {
  const { error, response } = await client.POST('/s/{token}/auth', {
    params: { path: { token } },
    body: { password }
  })
  if (response.ok) return true
  if (response.status === 404 || response.status === 410) throw new ShareNotFoundError(token)
  if (response.status === 422 && errorFrom(response, error).detail?.reason_key === 'fs.link_password') return false
  throw new ShareUnlockFailedError(token)
}

/** `?path=`, or nothing at the link's own root. */
function shareQuery(path: string): string {
  return path ? `?path=${encodeURIComponent(path)}` : ''
}

/** One file under the link, as a plain navigation: fetching it first would
 *  spend the download cap on a body thrown away. */
export function shareDownloadUrl(token: string, path = ''): string {
  return `${serverRoot}/s/${encodeURIComponent(token)}/download${shareQuery(path)}`
}

/** One folder under the link, streamed as a ZIP. */
export function shareZipUrl(token: string, path = ''): string {
  return `${serverRoot}/s/${encodeURIComponent(token)}/zip${shareQuery(path)}`
}

/** Uploads one file through a drop link and resolves to the name it was
 *  stored under, which differs from `file.name` when it collided. */
export async function dropUpload(token: string, file: File): Promise<string> {
  const url = `${serverRoot}/s/${encodeURIComponent(token)}/drop?name=${encodeURIComponent(file.name)}`
  const res = await send(url, { method: 'POST', body: file, signal: null })
  if (res.status === 413) throw new ShareTooLargeError(file.name)
  if (res.status === 404 || res.status === 410) throw new ShareNotFoundError(token)
  if (res.status === 403) throw new SharePasswordRequiredError(token)
  if (!res.ok) throw new Error(`upload failed: ${res.status}`)
  const body: unknown = await res.json().catch(() => null)
  const name = body !== null && typeof body === 'object' && 'name' in body ? body.name : undefined
  if (typeof name !== 'string') throw malformed(res.status, 'no stored name')
  return name
}
