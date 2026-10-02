// Lists a zip archive stored on an end-to-end encrypted share by reading the
// archive's central directory through ranged ciphertext fetches. The server
// holds no key for an encrypted share, so it cannot list this itself.
// Character-encoding fallback mirrors archive.go's ListArchive.
import { ZipReader, Reader } from '@zip.js/zip.js'
import { contentUrl, type ArchiveEntry, type ArchiveListing, type Entry } from '../file-browser/api'
import {
  BLOCK_SIZE,
  ciphertextSpanForRange,
  decryptPlaintextRange,
  plaintextSizeFromCiphertextSize
} from '../../lib/crypto/e2ee'
import { detectCjkCharset, isSafeArchiveName, isValidUtf8, MAX_SAMPLE_BYTES } from '../../lib/zip/archive-names'

// Matches limits.ArchiveEntriesListed in backend/internal/preview/limits.
// The plain-share listing uses the same cap, so encrypted archives do not
// disclose more than plain archives.
const MAX_ENTRIES = 10_000

/** A zip.js `Reader` over one encrypted file's plaintext, backed by ranged
 *  ciphertext fetches decrypted through e2ee.ts's range primitives.
 *
 * Decrypted blocks are cached by block index rather than by the exact
 * `(index, length)` zip.js happens to ask for: zip.js's own
 * end-of-central-directory search and the central directory read that
 * follows it commonly overlap for anything but a very large archive, and a
 * per-block cache turns the second of those into a cache hit instead of a
 * second fetch and decrypt.
 */
class EncryptedZipReader extends Reader<void> {
  private readonly entry: Entry
  private readonly salt: string
  private nonce0: Uint8Array | null = null
  private readonly blocks = new Map<number, Uint8Array>()

  constructor(entry: Entry, salt: string) {
    super(undefined as unknown as void)
    this.entry = entry
    this.salt = salt
  }

  async init(): Promise<void> {
    this.size = plaintextSizeFromCiphertextSize(this.entry.size)
  }

  /** The file's own first-block nonce (bytes 8..32 of its ciphertext),
   *  fetched once and reused for every block this reader ever decrypts;
   *  the same single small ranged read download-sw.ts's `nonceForToken`
   *  makes for the same reason. */
  private async nonce(): Promise<Uint8Array> {
    if (this.nonce0) return this.nonce0
    const res = await fetch(contentUrl(this.entry), { credentials: 'include', headers: { Range: 'bytes=0-31' } })
    if (!res.ok || !res.body) {
      throw new Error(`could not read the rclone-crypt header for ${this.entry.path}: HTTP ${res.status}`)
    }
    const header = new Uint8Array(await res.arrayBuffer())
    if (header.length < 32) {
      throw new Error(`${this.entry.path} is too short to be an rclone-crypt file`)
    }
    this.nonce0 = header.subarray(8, 32)
    return this.nonce0
  }

  /** Fetches and decrypts every block in `[startBlock, endBlock]` this
   *  reader has not already cached, as one merged ranged request (never
   *  one request per block), and caches each block it produces. */
  private async ensureBlocks(startBlock: number, endBlock: number): Promise<void> {
    let missing = false
    for (let b = startBlock; b <= endBlock; b++) {
      if (!this.blocks.has(b)) {
        missing = true
        break
      }
    }
    if (!missing) return

    const nonce0 = await this.nonce()
    const rangeStart = startBlock * BLOCK_SIZE
    const rangeEnd = Math.min(endBlock * BLOCK_SIZE + BLOCK_SIZE, this.size)
    const span = ciphertextSpanForRange(rangeStart, rangeEnd, this.size)
    const res = await fetch(contentUrl(this.entry), {
      credentials: 'include',
      headers: { Range: `bytes=${span.offset}-${span.offset + span.length - 1}` }
    })
    if (!res.ok || !res.body) throw new Error(`could not fetch ${this.entry.path}: HTTP ${res.status}`)
    const cipherBytes = new Uint8Array(await res.arrayBuffer())
    const plaintext = decryptPlaintextRange(this.salt, nonce0, rangeStart, rangeEnd, cipherBytes)

    for (let b = startBlock; b <= endBlock; b++) {
      const blockStart = b * BLOCK_SIZE - rangeStart
      const blockEnd = Math.min(blockStart + BLOCK_SIZE, plaintext.length)
      this.blocks.set(b, plaintext.subarray(blockStart, blockEnd))
    }
  }

