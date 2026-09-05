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
  import { goto } from '$app/navigation'
  import { page } from '$app/state'
  import { api, type Entry } from '../../../../lib/api/client'
  import { normalizePath, parentOf } from '../../../../lib/api/path-utils'
  import Button from '../../../../lib/ui/Button.svelte'
  import CodeEditor from '../../../../lib/ui/CodeEditor.svelte'
  import EditConflictDialog from '../../../../lib/ui/EditConflictDialog.svelte'
  import { Icon } from 'm3-svelte'
  import { icons } from '../../../../lib/icons'
  import IconButton from '../../../../lib/ui/IconButton.svelte'
  import ProgressCircular from '../../../../lib/ui/ProgressCircular.svelte'
  import Snackbar from '../../../../lib/ui/Snackbar.svelte'
  import { formatBytes } from '../../../../lib/format/bytes'
  import { describeApiError } from '../../../../lib/api/error-text'
  import { createEditorStore, isDirty, canSave } from '../../../../lib/store/slices/editor.slice'
  import { useRunesStore } from '../../../../lib/store/core/bridge.svelte'

  const rawPath = $derived(page.params.path ?? '')
  const path = $derived(normalizePath(`/${rawPath}`))
  const fileName = $derived(path.slice(path.lastIndexOf('/') + 1) || path)
  const parentPath = $derived(parentOf(path))

  const editorStore = createEditorStore()
  const snap = useRunesStore(editorStore)

  const entry = $derived(snap.current.entry)
  const content = $derived(snap.current.content)
  const loading = $derived(snap.current.status === 'loading')
  const loadError = $derived(snap.current.errorMessage)
  const saving = $derived(snap.current.isSaving)
  const dirty = $derived(isDirty(snap.current))
  const readOnly = $derived(entry ? !entry.perms.write : true)
  const conflict = $derived(snap.current.conflict)
  let snackbarMsg = $state<string | null>(null)
  let loadedPath = ''
  $effect(() => {
    if (path !== loadedPath) {
      loadedPath = path
      void load(path)
    }
  })

  async function load(p: string): Promise<void> {
    editorStore.dispatch({ type: 'LOAD_START' })
    try {
      const statRes = await api.stat(p)
      if (statRes.kind === 'dir') {
        editorStore.dispatch({ type: 'LOAD_ERROR', message: t('editor.folder_cannot_opened_editor') })
        return
      }
      const readRes = await api.readFile(statRes)
      editorStore.dispatch({ type: 'LOAD_SUCCESS', entry: statRes, content: readRes.content })
    } catch (err) {
      editorStore.dispatch({
        type: 'LOAD_ERROR',
        message: describeApiError(err, t('editor.could_not_load_file'))
      })
    }
  }

  function onContentChange(text: string): void {
    editorStore.dispatch({ type: 'SET_CONTENT', content: text })
  }
  async function save(): Promise<void> {
    if (!canSave(snap.current)) return
    editorStore.dispatch({ type: 'SAVE_START' })
    try {
      const latest = await api.stat(path)
      if (snap.current.etag !== null && latest.etag !== snap.current.etag) {
        editorStore.dispatch({
          type: 'SAVE_CONFLICT',
          currentEtag: latest.etag,
          isWeak: latest.etag_weak
        })
        return
      }
      const updated = await api.writeFile(path, snap.current.content)
      editorStore.dispatch({ type: 'SAVE_SUCCESS', updated, content: snap.current.content })
      snackbarMsg = t('common.saved')
    } catch (err) {
      const msg = describeApiError(err, t('common.could_not_save'))
      editorStore.dispatch({ type: 'SAVE_ERROR', message: msg })
      snackbarMsg = msg
    }
  }

  /** EditConflictDialog: keep my edits and overwrite the other writer's
   *  change. The token comparison is skipped rather than repeated: it already
   *  said the file moved, and somebody read that and asked for this. */
  async function overwriteAfterConflict(): Promise<void> {
    editorStore.dispatch({ type: 'DISMISS_CONFLICT' })
    editorStore.dispatch({ type: 'SAVE_START' })
    try {
      const updated = await api.writeFile(path, snap.current.content)
      editorStore.dispatch({ type: 'SAVE_SUCCESS', updated, content: snap.current.content })
      snackbarMsg = t('editor.overwritten')
    } catch (err) {
      const msg = describeApiError(err, t('common.could_not_save'))
      editorStore.dispatch({ type: 'SAVE_ERROR', message: msg })
      snackbarMsg = msg
    }
  }

  /** EditConflictDialog: discard my edits, adopt the latest content+etag. */
  async function reloadAfterConflict(): Promise<void> {
    editorStore.dispatch({ type: 'DISMISS_CONFLICT' })
    await load(path)
    snackbarMsg = t('editor.reloaded_newer_version')
  }

  function goBack(): void {
    goto(`/b${parentPath}`)
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
      <Button variant="filled" disabled={readOnly || !dirty || saving} onclick={save}>{t('editor.save_ctrl_s')}</Button>
    </div>
  </header>

  <div class="sc-edit__body">
    {#if loading}
      <div class="sc-edit__loading"><ProgressCircular /></div>
    {:else if loadError}
      <p class="sc-edit__error" role="alert">{loadError}</p>
    {:else}
      <CodeEditor value={content} filename={fileName} {readOnly} onchange={onContentChange} onsave={save} />
    {/if}
  </div>
</div>

<EditConflictDialog
  weak={conflict?.isWeak ?? false}
  open={conflict !== null}
  name={fileName}
  onclose={() => editorStore.dispatch({ type: 'DISMISS_CONFLICT' })}
  onreload={reloadAfterConflict}
  onoverwrite={overwriteAfterConflict}
/>
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
