// The two-step download every single-file
// download in the app goes through: mint a ticket naming the file
// (`POST /api/v1/files/download`), then hand its `url` to the browser's own
// navigation so its download manager fetches the bytes. Nothing here reads
// a response body: that is the whole point of the ticket. The archive
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
import { downloadEncryptedFile } from '../crypto/download-sw'
import { encryptionForLabel, shareLabelOf } from '../crypto/encrypted-shares'

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
 * is full); the caller is the one with a snackbar to put it in.
 *
 * An encrypted path skips the ticket entirely: the server holds no key, so
 * it cannot mint one for content it cannot read, and routes instead through
 * `downloadEncryptedFile`, which fetches the ciphertext itself and decrypts
 * it in the browser on the way to the Service Worker download. A plain
 * path keeps using the ticket above, since the server can build that far
 * more cheaply than the browser fetching and re-streaming its own bytes.
 */
export async function downloadPath(path: string): Promise<void> {
  const encryption = await encryptionForLabel(shareLabelOf(path))
  if (encryption) {
    await downloadEncryptedFile(await api.stat(path))
    return
  }
  const ticket = await api.download(path)
  triggerUrlDownload(ticket.url, ticket.name)
}
