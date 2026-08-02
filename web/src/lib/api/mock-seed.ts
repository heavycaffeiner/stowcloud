// web/src/lib/api/mock-seed.ts — deterministic, storage-free generation of a
// 100,000-entry directory so the virtual scroll has something real to
// exercise (task requirement: "Seed it with a 100,000-entry directory").
// Entries are computed from an index rather than materialized/stored, so
// seeding costs ~0 regardless of directory size.
import type { Entry, Kind } from './types'

const EXTENSIONS = ['jpg', 'png', 'pdf', 'docx', 'mp4', 'mp3', 'txt', 'zip', 'csv', 'log']
const WORDS = [
  '보고서', '휴가사진', '회의록', '견적서', '스크린샷', 'invoice', 'backup', 'draft',
  'presentation', 'notes', '계약서', '영수증', 'photo', 'export', 'render'
]

// Small mixing hash — deterministic per index, good enough distribution for fixture data.
function mix(i: number): number {
  let x = i + 0x9e3779b9
  x = Math.imul(x ^ (x >>> 16), 0x21f0aaad)
  x = Math.imul(x ^ (x >>> 15), 0x735a2d97)
  return (x ^ (x >>> 15)) >>> 0
}

// Every API path is `/{root-label}/...` and the mock
// session hands out exactly one root, `home`. The seed used to hang off `/`,
// which no screen ever asks for — `/b/home` lists `/home`, so the browser was
// empty in mock mode and the file list couldn't be looked at during dev.
const ROOT = '/home'

export const BENCH_DIR = `${ROOT}/bench`
export const BENCH_COUNT = 100_000

export function benchEntryAt(index: number): Entry {
  const h = mix(index)
  const word = WORDS[h % WORDS.length]
  const ext = EXTENSIONS[(h >>> 8) % EXTENSIONS.length]
  const isDir = index % 97 === 0
  const size = isDir ? 0 : ((h >>> 3) % (200 * 1024 * 1024)) + 512
  const mtimeNs = BigInt(1_700_000_000 + (h % 60_000_000)) * 1_000_000_000n
  const kind: Kind = isDir ? 'dir' : 'file'
  const name = isDir ? `${word}-${index}` : `${word}-${String(index).padStart(6, '0')}.${ext}`

  return {
    name,
    kind,
    size,
    mtime_ns: mtimeNs.toString(),
    etag: h.toString(16),
    perms: { read: true, write: true, create: isDir, delete: true, rename: true, move: true, share: true, download: true },
    // `100_000 +` keeps `/bench`'s ids well clear of `STATIC_SEED`'s
    // low fixed range (see `fileEntry`/`dirEntry` below) and of
    // `mock.ts`'s runtime-allocated range (`newMockFileId`, `1_000_000+`).
    id: 100_000 + index,
    preview: kind === 'file' && (ext === 'jpg' || ext === 'png') ? { available: true } : undefined,
    confusable: false
  }
}

// A shared, reused Intl.Collator: creating a fresh Collator (or calling
// localeCompare with an inline options object) per comparison is ~20x
// slower and makes sorting a 100k-entry directory take seconds instead of
// milliseconds. Reusing one instance is what makes it a one-time cost.
const NAME_COLLATOR = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

export function compareEntries(a: Entry, b: Entry, sort: 'name' | 'size' | 'mtime' | 'kind', order: 'asc' | 'desc'): number {
  let cmp = 0
  switch (sort) {
    case 'name':
      cmp = NAME_COLLATOR.compare(a.name, b.name)
      break
    case 'size':
      cmp = a.size - b.size
      break
    case 'mtime':
      cmp = BigInt(a.mtime_ns) < BigInt(b.mtime_ns) ? -1 : BigInt(a.mtime_ns) > BigInt(b.mtime_ns) ? 1 : 0
      break
    case 'kind':
      cmp = a.kind.localeCompare(b.kind)
      break
  }
  return order === 'asc' ? cmp : -cmp
}

/** Root-level seed directories, present in addition to the /bench stress fixture. */
export interface SeedDir {
  path: string
  entries: Entry[]
}

// Low, fixed range for the static seed's own entries — clear of `/bench`'s
// `100_000 +` range and `mock.ts`'s runtime-allocated `1_000_000 +` range.
let nextSeedId = 1

function fileEntry(name: string, size: number, daysAgo: number, extra: Partial<Entry> = {}): Entry {
  const mtimeNs = BigInt(Date.now() - daysAgo * 86_400_000) * 1_000_000n
  return {
    name,
    kind: 'file',
    size,
    mtime_ns: mtimeNs.toString(),
    etag: Math.random().toString(16).slice(2, 10),
    perms: { read: true, write: true, create: false, delete: true, rename: true, move: true, share: true, download: true },
    id: nextSeedId++,
    ...extra
  }
}

function dirEntry(name: string, daysAgo: number, extra: Partial<Entry> = {}): Entry {
  const mtimeNs = BigInt(Date.now() - daysAgo * 86_400_000) * 1_000_000n
  return {
    name,
    kind: 'dir',
    size: 0,
    mtime_ns: mtimeNs.toString(),
    etag: Math.random().toString(16).slice(2, 10),
    perms: { read: true, write: true, create: true, delete: true, rename: true, move: true, share: true, download: true },
    id: nextSeedId++,
    ...extra
  }
}

export const STATIC_SEED: SeedDir[] = [
  {
    path: ROOT,
    entries: [
      dirEntry('Documents', 1),
      dirEntry('Photos', 3),
      dirEntry('Videos', 10),
      dirEntry('Music', 20),
      dirEntry('bench (100,000 items)', 0, { name: 'bench' }),
      fileEntry('README.txt', 1024, 5)
    ]
  },
  {
    path: `${ROOT}/Documents`,
    entries: [
      fileEntry('2026-예산안.xlsx', 45_211, 2),
      fileEntry('제안서.docx', 128_933, 6),
      fileEntry('meeting-notes.txt', 2_048, 1),
      dirEntry('계약서', 30)
    ]
  },
  {
    path: `${ROOT}/Documents/계약서`,
    entries: [fileEntry('nda.pdf', 88_213, 30), fileEntry('service-agreement.pdf', 210_442, 45)]
  },
  {
    path: `${ROOT}/Photos`,
    entries: [
      fileEntry('휴가-2026-07-01.jpg', 4_213_665, 26, { preview: { available: true, width: 4032, height: 3024 } }),
      fileEntry('휴가-2026-07-02.jpg', 3_982_211, 26, { preview: { available: true, width: 4032, height: 3024 } }),
      fileEntry('가족사진.png', 2_112_004, 100, { preview: { available: true, width: 1920, height: 1080 } })
    ]
  },
  {
    path: `${ROOT}/Videos`,
    entries: [fileEntry('발표녹화.mp4', 1_288_490_188, 15)]
  },
  {
    path: `${ROOT}/Music`,
    entries: [fileEntry('playlist.m3u', 512, 200)]
  }
]
