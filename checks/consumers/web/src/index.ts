import {
  decryptDownload,
  deriveKeys,
  encryptForUpload,
  generateSalt,
  makeVerifier,
  unlock
} from '@stowcloud/rclone-crypt'

export async function smoke(): Promise<Uint8Array> {
  const salt = generateSalt()
  const keys = await deriveKeys('consumer passphrase', salt)
  const verifier = await makeVerifier(keys)
  await unlock('consumer passphrase', salt, verifier)
  const input = new TextEncoder().encode('independent public consumer')
  const ciphertext = await encryptForUpload(input, salt)
  return decryptDownload(ciphertext, salt)
}
