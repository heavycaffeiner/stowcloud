import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { dropUpload, getShare, ShareNotFoundError, SharePasswordRequiredError, SharePathGoneError, ShareTooLargeError, shareDownloadUrl, shareZipUrl, unlockShare } from '../../../lib/api/share'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { useRouteStore } from '../../use-route-store'

export type DropItem = { id: number; file: File; status: 'pending' | 'uploading' | 'done' | 'error'; storedAs: string; failure: 'too_large' | 'failed' | null }
type PublicShareState = { password: string; unlockError: string | null; unlocking: boolean; queue: DropItem[]; uploading: boolean; pathGone: boolean }

export function usePublicShare() {
  const { t } = useI18n()
  const { token = '' } = useParams()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const path = searchParams.get('path') ?? ''
  const queueRef = useRef<DropItem[]>([])
  const nextQueueId = useRef(0)
  const uploadingRef = useRef(false)
  const [state, setState] = useRouteStore<PublicShareState>({ password: '', unlockError: null, unlocking: false, queue: [], uploading: false, pathGone: false })
  const { password, unlockError, unlocking, queue, uploading, pathGone } = state
  const fileInput = useRef<HTMLInputElement>(null)
  const share = useQuery({ queryKey: ['share', token, path], queryFn: () => getShare(token, path), retry: false, enabled: token.length > 0 })
  const info = share.data
  const needsPassword = share.error instanceof SharePasswordRequiredError
  const retryableError = Boolean(share.error && !needsPassword && !(share.error instanceof ShareNotFoundError) && !(share.error instanceof SharePathGoneError))
  const crumbs = useMemo(() => path.split('/').filter(Boolean).map((name, index, parts) => ({ name, path: parts.slice(0, index + 1).join('/') })), [path])
  useEffect(() => {
    if (!(share.error instanceof SharePathGoneError)) return
    setState({ pathGone: true })
    void navigate(`/s/${encodeURIComponent(token)}`, { replace: true })
  }, [navigate, setState, share.error, token])
  const unlock = async (event: React.FormEvent): Promise<void> => {
    event.preventDefault()
    if (unlocking || !password) return
    setState({ unlocking: true, unlockError: null })
    try {
      const ok = await unlockShare(token, password)
      if (!ok) { setState({ unlockError: t('common.incorrect_password') }); return }
      setState({ password: '' })
      await share.refetch()
    } catch { setState({ unlockError: t('public_share.could_not_verify_try_again') }) }
    finally { setState({ unlocking: false }) }
  }
  const openFolder = (nextPath: string): void => { setState({ pathGone: false }); void navigate(`/s/${encodeURIComponent(token)}${nextPath ? `?path=${encodeURIComponent(nextPath)}` : ''}`) }
  const childPath = (name: string): string => path ? `${path}/${name}` : name
  const download = (downloadPath: string): void => { window.location.href = shareDownloadUrl(token, downloadPath) }
  const downloadFolder = (): void => { window.location.href = shareZipUrl(token, path) }
  const runQueue = async (): Promise<void> => {
    if (uploadingRef.current) return
    uploadingRef.current = true; setState({ uploading: true })
    try {
      let index = 0
      while (index < queueRef.current.length) {
        const item = queueRef.current[index++]
        if (!item || item.status !== 'pending') continue
        item.status = 'uploading'; setState({ queue: [...queueRef.current] })
        try { item.storedAs = await dropUpload(token, item.file); item.status = 'done' }
        catch (error) { item.status = 'error'; item.failure = error instanceof ShareTooLargeError ? 'too_large' : 'failed' }
        setState({ queue: [...queueRef.current] })
      }
    } finally {
      uploadingRef.current = false; setState({ uploading: false })
      if (queueRef.current.some((item) => item.status === 'pending')) queueMicrotask(() => void runQueue())
    }
  }
  const pickFiles = (event: React.ChangeEvent<HTMLInputElement>): void => {
    const files = Array.from(event.target.files ?? []); event.target.value = ''; if (!files.length) return
    const limit = info?.maxUploadBytes ?? null
    const additions = files.map((file): DropItem => ({ id: nextQueueId.current++, file, status: limit !== null && file.size > limit ? 'error' : 'pending', storedAs: '', failure: limit !== null && file.size > limit ? 'too_large' : null }))
    queueRef.current = [...queueRef.current, ...additions]; setState({ queue: [...queueRef.current] }); queueMicrotask(() => void runQueue())
  }
  const retryUpload = (index: number): void => { const item = queueRef.current[index]; if (!item || item.status !== 'error') return; item.status = 'pending'; item.storedAs = ''; item.failure = null; setState({ queue: [...queueRef.current] }); queueMicrotask(() => void runQueue()) }
  const removeFailedUpload = (index: number): void => { const item = queueRef.current[index]; if (!item || item.status !== 'error') return; queueRef.current = queueRef.current.filter((_, itemIndex) => itemIndex !== index); setState({ queue: [...queueRef.current] }) }
  return { t, token, path, fileInput, state: { password, unlockError, unlocking, queue, uploading, pathGone }, setState, share, info, needsPassword, retryableError, crumbs, doneCount: queue.filter((item) => item.status === 'done').length, unlock, openFolder, childPath, download, downloadFolder, pickFiles, retryUpload, removeFailedUpload }
}
