import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react'
import type { Ref } from 'react'
import { useI18n } from '../i18n/use-i18n'
import type { EditorView } from '@codemirror/view'
import type { LanguageSupport } from '@codemirror/language'

export interface CodeEditorProps {
  value: string
  filename: string
  readOnly?: boolean
  maxBytes?: number
  onChange: (text: string) => void
  onSave?: () => void
  onLimit?: () => void
  onLanguageChange?: (language: string | null) => void
}

export interface CodeEditorHandle {
  focus: () => void
}

function exceedsUtf8Limit(text: Iterable<string>, limit: number): boolean {
  let bytes = 0
  let pendingHighSurrogate = false
  for (const chunk of text) {
    for (let i = 0; i < chunk.length; i++) {
      const code = chunk.charCodeAt(i)
      if (pendingHighSurrogate) {
        if (code >= 0xdc00 && code <= 0xdfff) {
          bytes += 4
          pendingHighSurrogate = false
          if (bytes > limit) return true
          continue
        }
        bytes += 3
        pendingHighSurrogate = false
        if (bytes > limit) return true
      }
      if (code <= 0x7f) bytes++
      else if (code <= 0x7ff) bytes += 2
      else if (code >= 0xd800 && code <= 0xdbff) {
        if (i + 1 < chunk.length) {
          const low = chunk.charCodeAt(i + 1)
          if (low >= 0xdc00 && low <= 0xdfff) {
            bytes += 4
            i++
          } else bytes += 3
        } else pendingHighSurrogate = true
      } else bytes += 3
      if (bytes > limit) return true
    }
  }
  if (pendingHighSurrogate) bytes += 3
  return bytes > limit
}

