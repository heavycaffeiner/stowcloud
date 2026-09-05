// web/src/lib/crypto/e2ee.ts: the only place in the frontend that touches
// this share's rclone-crypt encryption format (rclone's own `crypt` backend
// format, byte for byte): an 8-byte `RCLONE\x00\x00` header, a 24-byte
// nonce, then one SecretBox block per 65536 plaintext bytes. The derived
// key lives only in this module's own scope; nothing exports it.
import { xsalsa20poly1305 } from '@noble/ciphers/salsa.js'
import { clean, concatBytes, equalBytes, randomBytes } from '@noble/ciphers/utils.js'
import { scryptAsync } from '@noble/hashes/scrypt.js'
import { ByteAccumulator } from './decrypt-stream'

/** Hard ceiling on one whole-buffer encrypt or decrypt: encryptForUpload
 *  and decryptDownload hold a whole file's plaintext and ciphertext in
 *  memory at once, so an unbounded file would crash the tab instead of being refused cleanly. */
export const MAX_ENCRYPTABLE_BYTES = 1 << 30 // 1 GiB

/** "RCLONE\x00\x00", the fixed 8-byte header rclone-crypt puts at the start
 *  of every file it writes and every verifier's own 67-byte body. */
const HEADER = new Uint8Array([0x52, 0x43, 0x4c, 0x4f, 0x4e, 0x45, 0x00, 0x00])

/** Bytes 8..32 of a file: the SecretBox nonce for its first block. Each
 *  later block's nonce is this one incremented once per block. */
const NONCE_BYTES = 24

/** Plaintext bytes per SecretBox block; only the file's last block may hold
 *  fewer. Fixed by the format, not a tuning knob. */
const BLOCK_SIZE = 65536

/** The exact 19 bytes a verifier is the rclone-crypt encryption of. Picked
 *  here, not by rclone: nothing about this string is part of the on-disk
 *  format itself, it only has to be a fixed value both the enable flow and
 *  every later unlock agree on. */
const VERIFIER_PLAINTEXT = new TextEncoder().encode('stowcloud/verify/v1')

/** Thrown by `encryptForUpload`/`decryptDownload` when no key is unlocked.
 *  Distinguishes "ask for the passphrase" from a real decrypt failure
 *  (corrupt or truncated ciphertext), which a caller needs to tell apart:
 *  one prompts, the other shows a broken-file state. */
export class LockedSessionError extends Error {
  constructor() {
    super('the share key is locked; unlock with the passphrase before encrypting or decrypting')
    this.name = 'LockedSessionError'
  }
}

/** Thrown by every encrypt/decrypt entry point in this module for a buffer
 *  over `MAX_ENCRYPTABLE_BYTES`, before any cipher call is made. */
export class FileTooLargeError extends Error {
  constructor(public readonly byteLength: number) {
    super(`file is ${byteLength} bytes, over the ${MAX_ENCRYPTABLE_BYTES}-byte limit this path accepts`)
    this.name = 'FileTooLargeError'
  }
}

/** Thrown by `unlock` when the passphrase does not reproduce the key the
 *  share's verifier was made with. */
export class WrongPassphraseError extends Error {
  constructor() {
    super('the passphrase does not open this share')
    this.name = 'WrongPassphraseError'
  }
}

/**
 * The scrypt-derived key material for one share, opaque to every caller
 * outside this module. Holds only `dataKey`, the 32-byte SecretBox key
 * (bytes 0..32 of the 80-byte scrypt output): `nameKey`/`nameTweak` (bytes
 * 32..80) exist only because shortening scrypt's `dkLen` would change every
 * byte it produces, and this share never turns on filename encryption
 * (`filename_encryption = off`), so nothing here ever reads them.
 */
export interface DerivedKeys {
  readonly dataKey: Uint8Array
}

