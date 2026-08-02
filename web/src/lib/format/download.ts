// web/src/lib/format/download.ts — the browser-download trigger shared by
// the browse page's single-file/archive download and JobTray's finished
// archive-job download (both hand a same-origin URL or Blob to the browser
// and want it saved, not navigated to).

/**
 * `newTab` matters more than it looks: verified live against the real dev
 * server that a signed URL minted with no `content_hosts` configured
 * (the single-origin fallback) comes
 * back as `https:///c/<token>` -- `fs_link`'s `format!("https://{host}/c/{token}")`
 * with an empty host. A browser's URL parser does not read that as "no host"
 * (which would at least fail loudly); the special-scheme "collapse the
 * slashes" rule in the WHATWG spec reads it as **host `c`, path `/<token>`**
 * -- i.e. a real, different, unrelated domain. Clicking a same-tab `<a href>`
 * to that navigates the whole tab to it and (since nothing answers there)
 * strands the user on `chrome-error://chromewebdata/`, wiping their place in
 * the file browser. `target="_blank"` contains that failure to a tab nobody
 * was using instead of destroying the one they were on -- independent of
 * whether the URL is well-formed, and cheap insurance once it is.
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
