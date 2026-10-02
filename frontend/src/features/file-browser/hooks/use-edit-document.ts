import { useEffect, useState } from 'react'
import { describeApiError } from '../../../api/error-text'
import { openSessionValue, sealSessionValue } from '../../../lib/crypto/e2ee'
import { t } from '../../../i18n'
import { useKeyChange } from '../../shares/e2ee-store'
import { askEditConflict } from '../components/EditDialogs'
import { type Entry, useFileCache, useFileContent, useWriteFile } from '../api'

/** Unsaved text sealed under the session key while its share is locked. */
type SealedDraft = { salt: string; bytes: Uint8Array }

export interface EditDocumentOptions {
  path: string
  entry: Entry | null
  /** The salt of the encrypted share holding the file, if it is in one. */
  salt: string | undefined
  unlocked: boolean
  enabled: boolean
  notify: (message: string) => void
  focus: () => void
}

/** The text of one file in the editor: the loaded version, the draft over it, and writing it back. */
export function useEditDocument({ path, entry, salt, unlocked, enabled, notify, focus }: EditDocumentOptions) {
  const files = useFileCache()
  const query = useFileContent(entry, unlocked, enabled)
  const mutation = useWriteFile()
  const [draft, setDraft] = useState<string | null>(null)
  const [sealedDraft, setSealedDraft] = useState<SealedDraft | null>(null)
  // The version the draft started from. A save that finds another one on the server asks first.
  const [baselineEtag, setBaselineEtag] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  if (baselineEtag === null && entry && query.data) setBaselineEtag(entry.etag)

  const loaded = query.data?.content ?? ''
  const content = draft ?? loaded
  const dirty = sealedDraft !== null || (draft !== null && draft !== loaded)
  const canSave = unlocked && dirty && baselineEtag !== null && !mutation.isPending && Boolean(entry?.perms.write)

  // A sealed draft is ciphertext of unsaved text; wipe it once it is replaced or the editor goes away.
  useEffect(() => {
    return () => {
      sealedDraft?.bytes.fill(0)
    }
  }, [sealedDraft])

  // The codec announces a lock before it drops the key, so a draft in an encrypted share is sealed
  // under the session key instead of staying readable, and opened again when the share unlocks.
  useKeyChange((change) => {
    if (change.kind === 'before-lock') sealDraft(change.salt)
    else if (change.kind === 'unlock') openDraft(change.salt)
    else files.forgetContent(entry?.path ?? path)
  })

  function sealDraft(locking: string): void {
    if (!dirty || draft === null || locking !== salt) return
    const plaintext = new TextEncoder().encode(draft)
    try {
      setSealedDraft({ salt: locking, bytes: sealSessionValue(plaintext, locking) })
      setDraft(null)
    } catch (error) {
      notify(describeApiError(error, t('common.could_not_save')))
    } finally {
      plaintext.fill(0)
    }
  }

  function openDraft(unlocking: string): void {
    if (!sealedDraft || sealedDraft.salt !== unlocking) return
    let plaintext: Uint8Array | null = null
    try {
      plaintext = openSessionValue(sealedDraft.bytes, sealedDraft.salt)
      setDraft(new TextDecoder().decode(plaintext))
      setSealedDraft(null)
    } catch (error) {
      notify(describeApiError(error, t('editor.could_not_load_file')))
    } finally {
      plaintext?.fill(0)
    }
  }

  async function write(text: string, notice: string): Promise<boolean> {
    try {
      const updated = await mutation.mutateAsync({ path, content: text })
      files.rememberSaved(path, updated, text)
      setBaselineEtag(updated.etag)
      setDraft((current) => (current === text ? null : current))
      notify(notice)
      focus()
      return true
    } catch (error) {
      setSaveError(describeApiError(error, t('common.could_not_save')))
      return false
    }
  }

  async function reload(): Promise<void> {
    try {
      const fresh = await files.fetchStat(path, true)
      await files.fetchContent(fresh, unlocked)
      setBaselineEtag(fresh.etag)
      discard()
      notify(t('editor.reloaded_newer_version'))
      focus()
    } catch (error) {
      notify(describeApiError(error, t('editor.could_not_load_file')))
    }
  }

  /** Writes the draft back, asking first when the file changed on the server. True once it is written. */
  async function save(): Promise<boolean> {
    if (!canSave || !entry) return false
    const text = content
    setSaveError(null)
    let latest: Entry
    try {
      latest = await files.fetchStat(path, true)
    } catch (error) {
      setSaveError(describeApiError(error, t('common.could_not_save')))
      return false
    }
    if (latest.etag === baselineEtag) return write(text, t('common.saved'))
    const choice = await askEditConflict(entry.name, latest.etag_weak)
    if (choice === 'overwrite') return write(text, t('editor.overwritten'))
    if (choice === 'reload') await reload()
    return false
  }

  function discard(): void {
    setDraft(null)
    setSealedDraft(null)
  }

  return {
    query,
    content,
    dirty,
    canSave,
    saving: mutation.isPending,
    saveError,
    setDraft,
    save,
    discard
  }
}
