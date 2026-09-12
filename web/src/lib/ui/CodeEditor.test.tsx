import { describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '../../test/test-utils'
import { createRef } from 'react'
import { CodeEditor, type CodeEditorHandle } from './CodeEditor'
vi.mock('../i18n/use-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('CodeEditor', () => {
  it('loads CodeMirror and exposes focus', async () => {
    const ref = createRef<CodeEditorHandle>()
    const { container } = render(<CodeEditor ref={ref} value="hello" filename="note.txt" onChange={vi.fn()} />)

    await waitFor(() => expect(container.querySelector('.cm-editor')).not.toBeNull(), { timeout: 5000 })
    const content = container.querySelector('.cm-content') as HTMLElement
    expect(content.textContent).toContain('hello')

    ref.current?.focus()
    expect(document.activeElement).toBe(content)
  })

  it('loads syntax highlighting for the file type', async () => {
    const onLanguageChange = vi.fn()
    const { container } = render(
      <CodeEditor value={'const answer = \"yes\"'} filename="example.ts" onChange={vi.fn()} onLanguageChange={onLanguageChange} />
    )

    await waitFor(() => expect(onLanguageChange).toHaveBeenCalledWith('TypeScript'), { timeout: 5000 })
    const tokens = Array.from(container.querySelectorAll('.cm-line span'), (element) => element.textContent)
    expect(tokens).toContain('const')
    expect(tokens).toContain('\"yes\"')
  })

})
