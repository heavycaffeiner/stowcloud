import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { api, type Entry } from '../api/client'
import type { ArchiveEntry, ArchiveListing, ShareEncryption } from '../api/types'
import { ApiError } from '../api/types'
import { fileContentQuery, archiveEntriesQuery } from '../query/files'
import { formatBytes } from '../format/bytes'
import { formatEntrySize } from '../format/entry-size'
import { useI18n } from '../i18n/use-i18n'
import { IMAGE_EXT, VIDEO_EXT, extensionOf, mimeTypeOf } from './media-utils'
import { registerMediaSource, releaseMediaSource, swReady } from '../crypto/download-sw'
import { decryptDownload, isUnlocked, MAX_ENCRYPTABLE_BYTES } from '../crypto/e2ee'
import { encryptionForLabel, shareLabelOf } from '../crypto/encrypted-shares'
import { listEncryptedArchive } from '../crypto/zip-listing'
import { Button } from './Button'
import { UnlockShareDialog } from './UnlockShareDialog'
import { Icon } from './Icon'

const TEXT_EXT: Record<string, true> = {
  txt: true, md: true, markdown: true, log: true, csv: true, tsv: true, json: true, yaml: true, yml: true, toml: true, ini: true,
  conf: true, cfg: true, xml: true, html: true, htm: true, css: true, scss: true, js: true, ts: true, jsx: true, tsx: true,
  svelte: true, vue: true, rs: true, go: true, py: true, rb: true, php: true, java: true, kt: true, c: true, h: true, cpp: true, hpp: true, cs: true,
  sh: true, bash: true, zsh: true, sql: true, env: true, gitignore: true, dockerfile: true, makefile: true
}
const TEXT_MAX_BYTES = 2 * 1024 * 1024
const PREVIEW_DIM = 1600

type Body = { kind: 'image' | 'video' | 'text' | 'too-large-text' | 'archive' | 'none' }
type MediaKind = 'idle' | 'loading' | 'ready' | 'too-large' | 'no-worker-video' | 'failed'

