import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import {
  dropUpload,
  getShare,
  ShareNotFoundError,
  SharePasswordRequiredError,
  SharePathGoneError,
  ShareTooLargeError,
  shareDownloadUrl,
  shareZipUrl,
  unlockShare
} from '../../lib/api/share'
import { formatBytes } from '../../lib/format/bytes'
import { useI18n } from '../../lib/i18n/use-i18n'
import { Button } from '../../lib/ui/Button'
import { TextField } from '../../lib/ui/TextField'
import { VirtualList } from '../../lib/ui/VirtualList'
import { useDocumentTitle } from '../use-document-title'
import './public-share.css'

type DropItem = {
  id: number
  file: File
  status: 'pending' | 'uploading' | 'done' | 'error'
  storedAs: string
  failure: 'too_large' | 'failed' | null
}

export function PublicSharePage() {
  const { t } = useI18n()
  const { token = '' } = useParams()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const path = searchParams.get('path') ?? ''
  const [password, setPassword] = useState('')
  const [unlockError, setUnlockError] = useState<string | null>(null)
  const [unlocking, setUnlocking] = useState(false)
  const [queue, setQueue] = useState<DropItem[]>([])
  const queueRef = useRef<DropItem[]>([])
  const nextQueueId = useRef(0)
  const uploadingRef = useRef(false)
  const [uploading, setUploading] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const [pathGone, setPathGone] = useState(false)

  const share = useQuery({
    queryKey: ['share', token, path],
    queryFn: () => getShare(token, path),
    retry: false,
    enabled: token.length > 0
  })
  const info = share.data
  const needsPassword = share.error instanceof SharePasswordRequiredError
  const retryableError = Boolean(share.error && !needsPassword && !(share.error instanceof ShareNotFoundError) && !(share.error instanceof SharePathGoneError))
  const title = info?.label ?? info?.name ?? t('common.share_links')
  useDocumentTitle(`${title} - Stowcloud`)

  useEffect(() => {
    if (!(share.error instanceof SharePathGoneError)) return
    setPathGone(true)
    void navigate(`/s/${encodeURIComponent(token)}`, { replace: true })
  }, [navigate, share.error, token])

  const crumbs = useMemo(() => {
    const parts = path.split('/').filter(Boolean)
    return parts.map((name, index) => ({ name, path: parts.slice(0, index + 1).join('/') }))
  }, [path])
  const doneCount = queue.filter((item) => item.status === 'done').length

  const unlock = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    if (unlocking || !password) return
    setUnlocking(true)
    setUnlockError(null)
    try {
      const ok = await unlockShare(token, password)
      if (!ok) {
        setUnlockError(t('common.incorrect_password'))
        return
      }
      setPassword('')
      await share.refetch()
    } catch {
      setUnlockError(t('public_share.could_not_verify_try_again'))
    } finally {
      setUnlocking(false)
    }
  }

  const openFolder = (nextPath: string): void => {
    setPathGone(false)
    const suffix = nextPath ? `?path=${encodeURIComponent(nextPath)}` : ''
    void navigate(`/s/${encodeURIComponent(token)}${suffix}`)
  }
  const childPath = (name: string): string => path ? `${path}/${name}` : name
  const download = (downloadPath: string): void => {
    window.location.href = shareDownloadUrl(token, downloadPath)
  }
  const downloadFolder = (): void => {
    window.location.href = shareZipUrl(token, path)
  }

  const runQueue = async (): Promise<void> => {
    if (uploadingRef.current) return
    uploadingRef.current = true
    setUploading(true)
    try {
      let index = 0
      while (index < queueRef.current.length) {
        const item = queueRef.current[index++]
        if (!item || item.status !== 'pending') continue
        item.status = 'uploading'
        setQueue([...queueRef.current])
        try {
          item.storedAs = await dropUpload(token, item.file)
          item.status = 'done'
        } catch (error) {
          item.status = 'error'
          item.failure = error instanceof ShareTooLargeError ? 'too_large' : 'failed'
        }
        setQueue([...queueRef.current])
      }
    } finally {
      uploadingRef.current = false
      setUploading(false)
      if (queueRef.current.some((item) => item.status === 'pending')) queueMicrotask(() => void runQueue())
    }
  }

  const pickFiles = (event: React.ChangeEvent<HTMLInputElement>): void => {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ''
    if (!files.length) return
    const limit = info?.maxUploadBytes ?? null
    const additions = files.map((file): DropItem => ({
      id: nextQueueId.current++,
      file,
      status: limit !== null && file.size > limit ? 'error' : 'pending',
      storedAs: '',
      failure: limit !== null && file.size > limit ? 'too_large' : null
    }))
    queueRef.current = [...queueRef.current, ...additions]
    setQueue([...queueRef.current])
    queueMicrotask(() => void runQueue())
  }

  const retryUpload = (index: number): void => {
    const item = queueRef.current[index]
    if (!item || item.status !== 'error') return
    item.status = 'pending'
    item.storedAs = ''
    item.failure = null
    setQueue([...queueRef.current])
    queueMicrotask(() => void runQueue())
  }
  const removeFailedUpload = (index: number): void => {
    const item = queueRef.current[index]
    if (!item || item.status !== 'error') return
    queueRef.current = queueRef.current.filter((_, itemIndex) => itemIndex !== index)
    setQueue([...queueRef.current])
  }

  let loadError: string | null = null
  if (share.error instanceof ShareNotFoundError) loadError = t('public_share.link_has_expired_or_does')
  else if (share.error && !needsPassword && !(share.error instanceof SharePathGoneError)) loadError = t('public_share.could_not_load')

  return (
    <main className="sc-public-share">
      <header className="sc-public-share__header"><strong>Stowcloud</strong>{t('public_share.public_share_link')}</header>
      {share.isPending ? <p className="sc-public-share__status">{t('common.loading')}</p> : null}
      {loadError ? (
        <section role="alert" className="sc-public-share__state sc-public-share__state--error">
          <p>{loadError}</p>
          {retryableError ? <Button variant="outlined" loading={share.isFetching} onClick={() => void share.refetch()}>{t('public_share.retry')}</Button> : null}
        </section>
      ) : null}
      {needsPassword ? (
        <form className="sc-public-share__unlock" onSubmit={unlock}>
          <h1>{t('public_share.link_password_protected')}</h1>
          <TextField value={password} label={t('common.password')} type="password" autoFocus autoComplete="off" error={unlockError} onValueChange={setPassword} />
          <div className="sc-public-share__unlock-actions"><Button type="submit" disabled={!password} loading={unlocking}>{t('public_share.unlock')}</Button></div>
        </form>
      ) : null}
      {info ? (
        <section>
          <h1 className="sc-public-share__title">{info.label || info.name}</h1>
          {crumbs.length > 0 ? (
            <nav className="sc-public-share__crumbs" aria-label={t('public_share.location')}>
              <ol>
                <li><button type="button" className="sc-public-share__crumb sc-focus-ring" onClick={() => openFolder('')}>{t('public_share.top_folder')}</button></li>
                {crumbs.map((crumb, index) => (
                  <li key={crumb.path}>
                    <span className="sc-public-share__crumb-sep" aria-hidden="true">/</span>
                    {index === crumbs.length - 1 ? <span aria-current="page">{crumb.name}</span> : <button type="button" className="sc-public-share__crumb sc-focus-ring" onClick={() => openFolder(crumb.path)}>{crumb.name}</button>}
                  </li>
                ))}
              </ol>
            </nav>
          ) : null}
          {pathGone ? <p className="sc-public-share__status sc-public-share__status--error">{t('public_share.folder_gone')}</p> : null}
          {info.isDrop ? (
            <>
              <p className="sc-public-share__status">{t('public_share.link_upload_only_nothing_can')}</p>
              <div className="sc-public-share__drop">
                <input ref={fileInput} className="sc-public-share__file" type="file" multiple onChange={pickFiles} />
                <Button disabled={uploading} onClick={() => fileInput.current?.click()}>{t('share_drop.pick_files')}</Button>
                {info.maxUploadBytes !== null ? <p className="sc-public-share__status">{t('share_drop.limit_hint', { size: formatBytes(info.maxUploadBytes) })}</p> : null}
              </div>
              {queue.length > 0 ? (
                <>
                  <p className="sc-public-share__status">{t('share_drop.uploading', { done: doneCount, total: queue.length })}</p>
                  <VirtualList
                    className="sc-public-share__list"
                    items={queue}
                    itemKey={(item) => item.id}
                    estimateSize={56}
                    itemProps={() => ({ className: 'sc-public-share__row' })}
                    renderItem={(item, index) => (
                      <>
                        <span className="sc-filename sc-public-share__name">{item.file.name}</span>
                        <span className={`sc-public-share__size${item.status === 'error' ? ' sc-public-share__status--error' : ''}`}>
                          {item.status === 'done' ? t('share_drop.uploaded_as', { name: item.storedAs }) : item.failure === 'too_large' ? t('share_drop.too_large') : item.failure === 'failed' ? t('share_drop.failed') : item.status === 'uploading' ? t('share_drop.in_progress') : formatBytes(item.file.size)}
                        </span>
                        {item.status === 'error' ? <div className="sc-public-share__row-actions"><Button variant="text" onClick={() => retryUpload(index)}>{t('share_drop.retry')}</Button><Button variant="text" onClick={() => removeFailedUpload(index)}>{t('share_drop.remove')}</Button></div> : null}
                      </>
                    )}
                  />
                </>
              ) : null}
            </>
          ) : !info.isDir ? (
            <>
              <p className="sc-public-share__status">{formatBytes(info.size)}</p>
              {info.canDownload ? <Button onClick={() => download(path)}>{t('common.download')}</Button> : null}
            </>
          ) : (
            <>
              {(info.entries ?? []).length > 0 ? <VirtualList
                key={path}
                className="sc-public-share__list"
                items={info.entries ?? []}
                itemKey={(entry) => entry.name}
                estimateSize={56}
                itemProps={() => ({ className: 'sc-public-share__row' })}
                renderItem={(entry) => (
                  <>
                    {entry.kind === 'dir' ? <button type="button" className="sc-filename sc-public-share__name sc-public-share__folder sc-focus-ring" aria-label={t('public_share.open_folder', { name: entry.name })} onClick={() => openFolder(childPath(entry.name))}>{entry.name}</button> : <span className="sc-filename sc-public-share__name">{entry.name}</span>}
                    <span className="sc-public-share__size">{entry.kind === 'dir' ? '-' : formatBytes(entry.size)}</span>
                    {entry.kind === 'file' && info.canDownload ? <Button variant="text" onClick={() => download(childPath(entry.name))}>{t('common.download')}</Button> : null}
                  </>
                )}
              /> : <ul className="sc-public-share__list"><li className="sc-public-share__row sc-public-share__row--empty">{t('public_share.empty')}</li></ul>}
              {info.canDownload ? <Button onClick={downloadFolder}>{t('public_share.download_folder')}</Button> : null}
            </>
          )}
        </section>
      ) : null}
    </main>
  )
}
