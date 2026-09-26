import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useRef } from 'react'
import { useBlocker, useNavigate, useParams } from 'react-router-dom'
import { normalizePath, parentOf } from '../../../lib/api/path-utils'
import { describeApiError } from '../../../lib/api/error-text'
import { isUnlocked, MAX_ENCRYPTABLE_BYTES } from '../../../lib/crypto/e2ee'
import { formatBytes } from '../../../lib/format/bytes'
import { useI18n } from '../../../lib/i18n/use-i18n'
import { fileContentQuery, shareEncryptionQuery, statQuery, writeFileMutation } from '../../../lib/query/files'
import { Button } from '../../../lib/ui/Button'
import { CodeEditor, type CodeEditorHandle } from './CodeEditor'
import { EditConflictDialog } from './EditConflictDialog'
import { useEditState } from './use-edit-state'
import { useEditBaseline } from './use-edit-baseline'
import { useEditNavigation } from './use-edit-navigation'
import { useEditSave } from './use-edit-save'
import { useEditSessionLock } from './use-edit-session-lock'
import { Snackbar } from '../../../lib/ui/Snackbar'
import { UnlockShareDialog } from '../../../features/shares/UnlockShareDialog'
import { Icon } from '../../../lib/ui/Icon'
import { IconButton } from '../../../lib/ui/IconButton'
import { MiddleEllipsis } from '../../../features/files/MiddleEllipsis'
import { useDocumentTitle } from '../../use-document-title'
import '../../../styles/app/routes/edit/editor.css.ts'

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
  const { state, actions } = useEditState()
  const { draft, sealedDraft, baselineEtag, loadedPath, awaitingBaseline, unlockRequested, conflictOpen, conflictWeak, leaveDialogOpen, saveError, snackbar, sessionRevision, languageName } = state
  const content = draft ?? contentQuery.data?.content ?? ''
  const dirty = sealedDraft !== null || (draft !== null && draft !== (contentQuery.data?.content ?? ''))
  const blocker = useBlocker(dirty && loadedPath === path ? ({ currentLocation, nextLocation }) => currentLocation.pathname !== nextLocation.pathname : false)
  const readOnly = entry ? !entry.perms.write : true
  const canSave = Boolean(unlocked && dirty && baselineEtag !== null && !saveMutation.isPending && entry?.perms.write)
  useDocumentTitle(`${filename} - Stowcloud`)

  useEditBaseline({
    path,
    loadedPath,
    awaitingBaseline,
    entryPath: stat.data?.path,
    etag: stat.data?.etag,
    content: contentQuery.data,
    unlocked,
    queryClient,
    actions
  })
  const removeContent = useCallback((contentPath: string) => {
    queryClient.removeQueries({ queryKey: ['path', contentPath, 'content'] })
  }, [queryClient])
  useEditSessionLock({
    path,
    entryPath: entry?.path,
    shareSalt: share?.salt,
    dirty,
    draft,
    sealedDraft,
    actions,
    removeContent,
    translate: t
  })
  useEditNavigation({ dirty, blocker, actions })

  const focusEditor = useCallback(() => {
    editorRef.current?.focus()
    queueMicrotask(() => editorRef.current?.focus())
  }, [])
  const saveFlow = useEditSave({
    path,
    entry,
    unlocked,
    canSave,
    content,
    baselineEtag,
    queryClient,
    mutation: saveMutation,
    actions,
    focus: focusEditor,
    translate: t
  }, blocker)

  if (isDir) return <main className="sc-edit"><p className="sc-edit-error" role="alert">{t('editor.folder_cannot_opened_editor')}</p></main>
  const loadError = stat.error ? describeApiError(stat.error, t('editor.could_not_load_file')) : encryption.error ? describeApiError(encryption.error, t('editor.could_not_load_file')) : contentQuery.error ? describeApiError(contentQuery.error, t('editor.could_not_load_file')) : null
  const loading = stat.isPending || (Boolean(entry) && encryption.isPending) || (contentEnabled && contentQuery.isPending)
  void sessionRevision

  return (
    <main className="sc-edit">
      <header className="sc-edit-toolbar">
        <IconButton label={t('editor.go_back')} onClick={() => void navigate(`/b${parentOf(path)}`)}><Icon name="chevron_left" /></IconButton>
        <span className="sc-edit-file-icon" aria-hidden="true"><Icon name="edit_document" /></span>
        <div className="sc-edit-identity">
          <div className="sc-edit-title">
            <MiddleEllipsis name={filename} className="sc-edit-filename" />
            {dirty ? <span className="sc-edit-badge sc-edit-badge-dirty" title={t('editor.unsaved_changes')}>{t('editor.unsaved_changes')}</span> : null}
          </div>
          <div className="sc-edit-details">
            <span className="sc-edit-language">{languageName ?? t('editor.plain_text')}</span>
            {entry ? <span className="sc-edit-meta">{formatBytes(entry.size)}</span> : null}
            {readOnly && entry ? <span className="sc-edit-badge sc-edit-badge-readonly">{t('common.read_only')}</span> : null}
          </div>
        </div>
        <div className="sc-edit-actions"><Button loading={saveMutation.isPending} disabled={!canSave} onClick={() => void saveFlow.save()}>{t('editor.save_ctrl_s')}</Button></div>
      </header>
      <div className="sc-edit-body">
        {locked ? <div className="sc-edit-locked" role="status"><p>{t('encryption.unlock_hint')}</p><Button onClick={() => actions.setUnlockRequested(true)}>{t('encryption.unlock')}</Button></div> : loading ? <div className="sc-edit-loading"><mdui-circular-progress></mdui-circular-progress></div> : loadError ? <p className="sc-edit-error" role="alert">{loadError}</p> : <CodeEditor ref={editorRef} value={content} filename={filename} readOnly={readOnly} maxBytes={MAX_ENCRYPTABLE_BYTES} onChange={actions.setDraft} onLimit={() => actions.setSnackbar(t('editor.file_too_large_to_edit'))} onSave={() => void saveFlow.save()} onLanguageChange={actions.setLanguage} />}
      </div>
      {saveError ? <p className="sc-edit-error" role="alert">{saveError}</p> : null}
      <EditConflictDialog open={conflictOpen} name={filename} weak={conflictWeak} onClose={() => { actions.setConflict(false); focusEditor() }} onReload={() => void saveFlow.reloadAfterConflict()} onOverwrite={() => void saveFlow.overwriteAfterConflict()} />
      <UnlockShareDialog open={Boolean(locked && unlockRequested)} salt={share?.salt ?? ''} verifier={share?.verifier ?? ''} onUnlock={actions.completeUnlock} onClose={() => { if (dirty) actions.setUnlockRequested(false); else void navigate(`/b${parentOf(path)}`) }} />
      <mdui-dialog open={leaveDialogOpen} headline={t('editor.unsaved_changes')} close-on-overlay-click={false} close-on-esc={false}>
        <p>{t('editor.unsaved_changes_prompt', { name: filename })}</p>
        <mdui-button slot="action" variant="text" onClick={() => { blocker.reset?.(); actions.setLeaveDialog(false); focusEditor() }}>{t('editor.stay')}</mdui-button>
        <mdui-button slot="action" variant="outlined" onClick={saveFlow.discardAndLeave}>{t('editor.discard_and_leave')}</mdui-button>
        <mdui-button slot="action" variant="filled" loading={saveMutation.isPending} disabled={!canSave} onClick={() => void saveFlow.saveAndLeave()}>{t('editor.save_and_leave')}</mdui-button>
      </mdui-dialog>
      <Snackbar message={snackbar} onDismiss={() => actions.setSnackbar(null)} />
    </main>
  )
}
