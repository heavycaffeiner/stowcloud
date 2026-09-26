import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { getShare, ShareNotFoundError, SharePasswordRequiredError, SharePathGoneError, shareDownloadUrl, shareZipUrl, unlockShare } from '../../../lib/api/share'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { useRouteStore } from '../../use-route-store'
import { createPublicShareQueue, type DropItem, type PublicShareQueue } from './public-share-queue'
type PublicShareState = { password: string; unlockError: string | null; unlocking: boolean; queue: DropItem[]; uploading: boolean; pathGone: boolean }

export function usePublicShare() {
  const { t } = useI18n()
  const { token = '' } = useParams()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const path = searchParams.get('path') ?? ''
  const [state, setState] = useRouteStore<PublicShareState>({ password: '', unlockError: null, unlocking: false, queue: [], uploading: false, pathGone: false })
  const { password, unlockError, unlocking, queue: queuedItems, uploading, pathGone } = state
  const queueMapRef = useRef(new Map<string, PublicShareQueue>())
  const activeTokenRef = useRef(token)
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
  activeTokenRef.current = token
  let queue = queueMapRef.current.get(token)
  if (!queue) {
    queue = createPublicShareQueue({ token, setState: (next) => setState(next), isActive: () => activeTokenRef.current === token })
    queueMapRef.current.set(token, queue)
  }
  useEffect(() => {
    setState({ queue: [...queue.items], uploading: queue.uploading })
  }, [queue, setState])
  const pickFiles = (event: React.ChangeEvent<HTMLInputElement>): void => {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ''
    if (!files.length) return
    queue.add(files, info?.maxUploadBytes ?? null)
  }
  const retryUpload = (index: number): void => queue.retry(index)
  const removeFailedUpload = (index: number): void => queue.removeFailed(index)
  return { t, token, path, fileInput, state: { password, unlockError, unlocking, queue: queuedItems, uploading, pathGone }, setState, share, info, needsPassword, retryableError, crumbs, doneCount: queuedItems.filter((item) => item.status === 'done').length, unlock, openFolder, childPath, download, downloadFolder, pickFiles, retryUpload, removeFailedUpload }
}
