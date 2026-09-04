// web/src/lib/format/download.ts — the two-step download every single-file
// download in the app goes through: mint a ticket naming the file
// (`POST /api/v1/files/download`), then hand its `url` to the browser's own
// navigation so its download manager fetches the bytes. Nothing here reads
// a response body — that is the whole point of the ticket. The archive
// download (`POST /api/v1/files/archive`) is the same two-step shape with
// its own multi-path ticket call, minted where the selection is built
// (`(app)/b/[...path]/+page.svelte`) and handed to `triggerUrlDownload`
// below.
//
// `newTab` in `triggerUrlDownload` opens a malformed URL onto a tab nobody
// was using. A same-tab `<a href>` to a host that answers nothing navigates
// the whole tab and strands the user on the browser's own error page,
// wiping their place in the file browser; a new tab costs nothing and
// cannot do that.
import { api } from '../api/client'

export function triggerUrlDownload(url: string, filename?: string, newTab = false): void {
  const a = document.createElement('a')
  a.href = url
  if (filename) a.download = filename
  a.rel = 'noopener'
  if (newTab) a.target = '_blank'
  document.body.appendChild(a)
  a.click()
  a.remove()
}

/**
 * Mints a download ticket for one file and hands its `url` to the browser.
 * Throws the `ApiError` the mint refused with (422 for a folder or empty
 * path, 404 for a path the account cannot reach, 413 when the ticket store
 * is full) — the caller is the one with a snackbar to put it in.
 */
export async function downloadPath(path: string): Promise<void> {
  const ticket = await api.download(path)
  triggerUrlDownload(ticket.url, ticket.name)
}
