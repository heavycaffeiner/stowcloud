<script lang="ts">
  // /edit/[...path] — DESIGN-FRONTEND.md §7: CodeMirror behind a dynamic
  // import (see CodeEditor.svelte), backed by `GET /api/fs/read` (size-capped,
  // `MAX_INLINE_READ` in `crates/sc-server/src/bridge.rs`) and
  // `PUT /api/fs/write` with `If-Match`.
  //
  // `If-Match` is the point, not a detail: this page always sends the etag
  // of the version it actually has open, and a `412 fs.precondition` (two
  // people editing the same file) opens EditConflictDialog with a real
  // choice, never a swallowed error.
  import { t } from '../../../../lib/i18n'
  import { goto } from '$app/navigation'
  import { page } from '$app/state'
  import { api, ApiError, type Entry } from '../../../../lib/api/client'
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

  const rawPath = $derived(page.params.path ?? '')
  const path = $derived(normalizePath(`/${rawPath}`))
  const fileName = $derived(path.slice(path.lastIndexOf('/') + 1) || path)
  const parentPath = $derived(parentOf(path))

  let loading = $state(true)
  let loadError = $state<string | null>(null)
  let entry = $state<Entry | null>(null)
  let etag = $state<string | null>(null)
  let originalContent = $state('')
  let content = $state('')
  let saving = $state(false)
  let snackbarMsg = $state<string | null>(null)
  let conflictOpen = $state(false)
  let conflictEtag = $state('')

  const dirty = $derived(content !== originalContent)
  const readOnly = $derived(entry ? !entry.perms.write : true)

  let loadedPath = ''
  $effect(() => {
    if (path !== loadedPath) {
      loadedPath = path
      void load(path)
    }
  })

  async function load(p: string): Promise<void> {
    loading = true
    loadError = null
    entry = null
    try {
      // `stat` first, deliberately sequential rather than `Promise.all`-ed
      // with `readFile`: a directory must never reach the server's
      // `GET /api/fs/read` at all here, because that returns a generic
      // `fs.invalid_name` ("directory" buried in `detail.reason`) that reads
      // like a broken path, not "you can't edit a folder". `stat` alone lets
      // this page recognize a directory itself and say so plainly.
      const statRes = await api.stat(p)
      if (statRes.kind === 'dir') {
        loadError = t('editor.folder_cannot_opened_editor')
        return
      }
      const readRes = await api.readFile(p)
      entry = statRes
      etag = statRes.etag
      originalContent = readRes.content
      content = readRes.content
    } catch (err) {
      loadError = err instanceof ApiError ? err.message : t('editor.could_not_load_file')
    } finally {
      loading = false
    }
  }

  function onContentChange(text: string): void {
    content = text
  }

  async function save(): Promise<void> {
    if (readOnly || saving || !dirty) return
    saving = true
    try {
      const updated = await api.writeFile(path, content, etag ?? undefined)
      entry = updated
      etag = updated.etag
      originalContent = content
      snackbarMsg = t('common.saved')
    } catch (err) {
      if (err instanceof ApiError && err.code === 'fs.precondition') {
        conflictEtag = String(err.detail?.current_etag ?? '')
        conflictOpen = true
      } else {
        snackbarMsg = err instanceof ApiError ? err.message : t('common.could_not_save')
      }
    } finally {
      saving = false
    }
  }

  /** EditConflictDialog: keep my edits, overwrite the other writer's change
   *  using the etag we just learned is current. */
  async function overwriteAfterConflict(): Promise<void> {
    conflictOpen = false
    saving = true
    try {
      const updated = await api.writeFile(path, content, conflictEtag || undefined)
      entry = updated
      etag = updated.etag
      originalContent = content
      snackbarMsg = t('editor.overwritten')
    } catch (err) {
      // A second conflict inside a few hundred ms (a third writer, or the
      // server rejecting entirely) shows plainly rather than looping the
      // dialog silently.
      if (err instanceof ApiError && err.code === 'fs.precondition') {
        conflictEtag = String(err.detail?.current_etag ?? '')
        conflictOpen = true
      } else {
        snackbarMsg = err instanceof ApiError ? err.message : t('common.could_not_save')
      }
    } finally {
      saving = false
    }
  }

  /** EditConflictDialog: discard my edits, adopt the latest content+etag. */
  async function reloadAfterConflict(): Promise<void> {
    conflictOpen = false
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
  <title>{fileName} · Stowcloud</title>
</svelte:head>

<svelte:window onbeforeunload={onWindowBeforeUnload} />

<div class="sc-edit">
  <header class="sc-edit__toolbar">
    <IconButton label={t('editor.go_back')} onclick={goBack}><Icon icon={icons['chevron-left']} /></IconButton>
    <div class="sc-edit__title">
      <span class="sc-edit__filename">{fileName}</span>
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
  open={conflictOpen}
  name={fileName}
  onclose={() => (conflictOpen = false)}
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
