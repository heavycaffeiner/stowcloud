import { describe, expect, it } from 'vitest'
import { isEditableFileName } from './editable-files'

describe('isEditableFileName', () => {
  it('accepts common source, configuration, data, and documentation formats', () => {
    for (const name of ['main.rs', 'schema.graphql', 'infra.tfvars', 'notes.rst', 'Dockerfile.dev', '.env.production', 'CMakeLists.txt']) {
      expect(isEditableFileName(name), name).toBe(true)
    }
  })

  it('refuses formats whose bytes should not be decoded and saved as text', () => {
    for (const name of ['photo.jpg', 'manual.pdf', 'archive.zip', 'program.exe', 'document.docx']) {
      expect(isEditableFileName(name), name).toBe(false)
    }
  })
})
