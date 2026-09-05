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
  import { createQuery, createMutation } from '@tanstack/svelte-query'
  import { normalizePath, parentOf } from '../../../../lib/api/path-utils'
  import { queryClient } from '../../../../lib/query/client'
  import { keys } from '../../../../lib/query/keys'
  import { statQuery, fileContentQuery, writeFileMutation } from '../../../../lib/query/files'
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

  const rawPath = $derived(page.params.path ?? '')
  const path = $derived(normalizePath(`/${rawPath}`))
  const fileName = $derived(path.slice(path.lastIndexOf('/') + 1) || path)
  const parentPath = $derived(parentOf(path))

  const meta = createQuery(() => statQuery(path))
  const isDir = $derived(meta.data?.kind === 'dir')
  const file = createQuery(() => fileContentQuery(isDir ? null : meta.data))

  const entry = $derived(isDir ? null : (meta.data ?? null))
  const readOnly = $derived(entry ? !entry.perms.write : true)
  const loading = $derived(meta.isPending || (!isDir && file.isPending))
  const loadError = $derived(
    isDir
      ? t('editor.folder_cannot_opened_editor')
      : meta.error
        ? describeApiError(meta.error, t('editor.could_not_load_file'))
        : file.error
          ? describeApiError(file.error, t('editor.could_not_load_file'))
          : null
  )

  // The unsaved buffer. `null` means "no local edits": the editor shows the
  // query's own content.
  let draft = $state<string | null>(null)
  const content = $derived(draft ?? file.data?.content ?? '')
  const dirty = $derived(draft !== null && draft !== (file.data?.content ?? ''))

  // Baseline change token: the version the editor actually loaded its text from,
  // captured once when the content is loaded and at each successful save.
  // Held in local state so background refetches cannot mutate the comparison
  // baseline out from under the open buffer.
  let baselineEtag = $state<string | null>(null)
  let initialEtag = $state<string | null>(null)
  let loadedPath = $state<string | null>(null)
  let editorRef = $state<ReturnType<typeof CodeEditor> | undefined>()

  $effect(() => {
    if (path !== loadedPath) {
      loadedPath = path
      baselineEtag = null
      initialEtag = null
      draft = null
    }
  })

  $effect(() => {
    if (initialEtag === null && meta.data?.etag) {
      initialEtag = meta.data.etag
    }
  })

  $effect(() => {
    if (baselineEtag === null && file.isSuccess && initialEtag !== null) {
      baselineEtag = initialEtag
    }
  })

  function focusEditor(): void {
    editorRef?.focus()
    queueMicrotask(() => editorRef?.focus())
  }

  const saveMutation = createMutation(() => writeFileMutation())
  const saving = $derived(saveMutation.isPending)
  const canSave = $derived(dirty && !saving && entry?.perms.write === true)

  let snackbarMsg = $state<string | null>(null)
  let conflictOpen = $state(false)
  let conflictWeak = $state(false)

  function onContentChange(text: string): void {
    draft = text
  }

  async function save(): Promise<void> {
    if (!canSave) return
    try {
      const latest = await queryClient.fetchQuery({ ...statQuery(path), staleTime: 0 })
      if (baselineEtag !== null && latest.etag !== baselineEtag) {
        conflictWeak = latest.etag_weak
        conflictOpen = true
        return
      }
      const updated = await saveMutation.mutateAsync({ path, content })
      queryClient.setQueryData(keys.pathStat(path), updated)
      baselineEtag = updated.etag
      initialEtag = updated.etag
      draft = null
      snackbarMsg = t('common.saved')
      focusEditor()
    } catch (err) {
      snackbarMsg = describeApiError(err, t('common.could_not_save'))
    }
  }

  /** EditConflictDialog: keep my edits and overwrite the other writer's
   *  change. The token comparison is skipped rather than repeated: it already
   *  said the file moved, and somebody read that and asked for this. */
  async function overwriteAfterConflict(): Promise<void> {
    conflictOpen = false
    try {
      const updated = await saveMutation.mutateAsync({ path, content })
      queryClient.setQueryData(keys.pathStat(path), updated)
      baselineEtag = updated.etag
      initialEtag = updated.etag
      draft = null
      snackbarMsg = t('editor.overwritten')
      focusEditor()
    } catch (err) {
      snackbarMsg = describeApiError(err, t('common.could_not_save'))
    }
  }

  /** EditConflictDialog: discard my edits, adopt the latest content+etag. */
  async function reloadAfterConflict(): Promise<void> {
    conflictOpen = false
    draft = null
    baselineEtag = null
    initialEtag = null
    // A prefix, not an exact key: the content key carries the unlock state
    // as its last element, and a reload has to clear whichever of those the
    // editor happens to be holding.
    queryClient.removeQueries({ queryKey: ['path', path, 'content'] })
    const [freshMeta] = await Promise.all([
      queryClient.fetchQuery({ ...statQuery(path), staleTime: 0 }),
      file.refetch()
    ])
    if (freshMeta?.etag) {
      baselineEtag = freshMeta.etag
      initialEtag = freshMeta.etag
    }
    snackbarMsg = t('editor.reloaded_newer_version')
    focusEditor()
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
      <CodeEditor bind:this={editorRef} value={content} filename={fileName} {readOnly} onchange={onContentChange} onsave={save} />
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
