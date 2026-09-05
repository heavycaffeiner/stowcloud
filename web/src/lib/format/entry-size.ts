// web/src/lib/format/entry-size.ts: formats file entry sizes, deriving
// plaintext size when browsing an end-to-end encrypted share.
import { formatBytes } from './bytes'
import { tryPlaintextSize } from '../crypto/e2ee'
import { t } from '../i18n'

export function formatEntrySize(size: number, isEncrypted = false): string {
  if (!isEncrypted) return formatBytes(size)
  const plain = tryPlaintextSize(size)
  // A size that does not decompose into whole crypt blocks is not a plaintext
  // size, so it is labelled as the encrypted one rather than shown as the
  // file's own.
  if (plain === null) return t('browse.encrypted_size', { size: formatBytes(size) })
  return formatBytes(plain)
}
