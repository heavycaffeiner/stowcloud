import type { Download } from '@playwright/test';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { sha256 } from './hashes';

export interface VerifiedDownload {
  filename: string;
  filePath: string;
  buffer: Buffer;
  hash: string;
  size: number;
}

export async function captureAndVerifyDownload(
  downloadPromise: Promise<Download>,
  expectedHash?: string,
): Promise<VerifiedDownload> {
  const download = await downloadPromise;
  const filename = download.suggestedFilename();
  const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'sc-download-'));
  const filePath = path.join(tempDir, filename);

  await download.saveAs(filePath);
  const buffer = fs.readFileSync(filePath);
  const hash = sha256(buffer);

  if (expectedHash && hash !== expectedHash) {
    throw new Error(
      `Download SHA-256 mismatch for ${filename}: expected ${expectedHash}, got ${hash}`,
    );
  }

  return {
    filename,
    filePath,
    buffer,
    hash,
    size: buffer.length,
  };
}

export function isZipArchive(buffer: Buffer): boolean {
  if (buffer.length < 4) return false;
  return (
    buffer[0] === 0x50 &&
    buffer[1] === 0x4b &&
    buffer[2] === 0x03 &&
    buffer[3] === 0x04
  );
}
