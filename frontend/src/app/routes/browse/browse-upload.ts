import type { Entry, OnConflict } from '../../../lib/api/client'
import { addEntries, addFiles } from '../../../lib/upload/queue'
import type { BrowseState } from './types'

type Patch = (patch: Partial<BrowseState> | ((state: BrowseState) => Partial<BrowseState>)) => void
type PickedEntry = { file: File; relativePath: string }

type UploadContext = {
  entries: readonly Entry[]
  path: string
  patch: Patch
}

function uniqueUploadName(name: string, isTaken: (candidate: string) => boolean): string {
  if (!isTaken(name)) return name
  const dot = name.lastIndexOf('.')
  const stem = dot > 0 ? name.slice(0, dot) : name
  const ext = dot > 0 ? name.slice(dot) : ''
  let count = 1
  while (isTaken(`${stem} (${count})${ext}`)) count += 1
  return `${stem} (${count})${ext}`
}

export function createBrowseUploadActions({ entries, path, patch }: UploadContext) {
  const showConflict = (name: string, retry: (policy: OnConflict) => void, count: number): void => patch({ conflictName: count === 1 ? name : `${name} (+${count - 1})`, conflictRetry: () => retry, conflictOpen: true })
  const handleFiles = (files: FileList | File[]): void => {
    const incoming = Array.from(files)
    if (!incoming.length) return
    const conflicts = incoming.filter((file) => entries.some((entry) => entry.name === file.name))
    if (!conflicts.length) { void addFiles(incoming, path); return }
    showConflict(conflicts[0].name, (policy) => {
      patch({ conflictOpen: false })
      if (policy === 'overwrite') { void addFiles(incoming, path); return }
      if (policy === 'skip') {
        const conflictNames = new Set(conflicts.map((file) => file.name))
        const remaining = incoming.filter((file) => !conflictNames.has(file.name))
        if (remaining.length) void addFiles(remaining, path)
        return
      }
      const handedOut = new Set<string>()
      const renamed = incoming.map((file) => {
        if (!entries.some((entry) => entry.name === file.name)) return file
        const name = uniqueUploadName(file.name, (candidate) => entries.some((entry) => entry.name === candidate) || handedOut.has(candidate))
        handedOut.add(name)
        return new File([file], name, { type: file.type, lastModified: file.lastModified })
      })
      void addFiles(renamed, path)
    }, conflicts.length)
  }
  const handleEntries = (picked: readonly PickedEntry[]): void => {
    if (!picked.length) return
    const conflicts = picked.filter((entry) => !entry.relativePath && entries.some((listed) => listed.name === entry.file.name))
    if (!conflicts.length) { void addEntries(picked, path); return }
    showConflict(conflicts[0].file.name, (policy) => {
      patch({ conflictOpen: false })
      if (policy === 'overwrite') { void addEntries(picked, path); return }
      if (policy === 'skip') {
        const conflictNames = new Set(conflicts.map((entry) => entry.file.name))
        const remaining = picked.filter((entry) => entry.relativePath || !conflictNames.has(entry.file.name))
        if (remaining.length) void addEntries(remaining, path)
        return
      }
      const handedOut = new Set<string>()
      const renamed = picked.map((entry) => {
        if (entry.relativePath || !entries.some((listed) => listed.name === entry.file.name)) return entry
        const name = uniqueUploadName(entry.file.name, (candidate) => entries.some((listed) => listed.name === candidate) || handedOut.has(candidate))
        handedOut.add(name)
        return { file: new File([entry.file], name, { type: entry.file.type, lastModified: entry.file.lastModified }), relativePath: entry.relativePath }
      })
      void addEntries(renamed, path)
    }, conflicts.length)
  }
  return { handleFiles, handleEntries }
}
