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

/**
 * Saves a streamed response without collecting it first.
 *
 * A streamed archive has no length and can be any size, so `res.blob()` would
 * hold the whole thing in the tab before a byte reached the disk. Where the
 * File System Access API exists the body is piped straight into the file the
 * person picked, so memory stays flat however large the folder is. Everywhere
 * else it falls back to a blob, which is what the browser can do: the download
 * works and costs the tab its size. Firefox and Safari are that case today.
 *
 * `fetch` is a parameter rather than a response, because the picker has to be
 * opened while the click that started this is still the current user
 * activation. Awaiting the request first spends it, and Chrome then refuses
 * the picker with a SecurityError: the download silently does nothing.
 */
export async function saveStream(fetch: () => Promise<Response>, filename: string): Promise<void> {
  const picker = (
    window as unknown as {
      showSaveFilePicker?: (o: { suggestedName?: string }) => Promise<FileSystemFileHandle>
    }
  ).showSaveFilePicker

  if (!picker) {
    triggerBlobDownload(await (await fetch()).blob(), filename)
    return
  }

  let handle: FileSystemFileHandle
  try {
    handle = await picker({ suggestedName: filename })
  } catch (err) {
    // Only a dismissal is a decision to respect. Anything else is the picker
    // failing, and falling back is better than a button that does nothing.
    if (err instanceof DOMException && err.name === 'AbortError') return
    triggerBlobDownload(await (await fetch()).blob(), filename)
    return
  }

  const res = await fetch()
  if (!res.body) {
    triggerBlobDownload(await res.blob(), filename)
    return
  }
  await res.body.pipeTo(await handle.createWritable())
}
