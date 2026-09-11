import { useEffect, useState } from 'react'
import { api, type Entry } from '../api/client'
import { isVideoFile, mimeTypeOf } from './media-utils'
import { registerMediaSource, releaseMediaSource, swReady } from '../crypto/download-sw'
import { decryptDownload, isUnlocked } from '../crypto/e2ee'
import { encryptionForLabel, shareLabelOf } from '../crypto/encrypted-shares'
import type { IconName } from '../icons'
import { Icon } from './Icon'

const CACHE = new Map<string, string>()
const CACHE_MAX = 300
const ENCRYPTED_THUMB_MAX_BYTES = 8 * 1024 * 1024

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

function extractVideoFrame(src: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const video = document.createElement('video')
    video.preload = 'metadata'
    video.muted = true
    video.playsInline = true
    video.crossOrigin = 'anonymous'
    video.src = src
    let done = false
    const cleanup = () => {
      if (done) return
      done = true
      video.removeAttribute('src')
      video.load()
    }
    const timer = window.setTimeout(() => { cleanup(); reject(new Error('timeout')) }, 8000)
    video.onloadedmetadata = () => {
      const seek = Math.min(1, video.duration > 1 ? 0.5 : video.duration * 0.1)
      video.currentTime = Math.max(0.1, seek)
    }
    video.onseeked = () => {
      window.clearTimeout(timer)
      try {
        const canvas = document.createElement('canvas')
        const width = video.videoWidth || 320
        const height = video.videoHeight || 180
        const aspect = width / height
        const max = 320
        const targetW = aspect < 1 ? max * aspect : max
        const targetH = aspect < 1 ? max : max / aspect
        canvas.width = Math.round(targetW)
        canvas.height = Math.round(targetH)
        const ctx = canvas.getContext('2d')
        if (!ctx) throw new Error('canvas context error')
        ctx.drawImage(video, 0, 0, canvas.width, canvas.height)
        const dataUrl = canvas.toDataURL('image/jpeg', 0.8)
        cleanup()
        resolve(dataUrl)
      } catch (error) {
        cleanup()
        reject(error)
      }
    }
    video.onerror = () => { window.clearTimeout(timer); cleanup(); reject(new Error('video load error')) }
  })
}

export interface ThumbnailProps {
  entry: Entry
  dim: number
  fallback: IconName
  iconSize: number
}

export function Thumbnail({ entry, dim, fallback, iconSize }: ThumbnailProps) {
  const [url, setUrl] = useState<string | null>(null)
  const key = `${entry.name}\x00${entry.etag}`
  const isVid = isVideoFile(entry.name)
  const eligible = entry.kind !== 'dir' && (entry.preview?.available === true || isVid)

  useEffect(() => {
    let cancelled = false
    let token: string | null = null
    let objectUrl: string | null = null
    setUrl(null)
    if (!eligible) return () => undefined
    const hit = cacheGet(key)
    if (hit !== undefined) {
      setUrl(hit)
      return () => undefined
    }
    if (isVid) {
      const source = api.contentUrl(entry)
      if (source) void extractVideoFrame(source).then((next) => {
        if (!cancelled) { cachePut(key, next); setUrl(next) }
      }).catch(() => { if (!cancelled) setUrl(null) })
      return () => { cancelled = true }
    }
    void (async () => {
      const encryption = await encryptionForLabel(shareLabelOf(entry.path)).catch(() => null)
      if (cancelled) return
      if (!encryption) {
        const next = api.thumbUrl(entry, dim)
        if (next) { cachePut(key, next); setUrl(next) }
        return
      }
      if (!isUnlocked(encryption.salt) || entry.size > ENCRYPTED_THUMB_MAX_BYTES) return
      const contentType = mimeTypeOf(entry.name) ?? 'application/octet-stream'
      const registration = await swReady()
      if (cancelled) return
      if (registration?.active) {
        const registered = registerMediaSource(entry, encryption.salt, contentType)
        token = registered.token
        setUrl(registered.url)
        return
      }
      try {
        const response = await fetch(api.contentUrl(entry))
        if (!response.ok) throw new Error(`HTTP ${response.status}`)
        const plaintext = await decryptDownload(new Uint8Array(await response.arrayBuffer()), encryption.salt)
        if (cancelled) return
        objectUrl = URL.createObjectURL(new Blob([plaintext.buffer as ArrayBuffer], { type: contentType }))
        setUrl(objectUrl)
      } catch {
        if (!cancelled) setUrl(null)
      }
    })()
    return () => {
      cancelled = true
      if (token) releaseMediaSource(token)
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [dim, eligible, entry, isVid, key])

  function onError() {
    CACHE.delete(key)
    setUrl(null)
  }

  if (!url) return <span className="sc-thumb__icon"><Icon name={fallback} size={iconSize} /></span>
  return (
    <div className="sc-thumb__wrap">
      <img className="sc-thumb__img" src={url} alt="" loading="lazy" decoding="async" onError={onError} />
      {isVid ? <span className="sc-thumb__badge" aria-hidden="true"><Icon name="video" /></span> : null}
    </div>
  )
}
