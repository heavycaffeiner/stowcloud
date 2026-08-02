// Directory upload entry points: showDirectoryPicker() first, with an
// <input webkitdirectory> fallback; drag-drop via getAsFileSystemHandle(), webkitGetAsEntry() fallback.

export interface PickedFile {
  file: File
  relativePath: string
}

// Minimal ambient shapes for the File System Access API (not yet in all lib.dom.d.ts).
interface FsaDirectoryHandle {
  kind: 'directory'
  name: string
  values(): AsyncIterableIterator<FsaDirectoryHandle | FsaFileHandle>
}
interface FsaFileHandle {
  kind: 'file'
  name: string
  getFile(): Promise<File>
}

async function walkFsaDirectory(dir: FsaDirectoryHandle, prefix: string, out: PickedFile[]): Promise<void> {
  for await (const entry of dir.values()) {
    const path = prefix ? `${prefix}/${entry.name}` : entry.name
    if (entry.kind === 'file') {
      out.push({ file: await (entry as FsaFileHandle).getFile(), relativePath: path })
    } else {
      await walkFsaDirectory(entry as FsaDirectoryHandle, path, out)
    }
  }
}

export function supportsDirectoryPicker(): boolean {
  return typeof (window as unknown as { showDirectoryPicker?: unknown }).showDirectoryPicker === 'function'
}

export async function pickDirectory(): Promise<PickedFile[]> {
  const w = window as unknown as { showDirectoryPicker: () => Promise<FsaDirectoryHandle> }
  const handle = await w.showDirectoryPicker()
  const out: PickedFile[] = []
  await walkFsaDirectory(handle, handle.name, out)
  return out
}

/** Fallback: <input type="file" webkitdirectory> — files carry webkitRelativePath already. */
export function filesFromWebkitDirectoryInput(input: HTMLInputElement): PickedFile[] {
  const out: PickedFile[] = []
  const list = input.files
  if (!list) return out
  for (const file of Array.from(list)) {
    const rel = (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name
    out.push({ file, relativePath: rel })
  }
  return out
}

// ── drag & drop ──

interface WebkitEntry {
  isFile: boolean
  isDirectory: boolean
  name: string
  file(cb: (f: File) => void, err: (e: unknown) => void): void
  createReader(): { readEntries(cb: (e: WebkitEntry[]) => void, err: (e: unknown) => void): void }
}

function readAllEntries(reader: { readEntries(cb: (e: WebkitEntry[]) => void, err: (e: unknown) => void): void }): Promise<WebkitEntry[]> {
  return new Promise((resolve, reject) => {
    const all: WebkitEntry[] = []
    const next = () => {
      reader.readEntries((batch) => {
        if (batch.length === 0) {
          resolve(all)
        } else {
          all.push(...batch)
          next()
        }
      }, reject)
    }
    next()
  })
}

async function walkWebkitEntry(entry: WebkitEntry, prefix: string, out: PickedFile[]): Promise<void> {
  const path = prefix ? `${prefix}/${entry.name}` : entry.name
  if (entry.isFile) {
    const file = await new Promise<File>((resolve, reject) => entry.file(resolve, reject))
    out.push({ file, relativePath: path })
  } else if (entry.isDirectory) {
    const reader = entry.createReader()
    const children = await readAllEntries(reader)
    for (const child of children) await walkWebkitEntry(child, path, out)
  }
}

/** Handles a DataTransfer from a drop event, files and/or directories. */
export async function pickedFilesFromDataTransfer(dt: DataTransfer): Promise<PickedFile[]> {
  const out: PickedFile[] = []
  const items = Array.from(dt.items)

  for (const item of items) {
    if (item.kind !== 'file') continue

    const withFsa = item as DataTransferItem & { getAsFileSystemHandle?: () => Promise<FsaFileHandle | FsaDirectoryHandle> }
    if (typeof withFsa.getAsFileSystemHandle === 'function') {
      const handle = await withFsa.getAsFileSystemHandle()
      if (handle) {
        if (handle.kind === 'file') {
          out.push({ file: await (handle as FsaFileHandle).getFile(), relativePath: handle.name })
        } else {
          await walkFsaDirectory(handle as FsaDirectoryHandle, handle.name, out)
        }
        continue
      }
    }

    // lib.dom now declares `webkitGetAsEntry(): FileSystemEntry | null`, whose
    // FileSystemEntry has neither `file()` nor `createReader()`. Cast the
    // result, not the method — intersecting a narrower signature onto
    // DataTransferItem no longer wins overload resolution.
    const entry = item.webkitGetAsEntry?.() as WebkitEntry | null
    if (entry) {
      await walkWebkitEntry(entry, '', out)
      continue
    }

    const file = item.getAsFile()
    if (file) out.push({ file, relativePath: file.name })
  }

  return out
}
