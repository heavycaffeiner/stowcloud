import { useCallback, useRef } from 'react'
import { useBlocker, useNavigate, useParams } from 'react-router-dom'
import { normalizePath, parentOf } from '../../../lib/path-utils'
import { describeApiError } from '../../../api/error-text'
import { isUnlocked, MAX_ENCRYPTABLE_BYTES } from '../../../lib/crypto/e2ee'
import { formatBytes } from '../../../lib/format/bytes'
import { useI18n } from '../../../hooks/use-i18n'
import { useFileCache, useFileContent, useShareEncryption, useStat, useWriteFile } from '../api'
import { Button } from '../../../ui/Button'
import { Dialog } from '../../../ui/Dialog'
import { ProgressCircular } from '../../../ui/ProgressCircular'
import { CodeEditor, type CodeEditorHandle } from './CodeEditor'
import { EditConflictDialog } from './EditConflictDialog'
import { useEditState } from '../hooks/use-edit-state'
import { useEditBaseline } from '../hooks/use-edit-baseline'
import { useEditNavigation } from '../hooks/use-edit-navigation'
import { useEditSave } from '../hooks/use-edit-save'
import { useEditSessionLock } from '../hooks/use-edit-session-lock'
import { Snackbar } from '../../../ui/Snackbar'
import { UnlockShareDialog } from '../../shares/UnlockShareDialog'
import { Icon } from '../../../ui/Icon'
import { IconButton } from '../../../ui/IconButton'
import { MiddleEllipsis } from '../MiddleEllipsis'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import * as styles from './EditPage.css'
import { cx } from '../../../ui/cx'