/**
 * The unlocked key, held only for this page's lifetime, together with the
 * salt it was derived from.
 *
 * The salt is stored alongside it because it identifies which share the key
 * belongs to: it is 128 random bits minted once per share. Holding the key
 * without it made a silent data-loss path: unlock share A, upload to
 * encrypted share B, and the file is encrypted under A's key, so B's real
 * passphrase can never open it. Decryption catches a mismatch by itself
 * because the Poly1305 tag fails, but encryption cannot, so every entry
 * point below names the salt it is working for and this module refuses
 * rather than guessing.
 *
 * MUST NOT be written to `localStorage`, `sessionStorage`, `IndexedDB`, a
 * cookie or the URL, and neither may the passphrase that derives it: both
 * are the one secret this whole feature exists to keep off the server, and
 * writing either to persistent browser storage would keep it beyond a tab
 * close, defeating that. It lives in this module-scoped variable alone,
 * gone on reload and cleared early by `lock()`.
 */
let unlocked: { salt: string; dataKey: Uint8Array } | null = null

/** Whether a key is unlocked in this tab, and when `salt` is given, whether
 *  it is that share's key rather than some other share's. */
export function isUnlocked(salt?: string): boolean {
  if (unlocked === null) return false
  return salt === undefined || unlocked.salt === salt
}

/** Drops the unlocked key, zeroing its bytes first: `Uint8Array.fill(0)`
 *  gives no hard guarantee against a copy the engine already made
 *  elsewhere, but it is the same best-effort noble's own `clean()` performs,
 *  and it costs nothing to do. The passphrase was never held past
 *  `deriveKeys`'s own call frame, so this is the whole of "log out" for this
 *  module. */
export function lock(): void {
  if (unlocked !== null) clean(unlocked.dataKey)
  unlocked = null
}

/** The unlocked key for the share `salt` names, or a refusal naming which of
 *  the two reasons applies. One function so the two entry points below
 *  cannot drift apart on which case throws what. */
function keysFor(salt: string): DerivedKeys {
  if (unlocked === null || unlocked.salt !== salt) throw new LockedSessionError()
  return { dataKey: unlocked.dataKey }
}

function bytesToBase64(bytes: Uint8Array): string {
  // btoa works on a binary string, not bytes directly, and building that
  // string with one spread per call blows the call stack past a few tens of
  // thousands of bytes: chunked instead, well under any engine's argument
  // limit.
  let binary = ''
  const chunkSize = 0x8000
  for (let i = 0; i < bytes.length; i += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunkSize))
  }
  return btoa(binary)
}

