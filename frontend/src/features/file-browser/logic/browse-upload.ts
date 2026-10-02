import { addEntries, addFiles } from '../../upload/queue'
import { askConflictPolicy } from '../components/ConflictDialog'
import type { PickedFile } from '../../upload/directory-picker'
import type { Entry } from '../api'

function uniqueUploadName(name: string, taken: ReadonlySet<string>): string {
  if (!taken.has(name)) return name
  const dot = name.lastIndexOf('.')
  const stem = dot > 0 ? name.slice(0, dot) : name
  const ext = dot > 0 ? name.slice(dot) : ''
  let count = 1
  while (taken.has(`${stem} (${count})${ext}`)) count += 1
  return `${stem} (${count})${ext}`
}

/**
 * The items to upload once name conflicts with `listed` are settled. Asks once for all conflicts and
 * returns nothing when the question is dismissed. `nameOf` gives an item's top-level name, or '' for an
 * item inside a picked folder, which never conflicts.
 */
async function settleConflicts<T>(
  items: readonly T[],
  listed: readonly Entry[],
  nameOf: (item: T) => string,
  withName: (item: T, name: string) => T
): Promise<readonly T[]> {
  const taken = new Set(listed.map((entry) => entry.name))
  const conflicted = items.filter((item) => taken.has(nameOf(item)))
  if (!conflicted.length) return items
  const first = nameOf(conflicted[0])
  const policy = await askConflictPolicy(conflicted.length === 1 ? first : `${first} (+${conflicted.length - 1})`)
  const conflicts = new Set(conflicted)
  if (policy === 'overwrite') return items
  if (policy === 'skip') return items.filter((item) => !conflicts.has(item))
  if (policy !== 'rename') return []
  return items.map((item) => {
    if (!conflicts.has(item)) return item
    const name = uniqueUploadName(nameOf(item), taken)
    taken.add(name)
    return withName(item, name)
  })
}

const renamedFile = (file: File, name: string): File =>
  new File([file], name, { type: file.type, lastModified: file.lastModified })

export async function uploadFiles(files: readonly File[], dest: string, listed: readonly Entry[]): Promise<void> {
  const settled = await settleConflicts(files, listed, (file) => file.name, renamedFile)
  if (settled.length) await addFiles(settled, dest)
}

export async function uploadEntries(
  picked: readonly PickedFile[],
  dest: string,
  listed: readonly Entry[]
): Promise<void> {
  const settled = await settleConflicts(
    picked,
    listed,
    (entry) => (entry.relativePath ? '' : entry.file.name),
    (entry, name) => ({ file: renamedFile(entry.file, name), relativePath: entry.relativePath })
  )
  if (settled.length) await addEntries(settled, dest)
}
