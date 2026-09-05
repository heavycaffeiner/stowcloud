<script lang="ts" module>
  /** url by `name \0 etag`. Bounded, insertion-ordered LRU: a directory of
   *  100k images must not turn scrolling into an unbounded memory leak.
   *  Keyed on the etag rather than the fileid because the id is allocated
   *  lazily and can appear on a file that had none a listing ago, which would
   *  split the cache for a file whose bytes never changed. */
  const CACHE = new Map<string, string>()
  const CACHE_MAX = 300

  function cacheGet(key: string): string | undefined {
    const hit = CACHE.get(key)
    if (hit === undefined) return undefined
    CACHE.delete(key)
    CACHE.set(key, hit)
    return hit
  }

  function cachePut(key: string, url: string): void {
    CACHE.delete(key)
    CACHE.set(key, url)
    while (CACHE.size > CACHE_MAX) {
      const oldest = CACHE.keys().next().value
      if (oldest === undefined) break
      CACHE.delete(oldest)
    }
  }
</script>

<script lang="ts">
  // Thumbnail.svelte: the picture on a grid file card, or the type icon when
  // there isn't one.
  //
  // This component only exists while its card is inside the grid's rendered
  // window, so "fetch only what is visible" and "stop caring once it scrolls
  // away" are both just its lifetime. Scrolling past a card while its link
  // request is in flight discards the answer (the request itself has no
  // cancellation point: `POST /api/fs/link` is a single small round trip) but
  // still fills the cache, so scrolling back is free.
  import { api, type Entry } from '../api/client'
  import { Icon } from 'm3-svelte'
  import { icons, type IconName } from '../icons'
  import { isVideoFile, mimeTypeOf } from './media-utils'
  import { registerMediaSource, releaseMediaSource, swReady } from '../crypto/download-sw'
  import { decryptDownload, isUnlocked } from '../crypto/e2ee'
  import { encryptionForLabel, shareLabelOf } from '../crypto/encrypted-shares'

  interface Props {
    entry: Entry
    /** Longest edge requested from the server's re-encoder, in px. */
    dim: number
    fallback: IconName
    iconSize: number
  }

  let { entry, dim, fallback, iconSize }: Props = $props()

  let url = $state<string | null>(null)

  const key = $derived(`${entry.name}\x00${entry.etag}`)
  const isVid = $derived(isVideoFile(entry.name))
  /**
   * `preview.available` is the server saying it can probably re-encode this
   * file. It is a hint from the name, and the route is the authority: a file
   * that turns out not to be an image answers 415 and the icon below stays.
   *
   * Videos extract a representative frame client-side using offscreen canvas.
   */
  const eligible = $derived(entry.kind !== 'dir' && (entry.preview?.available === true || isVid))

  /** Bound on decrypting a whole source image just to draw one grid tile.
   *  There is no server-side thumbnail cache for an encrypted share (the
   *  server holds no key), so every tile above the fallback icon pays this
   *  file's full decrypt cost on every mount, not once. No byte-size preview
   *  cap exists to reuse here: `ClientLimits.max_file_size`
   *  (api/types.ts) is the upload ceiling, not a preview source bound, and
   *  the server's own preview decode limits
   *  (`go/engine/service/preview/decode.go`'s `DefaultDecodeLimits`) bound
   *  pixels, not bytes, and are unreachable here anyway since this path
   *  never touches the Go decoder. 8 MiB comfortably covers an ordinary
   *  compressed photo (a 24 MP JPEG is typically 4-10 MB) while refusing a
   *  raw scan or an uncompressed TIFF, which would cost real seconds to
   *  decrypt and decode for a tile nobody can see the detail of. */
  const ENCRYPTED_THUMB_MAX_BYTES = 8 * 1024 * 1024

  function extractVideoFrame(src: string): Promise<string> {
    return new Promise((resolve, reject) => {
      const v = document.createElement('video')
      v.preload = 'metadata'
      v.muted = true
      v.playsInline = true
      v.crossOrigin = 'anonymous'
      v.src = src

      let cleaned = false
      const cleanup = () => {
        if (cleaned) return
        cleaned = true
        v.removeAttribute('src')
        v.load()
      }

      const timer = setTimeout(() => {
        cleanup()
        reject(new Error('timeout'))
      }, 8000)

      v.onloadedmetadata = () => {
        const seek = Math.min(1.0, v.duration > 1 ? 0.5 : v.duration * 0.1)
        v.currentTime = Math.max(0.1, seek)
      }

      v.onseeked = () => {
        clearTimeout(timer)
        try {
          const canvas = document.createElement('canvas')
          const vw = v.videoWidth || 320
          const vh = v.videoHeight || 180
          const aspect = vw / vh
          const maxDim = 320
          let w = maxDim
          let h = maxDim / aspect
          if (aspect < 1) {
            h = maxDim
            w = maxDim * aspect
          }
          canvas.width = Math.round(w)
          canvas.height = Math.round(h)
          const ctx = canvas.getContext('2d')
          if (ctx) {
            ctx.drawImage(v, 0, 0, canvas.width, canvas.height)
            const dataUrl = canvas.toDataURL('image/jpeg', 0.8)
            cleanup()
            resolve(dataUrl)
            return
          }
        } catch (e) {
          cleanup()
          reject(e)
          return
        }
        cleanup()
        reject(new Error('canvas context error'))
      }

      v.onerror = () => {
        clearTimeout(timer)
        cleanup()
        reject(new Error('video load error'))
      }
    })
  }

  $effect(() => {
    const k = key
    if (!eligible) {
      url = null
      return
    }
    const hit = cacheGet(k)
    if (hit !== undefined) {
      url = hit
      return
    }

    if (isVid) {
      let cancelled = false
      const readUrl = api.contentUrl(entry)
      if (!readUrl) {
        url = null
        return
      }
      void extractVideoFrame(readUrl)
        .then((dataUrl) => {
          if (!cancelled && dataUrl) {
            cachePut(k, dataUrl)
            url = dataUrl
          }
        })
        .catch(() => {
          if (!cancelled) {
            url = null
          }
        })
      return () => {
        cancelled = true
      }
    }

    // Not a video: an image candidate. The server's thumbnail route 422s
    // for an encrypted share (it holds no key), so this checks the share
    // first and, only when it is encrypted, decrypts the file itself
    // instead of asking the server.
    let cancelled = false
    let token: string | null = null
    let objectUrl: string | null = null

    void (async () => {
      const encryption = await encryptionForLabel(shareLabelOf(entry.path)).catch(() => null)
      if (cancelled) return
      if (!encryption) {
        // Straight to the thumbnail route with the reference this row
        // already carries, so there is nothing to mint and nothing to
        // await beyond the encryption check itself: the <img> below does
        // the fetching. That is the property that rules out a per-tile
        // ticket, since a grid mounts one of these per visible file.
        const next = api.thumbUrl(entry, dim)
        // Empty when the row carries no preview reference, and when the
        // mock has no decoder. An <img> with an empty src resolves to the
        // page itself, which renders as broken.
        if (!next) {
          url = null
          return
        }
        cachePut(k, next)
        url = next
        return
      }
      // Encrypted: no shared cache. A Blob URL or a registered media
      // token is a real resource this LRU has no eviction hook to
      // release, and no attempt at all past the size bound or while
      // locked. The fallback icon is the right answer for either, same as
      // a file with no preview reference at all.
      if (!isUnlocked(encryption.salt) || entry.size > ENCRYPTED_THUMB_MAX_BYTES) {
        url = null
        return
      }
      const contentType = mimeTypeOf(entry.name) ?? 'application/octet-stream'
      const reg = await swReady()
      if (cancelled) return
      if (reg?.active) {
        const registered = registerMediaSource(entry, encryption.salt, contentType)
        token = registered.token
        url = registered.url
        return
      }
      try {
        const res = await fetch(api.contentUrl(entry))
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const bytes = new Uint8Array(await res.arrayBuffer())
        const plaintext = await decryptDownload(bytes, encryption.salt)
        if (cancelled) return
        objectUrl = URL.createObjectURL(new Blob([plaintext] as BlobPart[], { type: contentType }))
        url = objectUrl
      } catch {
        if (!cancelled) url = null
      }
    })()

    return () => {
      cancelled = true
      if (token) releaseMediaSource(token)
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  })

  function onError(): void {
    // A reference outlives its usefulness eventually: it is sealed with a
    // deadline, and a listing left open past it holds stale ones. Drop it so
    // the next pass asks for a fresh one rather than showing a broken image
    // forever.
    CACHE.delete(key)
    url = null
  }
</script>

{#if url}
  <div class="sc-thumb__wrap">
    <img class="sc-thumb__img" src={url} alt="" loading="lazy" decoding="async" onerror={onError} />
    {#if isVid}
      <span class="sc-thumb__badge" aria-hidden="true">
        <Icon icon={icons.video} size={14} />
      </span>
    {/if}
  </div>
{:else}
  <span class="sc-thumb__icon"><Icon icon={icons[fallback]} size={iconSize} /></span>
{/if}

<style>
  .sc-thumb__wrap {
    position: relative;
    width: 100%;
    height: 100%;
    overflow: hidden;
  }
  .sc-thumb__img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }
  .sc-thumb__badge {
    position: absolute;
    bottom: 8px;
    right: 8px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border-radius: var(--m3-shape-small, 4px);
    background: rgb(0 0 0 / 65%);
    color: #fff;
    pointer-events: none;
  }
  .sc-thumb__icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 100%;
    height: 100%;
    color: var(--m3c-on-surface-variant);
  }
</style>
