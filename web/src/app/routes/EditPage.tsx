import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useBlocker, useNavigate, useParams } from 'react-router-dom'
import { normalizePath, parentOf } from '../../lib/api/path-utils'
import { describeApiError } from '../../lib/api/error-text'
import { isUnlocked, MAX_ENCRYPTABLE_BYTES, openSessionValue, sealSessionValue } from '../../lib/crypto/e2ee'
import { formatBytes } from '../../lib/format/bytes'
import { useI18n } from '../../lib/i18n/use-i18n'
import { fileContentQuery, shareEncryptionQuery, statQuery, writeFileMutation } from '../../lib/query/files'
import { keys } from '../../lib/query/keys'
import { Button } from '../../lib/ui/Button'
import { CodeEditor, type CodeEditorHandle } from '../../lib/ui/CodeEditor'
import { EditConflictDialog } from '../../lib/ui/EditConflictDialog'
import { Snackbar } from '../../lib/ui/Snackbar'
import { UnlockShareDialog } from '../../lib/ui/UnlockShareDialog'
import { Icon } from '../../lib/ui/Icon'
import { IconButton } from '../../lib/ui/IconButton'
import { MiddleEllipsis } from '../../lib/ui/MiddleEllipsis'
import { useDocumentTitle } from '../use-document-title'
import './editor.css'


