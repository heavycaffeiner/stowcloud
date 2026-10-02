import { useEffect, useEffectEvent, useRef, useState } from 'react'
import { useNavigate, useParams } from '@tanstack/react-router'
import { parentOf } from '../../../lib/path-utils'
import { describeApiError } from '../../../api/error-text'
import { MAX_ENCRYPTABLE_BYTES } from '../../../lib/crypto/e2ee'
import { useShareUnlocked } from '../../shares/e2ee-store'
import { formatBytes } from '../../../lib/format/bytes'
import { useI18n } from '../../../hooks/use-i18n'
import { useShareEncryption, useStat } from '../api'
import { splatOf, splatPath } from '../browse-search'
import { Icon, StowBadge, StowButton, StowIconButton, StowProgressCircular, StowSnackbar } from '@/shared/ui'
import { CodeEditor, type CodeEditorHandle } from '../components/CodeEditor'
import { useEditDocument } from '../hooks/use-edit-document'
import { useEditNavigation } from '../hooks/use-edit-navigation'
import { askUnlock } from '../../shares/components/UnlockShareDialog'
import { MiddleEllipsis } from '../components/MiddleEllipsis'
import { useDocumentTitle } from '../../../hooks/use-document-title'
import * as styles from './EditPage.css'

export function EditPage() {
  const path = splatPath(useParams({ from: '/_app/edit/$', select: (params) => params._splat }))
  // Another file starts from a clean draft.
  return <Editor key={path} path={path} />
}

function Editor({ path }: { path: string }) {
  const { t } = useI18n()
  const navigate = useNavigate()
  const filename = path.split('/').filter(Boolean).at(-1) ?? path
  const stat = useStat(path)
  const isDir = stat.data?.kind === 'dir'
  const entry = stat.data && !isDir ? stat.data : null
  const encryption = useShareEncryption(path, entry !== null)
  const share = encryption.data ?? null
  const keyHeld = useShareUnlocked(share?.salt)
  const unlocked = share === null || keyHeld
  const locked = entry !== null && !unlocked
  const contentEnabled = entry !== null && !encryption.isPending && !encryption.error && unlocked
  const editorRef = useRef<CodeEditorHandle>(null)
  const [languageName, setLanguageName] = useState<string | null>(null)
  const [snackbar, setSnackbar] = useState<string | null>(null)
  const focusEditor = (): void => {
    editorRef.current?.focus()
    queueMicrotask(() => editorRef.current?.focus())
  }
  const edit = useEditDocument({
    path,
    entry,
    salt: share?.salt,
    unlocked,
    enabled: contentEnabled,
    notify: setSnackbar,
    focus: focusEditor
  })
  useEditNavigation({
    name: filename,
    dirty: edit.dirty,
    canSave: edit.canSave,
    save: edit.save,
    discard: edit.discard
  })
  useDocumentTitle(`${filename} - Stowcloud`)

  const asking = useRef(false)
  /** Asks for the share's passphrase. Cancelling with nothing unsaved goes back to the folder. */
  async function requestUnlock(): Promise<void> {
    if (!share || asking.current) return
    asking.current = true
    const done = await askUnlock(share)
    asking.current = false
    if (!done && !edit.dirty) void navigate({ to: '/b/$', params: splatOf(parentOf(path)) })
  }
  const onLocked = useEffectEvent(() => void requestUnlock())
  useEffect(() => {
    if (locked) onLocked()
  }, [locked])

  if (isDir)
    return (
      <main className={styles.root}>
        <p className={styles.error} role="alert">
          {t('editor.folder_cannot_opened_editor')}
        </p>
      </main>
    )
  const failure = stat.error ?? encryption.error ?? edit.query.error
  const loadError = failure ? describeApiError(failure, t('editor.could_not_load_file')) : null
  const loading = stat.isPending || (entry !== null && encryption.isPending) || (contentEnabled && edit.query.isPending)
  const readOnly = !entry?.perms.write

  return (
    <main className={styles.root}>
      <header className={styles.toolbar}>
        <StowIconButton
          label={t('editor.go_back')}
          onClick={() => void navigate({ to: '/b/$', params: splatOf(parentOf(path)) })}
          icon="chevron_left"
        />
        <span className={styles.fileIcon} aria-hidden="true">
          <Icon name="edit_document" />
        </span>
        <div className={styles.identity}>
          <div className={styles.title}>
            <MiddleEllipsis name={filename} className={styles.filename} />
            {edit.dirty ? (
              <StowBadge tone="accent" className={styles.dirty} title={t('editor.unsaved_changes')}>
                {t('editor.unsaved_changes')}
              </StowBadge>
            ) : null}
          </div>
          <div className={styles.details}>
            <StowBadge tone="accent">{languageName ?? t('editor.plain_text')}</StowBadge>
            {entry ? <span className={styles.meta}>{formatBytes(entry.size)}</span> : null}
            {readOnly && entry ? <StowBadge tone="danger">{t('common.read_only')}</StowBadge> : null}
          </div>
        </div>
        <div className={styles.actions}>
          <StowButton loading={edit.saving} disabled={!edit.canSave} onClick={() => void edit.save()}>
            {t('editor.save_ctrl_s')}
          </StowButton>
        </div>
      </header>
      <div className={styles.body}>
        {locked ? (
          <div className={styles.locked} role="status">
            <p>{t('encryption.unlock_hint')}</p>
            <StowButton onClick={() => void requestUnlock()}>{t('encryption.unlock')}</StowButton>
          </div>
        ) : loading ? (
          <div className={styles.loading}>
            <StowProgressCircular size={40} />
          </div>
        ) : loadError ? (
          <p className={styles.error} role="alert">
            {loadError}
          </p>
        ) : (
          <CodeEditor
            ref={editorRef}
            value={edit.content}
            filename={filename}
            readOnly={readOnly}
            maxBytes={MAX_ENCRYPTABLE_BYTES}
            onChange={edit.setDraft}
            onLimit={() => setSnackbar(t('editor.file_too_large_to_edit'))}
            onSave={() => void edit.save()}
            onLanguageChange={setLanguageName}
          />
        )}
      </div>
      {edit.saveError ? (
        <p className={styles.error} role="alert">
          {edit.saveError}
        </p>
      ) : null}
      <StowSnackbar className={styles.snackbar} message={snackbar} onDismiss={() => setSnackbar(null)} />
    </main>
  )
}
