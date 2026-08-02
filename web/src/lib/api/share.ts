// web/src/lib/api/share.ts — standalone client for the public share page.
// Deliberately does NOT import ./client, ./mock, or ./http: those pull in
// the full fs mock (100k-row generator) and admin-adjacent surface. The
// public share bundle must stay small and must not
// ship code the anonymous visitor has no use for.
//
// Talks to `GET/POST /s/{token}[...]` — NOT `/api/shares/{id}`. That second
// path exists too, but it's the *owner's* authenticated CRUD surface for
// managing their own share links (DESIGN-API.md), keyed by numeric id and
// gated by session cookie + CSRF. An anonymous visitor opening a share link
// has neither, so calling it here always 401'd — this page never actually
// worked against the real backend until this fixed the endpoint. The public,
// unauthenticated read is `GET /s/{token}` (`sc-http::routes::public_link_get`).

export interface ShareEntry {
  name: string
  kind: 'file' | 'dir'
  size: number
}

export interface ShareInfo {
  /** Name of the shared file/folder itself (not a per-entry name). */
  name: string
  isDir: boolean
  size: number
  label: string | null
  /** A file-drop link: uploads-only, never lists or serves its contents. */
  isDrop: boolean
  /** Largest single upload the server will accept, in bytes. Only sent for a
   *  drop link — `null` everywhere else, because nothing else here uploads.
   *  This page can't ask `/api/capabilities` (that lives behind `./client`,
   *  which the header comment forbids importing), so without this field the
   *  only way to find the ceiling is to hit it. */
  maxUploadBytes: number | null
  canDownload: boolean
  /** `entries` is `null` for a file share, a drop link, or a folder share
   *  that's still password-locked -- never an empty-but-present array used
   *  to mean any of those. */
  entries: ShareEntry[] | null
}

export class ShareNotFoundError extends Error {}
/** A drop upload the server refused for size. Its own class so the page can
 *  say "too large" rather than the generic failure — the client-side check
 *  against `maxUploadBytes` catches this first, but not if the operator
 *  lowered the limit between page load and upload. */
export class ShareTooLargeError extends Error {}
/** Thrown by `getShare` when the link exists but needs `unlockShare` first --
 *  distinct from "gone" so the page can show a password form instead of the
 *  generic not-found message. */
export class SharePasswordRequiredError extends Error {}

const IS_MOCK = import.meta.env.VITE_API_MOCK === '1'
// Deliberately NOT `/api` — see header comment. `/s/...` is a top-level
// route, same reasoning `vite.config.ts`'s proxy list keys off of.
const ORIGIN = import.meta.env.VITE_API_BASE ?? ''

/** The mock drop link's ceiling — `HttpConfig::body_limit_bytes`'s own
 *  default, so the mock refuses exactly what the server would. */
const MOCK_DROP_LIMIT = 16 * 1024 * 1024
/** Names the mock drop box already holds, so a repeat upload demonstrates the
 *  server's auto-rename instead of silently looking like an overwrite. */
const mockDropped = new Set<string>()

async function mockGetShare(token: string): Promise<ShareInfo> {
  await new Promise((r) => setTimeout(r, 150))
  if (token === 'expired') throw new ShareNotFoundError('expired')
  if (token === 'locked') throw new SharePasswordRequiredError(token)
  if (token === 'drop') {
    return {
      name: 'Drop box',
      isDir: true,
      size: 0,
      label: 'Drop box',
      isDrop: true,
      maxUploadBytes: MOCK_DROP_LIMIT,
      canDownload: false,
      entries: null
    }
  }
  return {
    name: '공유된 사진',
    isDir: true,
    size: 0,
    label: '공유된 사진',
    isDrop: false,
    maxUploadBytes: null,
    canDownload: true,
    entries: [
      { name: '휴가-2026-07-01.jpg', kind: 'file', size: 4_213_665 },
      { name: '휴가-2026-07-02.jpg', kind: 'file', size: 3_982_211 },
      { name: '가족사진.png', kind: 'file', size: 2_112_004 }
    ]
  }
}

interface RawLinkGetResponse {
  protected: boolean
  name?: string
  is_dir?: boolean
  size?: number
  label?: string | null
  drop?: boolean
  max_upload_bytes?: number
  can_download?: boolean
  entries?: { name: string; kind: 'file' | 'dir'; size: number }[]
}