export function EditPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { '*': rawPath } = useParams()
  const path = normalizePath(`/${rawPath ?? ''}`)
  const filename = path.split('/').filter(Boolean).at(-1) ?? path
  const stat = useQuery(statQuery(path))
  const isDir = stat.data?.kind === 'dir'
  const encryption = useQuery(shareEncryptionQuery(path, stat.data !== undefined && !isDir))
  const share = encryption.data ?? null
  const unlocked = share === null || isUnlocked(share.salt)
  const locked = Boolean(stat.data && !isDir && share && !unlocked)
  const entry = stat.data && !isDir ? stat.data : null
  const contentEnabled = Boolean(entry && !encryption.isPending && !encryption.error && unlocked)
  const contentQuery = useQuery({ ...fileContentQuery(entry, unlocked), enabled: contentEnabled })
  const saveMutation = useMutation(writeFileMutation())
  const editorRef = useRef<CodeEditorHandle>(null)
  const [draft, setDraft] = useState<string | null>(null)
  const [sealedDraft, setSealedDraft] = useState<{ salt: string; bytes: Uint8Array } | null>(null)
  const [baselineEtag, setBaselineEtag] = useState<string | null>(null)
  const [loadedPath, setLoadedPath] = useState<string | null>(null)
  const [awaitingBaseline, setAwaitingBaseline] = useState(true)
  const [unlockRequested, setUnlockRequested] = useState(true)
  const [conflictOpen, setConflictOpen] = useState(false)
  const [conflictWeak, setConflictWeak] = useState(false)
  const [leaveDialogOpen, setLeaveDialogOpen] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [snackbar, setSnackbar] = useState<string | null>(null)
  const [sessionRevision, setSessionRevision] = useState(0)
  const [languageName, setLanguageName] = useState<string | null>(null)
  const content = draft ?? contentQuery.data?.content ?? ''
  const dirty = sealedDraft !== null || (draft !== null && draft !== (contentQuery.data?.content ?? ''))
  const blocker = useBlocker(dirty && loadedPath === path ? ({ currentLocation, nextLocation }) => currentLocation.pathname !== nextLocation.pathname : false)
  const readOnly = entry ? !entry.perms.write : true
  const canSave = Boolean(unlocked && dirty && baselineEtag !== null && !saveMutation.isPending && entry?.perms.write)
  useDocumentTitle(`${filename} - Stowcloud`)

  useEffect(() => {
    if (path === loadedPath) return
    setLoadedPath(path)
    setBaselineEtag(null)
    setAwaitingBaseline(true)
    setDraft(null)
    setLanguageName(null)
    setSealedDraft((previous) => {
      previous?.bytes.fill(0)
      return null
    })
  }, [loadedPath, path])

  useEffect(() => {
    const etag = stat.data?.etag
    if (!awaitingBaseline || !etag || !contentQuery.data || path !== loadedPath) return
    const expected = queryClient.getQueryData(keys.pathContent(stat.data?.path ?? path, etag, unlocked))
    if (expected !== contentQuery.data) return
    setBaselineEtag(etag)
    setAwaitingBaseline(false)
  }, [awaitingBaseline, contentQuery.data, loadedPath, path, queryClient, stat.data?.etag, stat.data?.path, unlocked])

  useEffect(() => {
    const beforeLock = (event: Event) => {
      const custom = event as CustomEvent<{ salt?: string }>
      const salt = custom.detail?.salt
      if (!dirty || draft === null || !salt || share?.salt !== salt) return
      const plaintext = new TextEncoder().encode(draft)
      try {
        const bytes = sealSessionValue(plaintext, salt)
        setSealedDraft((previous) => {
          previous?.bytes.fill(0)
          return { salt, bytes }
        })
        setDraft(null)
      } catch (error) {
        setSnackbar(describeApiError(error, t('common.could_not_save')))
      } finally {
        plaintext.fill(0)
      }
    }
    const onUnlock = (event: Event) => {
      const custom = event as CustomEvent<{ salt?: string }>
      const salt = custom.detail?.salt
      const sealed = sealedDraft
      if (salt && sealed?.salt === salt) {
        let plaintext: Uint8Array | null = null
        try {
          plaintext = openSessionValue(sealed.bytes, salt)
          setDraft(new TextDecoder().decode(plaintext))
          sealed.bytes.fill(0)
          setSealedDraft(null)
        } catch (error) {
          setSnackbar(describeApiError(error, t('editor.could_not_load_file')))
        } finally {
          plaintext?.fill(0)
        }
      }
    }
    const onLock = () => {
      setUnlockRequested(sealedDraft === null)
      setConflictOpen(false)
      setLeaveDialogOpen(false)
      queryClient.removeQueries({ queryKey: ['path', entry?.path ?? path, 'content'] })
    }
    window.addEventListener('sc:before-lock', beforeLock)
    window.addEventListener('sc:unlock', onUnlock)
    window.addEventListener('sc:lock', onLock)
    return () => {
      window.removeEventListener('sc:before-lock', beforeLock)
      window.removeEventListener('sc:unlock', onUnlock)
      window.removeEventListener('sc:lock', onLock)
    }
  }, [dirty, draft, entry?.path, path, queryClient, sealedDraft, share?.salt, t])

  useEffect(() => {
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      if (!dirty) return
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [dirty])

  useEffect(() => {
    if (blocker.state === 'blocked') {
      setLeaveDialogOpen(true)
      return
    }
    setLeaveDialogOpen(false)
  }, [blocker.state])

  function focusEditor(): void {
    editorRef.current?.focus()
    queueMicrotask(() => editorRef.current?.focus())
  }

  async function save(): Promise<boolean> {
    if (!canSave || !entry) return false
    setSaveError(null)
    try {
      const latest = await queryClient.fetchQuery({ ...statQuery(path), staleTime: 0 })
      if (baselineEtag !== null && latest.etag !== baselineEtag) {
        setConflictWeak(latest.etag_weak)
        setConflictOpen(true)
        return false
      }
      const savedContent = content
      const updated = await saveMutation.mutateAsync({ path, content: savedContent })
      queryClient.setQueryData(keys.pathContent(updated.path, updated.etag, true), { content: savedContent })
      queryClient.setQueryData(keys.pathStat(path), updated)
      setBaselineEtag(updated.etag)
      setAwaitingBaseline(false)
      setDraft((current) => current === savedContent ? null : current)
      setSnackbar(t('common.saved'))
      focusEditor()
      return true
    } catch (error) {
      setSaveError(describeApiError(error, t('common.could_not_save')))
      return false
    }
  }

  async function overwriteAfterConflict(): Promise<void> {
    setConflictOpen(false)
    if (!entry) return
    try {
      const savedContent = content
      const updated = await saveMutation.mutateAsync({ path, content: savedContent })
      queryClient.setQueryData(keys.pathContent(updated.path, updated.etag, true), { content: savedContent })
      queryClient.setQueryData(keys.pathStat(path), updated)
      setBaselineEtag(updated.etag)
      setAwaitingBaseline(false)
      setDraft((current) => current === savedContent ? null : current)
      setSnackbar(t('editor.overwritten'))
      focusEditor()
      if (blocker.state === 'blocked') blocker.proceed()
    } catch (error) {
      setSnackbar(describeApiError(error, t('common.could_not_save')))
    }
  }

  async function reloadAfterConflict(): Promise<void> {
    setConflictOpen(false)
    try {
      const freshMeta = await queryClient.fetchQuery({ ...statQuery(path), staleTime: 0 })
      await queryClient.fetchQuery(fileContentQuery(freshMeta, unlocked))
      setBaselineEtag(freshMeta.etag)
      setAwaitingBaseline(false)
      setDraft(null)
      setSnackbar(t('editor.reloaded_newer_version'))
      focusEditor()
    } catch (error) {
      setSnackbar(describeApiError(error, t('editor.could_not_load_file')))
    }
  }

  function discardAndLeave(): void {
    setDraft(null)
    setSealedDraft((previous) => { previous?.bytes.fill(0); return null })
    setLeaveDialogOpen(false)
    if (blocker.state === 'blocked') blocker.proceed()
  }

  async function saveAndLeave(): Promise<void> {
    if (await save() && blocker.state === 'blocked') {
      setLeaveDialogOpen(false)
      blocker.proceed()
    }
  }

  if (isDir) return <main className="sc-edit"><p className="sc-edit__error" role="alert">{t('editor.folder_cannot_opened_editor')}</p></main>
  const loadError = stat.error ? describeApiError(stat.error, t('editor.could_not_load_file')) : encryption.error ? describeApiError(encryption.error, t('editor.could_not_load_file')) : contentQuery.error ? describeApiError(contentQuery.error, t('editor.could_not_load_file')) : null

  const loading = stat.isPending || (Boolean(entry) && encryption.isPending) || (contentEnabled && contentQuery.isPending)
  void sessionRevision

  return (
    <main className="sc-edit">
      <header className="sc-edit__toolbar">
        <IconButton label={t('editor.go_back')} onClick={() => void navigate(`/b${parentOf(path)}`)}><Icon name="chevron_left" /></IconButton>
        <span className="sc-edit__file-icon" aria-hidden="true"><Icon name="edit_document" /></span>
        <div className="sc-edit__identity">
          <div className="sc-edit__title">
            <MiddleEllipsis name={filename} className="sc-edit__filename" />
            {dirty ? <span className="sc-edit__badge sc-edit__badge--dirty" title={t('editor.unsaved_changes')}>{t('editor.unsaved_changes')}</span> : null}
          </div>
          <div className="sc-edit__details">
            <span className="sc-edit__language">{languageName ?? t('editor.plain_text')}</span>
            {entry ? <span className="sc-edit__meta">{formatBytes(entry.size)}</span> : null}
            {readOnly && entry ? <span className="sc-edit__badge sc-edit__badge--readonly">{t('common.read_only')}</span> : null}
          </div>
        </div>
        <div className="sc-edit__actions"><Button loading={saveMutation.isPending} disabled={!canSave} onClick={() => void save()}>{t('editor.save_ctrl_s')}</Button></div>
      </header>
      <div className="sc-edit__body">
        {locked ? <div className="sc-edit__locked" role="status"><p>{t('encryption.unlock_hint')}</p><Button onClick={() => setUnlockRequested(true)}>{t('encryption.unlock')}</Button></div> : loading ? <div className="sc-edit__loading"><mdui-circular-progress></mdui-circular-progress></div> : loadError ? <p className="sc-edit__error" role="alert">{loadError}</p> : <CodeEditor ref={editorRef} value={content} filename={filename} readOnly={readOnly} maxBytes={MAX_ENCRYPTABLE_BYTES} onChange={setDraft} onLimit={() => setSnackbar(t('editor.file_too_large_to_edit'))} onSave={() => void save()} onLanguageChange={setLanguageName} />}
      </div>
      {saveError ? <p className="sc-edit__error" role="alert">{saveError}</p> : null}
      <EditConflictDialog open={conflictOpen} name={filename} weak={conflictWeak} onClose={() => { setConflictOpen(false); focusEditor() }} onReload={() => void reloadAfterConflict()} onOverwrite={() => void overwriteAfterConflict()} />
      <UnlockShareDialog open={Boolean(locked && unlockRequested)} salt={share?.salt ?? ''} verifier={share?.verifier ?? ''} onUnlock={() => { setUnlockRequested(false); setSessionRevision((value) => value + 1) }} onClose={() => { if (dirty) setUnlockRequested(false); else void navigate(`/b${parentOf(path)}`) }} />
      <mdui-dialog open={leaveDialogOpen} headline={t('editor.unsaved_changes')} close-on-overlay-click={false} close-on-esc={false}>
        <p>{t('editor.unsaved_changes_prompt', { name: filename })}</p>
        <mdui-button slot="action" variant="text" onClick={() => { blocker.reset?.(); setLeaveDialogOpen(false); focusEditor() }}>{t('editor.stay')}</mdui-button>
        <mdui-button slot="action" variant="outlined" onClick={discardAndLeave}>{t('editor.discard_and_leave')}</mdui-button>
        <mdui-button slot="action" variant="filled" loading={saveMutation.isPending} disabled={!canSave} onClick={() => void saveAndLeave()}>{t('editor.save_and_leave')}</mdui-button>
      </mdui-dialog>
      <Snackbar message={snackbar} onDismiss={() => setSnackbar(null)} />
    </main>
  )
}