export function EditPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const files = useFileCache()
  const { '*': rawPath } = useParams()
  const path = normalizePath(`/${rawPath ?? ''}`)
  const filename = path.split('/').filter(Boolean).at(-1) ?? path
  const stat = useStat(path)
  const isDir = stat.data?.kind === 'dir'
  const encryption = useShareEncryption(path, stat.data !== undefined && !isDir)
  const share = encryption.data ?? null
  const unlocked = share === null || isUnlocked(share.salt)
  const locked = Boolean(stat.data && !isDir && share && !unlocked)
  const entry = stat.data && !isDir ? stat.data : null
  const contentEnabled = Boolean(entry && !encryption.isPending && !encryption.error && unlocked)
  const contentQuery = useFileContent(entry, unlocked, contentEnabled)
  const saveMutation = useWriteFile()
  const editorRef = useRef<CodeEditorHandle>(null)
  const { state, actions } = useEditState()
  const {
    draft,
    sealedDraft,
    baselineEtag,
    loadedPath,
    awaitingBaseline,
    unlockRequested,
    conflictOpen,
    conflictWeak,
    leaveDialogOpen,
    saveError,
    snackbar,
    sessionRevision,
    languageName
  } = state
  const content = draft ?? contentQuery.data?.content ?? ''
  const dirty = sealedDraft !== null || (draft !== null && draft !== (contentQuery.data?.content ?? ''))
  const blocker = useBlocker(
    dirty && loadedPath === path
      ? ({ currentLocation, nextLocation }) => currentLocation.pathname !== nextLocation.pathname
      : false
  )
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
    files,
    actions
  })
  useEditSessionLock({
    path,
    entryPath: entry?.path,
    shareSalt: share?.salt,
    dirty,
    draft,
    sealedDraft,
    actions,
    removeContent: files.forgetContent,
    translate: t
  })
  useEditNavigation({ dirty, blocker, actions })

  const focusEditor = useCallback(() => {
    editorRef.current?.focus()
    queueMicrotask(() => editorRef.current?.focus())
  }, [])
  const saveFlow = useEditSave(
    {
      path,
      entry,
      unlocked,
      canSave,
      content,
      baselineEtag,
      files,
      mutation: saveMutation,
      actions,
      focus: focusEditor,
      translate: t
    },
    blocker
  )

  if (isDir)
    return (
      <main className={styles.root}>
        <p className={styles.error} role="alert">
          {t('editor.folder_cannot_opened_editor')}
        </p>
      </main>
    )
  const loadError = stat.error
    ? describeApiError(stat.error, t('editor.could_not_load_file'))
    : encryption.error
      ? describeApiError(encryption.error, t('editor.could_not_load_file'))
      : contentQuery.error
        ? describeApiError(contentQuery.error, t('editor.could_not_load_file'))
        : null
  const loading =
    stat.isPending || (Boolean(entry) && encryption.isPending) || (contentEnabled && contentQuery.isPending)
  void sessionRevision

  return (
    <main className={styles.root}>
      <header className={styles.toolbar}>
        <IconButton label={t('editor.go_back')} onClick={() => void navigate(`/b${parentOf(path)}`)}>
          <Icon name="chevron_left" />
        </IconButton>
        <span className={styles.fileIcon} aria-hidden="true">
          <Icon name="edit_document" />
        </span>
        <div className={styles.identity}>
          <div className={styles.title}>
            <MiddleEllipsis name={filename} className={styles.filename} />
            {dirty ? (
              <span className={cx(styles.badge, styles.badgeDirty)} title={t('editor.unsaved_changes')}>
                {t('editor.unsaved_changes')}
              </span>
            ) : null}
          </div>
          <div className={styles.details}>
            <span className={styles.language}>{languageName ?? t('editor.plain_text')}</span>
            {entry ? <span className={styles.meta}>{formatBytes(entry.size)}</span> : null}
            {readOnly && entry ? (
              <span className={cx(styles.badge, styles.badgeReadonly)}>{t('common.read_only')}</span>
            ) : null}
          </div>
        </div>
        <div className={styles.actions}>
          <Button loading={saveMutation.isPending} disabled={!canSave} onClick={() => void saveFlow.save()}>
            {t('editor.save_ctrl_s')}
          </Button>
        </div>
      </header>
      <div className={styles.body}>
        {locked ? (
          <div className={styles.locked} role="status">
            <p>{t('encryption.unlock_hint')}</p>
            <Button onClick={() => actions.setUnlockRequested(true)}>{t('encryption.unlock')}</Button>
          </div>
        ) : loading ? (
          <div className={styles.loading}>
            <ProgressCircular size={40} />
          </div>
        ) : loadError ? (
          <p className={styles.error} role="alert">
            {loadError}
          </p>
        ) : (
          <CodeEditor
            ref={editorRef}
            value={content}
            filename={filename}
            readOnly={readOnly}
            maxBytes={MAX_ENCRYPTABLE_BYTES}
            onChange={actions.setDraft}
            onLimit={() => actions.setSnackbar(t('editor.file_too_large_to_edit'))}
            onSave={() => void saveFlow.save()}
            onLanguageChange={actions.setLanguage}
          />
        )}
      </div>
      {saveError ? (
        <p className={styles.error} role="alert">
          {saveError}
        </p>
      ) : null}
      <EditConflictDialog
        open={conflictOpen}
        name={filename}
        weak={conflictWeak}
        onClose={() => {
          actions.setConflict(false)
          focusEditor()
        }}
        onReload={() => void saveFlow.reloadAfterConflict()}
        onOverwrite={() => void saveFlow.overwriteAfterConflict()}
      />
      <UnlockShareDialog
        open={Boolean(locked && unlockRequested)}
        salt={share?.salt ?? ''}
        verifier={share?.verifier ?? ''}
        onUnlock={actions.completeUnlock}
        onClose={() => {
          if (dirty) actions.setUnlockRequested(false)
          else void navigate(`/b${parentOf(path)}`)
        }}
      />
      <Dialog
        open={leaveDialogOpen}
        title={t('editor.unsaved_changes')}
        dismissible={false}
        actions={
          <>
            <Button
              variant="text"
              onClick={() => {
                blocker.reset?.()
                actions.setLeaveDialog(false)
                focusEditor()
              }}
            >
              {t('editor.stay')}
            </Button>
            <Button variant="outlined" onClick={saveFlow.discardAndLeave}>
              {t('editor.discard_and_leave')}
            </Button>
            <Button loading={saveMutation.isPending} disabled={!canSave} onClick={() => void saveFlow.saveAndLeave()}>
              {t('editor.save_and_leave')}
            </Button>
          </>
        }
      >
        <p>{t('editor.unsaved_changes_prompt', { name: filename })}</p>
      </Dialog>
      <Snackbar className={styles.snackbar} message={snackbar} onDismiss={() => actions.setSnackbar(null)} />
    </main>
  )
}