export const CodeEditor = forwardRef(function CodeEditor(
  { value, filename, readOnly = false, maxBytes, onChange, onSave, onLimit, onLanguageChange }: CodeEditorProps,
  ref: Ref<CodeEditorHandle>
) {
  const { t } = useI18n()
  const hostRef = useRef<HTMLDivElement>(null)
  const latestProps = useRef({ onChange, onSave, onLimit, onLanguageChange })
  latestProps.current = { onChange, onSave, onLimit, onLanguageChange }
  const viewRef = useRef<EditorView | null>(null)
  const lastEchoRef = useRef(value)
  const [ready, setReady] = useState(false)
  const [failed, setFailed] = useState(false)

  useImperativeHandle(ref, () => ({
    focus: () => {
      viewRef.current?.focus()
      hostRef.current?.querySelector<HTMLElement>('.cm-content')?.focus()
    }
  }), [])

  useEffect(() => {
    let disposed = false
    setReady(false)
    setFailed(false)
    lastEchoRef.current = value
    void (async () => {
      try {
        const [viewMod, stateMod, commandsMod, cmMod, langDataMod, languageMod, highlightMod] = await Promise.all([
          import('@codemirror/view'),
          import('@codemirror/state'),
          import('@codemirror/commands'),
          import('codemirror'),
          import('@codemirror/language-data'),
          import('@codemirror/language'),
          import('@lezer/highlight')
        ])
        if (disposed || !hostRef.current) return
        const { EditorView, keymap } = viewMod
        const { EditorState } = stateMod
        const { defaultKeymap, historyKeymap, history, indentWithTab } = commandsMod
        const { basicSetup } = cmMod
        const desc = languageMod.LanguageDescription.matchFilename(langDataMod.languages, filename)
        let languageExt: LanguageSupport | null = null
        if (desc) {
          try {
            languageExt = await desc.load()
          } catch (error) {
            console.warn(`Could not load syntax highlighting for ${filename}`, error)
          }
        }
        if (disposed || !hostRef.current) return
        latestProps.current.onLanguageChange?.(languageExt ? desc?.name ?? null : null)
        const syntaxTheme = languageMod.HighlightStyle.define([
          { tag: [highlightMod.tags.keyword, highlightMod.tags.modifier, highlightMod.tags.operatorKeyword], color: 'var(--sc-code-keyword)' },
          { tag: [highlightMod.tags.string, highlightMod.tags.regexp, highlightMod.tags.special(highlightMod.tags.string)], color: 'var(--sc-code-string)' },
          { tag: [highlightMod.tags.number, highlightMod.tags.bool, highlightMod.tags.null], color: 'var(--sc-code-number)' },
          { tag: [highlightMod.tags.typeName, highlightMod.tags.className, highlightMod.tags.namespace], color: 'var(--sc-code-type)' },
          { tag: [highlightMod.tags.definition(highlightMod.tags.variableName), highlightMod.tags.function(highlightMod.tags.variableName), highlightMod.tags.labelName], color: 'var(--sc-code-definition)' },
          { tag: [highlightMod.tags.comment, highlightMod.tags.lineComment, highlightMod.tags.blockComment], color: 'var(--sc-code-comment)', fontStyle: 'italic' },
          { tag: [highlightMod.tags.heading, highlightMod.tags.strong], color: 'var(--sc-code-heading)', fontWeight: '700' },
          { tag: highlightMod.tags.emphasis, fontStyle: 'italic' },
          { tag: [highlightMod.tags.link, highlightMod.tags.url], color: 'var(--sc-code-link)', textDecoration: 'underline' },
          { tag: highlightMod.tags.invalid, color: 'var(--sc-code-invalid)' }
        ])
        const byteLimitFilter = EditorState.transactionFilter.of((tr) => {
          if (maxBytes === undefined || !tr.docChanged) return tr
          let hasInsertion = false
          tr.changes.iterChanges((_fromA, _toA, _fromB, _toB, inserted) => {
            if (inserted.length > 0) hasInsertion = true
          })
          if (!hasInsertion || !exceedsUtf8Limit(tr.newDoc, maxBytes)) return tr
          if (tr.isUserEvent('input')) latestProps.current.onLimit?.()
          return { changes: [] }
        })
        const saveKeymap = keymap.of([{
          key: 'Mod-s', preventDefault: true,
          run: () => { latestProps.current.onSave?.(); return true }
        }])
        const updateListener = EditorView.updateListener.of((update) => {
          if (!update.docChanged) return
          const next = update.state.doc.toString()
          lastEchoRef.current = next
          latestProps.current.onChange(next)
        })
        viewRef.current = new EditorView({
          parent: hostRef.current,
          state: EditorState.create({
            doc: value,
            extensions: [
              basicSetup,
              history(),
              keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
              saveKeymap,
              languageExt ? languageExt.extension : [],
              languageMod.syntaxHighlighting(syntaxTheme),
              EditorView.editable.of(!readOnly),
              EditorState.readOnly.of(readOnly),
              byteLimitFilter,
              updateListener,
              EditorView.theme({
                '&': { height: '100%', fontSize: '0.875rem', backgroundColor: 'transparent', color: 'var(--sc-code-foreground)' },
                '&.cm-focused': { outline: 'none' },
                '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace', lineHeight: '1.65' },
                '.cm-content': { padding: '12px 0', caretColor: 'var(--sc-code-caret)' },
                '.cm-line': { padding: '0 18px 0 10px' },
                '.cm-gutters': { backgroundColor: 'var(--sc-code-gutter)', color: 'var(--sc-code-gutter-text)', border: 'none', paddingLeft: '8px' },
                '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'var(--sc-code-active-line)' },
                '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection': { backgroundColor: 'var(--sc-code-selection) !important' }
              })
            ]
          })
        })
        if (!disposed) setReady(true)
      } catch (error) {
        console.error('CodeMirror failed to load', error)
        if (!disposed) setFailed(true)
      }
    })()
    return () => {
      disposed = true
      viewRef.current?.destroy()
      viewRef.current = null
    }
  }, [filename, maxBytes, readOnly])

  useEffect(() => {
    const view = viewRef.current
    if (!view || value === lastEchoRef.current) return
    lastEchoRef.current = value
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value }, filter: false })
  }, [value])

  return (
    <div ref={hostRef} className="sc-code-editor">
      {failed ? <p className="sc-code-editor__status sc-code-editor__status--error">{t('editor.could_not_load_editor_check')}</p> : null}
      {!failed && !ready ? <p className="sc-code-editor__status">{t('editor.loading_editor')}</p> : null}
    </div>
  )
})
