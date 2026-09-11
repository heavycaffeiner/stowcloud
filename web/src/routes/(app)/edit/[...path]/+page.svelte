<script lang="ts">
  // /edit/[...path]: CodeMirror behind a dynamic import (see
  // CodeEditor.svelte), backed by GET /api/v1/files/read (size-capped) and
  // POST /api/v1/files/write.
  //
  // Concurrent-edit detection lives here rather than in an `If-Match` header.
  // The server's change tokens are metadata-derived and weak, and it refuses
  // every conditional write against one, so a save that sent the condition
  // could never succeed. Comparing the token this page loaded against a fresh
  // `stat` taken at save time is the check a weak token can answer: it opens
  // EditConflictDialog with a real choice, and the dialog says the check is
  // advisory because it is.
  import { t } from '../../../../lib/i18n'
  import { beforeNavigate, goto } from '$app/navigation'
  import { page } from '$app/state'
  import { createQuery, createMutation } from '@tanstack/svelte-query'
  import { normalizePath, parentOf } from '../../../../lib/api/path-utils'
  import { queryClient } from '../../../../lib/query/client'
  import { statQuery, fileContentQuery, shareEncryptionQuery, writeFileMutation } from '../../../../lib/query/files'
  import { keys } from '../../../../lib/query/keys'
  import Button from '../../../../lib/ui/Button.svelte'
  import CodeEditor from '../../../../lib/ui/CodeEditor.svelte'
  import Dialog from '../../../../lib/ui/Dialog.svelte'
  import EditConflictDialog from '../../../../lib/ui/EditConflictDialog.svelte'
  import { isUnlocked, MAX_ENCRYPTABLE_BYTES, openSessionValue, sealSessionValue } from '../../../../lib/crypto/e2ee'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../../../lib/icons'
  import IconButton from '../../../../lib/ui/IconButton.svelte'
  import ProgressCircular from '../../../../lib/ui/ProgressCircular.svelte'
  import Snackbar from '../../../../lib/ui/Snackbar.svelte'
  import UnlockShareDialog from '../../../../lib/ui/UnlockShareDialog.svelte'
  import { formatBytes } from '../../../../lib/format/bytes'
  import { describeApiError } from '../../../../lib/api/error-text'

  interface PendingNavigation {
    href: string
    delta?: number
  }

  const rawPath = $derived(page.params.path ?? '')
  const path = $derived(normalizePath(`/${rawPath}`))
  const fileName = $derived(path.slice(path.lastIndexOf('/') + 1) || path)
  const parentPath = $derived(parentOf(path))

  const meta = createQuery(() => statQuery(path))
  const isDir = $derived(meta.data?.kind === 'dir')
  const entry = $derived(isDir ? null : (meta.data ?? null))
  // Do not start a share lookup until stat has identified a file. A disabled
  // query is still pending with no data, so including it in the directory
  // loading gate would hide the folder error behind an irrelevant request.
  const shareEncryptionEnabled = $derived(meta.data !== undefined && !isDir)
  const shareEncryption = createQuery(() => shareEncryptionQuery(path, shareEncryptionEnabled))
  const encryption = $derived(shareEncryption.data ?? null)
  let unlockGeneration = $state(0)
  let unlockRequested = $state(true)
  $effect(() => {
    const onBeforeLock = (event: Event) => {
      const salt = (event as CustomEvent<{ salt: string }>).detail.salt
      if (!dirty || draft === null || encryption?.salt !== salt) return
      const plaintext = new TextEncoder().encode(draft)
      try {
        sealedDraft = { salt, bytes: sealSessionValue(plaintext, salt) }
        draft = null
      } catch (err) {
        sealedDraft?.bytes.fill(0)
        sealedDraft = null
        draft = null
        snackbarMsg = describeApiError(err, t('common.could_not_save'))
      } finally {
        plaintext.fill(0)
      }
    }
    const onUnlock = (event: Event) => {
      const salt = (event as CustomEvent<{ salt: string }>).detail.salt
      const sealed = sealedDraft
      if (sealed?.salt === salt) {
        let plaintext: Uint8Array | null = null
        try {
          plaintext = openSessionValue(sealed.bytes, salt)
          draft = new TextDecoder().decode(plaintext)
          sealed.bytes.fill(0)
          sealedDraft = null
        } catch (err) {
          snackbarMsg = describeApiError(err, t('editor.could_not_load_file'))
        } finally {
          plaintext?.fill(0)
        }
      }
      if (encryption?.salt === salt) unlockRequested = false
      unlockGeneration++
    }
    const onLock = () => {
      unlockGeneration++
      unlockRequested = sealedDraft === null
      conflictOpen = false
      leaveDialogOpen = false
      pendingNavigation = null
      queryClient.removeQueries({ queryKey: ['path', entry?.path ?? path, 'content'] })
    }
    window.addEventListener('sc:before-lock', onBeforeLock)
    window.addEventListener('sc:unlock', onUnlock)
    window.addEventListener('sc:lock', onLock)
    return () => {
      window.removeEventListener('sc:before-lock', onBeforeLock)
      window.removeEventListener('sc:unlock', onUnlock)
      window.removeEventListener('sc:lock', onLock)
    }
  })
  const unlocked = $derived.by((): boolean => {
    void unlockGeneration
    return encryption === null || isUnlocked(encryption.salt)
  })
  const locked = $derived(entry !== null && encryption !== null && !unlocked)
  const fileEnabled = $derived(entry !== null && shareEncryptionEnabled && !shareEncryption.isPending && !shareEncryption.error && unlocked)
  const file = createQuery(() => ({
    ...fileContentQuery(fileEnabled ? entry : null, unlocked),
    enabled: fileEnabled
  }))

  const readOnly = $derived(entry ? !entry.perms.write : true)
  const loading = $derived(meta.isPending || (shareEncryptionEnabled && shareEncryption.isPending) || (fileEnabled && file.isPending))
  const loadError = $derived(
    isDir
      ? t('editor.folder_cannot_opened_editor')
      : meta.error
        ? describeApiError(meta.error, t('editor.could_not_load_file'))
        : shareEncryption.error
          ? describeApiError(shareEncryption.error, t('editor.could_not_load_file'))
          : file.error
            ? describeApiError(file.error, t('editor.could_not_load_file'))
            : null
  )

  // The unsaved buffer. `null` means "no local edits": the editor shows the
  // query's own content.
  let draft = $state<string | null>(null)
  let sealedDraft = $state<{ salt: string; bytes: Uint8Array } | null>(null)
  const content = $derived(draft ?? file.data?.content ?? '')
  const dirty = $derived(sealedDraft !== null || (draft !== null && draft !== (file.data?.content ?? '')))

  // Baseline change token: the version the editor actually loaded its text from,
  // captured once when the content is loaded and at each successful save.
  // Held in local state so background refetches cannot mutate the comparison
  // baseline out from under the open buffer.
  let baselineEtag = $state<string | null>(null)
  let awaitingInitialBaseline = $state(true)
  let loadedPath = $state<string | null>(null)
  let editorRef = $state<ReturnType<typeof CodeEditor> | undefined>()
  function focusEditor(): void {
    editorRef?.focus()
    queueMicrotask(() => editorRef?.focus())
  }

  const saveMutation = createMutation(() => writeFileMutation())
  const saving = $derived(saveMutation.isPending)
  const canSave = $derived(unlocked && dirty && baselineEtag !== null && !saving && entry?.perms.write === true)

  let snackbarMsg = $state<string | null>(null)
  let conflictOpen = $state(false)
  let conflictWeak = $state(false)

  let leaveDialogOpen = $state(false)
  let pendingNavigation = $state<PendingNavigation | null>(null)
  let allowPendingNavigation = false

  // Internal route changes are cancellable, including browser back/forward.
  // Leave navigations are intentionally allowed through here: the native
  // beforeunload handler below is the only prompt a browser can show while the
  // document itself is being replaced.
  beforeNavigate((navigation) => {
    if (!dirty || navigation.willUnload || navigation.type === 'leave' || allowPendingNavigation) {
      if (allowPendingNavigation) allowPendingNavigation = false
      return
    }

    navigation.cancel()
    if (pendingNavigation !== null) return
    const href = navigation.to?.url.href
    if (!href) return
    pendingNavigation = {
      href,
      delta: navigation.type === 'popstate' ? navigation.delta : undefined
    }
    leaveDialogOpen = true
  })


  $effect(() => {
    if (path !== loadedPath) {
      loadedPath = path
      baselineEtag = null
      awaitingInitialBaseline = true
      draft = null
      sealedDraft?.bytes.fill(0)
      sealedDraft = null
    }
  })

  $effect(() => {
    const etag = meta.data?.etag
    const contentPath = meta.data?.path ?? path
    const expectedContent =
      etag === undefined ? undefined : queryClient.getQueryData(keys.pathContent(contentPath, etag, unlocked))
    // The observer can briefly retain its previous result while metadata
    // switches keys. Only establish a baseline once the result is the exact
    // object cached under the current metadata ETag.
    if (
      awaitingInitialBaseline &&
      path === loadedPath &&
      etag &&
      file.data !== undefined &&
      file.data === expectedContent
    ) {
      baselineEtag = etag
      awaitingInitialBaseline = false
    }
  })

  function continuePendingNavigation(): void {
    const pending = pendingNavigation
    pendingNavigation = null
    leaveDialogOpen = false
    if (!pending) return

    allowPendingNavigation = true
    if (pending.delta !== undefined) {
      history.go(pending.delta)
      return
    }
    void goto(pending.href).catch((err) => {
      allowPendingNavigation = false
      snackbarMsg = describeApiError(err, t('common.could_not_load_list'))
    })
  }

  function stayOnEditor(): void {
    pendingNavigation = null
    leaveDialogOpen = false
    focusEditor()
  }

  function discardAndLeave(): void {
    draft = null
    sealedDraft?.bytes.fill(0)
    sealedDraft = null
    continuePendingNavigation()
  }

  async function saveAndLeave(): Promise<void> {
    if (saving) return
    if (await save()) continuePendingNavigation()
  }

  async function save(): Promise<boolean> {
    if (!canSave) return false
    try {
      const latest = await queryClient.fetchQuery({ ...statQuery(path), staleTime: 0 })
      if (baselineEtag !== null && latest.etag !== baselineEtag) {
        conflictWeak = latest.etag_weak
        conflictOpen = true
        return false
      }
      const savedContent = content
      const updated = await saveMutation.mutateAsync({ path, content: savedContent })
      if (unlocked) {
        queryClient.setQueryData(keys.pathContent(updated.path, updated.etag, true), { content: savedContent })
      }
      queryClient.setQueryData(keys.pathStat(path), updated)
      baselineEtag = updated.etag
      awaitingInitialBaseline = false
      if (draft === savedContent) draft = null
      snackbarMsg = t('common.saved')
      focusEditor()
      return true
    } catch (err) {
      snackbarMsg = describeApiError(err, t('common.could_not_save'))
      return false
    }
  }

  function onContentChange(text: string): void {
    draft = text
  }

  function onEditorLimit(): void {
    snackbarMsg = t('editor.file_too_large_to_edit')
  }

  /** EditConflictDialog: keep my edits and overwrite the other writer's
   *  change. The token comparison is skipped rather than repeated: it already
   *  said the file moved, and somebody read that and asked for this. */
  async function overwriteAfterConflict(): Promise<void> {
    conflictOpen = false
    try {
      const savedContent = content
      const updated = await saveMutation.mutateAsync({ path, content: savedContent })
      if (unlocked) {
        queryClient.setQueryData(keys.pathContent(updated.path, updated.etag, true), { content: savedContent })
      }
      queryClient.setQueryData(keys.pathStat(path), updated)
      baselineEtag = updated.etag
      awaitingInitialBaseline = false
      if (draft === savedContent) draft = null
      snackbarMsg = t('editor.overwritten')
      focusEditor()
      continuePendingNavigation()
    } catch (err) {
      snackbarMsg = describeApiError(err, t('common.could_not_save'))
    }
  }

  /** EditConflictDialog: discard my edits, adopt the latest content+etag. */
  async function reloadAfterConflict(): Promise<void> {
    conflictOpen = false
    const reloadPath = path
    const reloadUnlocked = unlocked
    try {
      const freshMeta = await queryClient.fetchQuery({ ...statQuery(reloadPath), staleTime: 0 })
      // The ETag is part of the content key, so this fetch stages the new
      // content without disturbing the old draft or baseline.
      await queryClient.fetchQuery(fileContentQuery(freshMeta, reloadUnlocked))
      if (path !== reloadPath) return
      baselineEtag = freshMeta.etag
      awaitingInitialBaseline = false
      draft = null
      snackbarMsg = t('editor.reloaded_newer_version')
      focusEditor()
    } catch (err) {
      snackbarMsg = describeApiError(err, t('editor.could_not_load_file'))
    }
  }

  function goBack(): void {
    void goto(`/b${parentPath}`).catch((err) => {
      if (pendingNavigation !== null) return
      snackbarMsg = describeApiError(err, t('common.could_not_load_list'))
    })
  }

  function onWindowBeforeUnload(e: BeforeUnloadEvent): void {
    if (!dirty) return
    e.preventDefault()
    e.returnValue = ''
  }