function base64ToBytes(b64: string): Uint8Array {
  const binary = atob(b64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes
}

function toBytes(body: Uint8Array | ArrayBuffer): Uint8Array {
  return body instanceof Uint8Array ? body : new Uint8Array(body)
}

/**
 * Adds one to a nonce as a 24-byte little-endian counter: byte 0 carries
 * into byte 1 and so on (`nonce.carry(0)` in rclone's own cipher.go). Each
 * SecretBox block after the file's first uses the previous block's nonce
 * incremented this way, never a fresh random one.
 */
function incrementNonce(nonce: Uint8Array): Uint8Array {
  const next = nonce.slice()
  for (let i = 0; i < next.length; i++) {
    next[i] = (next[i] + 1) & 0xff
    if (next[i] !== 0) break
  }
  return next
}

/** Generates a fresh, random rclone `password2`: 16 random bytes as
 *  base64url without padding, which is exactly the 22 characters rclone's
 *  own wire shape and this server's validation both expect. Public by
 *  construction (a salt is not a secret); the user still has to see and
 *  retype it, since it is what they enter as `password2` in `rclone config`. */
export function generateSalt(): string {
  const raw = bytesToBase64(randomBytes(16))
  return raw.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/**
 * Derives one share's key material from a passphrase and its salt via
 * `scrypt(password, salt, N=16384, r=8, p=1, dkLen=80)`, exactly as rclone's
 * own `crypt` backend does. `salt` is passed through as the plain string it
 * is (rclone's `password2` value itself, not bytes decoded out of it): both
 * `password` and `salt` are UTF-8 encoded by `scryptAsync` itself.
 */
export async function deriveKeys(passphrase: string, salt: string): Promise<DerivedKeys> {
  const material = await scryptAsync(passphrase, salt, { N: 16384, r: 8, p: 1, dkLen: 80 })
  const key = material.slice(0, 32)
  clean(material)
  return { dataKey: key }
}

/** Encrypts `plaintext` as one rclone-crypt file (header, its own random
 *  nonce, one SecretBox block per 65536 plaintext bytes) under `keys`. Used
 *  both for a real upload (`encryptForUpload`, against the unlocked share
 *  key) and for minting a verifier (`makeVerifier`, against a freshly
 *  derived one before it is ever unlocked). */
function encryptRcloneCrypt(keys: DerivedKeys, plaintext: Uint8Array): Uint8Array {
  const nonce0 = randomBytes(NONCE_BYTES)
  const parts: Uint8Array[] = [HEADER, nonce0]
  let nonce: Uint8Array = nonce0
  for (let offset = 0; offset < plaintext.length; offset += BLOCK_SIZE) {
    const block = plaintext.subarray(offset, Math.min(offset + BLOCK_SIZE, plaintext.length))
    parts.push(xsalsa20poly1305(keys.dataKey, nonce).encrypt(block))
    nonce = incrementNonce(nonce)
  }
  return concatBytes(...parts)
}

/** Decrypts one SecretBox block (a 16-byte Poly1305 tag followed by 1..65536
 *  ciphertext bytes) under `keys` and `nonce`, and returns the nonce the
 *  next block uses. The one place both the whole-buffer decrypt
 *  (`decryptRcloneCrypt`) and the streaming decrypt (`decryptStream`) walk a
 *  block and its nonce, so the two can never drift on either step. Throws
 *  when the tag does not verify: wrong key, or the bytes were corrupted or
 *  truncated in transit. */
function decryptBlock(
  keys: DerivedKeys,
  nonce: Uint8Array,
  block: Uint8Array
): { plaintext: Uint8Array; nextNonce: Uint8Array } {
  return { plaintext: xsalsa20poly1305(keys.dataKey, nonce).decrypt(block), nextNonce: incrementNonce(nonce) }
}

/** Decrypts one rclone-crypt file under `keys`. Throws on a header that
 *  does not read `RCLONE\x00\x00`, on a file shorter than the fixed
 *  32-byte header+nonce, or on any block whose Poly1305 tag does not verify
 *  (wrong key, or the bytes were corrupted or truncated in transit). */
function decryptRcloneCrypt(keys: DerivedKeys, ciphertext: Uint8Array): Uint8Array {
  if (ciphertext.length < 32 || !equalBytes(ciphertext.subarray(0, 8), HEADER)) {
    throw new Error('not an rclone-crypt file: missing or wrong 8-byte header')
  }
  let nonce: Uint8Array = ciphertext.subarray(8, 32)
  const parts: Uint8Array[] = []
  let offset = 32
  while (offset < ciphertext.length) {
    const blockLen = Math.min(ciphertext.length - offset, BLOCK_SIZE + 16)
    const decrypted = decryptBlock(keys, nonce, ciphertext.subarray(offset, offset + blockLen))
    parts.push(decrypted.plaintext)
    nonce = decrypted.nextNonce
    offset += blockLen
  }
  return concatBytes(...parts)
}

/**
 * The base64-encoded 67-byte verifier an admin sends the enable-encryption
 * endpoint alongside the salt: the rclone-crypt encryption of a fixed
 * 19-byte string under `keys`. Exists so a mistyped passphrase is caught
 * immediately rather than silently writing files under a key nothing else
 * agrees on.
 */
export async function makeVerifier(keys: DerivedKeys): Promise<string> {
  return bytesToBase64(encryptRcloneCrypt(keys, VERIFIER_PLAINTEXT))
}

/** Whether `verifier` (the share's stored 67-byte verifier) decrypts under
 *  `keys` to exactly the fixed verifier plaintext. `false` for a wrong key,
 *  never a throw: this is the one place in the module a decrypt failure is
 *  an expected outcome, not an error condition. */
export async function checkVerifier(keys: DerivedKeys, verifier: Uint8Array): Promise<boolean> {
  if (verifier.length !== 67 || !equalBytes(verifier.subarray(0, 8), HEADER)) return false
  try {
    return equalBytes(decryptRcloneCrypt(keys, verifier), VERIFIER_PLAINTEXT)
  } catch {
    return false
  }
}

/**
 * Derives this share's key from `passphrase` and `salt`, proves it against
 * `verifier` (the share's own stored one, standard base64 or already-decoded
 * bytes), and, only once that check passes, holds the key unlocked in this
 * tab. Throws `WrongPassphraseError` rather than surfacing a generic decrypt
 * failure, so a caller can show "wrong passphrase" without inspecting one.
 */
export async function unlock(passphrase: string, salt: string, verifier: Uint8Array | ArrayBuffer | string): Promise<void> {
  const verifierBytes = typeof verifier === 'string' ? base64ToBytes(verifier) : toBytes(verifier)
  const keys = await deriveKeys(passphrase, salt)
  if (!(await checkVerifier(keys, verifierBytes))) {
    clean(keys.dataKey)
    throw new WrongPassphraseError()
  }
  lock()
  unlocked = { salt, dataKey: keys.dataKey }
}

/**
 * Encrypts one whole file body for upload to the share `salt` names.
 *
 * Unlike the withdrawn public-key design, rclone-crypt is symmetric: the
 * same key that decrypts a share also encrypts for it, so there is no
 * "encrypt without unlocking" path any more. Throws `LockedSessionError`
 * before doing anything else when that share's key is not the unlocked one,
 * whether because nothing is unlocked or because a different share is; the
 * caller prompts for this share's passphrase rather than encrypting under
 * whatever key happens to be in hand, which would produce a file this
 * share's own passphrase could never open.
 *
 * `body` is read fully into memory before encryption starts (see
 * `MAX_ENCRYPTABLE_BYTES`), refused with `FileTooLargeError` rather than
 * attempted when it is over that bound.
 */
export async function encryptForUpload(
  body: Blob | ArrayBuffer | Uint8Array,
  salt: string
): Promise<Uint8Array> {
  const keys = keysFor(salt)
  const bytes = body instanceof Blob ? new Uint8Array(await body.arrayBuffer()) : toBytes(body)
  if (bytes.byteLength > MAX_ENCRYPTABLE_BYTES) throw new FileTooLargeError(bytes.byteLength)
  return encryptRcloneCrypt(keys, bytes)
}

/**
 * Decrypts one downloaded file body from the share `salt` names.
 *
 * Throws `LockedSessionError` before touching any cipher at all when that
 * share's key is not the unlocked one, so a caller can tell "prompt for the
 * passphrase" apart from a real decrypt failure (corrupt or truncated
 * ciphertext) and show the right one of those two states instead of a
 * generic broken-file error either way.
 */
export async function decryptDownload(
  ciphertext: Uint8Array | ArrayBuffer,
  salt: string
): Promise<Uint8Array> {
  const keys = keysFor(salt)
  const bytes = toBytes(ciphertext)
  if (bytes.byteLength > MAX_ENCRYPTABLE_BYTES) throw new FileTooLargeError(bytes.byteLength)
  return decryptRcloneCrypt(keys, bytes)
}

/**
 * Decrypts a whole rclone-crypt file incrementally: the counterpart of
 * `decryptDownload` for a file too large to hold twice over in memory (once
 * as ciphertext, once as plaintext), which is exactly the situation a
 * folder download's zip stream is in. Pipe a fetch response's `body`
 * through this and the zip builder never sees more than one file's worth
 * of ciphertext-sized buffering at a time, regardless of how large the
 * share is.
 *
 * Throws `LockedSessionError` synchronously, when this is called, rather
 * than from inside the stream on the first chunk: a caller that is about to
 * start a download learns it needs a passphrase before it opens a
 * connection, not partway through one.
 *
 * Never holds more than about two blocks (a little over 128 KiB) of
 * buffered ciphertext at once: `transform` drains every full block it can
 * as soon as more than one block's worth is buffered, holding back only
 * the tail that might still be the file's final, possibly short, block
 * until `flush` confirms there is no more input.
 *
 * Fails the stream, never emitting a block it could not authenticate, on a
 * bad magic, a tag that does not verify, or an input that ends mid-block
 * (fewer than 16 tag bytes plus 1 plaintext byte remaining when the source
 * closes). A file that decrypts to exactly zero bytes is not truncation:
 * the format's own zero-length file is a bare 32-byte header with no
 * blocks at all.
 *
 * `expectedCiphertextSize`, when given, is compared against the total
 * bytes actually received once the source closes, and a mismatch fails the
 * stream: a source that stops exactly on a block boundary, or right after
 * the header, looks byte-for-byte like a valid final block or a valid
 * empty file, so without this the caller has no way to tell a
 * boundary-aligned truncation from a real end of file.
 */
export function decryptStream(salt: string, expectedCiphertextSize?: number): TransformStream<Uint8Array, Uint8Array> {
  const keys = keysFor(salt)
  const buf = new ByteAccumulator()
  let nonce: Uint8Array | null = null
  let received = 0

  return new TransformStream<Uint8Array, Uint8Array>({
    transform(chunk, controller) {
      received += chunk.length
      buf.push(chunk)
      if (nonce === null) {
        if (buf.length < HEADER.length + NONCE_BYTES) return
        const header = buf.take(HEADER.length + NONCE_BYTES)
        if (!equalBytes(header.subarray(0, HEADER.length), HEADER)) {
          throw new Error('not an rclone-crypt file: missing or wrong 8-byte header')
        }
        nonce = header.subarray(HEADER.length)
      }
      while (buf.length > BLOCK_SIZE + 16) {
        const decrypted = decryptBlock(keys, nonce, buf.take(BLOCK_SIZE + 16))
        nonce = decrypted.nextNonce
        controller.enqueue(decrypted.plaintext)
      }
    },
    flush(controller) {
      if (expectedCiphertextSize !== undefined && received !== expectedCiphertextSize) {
        throw new Error(`ciphertext ended after ${received} of ${expectedCiphertextSize} expected bytes: truncated in transit`)
      }
      if (nonce === null) {
        // Even an empty plaintext file's ciphertext is a full header: fewer
        // bytes than that arrived in the whole stream, so this is a
        // truncated file, never a valid empty one.
        throw new Error('not an rclone-crypt file: truncated before the 32-byte header')
      }
      const rest = buf.drain()
      if (rest.length === 0) return
      if (rest.length < 17) throw new Error('rclone-crypt file truncated mid-block')
      controller.enqueue(decryptBlock(keys, nonce, rest).plaintext)
    }
  })
}

/** The exact 8-byte magic every rclone-crypt file starts with, exported so
 *  a caller reading a file's header directly (the media Range responder in
 *  `download-sw.ts`, which fetches just the first 32 bytes to recover a
 *  file's own nonce rather than going through `decryptDownload`/
 *  `decryptStream`) can check it without a second copy of the magic. */
export const RCLONE_CRYPT_MAGIC = HEADER

/**
 * The plaintext size a `ciphertextSize`-byte rclone-crypt file holds: the
 * inverse of the format's `32 + ceil(n/65536)*16 + n` size formula.
 *
 * Exists so a byte-range read of a file's plaintext (`ciphertextSpanForRange`
 * below) never has to ask the server for anything beyond what a directory
 * listing already reports: a listing's `size` is the file's ciphertext
 * length, since rclone-crypt on this share encrypts content only:
 * `filename_encryption = off` keeps names and sizes in the clear on disk,
 * which is exactly what lets this be computed locally.
 *
 * Throws on a `ciphertextSize` under 32 (shorter than every valid file's
 * own header and first-block nonce) or on a remainder that leaves a partial
 * final block under 17 bytes (too short to hold even a 16-byte tag plus 1
 * plaintext byte).
 */
export function plaintextSizeFromCiphertextSize(ciphertextSize: number): number {
  const headerSize = HEADER.length + NONCE_BYTES
  if (ciphertextSize < headerSize) {
    throw new Error(`ciphertext of ${ciphertextSize} bytes is shorter than the ${headerSize}-byte header+nonce`)
  }
  const remainder = ciphertextSize - headerSize
  if (remainder === 0) return 0
  const fullBlockCiphertextSize = BLOCK_SIZE + 16
  const fullBlocks = Math.floor(remainder / fullBlockCiphertextSize)
  const tail = remainder % fullBlockCiphertextSize
  if (tail > 0 && tail < 17) {
    throw new Error(`ciphertext of ${ciphertextSize} bytes ends with a ${tail}-byte remainder, too short to be a partial block`)
  }
  return tail === 0 ? fullBlocks * BLOCK_SIZE : fullBlocks * BLOCK_SIZE + (tail - 16)
}

/** A contiguous byte span within a whole ciphertext file: `offset` counts
 *  from the very start of the file (header included), `length` is how many
 *  bytes from there to fetch. */
export interface CiphertextSpan {
  offset: number
  length: number
}

/**
 * Maps a half-open plaintext byte range `[start, end)` to the contiguous
 * ciphertext span covering every SecretBox block the range touches.
 *
 * A block's Poly1305 tag authenticates the whole block at once, so a range
 * that starts or ends mid-block still needs that entire block's ciphertext
 * fetched and decrypted; trimming to the exact requested bytes
 * (`decryptPlaintextRange` below) is the caller's job once it holds the
 * decrypted block. The result is one contiguous span rather than one per
 * block because every block but the file's last is a fixed 65552
 * ciphertext bytes, so the blocks a range touches are already contiguous.
 *
 * Throws on a range outside `[0, plaintextSize]`, or an empty one.
 */
export function ciphertextSpanForRange(start: number, end: number, plaintextSize: number): CiphertextSpan {
  if (start < 0 || end <= start || end > plaintextSize) {
    throw new RangeError(`range [${start}, ${end}) is not within [0, ${plaintextSize}]`)
  }
  const numBlocks = Math.ceil(plaintextSize / BLOCK_SIZE)
  const startBlock = Math.floor(start / BLOCK_SIZE)
  const endBlock = Math.floor((end - 1) / BLOCK_SIZE)
  const fullBlockCiphertextSize = BLOCK_SIZE + 16
  let length = 0
  for (let i = startBlock; i <= endBlock; i++) {
    const blockPlaintextSize = i === numBlocks - 1 ? plaintextSize - i * BLOCK_SIZE : BLOCK_SIZE
    length += 16 + blockPlaintextSize
  }
  return { offset: HEADER.length + NONCE_BYTES + startBlock * fullBlockCiphertextSize, length }
}

/**
 * Decrypts `ciphertextSpan` (the exact bytes `ciphertextSpanForRange`
 * named for `[start, end)`) under the share `salt` names, and trims the
 * result down from whole blocks to exactly `[start, end)`.
 *
 * `nonce0` is the file's own first-block nonce (bytes 8..32 of its
 * ciphertext, the same 24 bytes `decryptRcloneCrypt` reads once per file):
 * the caller fetches it itself, since a media Range read only ever fetches
 * the blocks a request actually touches, not the file's own header too.
 *
 * Throws `LockedSessionError` when `salt` is not the unlocked share, the
 * same as every other decrypt entry point here. Also throws when the
 * decrypted span is shorter than `[start, end)` needs: a fetch truncated
 * exactly on a block boundary still leaves every earlier block's tag
 * valid, so only this length check catches the missing tail.
 */
export function decryptPlaintextRange(
  salt: string,
  nonce0: Uint8Array,
  start: number,
  end: number,
  ciphertextSpan: Uint8Array
): Uint8Array {
  const keys = keysFor(salt)
  const startBlock = Math.floor(start / BLOCK_SIZE)
  let nonce = nonce0
  for (let i = 0; i < startBlock; i++) nonce = incrementNonce(nonce)

  const parts: Uint8Array[] = []
  let offset = 0
  while (offset < ciphertextSpan.length) {
    const blockLen = Math.min(ciphertextSpan.length - offset, BLOCK_SIZE + 16)
    const decrypted = decryptBlock(keys, nonce, ciphertextSpan.subarray(offset, offset + blockLen))
    parts.push(decrypted.plaintext)
    nonce = decrypted.nextNonce
    offset += blockLen
  }

  const trimStart = start - startBlock * BLOCK_SIZE
  const plaintext = concatBytes(...parts)
  if (plaintext.length < trimStart + (end - start)) {
    throw new Error(`ciphertext span decrypted to only ${plaintext.length} bytes, short of the ${trimStart + (end - start)} the range needs: truncated in transit`)
  }
  return plaintext.subarray(trimStart, trimStart + (end - start))
}
