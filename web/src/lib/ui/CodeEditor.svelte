<script lang="ts">
  // CodeEditor.svelte — CodeMirror 6 wrapper for `/edit/[...path]`.
  //
  // CodeMirror 6 is dynamically imported, never in the initial bundle. Every
  // `codemirror`/`@codemirror/*` import below is inside `onMount`'s async
  // callback, not at module top level — Vite/Rollup only pulls those chunks
  // in once this component actually mounts (i.e. once the `/edit` route is
  // navigated to and past its own permission/load gate), never as part of
  // the `/b` browse route's bundle.
  import { t } from '../i18n'
  import { onMount, untrack } from 'svelte'
  import type { EditorView } from '@codemirror/view'

  interface Props {
    /** Authoritative external value — e.g. the freshly-fetched content after
     *  a reload following a save conflict. Changing this
     *  after mount replaces the live document; the component's own edits
     *  (via `onchange`) don't count as an external change (see `lastEcho`). */
    value: string
    /** Used only for language auto-detection by extension (`@codemirror/language-data`'s
     *  `LanguageDescription.matchFilename`) — never sent anywhere. */
    filename: string
    readOnly?: boolean
    onchange: (text: string) => void
    onsave?: () => void
  }
  let { value, filename, readOnly = false, onchange, onsave }: Props = $props()

  let hostEl: HTMLDivElement | undefined = $state()
  let ready = $state(false)
  let failed = $state(false)
  let view: EditorView | undefined
  /** The last value this component itself produced via `onchange`, so the
   *  `$effect` below can tell "the parent reset `value` out from under me"
   *  (reload) apart from "the parent is just echoing my own edit back"
   *  (which would otherwise fight the user's cursor position every keystroke). */
  let lastEcho = untrack(() => value)

  onMount(() => {
    let disposed = false
    ;(async () => {
      try {
        const [viewMod, stateMod, commandsMod, cmMod, langDataMod] = await Promise.all([
          import('@codemirror/view'),
          import('@codemirror/state'),
          import('@codemirror/commands'),
          import('codemirror'),
          import('@codemirror/language-data')
        ])
        if (disposed || !hostEl) return

        const { EditorView, keymap } = viewMod
        const { EditorState } = stateMod
        const { defaultKeymap, historyKeymap, history, indentWithTab } = commandsMod
        const { basicSetup } = cmMod
        const { LanguageDescription } = await import('@codemirror/language')

        const desc = LanguageDescription.matchFilename(langDataMod.languages, filename)
        const languageExt = desc ? await desc.load() : null
        if (disposed || !hostEl) return

        const saveKeymap = keymap.of([
          {
            key: 'Mod-s',
            preventDefault: true,
            run: () => {
              onsave?.()
              return true
            }
          }
        ])

        const updateListener = EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            const text = update.state.doc.toString()
            lastEcho = text
            onchange(text)
          }
        })

        view = new EditorView({
          parent: hostEl,
          state: EditorState.create({
            doc: value,
            extensions: [
              basicSetup,
              history(),
              keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
              saveKeymap,
              languageExt ? languageExt.extension : [],
              EditorView.editable.of(!readOnly),
              EditorState.readOnly.of(readOnly),
              updateListener,
              EditorView.theme({
                '&': { height: '100%', fontSize: '0.875rem' },
                '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace' }
              })
            ]
          })
        })
        ready = true
      } catch (err) {
        console.error('CodeMirror failed to load', err)
        failed = true
      }
    })()

    return () => {
      disposed = true
      view?.destroy()
    }
  })

  // A reload replaces `value` from the outside; the editor's
  // own edits echo back through `lastEcho` first so this doesn't fight typing.
  $effect(() => {
    if (view && value !== lastEcho) {
      lastEcho = value
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } })
    }
  })
</script>

<div class="sc-code-editor" bind:this={hostEl}>
  {#if failed}
    <p class="sc-code-editor__status sc-code-editor__status--error">{t('editor.could_not_load_editor_check')}</p>
  {:else if !ready}
    <p class="sc-code-editor__status">{t('editor.loading_editor')}</p>
  {/if}
</div>

<style>
  .sc-code-editor {
    flex: 1;
    min-height: 0;
    overflow: hidden;
    background: var(--m3c-surface);
  }
  .sc-code-editor :global(.cm-editor) {
    height: 100%;
  }
  .sc-code-editor__status {
    padding: 24px;
    color: var(--m3c-on-surface-variant);
  }
  .sc-code-editor__status--error {
    color: var(--m3c-error);
  }
</style>