interface PreviewDialogProps {
  open: boolean
  entry: Entry | null
  path: string
  hasPrev: boolean
  hasNext: boolean
  onClose: () => void
  onPrev: () => void
  onNext: () => void
  onDownload: (entry: Entry) => void
  onEdit: (entry: Entry) => void
}
function DialogFrame({ open, title, className, onClose, children }: { open: boolean; title: string; className?: string; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLElement>(null)
  const opener = useRef<HTMLElement | null>(null)
  const wasOpen = useRef(false)
  useEffect(() => {
    const dialog = ref.current as unknown as { open?: boolean; addEventListener: typeof window.addEventListener; removeEventListener: typeof window.removeEventListener } | null
    if (!dialog) return
    if (open && !wasOpen.current) {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      wasOpen.current = true
    } else if (!open && wasOpen.current) {
      wasOpen.current = false
      queueMicrotask(() => {
        const target = opener.current
        opener.current = null
        if (target?.isConnected && !target.hasAttribute('disabled') && !target.hasAttribute('aria-hidden')) target.focus()
        else document.querySelector<HTMLElement>('[role="grid"][tabindex="0"], [role="tree"][tabindex="0"]')?.focus()
      })
    }
    dialog.open = open
    const close = () => { if (open) onClose() }
    dialog.addEventListener('close', close)
    return () => dialog.removeEventListener('close', close)
  }, [open, onClose])
  return <mdui-dialog ref={ref} className={className} headline={title} close-on-overlay-click={false} close-on-esc={false}>{children}</mdui-dialog>
}

function levelOf(entries: ArchiveEntry[], cwd: string): { path: string; label: string; kind: 'dir' | 'file'; size: number }[] {
  const prefix = cwd === '' ? '' : `${cwd}/`
  const dirs = new Map<string, { path: string; label: string; kind: 'dir'; size: number }>()
  const files: { path: string; label: string; kind: 'file'; size: number }[] = []
  for (const entry of entries) {
    const isDir = entry.kind === 'dir'
    const full = isDir ? entry.name.slice(0, -1) : entry.name
    if (!full.startsWith(prefix)) continue
    const rest = full.slice(prefix.length)
    if (!rest) continue
    const cut = rest.indexOf('/')
    if (cut < 0) {
      if (isDir) dirs.set(rest, { path: full, label: rest, kind: 'dir', size: 0 })
      else files.push({ path: full, label: rest, kind: 'file', size: entry.size })
    } else {
      const label = rest.slice(0, cut)
      if (!dirs.has(label)) dirs.set(label, { path: `${prefix}${label}`, label, kind: 'dir', size: 0 })
    }
  }
  const sort = (a: { label: string }, b: { label: string }) => a.label.localeCompare(b.label, undefined, { numeric: true, sensitivity: 'base' })
  return [...[...dirs.values()].sort(sort), ...files.sort(sort)]
}

export function PreviewDialog({ open, entry, path, hasPrev, hasNext, onClose, onPrev, onNext, onDownload, onEdit }: PreviewDialogProps) {
  const { t, tp } = useI18n()
  const [unlockOpen, setUnlockOpen] = useState(false)
  const [unlockGeneration, setUnlockGeneration] = useState(0)
  const [cwd, setCwd] = useState('')
  const [imageOverride, setImageOverride] = useState<string | null>(null)
  const [imageGaveUp, setImageGaveUp] = useState(false)
  const [videoGaveUp, setVideoGaveUp] = useState(false)
  const [mediaUrl, setMediaUrl] = useState<string | null>(null)
  const [mediaKind, setMediaKind] = useState<MediaKind>('idle')
  const videoRef = useRef<HTMLVideoElement>(null)
  const previewRef = useRef<HTMLDivElement>(null)
  const body: Body = useMemo(() => {
    if (!entry) return { kind: 'none' }
    const ext = extensionOf(entry.name)
    if (ext in VIDEO_EXT) return { kind: 'video' }
    if (ext in IMAGE_EXT || entry.preview?.available) return { kind: 'image' }
    if (ext === 'zip') return { kind: 'archive' }
    if (!(ext in TEXT_EXT)) return { kind: 'none' }
    return entry.size > TEXT_MAX_BYTES ? { kind: 'too-large-text' } : { kind: 'text' }
  }, [entry])
  const encryptionQuery = useQuery<ShareEncryption | null>({
    queryKey: ['preview-encryption', entry?.path ?? ''],
    queryFn: () => encryptionForLabel(shareLabelOf((entry as Entry).path)),
    enabled: open && entry !== null,
    staleTime: Infinity
  })
  const encryption = encryptionQuery.data ?? null
  const encryptionPending = open && entry !== null && encryptionQuery.isPending
  const unlocked = useMemo(() => encryption === null || isUnlocked(encryption.salt), [encryption, unlockGeneration])
  const locked = Boolean(entry && encryption && !unlocked)
  const textQuery = useQuery({ ...fileContentQuery(entry, unlocked), enabled: open && body.kind === 'text' && !encryptionPending && unlocked })
  const archiveQuery = useQuery({ ...archiveEntriesQuery(path, open && body.kind === 'archive' && !encryptionPending && encryption === null) })
  const encryptedArchiveQuery = useQuery<ArchiveListing>({
    queryKey: ['preview-encrypted-archive', entry?.path ?? '', unlocked],
    queryFn: () => listEncryptedArchive(entry as Entry, (encryption as ShareEncryption).salt),
    enabled: open && body.kind === 'archive' && encryption !== null && unlocked,
    staleTime: Infinity
  })
  const archiveListing = encryption ? encryptedArchiveQuery.data ?? null : archiveQuery.data ?? null
  const archiveError = encryption ? encryptedArchiveQuery.error : archiveQuery.error
  const archivePending = encryption ? encryptedArchiveQuery.isPending : archiveQuery.isPending
  const previewKey = open && entry ? `${body.kind}\x00${path}\x00${entry.id ?? ''}\x00${entry.size}` : null

  useEffect(() => {
    if (locked) setUnlockOpen(true)
  }, [locked])
  useEffect(() => {
    const bump = () => setUnlockGeneration((generation) => generation + 1)
    window.addEventListener('sc:unlock', bump)
    window.addEventListener('sc:lock', bump)
    return () => {
      window.removeEventListener('sc:unlock', bump)
      window.removeEventListener('sc:lock', bump)
    }
  }, [])
  useEffect(() => {
    if (!open || !entry) return
    queueMicrotask(() => previewRef.current?.querySelector<HTMLButtonElement>('button')?.focus())
  }, [open, entry])
  useEffect(() => {
    setImageOverride(null)
    setImageGaveUp(false)
    setVideoGaveUp(false)
    setCwd('')
    setUnlockOpen(false)
    videoRef.current?.pause()
  }, [previewKey])
  useEffect(() => {
    let cancelled = false
    let token: string | null = null
    let objectUrl: string | null = null
    setMediaUrl(null); setMediaKind('idle')
    if (!entry || (body.kind !== 'image' && body.kind !== 'video') || encryptionQuery.isPending || encryption === null || !unlocked) return () => undefined
    setMediaKind('loading')
    void (async () => {
      const contentType = mimeTypeOf(entry.name) ?? 'application/octet-stream'
      const registration = await swReady()
      if (cancelled) return
      if (registration?.active) {
        const registered = registerMediaSource(entry, encryption.salt, contentType)
        token = registered.token; setMediaUrl(registered.url); setMediaKind('ready'); return
      }
      if (body.kind === 'video') { setMediaKind('no-worker-video'); return }
      if (entry.size > MAX_ENCRYPTABLE_BYTES) { setMediaKind('too-large'); return }
      try {
        const response = await fetch(api.contentUrl(entry))
        if (!response.ok) throw new Error(`HTTP ${response.status}`)
        const plaintext = await decryptDownload(new Uint8Array(await response.arrayBuffer()), encryption.salt)
        if (cancelled) return
        objectUrl = URL.createObjectURL(new Blob([plaintext.buffer as ArrayBuffer], { type: contentType }))
        setMediaUrl(objectUrl); setMediaKind('ready')
      } catch { if (!cancelled) setMediaKind('failed') }
    })()
    return () => { cancelled = true; if (token) releaseMediaSource(token); if (objectUrl) URL.revokeObjectURL(objectUrl) }
  }, [body.kind, encryption, encryptionQuery.isPending, entry, unlocked])

  useEffect(() => {
    if (!open) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || document.querySelector('mdui-dialog[open]:not(.sc-preview-dialog)')) return
      if (event.key === 'Escape') { event.preventDefault(); if (archiveListing && cwd) setCwd(cwd.slice(0, Math.max(0, cwd.lastIndexOf('/')))); else onClose() }
      else if (event.key === 'ArrowLeft' && hasPrev) { event.preventDefault(); onPrev() }
      else if (event.key === 'ArrowRight' && hasNext) { event.preventDefault(); onNext() }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [archiveListing, cwd, hasNext, hasPrev, onClose, onNext, onPrev, open])

  if (!entry) return null
  const imageUrl = body.kind === 'image' && !imageGaveUp ? imageOverride ?? (encryption ? mediaKind === 'ready' ? mediaUrl : null : entry.preview?.available && extensionOf(entry.name) !== 'svg' ? api.thumbUrl(entry, PREVIEW_DIM) || api.contentUrl(entry) : api.contentUrl(entry)) : null
  const videoUrl = body.kind === 'video' && !videoGaveUp ? (encryption ? mediaKind === 'ready' ? mediaUrl : null : api.contentUrl(entry)) : null
  const loading = (body.kind === 'text' && textQuery.isPending) || (body.kind === 'archive' && !locked && (encryptionQuery.isPending || archivePending)) || ((body.kind === 'image' || body.kind === 'video') && !locked && encryption !== null && mediaKind === 'loading')
  const failed = body.kind === 'image' && imageGaveUp ? t('preview.failed') : body.kind === 'video' && videoGaveUp ? t('preview.failed') : mediaKind === 'too-large' ? t('preview.encrypted_too_large_to_buffer') : mediaKind === 'no-worker-video' ? t('preview.encrypted_video_needs_worker') : mediaKind === 'failed' || textQuery.error || archiveError ? t('preview.failed') : null
  const failedDetail = textQuery.error instanceof ApiError ? textQuery.error.detail?.reason : archiveError instanceof ApiError ? archiveError.detail?.reason : null
  const level = archiveListing ? levelOf(archiveListing.entries, cwd) : []
  const crumbs = cwd ? cwd.split('/').map((label, index, all) => ({ label, path: all.slice(0, index + 1).join('/') })) : []
  const archiveUp = () => setCwd((current) => { const cut = current.lastIndexOf('/'); return cut < 0 ? '' : current.slice(0, cut) })

  return <>
    <DialogFrame open={open} title={entry.name} className="sc-preview-dialog" onClose={onClose}>
      <div ref={previewRef} className="sc-preview">
        <header className="sc-preview__bar">
          <button type="button" aria-label={t('common.close')} onClick={onClose}><Icon name="close" /></button>
          <span className="sc-preview__name" title={entry.name}>{entry.name}</span>
          <span className="sc-preview__size">{formatEntrySize(entry.size, encryption !== null)}</span>
          <span className="sc-preview__gap"></span>
          {body.kind === 'text' || body.kind === 'too-large-text' ? <button type="button" aria-label={t('browse.open_text_editor')} onClick={() => onEdit(entry)}><Icon name="edit_document" /></button> : null}
          <button type="button" aria-label={t('common.download')} onClick={() => onDownload(entry)}><Icon name="download" /></button>
        </header>
        <div className="sc-preview__body">
          <div className={`sc-preview__nav ${hasPrev ? '' : 'sc-preview__nav--empty'}`}><button type="button" aria-label={t('preview.previous')} disabled={!hasPrev} onClick={onPrev}><Icon name="chevron_left" /></button></div>
          <div className="sc-preview__stage">
            {loading ? <mdui-circular-progress></mdui-circular-progress> : videoUrl ? <div className="sc-preview__video-container"><video ref={videoRef} className="sc-preview__video" src={videoUrl} controls autoPlay playsInline preload="metadata" onError={() => setVideoGaveUp(true)}><track kind="captions" />{t('preview.cannot_preview')}</video></div> : imageUrl ? <img className="sc-preview__image" src={imageUrl} alt={entry.name} onError={() => { const own = api.contentUrl(entry); if (!encryption && imageUrl !== own) setImageOverride(own); else setImageGaveUp(true) }} /> : textQuery.data?.content !== undefined ? <pre className="sc-preview__text">{textQuery.data.content}</pre> : archiveListing ? <div className="sc-preview__archive"><p className="sc-preview__archive-count">{tp('preview.archive_entries', level.length)} {archiveListing.skipped ? <span className="sc-preview__archive-skipped">{tp('preview.archive_skipped', archiveListing.skipped)}</span> : null} {archiveListing.truncated ? <span className="sc-preview__archive-skipped">{t('preview.archive_truncated', { limit: archiveListing.limit })}</span> : null}</p><nav className="sc-preview__crumbs" aria-label={t('preview.archive_location')}><button type="button" className="sc-preview__crumb" disabled={!cwd} onClick={() => setCwd('')}>{entry.name}</button>{crumbs.map((crumb, index) => <span key={crumb.path}><span className="sc-preview__crumb-sep" aria-hidden="true">/</span><button type="button" className="sc-preview__crumb" disabled={index === crumbs.length - 1} onClick={() => setCwd(crumb.path)}>{crumb.label}</button></span>)}</nav>{level.length === 0 ? <p className="sc-preview__archive-empty">{t('preview.archive_empty')}</p> : <ul className="sc-preview__archive-list">{cwd ? <li><button type="button" className="sc-preview__archive-row sc-preview__archive-row--up" onClick={archiveUp}><Icon name="chevron_left" /><span>{t('preview.archive_up')}</span></button></li> : null}{level.map((row) => <li key={row.path}>{row.kind === 'dir' ? <button type="button" className="sc-preview__archive-row sc-preview__archive-row--dir" onClick={() => setCwd(row.path)}><Icon name="folder" /><span className="sc-preview__archive-name">{row.label}</span><span>{t('details.folder')}</span></button> : <div className="sc-preview__archive-row"><Icon name="draft" /><span className="sc-preview__archive-name">{row.label}</span><span>{t('details.file')}</span><span>{formatBytes(row.size)}</span></div>}</li>)}</ul>}</div> : <div className="sc-preview__card" role={failed ? 'alert' : undefined}><p className="sc-preview__card-title">{locked ? t('preview.locked_title') : t('preview.cannot_preview')}</p><p className="sc-preview__card-reason">{locked ? t('preview.locked_reason') : failed ?? (body.kind === 'too-large-text' ? t('preview.too_large_for_text') : t('preview.no_preview'))}</p>{typeof failedDetail === 'string' ? <p className="sc-preview__card-detail">{failedDetail}</p> : null}<div className="sc-preview__card-actions">{locked ? <Button onClick={() => setUnlockOpen(true)}>{t('encryption.unlock')}</Button> : <><Button onClick={() => onDownload(entry)}>{t('common.download')}</Button>{body.kind === 'too-large-text' ? <Button variant="outlined" onClick={() => onEdit(entry)}>{t('browse.open_text_editor')}</Button> : null}</>}</div></div>}
          </div>
          <div className={`sc-preview__nav ${hasNext ? '' : 'sc-preview__nav--empty'}`}><button type="button" aria-label={t('preview.next')} disabled={!hasNext} onClick={onNext}><Icon name="chevron_right" /></button></div>
        </div>
      </div>
    </DialogFrame>
    <UnlockShareDialog open={unlockOpen} salt={encryption?.salt ?? ''} verifier={encryption?.verifier ?? ''} onUnlock={() => { setUnlockOpen(false); setUnlockGeneration((value) => value + 1) }} onClose={() => setUnlockOpen(false)} />
  </>
}
