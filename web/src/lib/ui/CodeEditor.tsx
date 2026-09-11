import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from 'react'
import type { Ref } from 'react'
import { useI18n } from '../i18n/use-i18n'
import type { EditorView } from '@codemirror/view'

export interface CodeEditorProps {
  value: string
  filename: string
  readOnly?: boolean
  maxBytes?: number
  onChange: (text: string) => void
  onSave?: () => void
  onLimit?: () => void
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
  { value, filename, readOnly = false, maxBytes, onChange, onSave, onLimit }: CodeEditorProps,
  ref: Ref<CodeEditorHandle>
) {
  const { t } = useI18n()
  const hostRef = useRef<HTMLDivElement>(null)
  const latestProps = useRef({ onChange, onSave, onLimit })
  latestProps.current = { onChange, onSave, onLimit }
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
    void (async () => {
      try {
        const [viewMod, stateMod, commandsMod, cmMod, langDataMod, languageMod] = await Promise.all([
          import('@codemirror/view'),
          import('@codemirror/state'),
          import('@codemirror/commands'),
          import('codemirror'),
          import('@codemirror/language-data'),
          import('@codemirror/language')
        ])
        if (disposed || !hostRef.current) return
        const { EditorView, keymap } = viewMod
        const { EditorState } = stateMod
        const { defaultKeymap, historyKeymap, history, indentWithTab } = commandsMod
        const { basicSetup } = cmMod
        const desc = languageMod.LanguageDescription.matchFilename(langDataMod.languages, filename)
        const languageExt = desc ? await desc.load() : null
        if (disposed || !hostRef.current) return
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
              EditorView.editable.of(!readOnly),
              EditorState.readOnly.of(readOnly),
              byteLimitFilter,
              updateListener,
              EditorView.theme({ '&': { height: '100%', fontSize: '0.875rem' }, '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace' } })
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
  }, [])

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