  async readUint8Array(index: number, length: number): Promise<Uint8Array> {
    const end = Math.min(index + length, this.size)
    if (end <= index) return new Uint8Array(0)
    const startBlock = Math.floor(index / BLOCK_SIZE)
    const endBlock = Math.floor((end - 1) / BLOCK_SIZE)
    await this.ensureBlocks(startBlock, endBlock)

    const out = new Uint8Array(end - index)
    let pos = 0
    for (let b = startBlock; b <= endBlock; b++) {
      // Set by ensureBlocks above; every block in range is guaranteed present.
      const block = this.blocks.get(b) as Uint8Array
      const blockStart = b * BLOCK_SIZE
      const from = Math.max(0, index - blockStart)
      const to = Math.min(block.length, end - blockStart)
      out.set(block.subarray(from, to), pos)
      pos += to - from
    }
    return out
  }
}

/**
 * Lists a zip archive stored on an encrypted share, in the same shape
 * the server's own listing returns for a plain one, so the preview dialog renders one
 * listing type regardless of which share produced it.
 *
 * `entry` is the archive's own listing row (its `size` is the file's
 * ciphertext length, its `content` reference is what `fetch` reads); `salt`
 * is the share's own salt. Throws `LockedSessionError` (from
 * `decryptPlaintextRange`) when that share's key is not the unlocked one, so
 * a caller can raise the same unlock prompt a download does.
 */
export async function listEncryptedArchive(entry: Entry, salt: string): Promise<ArchiveListing> {
  const reader = new EncryptedZipReader(entry, salt)
  const zipReader = new ZipReader(reader, {
    // archive.go's own reader (Go's archive/zip) never refuses a name for
    // being unsafe to display; safety is entirely this module's own filter
    // below, exactly as it is there. Without this, zip.js's default
    // "balanced" validation would throw before that filter ever ran.
    filenameValidation: 'tolerant'
  })
  try {
    const zipEntries = await zipReader.getEntries()

    // A name not declared UTF-8 needs decoding when its raw bytes are not
    // already valid UTF-8 on their own: archiveNameNeedsDecode's own test,
    // mirrored here. The sample built from those names is what the
    // detector above sees, capped the same way the server caps it.
    const needsDecode: boolean[] = new Array(zipEntries.length)
    let sample = new Uint8Array(0)
    for (let i = 0; i < zipEntries.length; i++) {
      const e = zipEntries[i]
      const need = !e.filenameUTF8 && !isValidUtf8(e.rawFilename)
      needsDecode[i] = need
      if (need && sample.length < MAX_SAMPLE_BYTES) {
        const merged = new Uint8Array(sample.length + e.rawFilename.length)
        merged.set(sample)
        merged.set(e.rawFilename, sample.length)
        sample = merged
      }
    }
    const label = detectCjkCharset(sample)

    const entries: ArchiveEntry[] = []
    let truncated = false
    let skipped = 0
    for (let i = 0; i < zipEntries.length; i++) {
      if (entries.length >= MAX_ENTRIES) {
        truncated = true
        break
      }
      const e = zipEntries[i]
      let name: string
      if (e.filenameUTF8) {
        name = e.filename
      } else if (!needsDecode[i]) {
        // Not flagged, but the raw bytes are already valid UTF-8 (a tool
        // that forgot to set the flag): decode as UTF-8 rather than taking
        // zip.js's own CP437-assuming default, which would be wrong here.
        name = new TextDecoder('utf-8').decode(e.rawFilename)
      } else if (label) {
        name = new TextDecoder(label).decode(e.rawFilename)
      } else {
        // No legacy code page could be placed: zip.js's own `filename` is
        // already the CP437 fallback the zip format specifies for this
        // case, so it is taken as-is rather than decoded a second time.
        name = e.filename
      }
      name = name.normalize('NFC')
      if (!isSafeArchiveName(name)) {
        skipped++
        continue
      }
      entries.push({ name, size: e.uncompressedSize, kind: e.directory ? 'dir' : 'file' })
    }
    return { entries, truncated, limit: entries.length, skipped }
  } finally {
    await zipReader.close()
  }
}
