// Standalone client for the public share page.
// Deliberately does NOT import ./client or ./http: those pull in the
// authenticated and admin-adjacent surface. The public share bundle must stay
// small and must not ship code the anonymous visitor has no use for.
//
// Talks to `GET/POST /s/{token}[...]`, not `/api/shares/{id}`. That second
// path exists too, but it's the *owner's* authenticated CRUD surface for
// managing their own share links, keyed by numeric id and
// gated by session cookie + CSRF. An anonymous visitor opening a share link
// has neither, so calling it here always 401'd; this page never actually
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
   *  drop link: `null` everywhere else, because nothing else here uploads.
   *  This page can't ask `/api/capabilities` (that lives behind `./client`,
   *  which the header comment forbids importing), so without this field the
   *  only way to find the ceiling is to hit it. */
  maxUploadBytes: number | null
  canDownload: boolean
  /** `entries` is `null` for a file share, a drop link, or a folder share
   *  that's still password-locked -- never an empty-but-present array used
   *  to mean any of those. */
  entries: ShareEntry[] | null
  /** The subpath the server actually resolved, relative to the link's own
   *  target and empty at its root. Echoed back so a client can tell that its
   *  request was honoured rather than quietly widened. */
  path: string
}

/** A subpath the server refused. Distinct from "gone" so the page can clear
 *  back to the link root and say the folder is no longer there, rather than
 *  showing an empty list that looks like an empty folder. */
export class SharePathGoneError extends Error {}

export class ShareNotFoundError extends Error {}
/** A drop upload the server refused for size. Its own class so the page can
 *  say "too large" rather than the generic failure; the client-side check
 *  against `maxUploadBytes` catches this first, but not if the operator
 *  lowered the limit between page load and upload. */
export class ShareTooLargeError extends Error {}
/** Thrown by `getShare` when the link exists but needs `unlockShare` first --
 *  distinct from "gone" so the page can show a password form instead of the
 *  generic not-found message. */
export class SharePasswordRequiredError extends Error {}
/** An unlock the server refused for a reason that is not the password: the
 *  page must not report it as a wrong one, which is the message guaranteed to
 *  send a visitor looking in the wrong place. */
export class ShareUnlockFailedError extends Error {}

// Deliberately NOT `/api`: see header comment. `/s/...` is a top-level
// route, same reasoning `vite.config.ts`'s proxy list keys off of.
const ORIGIN = import.meta.env.VITE_API_BASE ?? ''

interface RawLinkGetResponse {
  protected: boolean
  name?: string
  is_dir?: boolean
  size?: number
  label?: string | null
  drop?: boolean
  max_upload_bytes?: number
  can_download?: boolean
  path?: string
  entries?: { name: string; kind: 'file' | 'dir'; size: number }[]
}

export async function getShare(token: string, path = ''): Promise<ShareInfo> {
  // `/s/{token}` is both the page a visitor opens and the endpoint this
  // reads: the address in a share link has to be the one that works when
  // pasted into a browser. The Accept header is what tells the two apart, so
  // it is explicit here rather than left to whatever the browser defaults to.
  const res = await fetch(`${ORIGIN}/s/${encodeURIComponent(token)}${shareQuery(path)}`, {
    credentials: 'include',
    headers: { Accept: 'application/json' }
  })
  // A refused subpath and a dead link are different states to the page: the
  // first clears back to the root, the second has nowhere to go.
  if (res.status === 404 && path !== '') throw new SharePathGoneError(path)
  if (res.status === 404 || res.status === 410) throw new ShareNotFoundError(token)
  if (!res.ok) throw new Error(`share lookup failed: ${res.status}`)
  const body: RawLinkGetResponse = await res.json()
  // A password-protected link the visitor hasn't unlocked yet answers with
  // ONLY `{"protected": true}`: none of the other fields (`sc-http`'s
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
    entries: body.entries ? body.entries.map((e) => ({ name: e.name, kind: e.kind, size: e.size })) : null,
    path: body.path ?? ''
  }
}

/** `?path=…`, or nothing at the link's own root, so a client written against
 *  the previous API sends exactly what it always did. */
function shareQuery(path: string): string {
  return path ? `?path=${encodeURIComponent(path)}` : ''
}

/** `POST /s/{token}/auth`. On success the server sets an HttpOnly,
 *  `Path=/s/{token}`-scoped cookie (`Secure`, requires HTTPS, so this
 *  never succeeds over a plain-`http://` dev origin even with the right
 *  password; that's a real constraint of testing this locally, not a bug),
 *  so a following `getShare(token)` call sees through the lock.
 *
 *  No `Sc-Csrf` header: the link's own token is the authority, which the
 *  server declares by mounting `/s/**` as a public route. A browser that is
 *  also signed in sends its session cookie here regardless, and this bundle
 *  has no session surface to read a token from.
 *
 *  Resolves false for a wrong password, and throws for a refusal that is not
 *  one, so the page never reports "incorrect password" for something else.
 */
export function unlockShare(token: string, password: string): Promise<boolean> {
  return fetch(`${ORIGIN}/s/${encodeURIComponent(token)}/auth`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ password })
  }).then(async (res) => {
    if (res.ok) return true
    if (res.status === 404 || res.status === 410) throw new ShareNotFoundError(token)
    if (res.status === 422 && (await reasonKey(res)) === 'fs.link_password') return false
    throw new ShareUnlockFailedError(token)
  })
}

/** The `error.detail.reason_key` of a refusal, or null when the body is not
 *  the server's error envelope. */
async function reasonKey(res: Response): Promise<string | null> {
  const body: unknown = await res.json().catch(() => null)
  const key = (body as { error?: { detail?: { reason_key?: unknown } } } | null)?.error?.detail?.reason_key
  return typeof key === 'string' ? key : null
}

/** `GET /s/{token}/download?path=…`: one file under the link.
 *
 *  A plain navigation, like the zip: the response is the bytes themselves,
 *  with `Content-Disposition: attachment`. There is no URL to mint first, and
 *  fetching one only to navigate to it would spend the download cap on a
 *  request whose body was then thrown away. */
export function shareDownloadUrl(token: string, path = ''): string {
  return `${ORIGIN}/s/${encodeURIComponent(token)}/download${shareQuery(path)}`
}

/** `GET /s/{token}/zip?path=…`: the streamed archive of one folder under the
 *  link. A plain navigation rather than a `fetch`: the response is the bytes
 *  themselves, with `Content-Disposition: attachment`, and there is no signed
 *  URL to fetch first. */
export function shareZipUrl(token: string, path = ''): string {
  return `${ORIGIN}/s/${encodeURIComponent(token)}/zip${shareQuery(path)}`
}

/** `POST /s/{token}/drop?name=…`: upload one file through a file-drop link.
 *  Resolves to the name the file was **stored** under, which is not always
 *  `file.name`: the core never overwrites, so a collision comes back renamed
 * and the uploader has to be told which one is
 *  theirs.
 *
 *  No `Sc-Csrf` header: the link's own token is the authority, and the
 *  server declares `/s/**` public so a browser that happens to be signed in
 *  is still admitted as a visitor. This bundle has no session surface to
 *  read a token from in any case. */
export function dropUpload(token: string, file: File): Promise<string> {
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
