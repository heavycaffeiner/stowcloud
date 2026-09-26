import { describe, expect, it } from 'vitest'
import { QueryClient } from '@tanstack/react-query'
import { keys } from './keys'

describe('file content query keys', () => {
  it('separates content versions while retaining the path content prefix', () => {
    const oldVersion = keys.pathContent('share/note.txt', 'etag-old', true)
    const newVersion = keys.pathContent('share/note.txt', 'etag-new', true)

    expect(oldVersion).not.toEqual(newVersion)
    expect(oldVersion.slice(0, 3)).toEqual(['path', 'share/note.txt', 'content'])
    expect(newVersion.slice(0, 3)).toEqual(['path', 'share/note.txt', 'content'])
  })

  it('keeps saved content available under the new ETag key', () => {
    const client = new QueryClient()
    const oldKey = keys.pathContent('share/note.txt', 'etag-old', true)
    const savedKey = keys.pathContent('share/note.txt', 'etag-new', true)

    client.setQueryData(oldKey, { content: 'before' })
    client.setQueryData(savedKey, { content: 'after' })

    expect(client.getQueryData(savedKey)).toEqual({ content: 'after' })
    expect(client.getQueryData(oldKey)).toEqual({ content: 'before' })
    client.clear()
  })
})