</script>

<svelte:head>
  <title>{fileName} - Stowcloud</title>
</svelte:head>

<svelte:window onbeforeunload={onWindowBeforeUnload} />

<div class="sc-edit">
  <header class="sc-edit__toolbar">
    <IconButton label={t('editor.go_back')} onclick={goBack}><Icon icon={icons['chevron-left']} /></IconButton>
    <div class="sc-edit__title">
      <!-- The inner `<bdi>`: this truncates from the front
           (`direction: rtl`), which without an LTR isolate inside reorders
           the name as well as clipping it. -->
      <span class="sc-edit__filename"><bdi>{fileName}</bdi></span>
      {#if dirty}<span class="sc-edit__dirty" title={t('editor.unsaved_changes')}>•</span>{/if}
      {#if entry}<span class="sc-edit__meta">{formatBytes(entry.size)}</span>{/if}
      {#if readOnly && entry}<span class="sc-edit__badge">{t('common.read_only')}</span>{/if}
    </div>
    <div class="sc-edit__actions">
      {#if saving}<ProgressCircular size={20} />{/if}
      <Button variant="filled" disabled={!canSave} onclick={save}>{t('editor.save_ctrl_s')}</Button>
    </div>
  </header>

  <div class="sc-edit__body">
    {#if locked}
      <div class="sc-edit__locked" role="status">
        <p>{t('encryption.unlock_hint')}</p>
        <Button variant="filled" onclick={() => (unlockRequested = true)}>{t('encryption.unlock')}</Button>
      </div>
    {:else if loading}
      <div class="sc-edit__loading"><ProgressCircular /></div>
    {:else if loadError}
      <p class="sc-edit__error" role="alert">{loadError}</p>
    {:else}
      <CodeEditor
        bind:this={editorRef}
        value={content}
        filename={fileName}
        {readOnly}
        maxBytes={MAX_ENCRYPTABLE_BYTES}
        onchange={onContentChange}
        onlimit={onEditorLimit}
        onsave={save}
      />
    {/if}
  </div>
</div>

<EditConflictDialog
  weak={conflictWeak}
  open={conflictOpen}
  name={fileName}
  onclose={() => {
    conflictOpen = false
    focusEditor()
  }}
  onreload={reloadAfterConflict}
  onoverwrite={overwriteAfterConflict}
/>
<UnlockShareDialog
  open={locked && unlockRequested}
  salt={encryption?.salt ?? ''}
  verifier={encryption?.verifier ?? ''}
  onunlock={() => (unlockRequested = false)}
  onclose={() => {
    if (dirty) unlockRequested = false
    else goBack()
  }}
/>
<Dialog open={leaveDialogOpen} title={t('editor.unsaved_changes')} onclose={stayOnEditor}>
  <p>{t('editor.unsaved_changes_prompt', { name: fileName })}</p>
  {#snippet actions()}
    <Button variant="text" onclick={stayOnEditor}>{t('editor.stay')}</Button>
    <Button variant="outlined" danger onclick={discardAndLeave}>{t('editor.discard_and_leave')}</Button>
    <Button variant="filled" loading={saving} disabled={!canSave} onclick={saveAndLeave}>{t('editor.save_and_leave')}</Button>
  {/snippet}
</Dialog>
<Snackbar message={snackbarMsg} ondismiss={() => (snackbarMsg = null)} />

<style>
  .sc-edit {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .sc-edit__toolbar {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 16px;
    border-bottom: 1px solid var(--m3c-outline-variant);
  }
  .sc-edit__title {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 1;
    min-width: 0;
  }
  .sc-edit__filename {
    @apply --m3-title-medium;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    direction: rtl;
    text-align: left;
  }
  /* See the markup: the isolate is what keeps `direction: rtl` truncating
     the name instead of reordering it. */
  .sc-edit__locked {
    display: flex;
    flex: 1;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 16px;
    padding: 24px;
    text-align: center;
  }
  .sc-edit__filename > bdi {
    direction: ltr;
  }
  .sc-edit__dirty {
    color: var(--m3c-primary);
    @apply --m3-title-medium;
  }
  .sc-edit__meta {
    flex-shrink: 0;
    color: var(--m3c-on-surface-variant);
    @apply --m3-body-small;
  }
  .sc-edit__badge {
    flex-shrink: 0;
    padding-inline: 8px;
    height: 24px;
    display: inline-flex;
    align-items: center;
    border-radius: var(--m3-shape-full);
    background: var(--m3c-error-container);
    color: var(--m3c-on-error-container);
    @apply --m3-body-small;
  }
  .sc-edit__actions {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-shrink: 0;
  }
  .sc-edit__body {
    position: relative;
    flex: 1;
    min-height: 0;
    display: flex;
  }
  .sc-edit__loading {
    display: flex;
    align-items: center;
    justify-content: center;
    flex: 1;
  }
  .sc-edit__error {
    padding: 24px;
    color: var(--m3c-error);
  }
</style>