async function httpGetShare(token: string): Promise<ShareInfo> {
  const res = await fetch(`${ORIGIN}/s/${encodeURIComponent(token)}`, { credentials: 'include' })
  if (res.status === 404 || res.status === 410) throw new ShareNotFoundError(token)
  if (!res.ok) throw new Error(`share lookup failed: ${res.status}`)
  const body: RawLinkGetResponse = await res.json()
  // A password-protected link the visitor hasn't unlocked yet answers with
  // ONLY `{"protected": true}` — none of the other fields (`sc-http`'s
  // `public_link_get` returns early, before even checking `is_dir`).
  if (body.protected && body.name === undefined) {
    throw new SharePasswordRequiredError(token)
  }
  return {
    name: body.name ?? '',
    isDir: body.is_dir ?? false,
    size: body.size ?? 0,
    label: body.label ?? null,
    isDrop: body.drop ?? false,
    maxUploadBytes: body.max_upload_bytes ?? null,
    canDownload: body.can_download ?? false,
    entries: body.entries ? body.entries.map((e) => ({ name: e.name, kind: e.kind, size: e.size })) : null
  }
}

export function getShare(token: string): Promise<ShareInfo> {
  return IS_MOCK ? mockGetShare(token) : httpGetShare(token)
}

async function mockUnlockShare(token: string, password: string): Promise<boolean> {
  await new Promise((r) => setTimeout(r, 100))
  return password === 'hunter2' || token !== 'locked'
}

/** `POST /s/{token}/auth`. On success the server sets an HttpOnly,
 *  `Path=/s/{token}`-scoped cookie (`Secure` — requires HTTPS, so this
 *  never succeeds over a plain-`http://` dev origin even with the right
 *  password; that's a real constraint of testing this locally, not a bug),
 *  so a following `getShare(token)` call sees through the lock. */
export function unlockShare(token: string, password: string): Promise<boolean> {
  if (IS_MOCK) return mockUnlockShare(token, password)
  return fetch(`${ORIGIN}/s/${encodeURIComponent(token)}/auth`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ password })
  }).then((res) => res.ok)
}

async function mockRequestDownload(): Promise<string> {
  await new Promise((r) => setTimeout(r, 100))
  return '#mock-download'
}

/** `POST /s/{token}/download` — mints a one-time signed content URL for the
 *  link's target as a whole (the shared file, or the shared folder's root;
 *  there is no per-entry variant of this route, so a folder share only ever
 *  offers one "download everything" action, not per-row downloads). */
export function requestShareDownload(token: string): Promise<string> {
  if (IS_MOCK) return mockRequestDownload()
  return fetch(`${ORIGIN}/s/${encodeURIComponent(token)}/download`, {
    method: 'POST',
    credentials: 'include'
  }).then(async (res) => {
    if (!res.ok) throw new Error(`download request failed: ${res.status}`)
    const body: { url: string } = await res.json()
    return body.url
  })
}

/** Mirrors the core's own collision handling (`sc-core::links::unique_name`):
 *  `a.txt` becomes `a (1).txt`, never an overwrite. */
function mockUniqueName(name: string): string {
  if (!mockDropped.has(name)) return name
  const dot = name.lastIndexOf('.')
  const [stem, ext] = dot > 0 ? [name.slice(0, dot), name.slice(dot)] : [name, '']
  let n = 1
  while (mockDropped.has(`${stem} (${n})${ext}`)) n += 1
  return `${stem} (${n})${ext}`
}

async function mockDropUpload(file: File): Promise<string> {
  await new Promise((r) => setTimeout(r, 200))
  if (file.size > MOCK_DROP_LIMIT) throw new ShareTooLargeError(file.name)
  const stored = mockUniqueName(file.name)
  mockDropped.add(stored)
  return stored
}

/** `POST /s/{token}/drop?name=…` — upload one file through a file-drop link.
 *  Resolves to the name the file was **stored** under, which is not always
 *  `file.name`: the core never overwrites, so a collision comes back renamed
 *  (`DESIGN-PREVIEW.md` §7.2) and the uploader has to be told which one is
 *  theirs.
 *
 *  No `Sc-Csrf` header, deliberately: `/s/**` is a public path, so
 *  `middleware::auth` returns before inserting `SessionToken` and
 *  `middleware::csrf` only enforces when that extension exists. Sending one
 *  would be cargo cult — and this bundle has no session to read it from
 *  anyway. */
export function dropUpload(token: string, file: File): Promise<string> {
  if (IS_MOCK) return mockDropUpload(file)
  const url = `${ORIGIN}/s/${encodeURIComponent(token)}/drop?name=${encodeURIComponent(file.name)}`
  return fetch(url, { method: 'POST', credentials: 'include', body: file }).then(async (res) => {
    if (res.status === 413) throw new ShareTooLargeError(file.name)
    if (res.status === 404 || res.status === 410) throw new ShareNotFoundError(token)
    if (res.status === 403) throw new SharePasswordRequiredError(token)
    if (!res.ok) throw new Error(`upload failed: ${res.status}`)
    const body: { name: string } = await res.json()
    return body.name
  })
}
