// web/src/lib/format/download.ts — the browser-download trigger shared by
// the browse page's single-file/archive download and JobTray's finished
// archive-job download (both hand a same-origin URL or Blob to the browser
// and want it saved, not navigated to).

/**
 * `newTab` contains a malformed URL to a tab nobody was using. A same-tab
 * `<a href>` to a host that answers nothing navigates the whole tab and strands
 * the user on the browser's own error page, wiping their place in the file
 * browser; a new tab costs nothing and cannot do that.
 */
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

export function triggerBlobDownload(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  triggerUrlDownload(url, filename)
  // The object URL only needs to outlive the synchronous `click()` above;
  // freeing it a tick later (rather than immediately) avoids a Safari quirk
  // where revoking inside the same microtask can cancel the download.
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
