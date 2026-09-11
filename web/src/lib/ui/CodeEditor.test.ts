import { afterEach, describe, expect, it, vi } from 'vitest'

const { cleanup, fireEvent, render, waitFor } = await import('@testing-library/svelte')
const { default: CodeEditor } = await import('./CodeEditor.svelte')

afterEach(() => cleanup())

function textInEditor(container: HTMLElement): string {
  return Array.from(container.querySelectorAll('.cm-line'), (line) => line.textContent ?? '').join('\n')
}

async function mountEditor(maxBytes: number, onchange: (text: string) => void, onlimit: () => void) {
  const result = render(CodeEditor, {
    value: '',
    filename: 'fixture.unknown',
    maxBytes,
    onchange,
    onlimit
  })
  await waitFor(() => expect(result.container.querySelector('.cm-editor')).not.toBeNull())
  const content = result.container.querySelector('.cm-content')
  if (!(content instanceof HTMLElement)) throw new Error('CodeEditor did not render editable content')
  content.focus()
  return { ...result, content }
}

describe('CodeEditor', () => {
  it('counts UTF-8 newlines and rejects an oversized paste before it changes the document', async () => {
    const onchange = vi.fn()
    const onlimit = vi.fn()
    const editor = await mountEditor(4, onchange, onlimit)

    await fireEvent.paste(editor.content, {
      clipboardData: { getData: (type: string) => (type === 'text/plain' ? 'a\n\u00e9' : '') }
    })
    expect(textInEditor(editor.container)).toBe('a\n\u00e9')
    expect(onchange).toHaveBeenCalledTimes(1)
    expect(onchange).toHaveBeenLastCalledWith('a\n\u00e9')

    await fireEvent.paste(editor.content, {
      clipboardData: { getData: (type: string) => (type === 'text/plain' ? 'x' : '') }
    })
    expect(textInEditor(editor.container)).toBe('a\n\u00e9')
    expect(onchange).toHaveBeenCalledTimes(1)
    expect(onlimit).toHaveBeenCalledTimes(1)
  })
})
